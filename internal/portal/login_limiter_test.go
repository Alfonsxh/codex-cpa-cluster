package portal

import (
	"testing"
	"time"
)

func TestLoginLimiterUsesClientAndAccountBuckets(t *testing.T) {
	now := time.Unix(1_000, 0)
	limiter := NewLoginLimiter(func() time.Time { return now })
	for attempt := 0; attempt < defaultLoginBurst; attempt++ {
		if allowed, retry := limiter.Allow("ip:one", "account:alice"); !allowed || retry != 0 {
			t.Fatalf("attempt %d = (%v, %s)", attempt, allowed, retry)
		}
	}
	if allowed, retry := limiter.Allow("ip:two", "account:alice"); allowed || retry <= 0 {
		t.Fatalf("shared account bucket = (%v, %s)", allowed, retry)
	}
	now = now.Add(time.Minute)
	if allowed, _ := limiter.Allow("ip:two", "account:alice"); !allowed {
		t.Fatal("refilled account bucket remained blocked")
	}
	limiter.Forget("account:alice", "ip:two")
	for attempt := 0; attempt < defaultLoginBurst; attempt++ {
		if allowed, _ := limiter.Allow("ip:two", "account:alice"); !allowed {
			t.Fatalf("forgotten limiter blocked attempt %d", attempt)
		}
	}
}
