package failover

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestParseModeRemovesObserve(t *testing.T) {
	for _, value := range []string{"off", "active", " ACTIVE "} {
		if _, err := ParseMode(value); err != nil {
			t.Fatalf("ParseMode(%q): %v", value, err)
		}
	}
	if _, err := ParseMode("observe"); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("ParseMode(observe) error = %v", err)
	}
}

func TestPlanEvacuationMatchesWeightedAllocation(t *testing.T) {
	routes := make(map[string]string)
	users := make(map[string]struct{})
	for index := range 12 {
		user := fmt.Sprintf("user%02d@example.com", index)
		routes[user] = "source"
		users[user] = struct{}{}
	}
	usedLow, usedMiddle, usedHigh := 10.0, 40.0, 70.0
	states := map[string]AccountState{
		"source": {Exhausted: true},
		"low":    {Eligible: true, Headroom: 85, UsedPercent: &usedLow},
		"middle": {Eligible: true, Headroom: 55, UsedPercent: &usedMiddle},
		"high":   {Eligible: true, Headroom: 25, UsedPercent: &usedHigh},
	}
	plan := PlanEvacuation(routes, users, users, states, map[string]struct{}{"source": {}})
	if plan.PlannedUsers != 12 || plan.UnassignedUsers != 0 {
		t.Fatalf("plan summary = %#v", plan)
	}
	want := map[string]int{"high": 2, "low": 6, "middle": 4}
	if !reflect.DeepEqual(plan.Destinations, want) {
		t.Fatalf("destinations = %#v, want %#v", plan.Destinations, want)
	}
}

func TestPlanGlobalRebalanceMinimizesMovesForEqualAccounts(t *testing.T) {
	routes := map[string]string{
		"a@example.com": "alpha",
		"b@example.com": "alpha",
		"c@example.com": "alpha",
		"d@example.com": "alpha",
		"e@example.com": "alpha",
		"f@example.com": "beta",
	}
	users := userSet(routes)
	states := map[string]AccountState{
		"alpha": {Eligible: true, Headroom: 100},
		"beta":  {Eligible: true, Headroom: 100},
		"gamma": {Eligible: true, Headroom: 100},
	}
	plan := PlanGlobalRebalance(routes, users, users, states)
	if !reflect.DeepEqual(plan.TargetCounts, map[string]int{"alpha": 2, "beta": 2, "gamma": 2}) {
		t.Fatalf("target counts = %#v", plan.TargetCounts)
	}
	if plan.PlannedUsers != 3 || plan.UnassignedUsers != 0 {
		t.Fatalf("global plan = %#v", plan)
	}
	if !reflect.DeepEqual(plan.Destinations, map[string]int{"beta": 1, "gamma": 2}) {
		t.Fatalf("destinations = %#v", plan.Destinations)
	}
	if !reflect.DeepEqual(plan.Sources, map[string]int{"alpha": 3}) {
		t.Fatalf("sources = %#v", plan.Sources)
	}
}

func TestPlanGlobalRebalanceRejectsWholePlanForUnsafeUser(t *testing.T) {
	routes := map[string]string{"alice@example.com": "alpha", "bob@example.com": "alpha"}
	active := userSet(routes)
	routable := map[string]struct{}{"alice@example.com": {}}
	states := map[string]AccountState{
		"alpha": {Eligible: true, Headroom: 100},
		"beta":  {Eligible: true, Headroom: 100},
	}
	plan := PlanGlobalRebalance(routes, active, routable, states)
	if plan.SkippedUsers != 1 || plan.UnassignedUsers != 2 || len(plan.Assignments) != 0 {
		t.Fatalf("unsafe global plan = %#v", plan)
	}
}

func TestPlanGlobalRebalanceMovesUsersOffIneligibleAccounts(t *testing.T) {
	routes := map[string]string{"alice@example.com": "exhausted", "bob@example.com": "alpha"}
	users := userSet(routes)
	states := map[string]AccountState{
		"exhausted": {Exhausted: true},
		"alpha":     {Eligible: true, Headroom: 100},
		"beta":      {Eligible: true, Headroom: 100},
	}
	plan := PlanGlobalRebalance(routes, users, users, states)
	if plan.PlannedUsers != 1 || plan.Assignments["alice@example.com"] != "beta" {
		t.Fatalf("ineligible-account plan = %#v", plan)
	}
}

func userSet(routes map[string]string) map[string]struct{} {
	result := make(map[string]struct{}, len(routes))
	for user := range routes {
		result[user] = struct{}{}
	}
	return result
}
