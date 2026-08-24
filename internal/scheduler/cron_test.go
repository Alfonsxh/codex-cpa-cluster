package scheduler

import (
	"testing"
	"time"
)

func TestEveryUsesRobfigDurationSchedule(t *testing.T) {
	if got := Every(90 * time.Second); got != "@every 1m30s" {
		t.Fatalf("Every = %q", got)
	}
	scheduler := New(nil)
	if _, err := scheduler.AddFunc(Every(time.Minute), func() {}); err != nil {
		t.Fatalf("AddFunc: %v", err)
	}
}

func TestFieldsHandlesPairsAndOddValues(t *testing.T) {
	if got := fields([]any{"entry", 1, "dangling"}); len(got) != 2 {
		t.Fatalf("field count = %d", len(got))
	}
}
