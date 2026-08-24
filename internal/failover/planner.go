package failover

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type Mode string

const (
	ModeOff    Mode = "off"
	ModeActive Mode = "active"
)

var ErrInvalidMode = errors.New("invalid account failover mode")

func ParseMode(value string) (Mode, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case ModeOff, ModeActive:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: %q; supported modes are off and active", ErrInvalidMode, value)
	}
}

type AccountState struct {
	Account          string   `json:"account"`
	Eligible         bool     `json:"eligible"`
	Exhausted        bool     `json:"exhausted"`
	Reason           string   `json:"reason"`
	UsedPercent      *float64 `json:"used_percent"`
	RemainingPercent *float64 `json:"remaining_percent"`
	Headroom         float64  `json:"headroom"`
	ResetAt          int64    `json:"reset_at"`
	ObservedAt       int64    `json:"observed_at"`
}

type Plan struct {
	Assignments       map[string]string `json:"assignments"`
	ExpectedRoutes    map[string]string `json:"expected_routes"`
	Sources           map[string]int    `json:"sources"`
	Destinations      map[string]int    `json:"destinations"`
	CandidateAccounts []string          `json:"candidate_accounts"`
	AffectedUsers     int               `json:"affected_users"`
	PlannedUsers      int               `json:"planned_users"`
	SkippedUsers      int               `json:"skipped_users"`
	UnassignedUsers   int               `json:"unassigned_users"`
	TargetCounts      map[string]int    `json:"target_counts,omitempty"`
}

func PlanEvacuation(
	routes map[string]string,
	activeUsers map[string]struct{},
	routableUsers map[string]struct{},
	states map[string]AccountState,
	sourceAccounts map[string]struct{},
) Plan {
	if sourceAccounts == nil {
		sourceAccounts = make(map[string]struct{})
		for account, state := range states {
			if state.Exhausted {
				sourceAccounts[account] = struct{}{}
			}
		}
	}
	candidates := eligibleCandidates(states, sourceAccounts)
	affected := make([]routeEntry, 0)
	for user, account := range routes {
		if _, active := activeUsers[user]; !active {
			continue
		}
		if _, source := sourceAccounts[account]; source {
			affected = append(affected, routeEntry{User: user, Account: account})
		}
	}
	sortRouteEntries(affected)
	plan := newPlan(candidates, len(affected))
	routedCounts := make(map[string]int, len(candidates))
	for _, account := range candidates {
		routedCounts[account] = 0
	}
	for user, account := range routes {
		if _, active := activeUsers[user]; active {
			if _, candidate := routedCounts[account]; candidate {
				routedCounts[account]++
			}
		}
	}
	for _, entry := range affected {
		plan.Sources[entry.Account]++
		if _, routable := routableUsers[entry.User]; !routable {
			plan.SkippedUsers++
			continue
		}
		target, found := leastWeightedAccount(candidates, routedCounts, states)
		if !found {
			continue
		}
		plan.Assignments[entry.User] = target
		plan.ExpectedRoutes[entry.User] = entry.Account
		plan.Destinations[target]++
		routedCounts[target]++
	}
	plan.PlannedUsers = len(plan.Assignments)
	plan.UnassignedUsers = plan.AffectedUsers - plan.PlannedUsers
	return plan
}

func PlanGlobalRebalance(
	routes map[string]string,
	activeUsers map[string]struct{},
	routableUsers map[string]struct{},
	states map[string]AccountState,
) Plan {
	candidates := eligibleCandidates(states, nil)
	users := make([]string, 0, len(activeUsers))
	for user := range activeUsers {
		users = append(users, user)
	}
	sort.Strings(users)
	plan := newPlan(candidates, len(users))
	if len(candidates) == 0 {
		plan.UnassignedUsers = len(users)
		return plan
	}
	routable := make([]string, 0, len(users))
	for _, user := range users {
		if _, found := routableUsers[user]; !found {
			plan.SkippedUsers++
			continue
		}
		routable = append(routable, user)
	}
	if plan.SkippedUsers > 0 {
		plan.UnassignedUsers = plan.AffectedUsers
		return plan
	}

	targetCounts := weightedTargetCounts(len(routable), candidates, states)
	plan.TargetCounts = targetCounts
	retained := make(map[string]struct{}, len(routable))
	currentByAccount := make(map[string][]string, len(candidates))
	for _, user := range routable {
		currentByAccount[routes[user]] = append(currentByAccount[routes[user]], user)
	}
	for _, account := range candidates {
		current := currentByAccount[account]
		sort.Strings(current)
		keep := min(len(current), targetCounts[account])
		for _, user := range current[:keep] {
			retained[user] = struct{}{}
		}
	}

	remaining := make([]routeEntry, 0)
	for _, user := range routable {
		if _, keep := retained[user]; keep {
			continue
		}
		remaining = append(remaining, routeEntry{User: user, Account: routes[user]})
	}
	sortRouteEntries(remaining)
	deficits := make(map[string]int, len(candidates))
	for _, account := range candidates {
		deficits[account] = targetCounts[account] - min(len(currentByAccount[account]), targetCounts[account])
	}
	for _, entry := range remaining {
		target := firstDeficit(candidates, deficits)
		if target == "" {
			break
		}
		deficits[target]--
		if entry.Account == target {
			continue
		}
		plan.Assignments[entry.User] = target
		plan.ExpectedRoutes[entry.User] = entry.Account
		plan.Sources[entry.Account]++
		plan.Destinations[target]++
	}
	plan.PlannedUsers = len(plan.Assignments)
	plan.UnassignedUsers = 0
	return plan
}

type routeEntry struct {
	User    string
	Account string
}

func newPlan(candidates []string, affected int) Plan {
	return Plan{
		Assignments:       make(map[string]string),
		ExpectedRoutes:    make(map[string]string),
		Sources:           make(map[string]int),
		Destinations:      make(map[string]int),
		CandidateAccounts: candidates,
		AffectedUsers:     affected,
		TargetCounts:      make(map[string]int),
	}
}

func eligibleCandidates(states map[string]AccountState, excluded map[string]struct{}) []string {
	result := make([]string, 0)
	for account, state := range states {
		if state.Eligible && state.Headroom > 0 {
			if _, skip := excluded[account]; !skip {
				result = append(result, account)
			}
		}
	}
	sort.Strings(result)
	return result
}

func leastWeightedAccount(
	candidates []string,
	counts map[string]int,
	states map[string]AccountState,
) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	best := candidates[0]
	for _, account := range candidates[1:] {
		if weightedLess(account, best, counts, states) {
			best = account
		}
	}
	return best, true
}

func weightedLess(left string, right string, counts map[string]int, states map[string]AccountState) bool {
	leftScore := float64(counts[left]+1) / states[left].Headroom
	rightScore := float64(counts[right]+1) / states[right].Headroom
	if leftScore != rightScore {
		return leftScore < rightScore
	}
	leftUsed := 101.0
	if states[left].UsedPercent != nil {
		leftUsed = *states[left].UsedPercent
	}
	rightUsed := 101.0
	if states[right].UsedPercent != nil {
		rightUsed = *states[right].UsedPercent
	}
	if leftUsed != rightUsed {
		return leftUsed < rightUsed
	}
	return left < right
}

func weightedTargetCounts(total int, candidates []string, states map[string]AccountState) map[string]int {
	counts := make(map[string]int, len(candidates))
	for _, account := range candidates {
		counts[account] = 0
	}
	for range total {
		target, _ := leastWeightedAccount(candidates, counts, states)
		counts[target]++
	}
	return counts
}

func firstDeficit(candidates []string, deficits map[string]int) string {
	for _, account := range candidates {
		if deficits[account] > 0 {
			return account
		}
	}
	return ""
}

func sortRouteEntries(entries []routeEntry) {
	sort.Slice(entries, func(left int, right int) bool {
		if entries[left].User != entries[right].User {
			return entries[left].User < entries[right].User
		}
		return entries[left].Account < entries[right].Account
	})
}
