package gateway

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestAuthenticationReadyRequiresFreshNonEmptySnapshot(t *testing.T) {
	now := time.Unix(1000, 0)
	engine := NewEngine()
	if engine.AuthenticationReady(now) {
		t.Fatal("empty engine reported authentication readiness")
	}

	fixture := loadContractFixture(t)
	if err := engine.LoadAuthSnapshot(bytes.NewReader(fixture.AuthSnapshot), now); err != nil {
		t.Fatalf("load auth snapshot: %v", err)
	}
	if !engine.AuthenticationReady(now) {
		t.Fatal("fresh API Key snapshot did not report readiness")
	}
	if engine.AuthenticationReady(now.Add(AuthSnapshotMaxAge + time.Second)) {
		t.Fatal("stale API Key snapshot reported readiness")
	}

	var empty AuthSnapshot
	if err := json.Unmarshal(fixture.AuthSnapshot, &empty); err != nil {
		t.Fatalf("decode auth snapshot: %v", err)
	}
	empty.Records = []AuthRecord{}
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("encode empty auth snapshot: %v", err)
	}
	if err := engine.LoadAuthSnapshot(bytes.NewReader(raw), now); err != nil {
		t.Fatalf("load empty auth snapshot: %v", err)
	}
	if engine.AuthenticationReady(now) {
		t.Fatal("empty API Key snapshot reported readiness")
	}
}
