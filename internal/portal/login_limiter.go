package portal

import (
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultLoginBurst      = 5
	defaultLoginRefill     = time.Minute
	loginLimiterIdleExpiry = 30 * time.Minute
)

type loginLimitEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// LoginLimiter composes x/time/rate buckets for both client and account keys.
// The small map only coordinates bucket ownership and expiry; token accounting,
// refill, and retry timing remain delegated to the framework.
type LoginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginLimitEntry
	now     func() time.Time
	burst   int
	refill  time.Duration
}

func NewLoginLimiter(now func() time.Time) *LoginLimiter {
	if now == nil {
		now = time.Now
	}
	return &LoginLimiter{
		entries: make(map[string]*loginLimitEntry), now: now,
		burst: defaultLoginBurst, refill: defaultLoginRefill,
	}
}

func (limiter *LoginLimiter) Allow(keys ...string) (bool, time.Duration) {
	if limiter == nil {
		return true, 0
	}
	normalized := normalizedLimitKeys(keys)
	if len(normalized) == 0 {
		return true, 0
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	limiter.clean(now)
	entries := make([]*loginLimitEntry, 0, len(normalized))
	maximumWait := time.Duration(0)
	for _, key := range normalized {
		entry := limiter.entries[key]
		if entry == nil {
			entry = &loginLimitEntry{
				limiter: rate.NewLimiter(rate.Every(limiter.refill), limiter.burst),
			}
			limiter.entries[key] = entry
		}
		entry.lastSeen = now
		entries = append(entries, entry)
		if entry.limiter.TokensAt(now) < 1 {
			reservation := entry.limiter.ReserveN(now, 1)
			if reservation.OK() {
				wait := reservation.DelayFrom(now)
				reservation.CancelAt(now)
				if wait > maximumWait {
					maximumWait = wait
				}
			}
		}
	}
	if maximumWait > 0 {
		return false, maximumWait
	}
	for _, entry := range entries {
		entry.limiter.AllowN(now, 1)
	}
	return true, 0
}

func (limiter *LoginLimiter) Forget(keys ...string) {
	if limiter == nil {
		return
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	for _, key := range normalizedLimitKeys(keys) {
		delete(limiter.entries, key)
	}
}

func (limiter *LoginLimiter) clean(now time.Time) {
	for key, entry := range limiter.entries {
		if now.Sub(entry.lastSeen) > loginLimiterIdleExpiry {
			delete(limiter.entries, key)
		}
	}
}

func normalizedLimitKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
