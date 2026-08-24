package accountlifecycle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

var (
	ErrRouteEvacuationUnavailable = errors.New("account route evacuation is unavailable")
	ErrAccountDrainTimeout        = errors.New("account in-flight requests did not drain")
)

type AccountDrainer interface {
	WaitAccountDrained(context.Context, string) error
}

type routeTransition struct {
	manager     *Manager
	source      string
	fallback    string
	assignments map[string]string
	expected    map[string]string
}

func (manager *Manager) planRouteEvacuation(
	ctx context.Context,
	accountID string,
	requestedFallback string,
) (*routeTransition, error) {
	routes, err := manager.store.ReadRoutes(ctx)
	if err != nil {
		return nil, fmt.Errorf("read account routes for maintenance evacuation: %w", err)
	}
	users := make([]string, 0)
	for user, account := range routes {
		if account == accountID {
			users = append(users, user)
		}
	}
	sort.Strings(users)
	transition := &routeTransition{
		manager: manager, source: accountID,
		assignments: make(map[string]string, len(users)),
		expected:    make(map[string]string, len(users)),
	}
	if len(users) == 0 {
		return transition, nil
	}
	fallback, err := manager.selectEligibleFallback(ctx, accountID, requestedFallback)
	if err != nil {
		return nil, err
	}
	transition.fallback = fallback
	for _, user := range users {
		transition.assignments[user] = transition.fallback
		transition.expected[user] = accountID
	}
	return transition, nil
}

func (manager *Manager) selectEligibleFallback(
	ctx context.Context,
	accountID string,
	requestedFallback string,
) (string, error) {
	if manager.states == nil {
		return "", ErrRouteEvacuationUnavailable
	}
	accounts, err := manager.store.ReadAccounts(ctx)
	if err != nil {
		return "", fmt.Errorf("read fallback accounts for maintenance evacuation: %w", err)
	}
	states, err := manager.states.AccountStates(ctx)
	if err != nil {
		return "", fmt.Errorf("read fallback account state for maintenance evacuation: %w", err)
	}
	requestedFallback = strings.TrimSpace(requestedFallback)
	candidates := make([]controlplane.Account, 0)
	for _, account := range accounts {
		state := states[account.ID]
		if account.ID != accountID && account.GroupEnabled && state.Eligible && state.Headroom > 0 {
			candidates = append(candidates, account)
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].DefaultGroup != candidates[right].DefaultGroup {
			return candidates[left].DefaultGroup
		}
		return candidates[left].ID < candidates[right].ID
	})
	for _, candidate := range candidates {
		if requestedFallback == "" || requestedFallback == candidate.ID {
			return candidate.ID, nil
		}
	}
	return "", fmt.Errorf("%w: no enabled and eligible fallback exists for %s", controlplane.ErrAccountDeleteNeedsFallback, accountID)
}

func (transition *routeTransition) Empty() bool {
	return transition == nil || len(transition.assignments) == 0
}

func (transition *routeTransition) Apply(ctx context.Context) error {
	if transition.Empty() {
		return nil
	}
	if transition.manager.drainer == nil {
		return newCompensatedError(ErrRouteEvacuationUnavailable)
	}
	if _, err := transition.manager.store.ApplyRoutesExpected(ctx, transition.assignments, transition.expected); err != nil {
		return newCompensatedError(fmt.Errorf("evacuate account routes: %w", err))
	}
	if _, err := transition.manager.snapshots.PublishAuthSnapshot(ctx, true); err != nil {
		rollbackContext, cancel := lifecycleRollbackContext(ctx)
		defer cancel()
		rollbackError := transition.restoreTo(rollbackContext, transition.source)
		var snapshotError error
		if rollbackError == nil {
			_, snapshotError = transition.manager.snapshots.PublishAuthSnapshot(rollbackContext, true)
		}
		return newCompensatedError(
			fmt.Errorf("activate maintenance evacuation snapshot: %w", err),
			rollbackError,
			snapshotError,
		)
	}
	if err := transition.manager.drainer.WaitAccountDrained(ctx, transition.source); err != nil {
		rollbackContext, cancel := lifecycleRollbackContext(ctx)
		defer cancel()
		rollbackError := transition.restoreTo(rollbackContext, transition.source)
		var snapshotError error
		if rollbackError == nil {
			_, snapshotError = transition.manager.snapshots.PublishAuthSnapshot(rollbackContext, true)
		}
		return newCompensatedError(
			fmt.Errorf("%w: %v", ErrAccountDrainTimeout, err),
			rollbackError,
			snapshotError,
		)
	}
	return nil
}

func (transition *routeTransition) restoreTo(ctx context.Context, target string) error {
	if transition.Empty() {
		return nil
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("route restoration target is required")
	}
	routes, err := transition.manager.store.ReadRoutes(ctx)
	if err != nil {
		return err
	}
	assignments := make(map[string]string)
	expected := make(map[string]string)
	for user, fallback := range transition.assignments {
		current := routes[user]
		if current == target {
			continue
		}
		if current != fallback && current != transition.source {
			return fmt.Errorf("%w: route for %s changed to %s during account maintenance", controlplane.ErrRouteConflict, user, current)
		}
		assignments[user] = target
		expected[user] = current
	}
	if len(assignments) == 0 {
		return nil
	}
	if _, err := transition.manager.store.ApplyRoutesExpected(ctx, assignments, expected); err != nil {
		return fmt.Errorf("restore evacuated account routes to %s: %w", target, err)
	}
	return nil
}

func (transition *routeTransition) journalRoutes() map[string]string {
	if transition.Empty() {
		return nil
	}
	result := make(map[string]string, len(transition.assignments))
	for user, target := range transition.assignments {
		result[user] = target
	}
	return result
}
