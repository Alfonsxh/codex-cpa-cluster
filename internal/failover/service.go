package failover

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"golang.org/x/sync/errgroup"
)

var (
	ErrRebalanceUnsafe      = errors.New("account rebalance is unsafe")
	ErrRebalanceUnavailable = errors.New("account rebalance has no eligible capacity")
)

type RouteStore interface {
	ReadRoutes(context.Context) (map[string]string, error)
	ReadKeyRecords(context.Context) ([]controlplane.KeyRecord, error)
	ApplyRoutesExpected(context.Context, map[string]string, map[string]string) (controlplane.RouteUpdateResult, error)
	RestoreRoutesExpected(context.Context, map[string]string, map[string]string) error
}

type AccountStateProvider interface {
	AccountStates(context.Context) (map[string]AccountState, error)
}

type ActivityProvider interface {
	RefreshActiveUsersLastHour(context.Context) (map[string]int, error)
}

type Snapshot struct {
	Generation string `json:"generation"`
}

type SnapshotPublisher interface {
	PublishAuthSnapshot(context.Context, bool) (Snapshot, error)
}

type Service struct {
	Routes    RouteStore
	States    AccountStateProvider
	Activity  ActivityProvider
	Snapshots SnapshotPublisher
	Lock      sync.Locker
}

type RebalanceResult struct {
	MovedUsers         int            `json:"moved_users"`
	Destinations       map[string]int `json:"destinations"`
	TargetCounts       map[string]int `json:"target_counts"`
	SnapshotGeneration string         `json:"snapshot_generation"`
	ActiveUsers1H      map[string]int `json:"active_users_1h"`
	ActivityRefreshed  bool           `json:"activity_refreshed"`
	Warning            string         `json:"warning,omitempty"`
}

type PlanSummary struct {
	Sources           map[string]int `json:"sources"`
	Destinations      map[string]int `json:"destinations"`
	CandidateAccounts []string       `json:"candidate_accounts"`
	AffectedUsers     int            `json:"affected_users"`
	PlannedUsers      int            `json:"planned_users"`
	SkippedUsers      int            `json:"skipped_users"`
	UnassignedUsers   int            `json:"unassigned_users"`
}

type EvacuationResult struct {
	RebalanceResult
	Plan PlanSummary `json:"plan"`
}

// MoveUser applies one explicitly selected self-service route through the same
// transaction, atomic snapshot, activation wait, rollback, and activity-refresh
// path as automatic failover. It does not choose the target; the authenticated
// portal validates target visibility and status before calling it.
func (service *Service) MoveUser(
	ctx context.Context,
	user string,
	target string,
	expectedRoute string,
) (RebalanceResult, error) {
	if service.Lock != nil {
		service.Lock.Lock()
		defer service.Lock.Unlock()
	}
	if service.Routes == nil || service.Activity == nil || service.Snapshots == nil {
		return RebalanceResult{}, errors.New("self-service route change dependencies are incomplete")
	}
	user = strings.ToLower(strings.TrimSpace(user))
	target = strings.TrimSpace(target)
	expectedRoute = strings.TrimSpace(expectedRoute)
	if user == "" || target == "" {
		return RebalanceResult{}, fmt.Errorf("%w: self-service route user and target are required", controlplane.ErrInvalidCatalogInput)
	}
	if target == expectedRoute {
		return RebalanceResult{
			Destinations: make(map[string]int), TargetCounts: make(map[string]int),
			ActiveUsers1H: make(map[string]int),
		}, nil
	}
	plan := Plan{
		Assignments: map[string]string{user: target}, ExpectedRoutes: map[string]string{user: expectedRoute},
		Sources: map[string]int{expectedRoute: 1}, Destinations: map[string]int{target: 1},
		CandidateAccounts: []string{target}, AffectedUsers: 1, PlannedUsers: 1,
		TargetCounts: make(map[string]int),
	}
	return service.applyPlan(ctx, plan)
}

func (service *Service) RebalanceAll(ctx context.Context) (RebalanceResult, error) {
	if service.Lock != nil {
		service.Lock.Lock()
		defer service.Lock.Unlock()
	}
	if service.Routes == nil || service.States == nil || service.Activity == nil || service.Snapshots == nil {
		return RebalanceResult{}, errors.New("account rebalance dependencies are incomplete")
	}
	var (
		routes  map[string]string
		records []controlplane.KeyRecord
		states  map[string]AccountState
	)
	group, groupContext := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		routes, err = service.Routes.ReadRoutes(groupContext)
		return err
	})
	group.Go(func() error {
		var err error
		records, err = service.Routes.ReadKeyRecords(groupContext)
		return err
	})
	group.Go(func() error {
		var err error
		states, err = service.States.AccountStates(groupContext)
		return err
	})
	if err := group.Wait(); err != nil {
		return RebalanceResult{}, fmt.Errorf("collect account rebalance inputs: %w", err)
	}
	active := activeUsersFromRecords(records)
	routable := routableUsers(records, states)
	plan := PlanGlobalRebalance(routes, active, routable, states)
	if len(plan.CandidateAccounts) == 0 {
		return RebalanceResult{}, ErrRebalanceUnavailable
	}
	if plan.UnassignedUsers > 0 || plan.SkippedUsers > 0 {
		return RebalanceResult{}, fmt.Errorf(
			"%w: %d of %d active users cannot be moved safely",
			ErrRebalanceUnsafe,
			max(plan.UnassignedUsers, plan.SkippedUsers),
			plan.AffectedUsers,
		)
	}
	return service.applyPlan(ctx, plan)
}

func (service *Service) EvacuateExhausted(ctx context.Context) (EvacuationResult, error) {
	if service.Lock != nil {
		service.Lock.Lock()
		defer service.Lock.Unlock()
	}
	return service.evacuate(ctx, nil)
}

// EvacuateAccount moves every active, safely routable user off one explicitly
// confirmed source account. It uses the same all-or-nothing plan, expected-route
// write, activated auth snapshot, rollback, and immediate one-hour activity
// refresh as automatic exhausted-account evacuation.
func (service *Service) EvacuateAccount(ctx context.Context, account string) (EvacuationResult, error) {
	if service.Lock != nil {
		service.Lock.Lock()
		defer service.Lock.Unlock()
	}
	account = strings.ToLower(strings.TrimSpace(account))
	if account == "" {
		return EvacuationResult{}, fmt.Errorf("%w: source account is required", controlplane.ErrInvalidCatalogInput)
	}
	return service.evacuate(ctx, map[string]struct{}{account: {}})
}

func (service *Service) evacuate(
	ctx context.Context,
	sourceAccounts map[string]struct{},
) (EvacuationResult, error) {
	if service.Routes == nil || service.States == nil || service.Activity == nil || service.Snapshots == nil {
		return EvacuationResult{}, errors.New("account evacuation dependencies are incomplete")
	}
	var (
		routes  map[string]string
		records []controlplane.KeyRecord
		states  map[string]AccountState
	)
	group, groupContext := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		routes, err = service.Routes.ReadRoutes(groupContext)
		return err
	})
	group.Go(func() error {
		var err error
		records, err = service.Routes.ReadKeyRecords(groupContext)
		return err
	})
	group.Go(func() error {
		var err error
		states, err = service.States.AccountStates(groupContext)
		return err
	})
	if err := group.Wait(); err != nil {
		return EvacuationResult{}, fmt.Errorf("collect account evacuation inputs: %w", err)
	}
	active := activeUsersFromRecords(records)
	routable := routableUsers(records, states)
	plan := PlanEvacuation(routes, active, routable, states, sourceAccounts)
	result := EvacuationResult{Plan: summarizePlan(plan)}
	if plan.AffectedUsers == 0 {
		result.RebalanceResult = emptyRebalanceResult(plan)
		return result, nil
	}
	if len(plan.CandidateAccounts) == 0 {
		return result, ErrRebalanceUnavailable
	}
	if plan.UnassignedUsers > 0 || plan.SkippedUsers > 0 {
		return result, fmt.Errorf(
			"%w: %d of %d active users cannot be moved safely",
			ErrRebalanceUnsafe,
			max(plan.UnassignedUsers, plan.SkippedUsers),
			plan.AffectedUsers,
		)
	}
	applied, err := service.applyPlan(ctx, plan)
	result.RebalanceResult = applied
	return result, err
}

func (service *Service) applyPlan(ctx context.Context, plan Plan) (RebalanceResult, error) {
	result := emptyRebalanceResult(plan)
	if len(plan.Assignments) == 0 {
		return result, nil
	}
	updated, err := service.Routes.ApplyRoutesExpected(ctx, plan.Assignments, plan.ExpectedRoutes)
	if err != nil {
		return RebalanceResult{}, fmt.Errorf("apply account rebalance routes: %w", err)
	}
	result.MovedUsers = updated.MovedUsers
	result.Destinations = updated.Destinations
	snapshot, err := service.Snapshots.PublishAuthSnapshot(ctx, true)
	if err != nil {
		rollbackContext, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancelRollback()
		rollbackError := service.Routes.RestoreRoutesExpected(
			rollbackContext,
			plan.ExpectedRoutes,
			plan.Assignments,
		)
		var rollbackSnapshotError error
		if rollbackError == nil {
			_, rollbackSnapshotError = service.Snapshots.PublishAuthSnapshot(rollbackContext, true)
		}
		return RebalanceResult{}, errors.Join(
			fmt.Errorf("publish account rebalance snapshot: %w", err),
			wrapOptionalError("rollback account rebalance routes", rollbackError),
			wrapOptionalError("publish rollback auth snapshot", rollbackSnapshotError),
		)
	}
	result.SnapshotGeneration = snapshot.Generation
	activity, refreshError := service.Activity.RefreshActiveUsersLastHour(ctx)
	if refreshError != nil {
		result.Warning = "routes changed, but the one-hour active-user refresh failed: " + refreshError.Error()
		return result, nil
	}
	result.ActiveUsers1H = activity
	result.ActivityRefreshed = true
	return result, nil
}

func emptyRebalanceResult(plan Plan) RebalanceResult {
	return RebalanceResult{
		Destinations: make(map[string]int), TargetCounts: plan.TargetCounts,
		ActiveUsers1H: make(map[string]int),
	}
}

func summarizePlan(plan Plan) PlanSummary {
	return PlanSummary{
		Sources: plan.Sources, Destinations: plan.Destinations,
		CandidateAccounts: plan.CandidateAccounts, AffectedUsers: plan.AffectedUsers,
		PlannedUsers: plan.PlannedUsers, SkippedUsers: plan.SkippedUsers,
		UnassignedUsers: plan.UnassignedUsers,
	}
}

func activeUsersFromRecords(records []controlplane.KeyRecord) map[string]struct{} {
	result := make(map[string]struct{})
	for _, record := range records {
		if record.Status != "active" {
			continue
		}
		user := strings.ToLower(strings.TrimSpace(record.User))
		if user != "" {
			result[user] = struct{}{}
		}
	}
	return result
}

func routableUsers(records []controlplane.KeyRecord, states map[string]AccountState) map[string]struct{} {
	byUser := make(map[string][]controlplane.KeyRecord)
	for _, record := range records {
		if record.Status != "active" {
			continue
		}
		user := strings.ToLower(strings.TrimSpace(record.User))
		if user != "" {
			byUser[user] = append(byUser[user], record)
		}
	}
	accounts := make([]string, 0, len(states))
	for account := range states {
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)
	result := make(map[string]struct{})
	for user, userRecords := range byUser {
		secrets := make(map[string]struct{})
		availableAccounts := make(map[string]struct{})
		for _, record := range userRecords {
			secrets[record.Key] = struct{}{}
			availableAccounts[record.Account] = struct{}{}
		}
		if len(secrets) != 1 {
			continue
		}
		complete := true
		for _, account := range accounts {
			if _, found := availableAccounts[account]; !found {
				complete = false
				break
			}
		}
		if complete {
			result[user] = struct{}{}
		}
	}
	return result
}

func wrapOptionalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
