package accountlifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/accountprojection"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
)

func TestManagerCreateAllocatesUnusedDockerPortPreservesKeysAndActivatesSnapshot(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.runtime.ports[18319] = struct{}{}
	result, err := fixture.manager.Create(context.Background(), CreateRequest{
		ID: "Gamma", Email: "Gamma@Accounts.Example.com", ProxyMode: "custom",
		ProxyURL: "socks5://user:password@127.0.0.1:1081/",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.Account.ID != "gamma" || result.Account.Port != 18320 ||
		result.CreatedKeyRows != 2 || result.SnapshotGeneration != "generation-1" {
		t.Fatalf("result = %#v", result)
	}
	assertUnifiedKeyMatrix(t, fixture.store, map[string]string{
		"alice@example.com": "cpa_external_alice",
		"bob@example.com":   "cpa_external_bob",
	}, 3)
	proxy, found, err := fixture.store.ReadSecret(context.Background(), accountProxySecretPrefix+"gamma")
	if err != nil || !found || proxy != "socks5://user:password@127.0.0.1:1081" {
		t.Fatalf("created proxy secret = (%q, %v, %v)", proxy, found, err)
	}
	if !exists(filepath.Join(fixture.root, "auth", "gamma")) ||
		!strings.Contains(readTestFile(t, filepath.Join(fixture.root, "compose.accounts.yml")), "cliproxy-gamma") {
		t.Fatal("created account filesystem projection is incomplete")
	}
	if fixture.runtime.lastCreate.ID != "gamma" || fixture.runtime.transition.commits != 1 ||
		fixture.runtime.transition.rollbacks != 0 {
		t.Fatalf("runtime create = %#v transition=%#v", fixture.runtime.lastCreate, fixture.runtime.transition)
	}
}

func TestManagerCreateSnapshotFailureRollsBackStateRuntimeSecretsFilesAndProjection(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.snapshots.failures = []error{errors.New("Gateway activation timed out"), nil}
	_, err := fixture.manager.Create(context.Background(), CreateRequest{
		ID: "gamma", Email: "gamma@accounts.example.com", ProxyMode: "custom",
		ProxyURL: "http://proxy-user:proxy-password@127.0.0.1:8080",
	})
	if err == nil || !strings.Contains(err.Error(), "Gateway activation timed out") {
		t.Fatalf("Create error = %v", err)
	}
	accounts, readError := fixture.store.ReadAccounts(context.Background())
	if readError != nil || len(accounts) != 2 {
		t.Fatalf("accounts after rollback = (%#v, %v)", accounts, readError)
	}
	if _, found, readError := fixture.store.ReadSecret(context.Background(), accountProxySecretPrefix+"gamma"); readError != nil || found {
		t.Fatalf("created proxy remained after rollback: found=%v err=%v", found, readError)
	}
	for _, path := range []string{
		filepath.Join(fixture.root, "configs", "gamma.yaml"),
		filepath.Join(fixture.root, "auth", "gamma"),
		filepath.Join(fixture.root, "logs", "gamma"),
	} {
		if exists(path) {
			t.Fatalf("created path remains after rollback: %s", path)
		}
	}
	compose := readTestFile(t, filepath.Join(fixture.root, "compose.accounts.yml"))
	if strings.Contains(compose, "cliproxy-gamma") {
		t.Fatalf("rolled-back Compose still includes gamma: %s", compose)
	}
	if fixture.runtime.transition.rollbacks != 1 || fixture.snapshots.calls != 2 {
		t.Fatalf("rollback calls: runtime=%#v snapshots=%d", fixture.runtime.transition, fixture.snapshots.calls)
	}
	assertUnifiedKeyMatrix(t, fixture.store, map[string]string{
		"alice@example.com": "cpa_external_alice",
		"bob@example.com":   "cpa_external_bob",
	}, 2)
}

func TestManagerRenameMovesEncryptedProxyAndPathsWithoutRotatingKeys(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	writeTestFile(t, filepath.Join(fixture.root, "logs", "alpha", "main.log"), "runtime-log")
	if err := fixture.store.WriteSecret(context.Background(), accountProxySecretPrefix+"alpha", "http://old:secret@127.0.0.1:8080"); err != nil {
		t.Fatalf("seed account proxy: %v", err)
	}
	newProxy := "https://new-user:new-secret@127.0.0.1:8443/"
	disabled := false
	result, err := fixture.manager.Update(context.Background(), UpdateRequest{
		AccountID: "alpha", NewAccountID: "gamma", Email: "gamma@accounts.example.com",
		ProxyMode: "custom", ProxyURL: &newProxy, Enabled: &disabled,
		FallbackAccount: "beta",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.Account.ID != "gamma" || result.Account.GroupEnabled || result.RenamedFrom != "alpha" ||
		result.ReroutedUsers != 1 || result.Backup == "" {
		t.Fatalf("result = %#v", result)
	}
	if _, found, err := fixture.store.ReadSecret(context.Background(), accountProxySecretPrefix+"alpha"); err != nil || found {
		t.Fatalf("old proxy secret remained: found=%v err=%v", found, err)
	}
	proxy, found, err := fixture.store.ReadSecret(context.Background(), accountProxySecretPrefix+"gamma")
	if err != nil || !found || proxy != "https://new-user:new-secret@127.0.0.1:8443" {
		t.Fatalf("renamed proxy = (%q, %v, %v)", proxy, found, err)
	}
	if exists(filepath.Join(fixture.root, "auth", "alpha")) ||
		readTestFile(t, filepath.Join(fixture.root, "auth", "gamma", "oauth.json")) != "oauth-secret" ||
		exists(filepath.Join(fixture.root, "configs", "alpha.yaml")) {
		t.Fatal("renamed account paths are inconsistent")
	}
	assertUnifiedKeyMatrix(t, fixture.store, map[string]string{
		"alice@example.com": "cpa_external_alice",
		"bob@example.com":   "cpa_external_bob",
	}, 2)
	if fixture.runtime.lastUpdateBefore.ID != "alpha" || fixture.runtime.lastUpdateAfter.ID != "gamma" {
		t.Fatalf("runtime update = %#v -> %#v", fixture.runtime.lastUpdateBefore, fixture.runtime.lastUpdateAfter)
	}
	routes, err := fixture.store.ReadRoutes(context.Background())
	if err != nil || routes["alice@example.com"] != "beta" || fixture.drainer.calls != 1 || fixture.snapshots.calls != 2 {
		t.Fatalf("safe update route handoff = routes=%#v drainer=%d snapshots=%d err=%v", routes, fixture.drainer.calls, fixture.snapshots.calls, err)
	}
}

func TestManagerUpdateRuntimeFailureCompensatesRenameAndSnapshot(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	fixture.runtime.prepareError = errors.New("container probe failed")
	_, err := fixture.manager.Update(context.Background(), UpdateRequest{
		AccountID: "alpha", NewAccountID: "gamma", Email: "gamma@accounts.example.com",
		ProxyMode: "inherit",
	})
	if err == nil || !strings.Contains(err.Error(), "container probe failed") {
		t.Fatalf("Update error = %v", err)
	}
	accounts, readError := fixture.store.ReadAccounts(context.Background())
	if readError != nil || accounts[0].ID != "alpha" || len(accounts) != 2 {
		t.Fatalf("accounts after update rollback = (%#v, %v)", accounts, readError)
	}
	if !exists(filepath.Join(fixture.root, "auth", "alpha")) || exists(filepath.Join(fixture.root, "auth", "gamma")) ||
		exists(filepath.Join(fixture.root, "configs", "gamma.yaml")) {
		t.Fatal("rename paths were not compensated")
	}
	if fixture.snapshots.calls != 2 {
		t.Fatalf("rollback snapshot calls = %d, want 2", fixture.snapshots.calls)
	}
}

func TestManagerDeletePublishesBeforeDestructiveCommitsAndKeepsBackup(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	writeTestFile(t, filepath.Join(fixture.root, "logs", "alpha", "main.log"), "runtime-log")
	if err := fixture.store.WriteSecret(context.Background(), accountProxySecretPrefix+"alpha", "http://user:secret@127.0.0.1:8080"); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	result, err := fixture.manager.Delete(context.Background(), DeleteRequest{
		AccountID: "alpha", FallbackAccount: "beta", RevokeExclusive: true,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if result.AccountID != "alpha" || result.ReplacementAccount != "beta" || result.ReroutedUsers != 1 ||
		result.RemovedKeyRows != 2 || result.Backup == "" || result.SnapshotGeneration != "generation-1" {
		t.Fatalf("result = %#v", result)
	}
	if exists(filepath.Join(fixture.root, "configs", "alpha.yaml")) || exists(filepath.Join(fixture.root, "auth", "alpha")) {
		t.Fatal("deleted account paths remain")
	}
	if !exists(filepath.Join(fixture.root, filepath.FromSlash(result.Backup), "auth", "oauth.json")) {
		t.Fatal("recoverable account backup is missing OAuth data")
	}
	if _, found, err := fixture.store.ReadSecret(context.Background(), accountProxySecretPrefix+"alpha"); err != nil || found {
		t.Fatalf("deleted account proxy remains: found=%v err=%v", found, err)
	}
	if fixture.runtime.transition.commits != 1 || fixture.snapshots.calls != 1 {
		t.Fatalf("delete commit = %#v snapshots=%d", fixture.runtime.transition, fixture.snapshots.calls)
	}
}

func TestManagerUpdateDrainFailureRestoresRoutesWithoutTouchingRuntime(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.drainer.err = errors.New("stream still active")
	_, err := fixture.manager.Update(context.Background(), UpdateRequest{
		AccountID: "alpha", Email: "changed@accounts.example.com", ProxyMode: "inherit",
	})
	if !errors.Is(err, ErrAccountDrainTimeout) {
		t.Fatalf("Update error = %v, want ErrAccountDrainTimeout", err)
	}
	routes, readError := fixture.store.ReadRoutes(context.Background())
	if readError != nil || routes["alice@example.com"] != "alpha" {
		t.Fatalf("routes after drain failure = %#v err=%v", routes, readError)
	}
	accounts, readError := fixture.store.ReadAccounts(context.Background())
	if readError != nil || accounts[0].Email != "alpha@accounts.example.com" || fixture.runtime.lastUpdateBefore.ID != "" {
		t.Fatalf("runtime/control changed before drain: accounts=%#v runtime=%#v err=%v", accounts, fixture.runtime.lastUpdateBefore, readError)
	}
	var journal Operation
	if found, readError := fixture.store.ReadRuntimeState(context.Background(), lifecycleJournalStateName, &journal); readError != nil || found {
		t.Fatalf("compensated drain journal remains: found=%v err=%v", found, readError)
	}
}

func TestManagerUpdateRejectsFallbackWithoutQuotaHeadroomBeforeChangingState(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.manager.states = fakeAccountStates{states: map[string]failover.AccountState{
		"alpha": {Account: "alpha", Eligible: true, Headroom: 80},
		"beta":  {Account: "beta", Eligible: true, Headroom: 0},
	}}
	disabled := false
	_, err := fixture.manager.Update(context.Background(), UpdateRequest{
		AccountID: "alpha", Enabled: &disabled, FallbackAccount: "beta",
	})
	if !errors.Is(err, controlplane.ErrAccountDeleteNeedsFallback) {
		t.Fatalf("Update error = %v, want ErrAccountDeleteNeedsFallback", err)
	}
	routes, readError := fixture.store.ReadRoutes(context.Background())
	if readError != nil || routes["alice@example.com"] != "alpha" {
		t.Fatalf("routes changed without an eligible fallback: %#v err=%v", routes, readError)
	}
	accounts, readError := fixture.store.ReadAccounts(context.Background())
	if readError != nil || !accounts[0].GroupEnabled || fixture.drainer.calls != 0 || fixture.snapshots.calls != 0 ||
		fixture.runtime.lastUpdateBefore.ID != "" {
		t.Fatalf(
			"state changed without an eligible fallback: accounts=%#v drainer=%d snapshots=%d runtime=%#v err=%v",
			accounts, fixture.drainer.calls, fixture.snapshots.calls, fixture.runtime.lastUpdateBefore, readError,
		)
	}
	var journal Operation
	if found, readError := fixture.store.ReadRuntimeState(context.Background(), lifecycleJournalStateName, &journal); readError != nil || found {
		t.Fatalf("rejected fallback journal remains: found=%v err=%v", found, readError)
	}
}

func TestManagerUnavailableProxyRepairBreaksOnlyTheNoFallbackBootstrapDeadlock(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.manager.states = fakeAccountStates{states: map[string]failover.AccountState{
		"alpha": {Account: "alpha", Eligible: false, Reason: "upstream_unavailable"},
		"beta":  {Account: "beta", Eligible: false, Reason: "upstream_unavailable"},
	}}
	proxyURL := "http://gost-alpha:16169"
	result, err := fixture.manager.Update(context.Background(), UpdateRequest{
		AccountID: "alpha", ProxyMode: "custom", ProxyURL: &proxyURL,
		AllowUnavailableProxyRepair: true,
	})
	if err != nil {
		t.Fatalf("unavailable proxy repair: %v", err)
	}
	if result.Account.ID != "alpha" || result.Account.ProxyMode != "custom" ||
		result.ReroutedUsers != 0 || fixture.drainer.calls != 0 || fixture.snapshots.calls != 1 {
		t.Fatalf(
			"repair result=%#v drainer=%d snapshots=%d",
			result, fixture.drainer.calls, fixture.snapshots.calls,
		)
	}
	routes, readError := fixture.store.ReadRoutes(context.Background())
	if readError != nil || routes["alice@example.com"] != "alpha" {
		t.Fatalf("repair changed routes: %#v err=%v", routes, readError)
	}
	storedProxy, found, readError := fixture.store.ReadSecret(
		context.Background(), accountProxySecretPrefix+"alpha",
	)
	if readError != nil || !found || storedProxy != proxyURL {
		t.Fatalf("repair proxy secret = (%q, %v, %v)", storedProxy, found, readError)
	}
	if fixture.runtime.lastUpdateBefore.ID != "alpha" || fixture.runtime.lastUpdateAfter.ID != "alpha" ||
		fixture.runtime.lastUpdateAfter.ProxyMode != "custom" || fixture.runtime.transition.commits != 1 {
		t.Fatalf("repair runtime transition = %#v -> %#v, %#v",
			fixture.runtime.lastUpdateBefore, fixture.runtime.lastUpdateAfter, fixture.runtime.transition)
	}
	assertUnifiedKeyMatrix(t, fixture.store, map[string]string{
		"alice@example.com": "cpa_external_alice",
		"bob@example.com":   "cpa_external_bob",
	}, 2)
}

func TestManagerUnavailableProxyRepairRejectsWhenSafeMaintenanceIsPossible(t *testing.T) {
	tests := []struct {
		name   string
		states map[string]failover.AccountState
		mutate func(*UpdateRequest)
	}{
		{
			name: "eligible fallback exists",
			states: map[string]failover.AccountState{
				"alpha": {Account: "alpha", Eligible: false},
				"beta":  {Account: "beta", Eligible: true, Headroom: 50},
			},
		},
		{
			name: "source is eligible",
			states: map[string]failover.AccountState{
				"alpha": {Account: "alpha", Eligible: true, Headroom: 50},
				"beta":  {Account: "beta", Eligible: false},
			},
		},
		{
			name: "metadata also changes",
			states: map[string]failover.AccountState{
				"alpha": {Account: "alpha", Eligible: false},
				"beta":  {Account: "beta", Eligible: false},
			},
			mutate: func(request *UpdateRequest) { request.Email = "changed@accounts.example.com" },
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newManagerFixture(t)
			fixture.manager.states = fakeAccountStates{states: testCase.states}
			proxyURL := "http://gost-alpha:16169"
			request := UpdateRequest{
				AccountID: "alpha", ProxyMode: "custom", ProxyURL: &proxyURL,
				AllowUnavailableProxyRepair: true,
			}
			if testCase.mutate != nil {
				testCase.mutate(&request)
			}
			_, err := fixture.manager.Update(context.Background(), request)
			if !errors.Is(err, ErrUnavailableProxyRepairRejected) {
				t.Fatalf("repair error = %v, want ErrUnavailableProxyRepairRejected", err)
			}
			if fixture.runtime.lastUpdateBefore.ID != "" || fixture.snapshots.calls != 0 ||
				fixture.drainer.calls != 0 {
				t.Fatalf("rejected repair changed runtime: %#v", fixture.runtime)
			}
			accounts, readError := fixture.store.ReadAccounts(context.Background())
			if readError != nil || accounts[0].ProxyMode != "inherit" {
				t.Fatalf("rejected repair changed account: %#v err=%v", accounts, readError)
			}
		})
	}
}

func TestManagerDeleteDrainFailureRestoresControlRoutesRuntimeFilesAndKeys(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	writeTestFile(t, filepath.Join(fixture.root, "logs", "alpha", "main.log"), "runtime-log")
	if err := fixture.store.WriteSecret(context.Background(), accountProxySecretPrefix+"alpha", "http://user:secret@127.0.0.1:8080"); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	fixture.drainer.err = errors.New("stream still active")
	_, err := fixture.manager.Delete(context.Background(), DeleteRequest{
		AccountID: "alpha", FallbackAccount: "beta", RevokeExclusive: true,
	})
	if !errors.Is(err, ErrAccountDrainTimeout) {
		t.Fatalf("Delete error = %v, want ErrAccountDrainTimeout", err)
	}
	accounts, readError := fixture.store.ReadAccounts(context.Background())
	if readError != nil || len(accounts) != 2 || accounts[0].ID != "alpha" {
		t.Fatalf("accounts after drain rollback = %#v err=%v", accounts, readError)
	}
	routes, readError := fixture.store.ReadRoutes(context.Background())
	if readError != nil || routes["alice@example.com"] != "alpha" {
		t.Fatalf("routes after drain rollback = %#v err=%v", routes, readError)
	}
	proxy, found, readError := fixture.store.ReadSecret(context.Background(), accountProxySecretPrefix+"alpha")
	if readError != nil || !found || proxy != "http://user:secret@127.0.0.1:8080" {
		t.Fatalf("proxy after drain rollback = (%q, %v, %v)", proxy, found, readError)
	}
	if got := readTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json")); got != "oauth-secret" {
		t.Fatalf("OAuth after drain rollback = %q", got)
	}
	if got := readTestFile(t, filepath.Join(fixture.root, "logs", "alpha", "main.log")); got != "runtime-log" {
		t.Fatalf("log after drain rollback = %q", got)
	}
	if fixture.runtime.transition.commits != 0 || fixture.runtime.transition.rollbacks != 1 || fixture.snapshots.calls != 2 {
		t.Fatalf("runtime/snapshot rollback = %#v snapshots=%d", fixture.runtime.transition, fixture.snapshots.calls)
	}
	assertUnifiedKeyMatrix(t, fixture.store, map[string]string{
		"alice@example.com": "cpa_external_alice",
		"bob@example.com":   "cpa_external_bob",
	}, 2)
	var journal Operation
	if found, readError := fixture.store.ReadRuntimeState(context.Background(), lifecycleJournalStateName, &journal); readError != nil || found {
		t.Fatalf("compensated deletion journal remains: found=%v err=%v", found, readError)
	}
}

func TestManagerClearAuthRestoresBackupWhenRestartFails(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	fixture.runtime.restartErrors = []error{errors.New("restart failed"), nil}
	_, err := fixture.manager.ClearAuth(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "restart failed") {
		t.Fatalf("ClearAuth error = %v", err)
	}
	if got := readTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json")); got != "oauth-secret" {
		t.Fatalf("OAuth backup not restored: %q", got)
	}
	if fixture.runtime.restartCalls != 2 {
		t.Fatalf("restart calls = %d, want failure plus rollback restart", fixture.runtime.restartCalls)
	}
	routes, err := fixture.store.ReadRoutes(context.Background())
	if err != nil || routes["alice@example.com"] != "alpha" || fixture.drainer.calls != 1 || fixture.snapshots.calls != 2 {
		t.Fatalf(
			"OAuth rollback routes = %#v drainer=%d snapshots=%d err=%v",
			routes, fixture.drainer.calls, fixture.snapshots.calls, err,
		)
	}
	var journal Operation
	if found, readError := fixture.store.ReadRuntimeState(context.Background(), lifecycleJournalStateName, &journal); readError != nil || found {
		t.Fatalf("compensated OAuth clear journal remains: found=%v err=%v journal=%#v", found, readError, journal)
	}
}

func TestManagerClearAuthIncompleteRollbackKeepsFallbackAndJournal(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	fixture.runtime.restartErrors = []error{errors.New("restart failed"), errors.New("rollback restart failed")}
	_, err := fixture.manager.ClearAuth(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "rollback restart failed") {
		t.Fatalf("ClearAuth error = %v", err)
	}
	if got := readTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json")); got != "oauth-secret" {
		t.Fatalf("OAuth backup not restored before failed restart: %q", got)
	}
	routes, readError := fixture.store.ReadRoutes(context.Background())
	if readError != nil || routes["alice@example.com"] != "beta" || fixture.snapshots.calls != 2 {
		t.Fatalf(
			"unsafe OAuth rollback routes = %#v snapshots=%d err=%v",
			routes, fixture.snapshots.calls, readError,
		)
	}
	var journal Operation
	if found, journalError := fixture.store.ReadRuntimeState(context.Background(), lifecycleJournalStateName, &journal); journalError != nil || !found {
		t.Fatalf("incomplete OAuth rollback journal missing: found=%v err=%v", found, journalError)
	}
}

func TestManagerClearAuthEvacuatesAndDrainsBeforeRestartThenRestoresRoute(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	result, err := fixture.manager.ClearAuth(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("ClearAuth: %v", err)
	}
	if result.AccountID != "alpha" || result.Backup == "" || fixture.runtime.restartCalls != 1 ||
		fixture.drainer.calls != 1 || fixture.snapshots.calls != 2 {
		t.Fatalf(
			"ClearAuth result=%#v restart=%d drainer=%d snapshots=%d",
			result, fixture.runtime.restartCalls, fixture.drainer.calls, fixture.snapshots.calls,
		)
	}
	if exists(filepath.Join(fixture.root, "auth", "alpha", "oauth.json")) {
		t.Fatal("OAuth file remains after successful clear")
	}
	if !exists(filepath.Join(fixture.root, filepath.FromSlash(result.Backup), "auth", "oauth.json")) {
		t.Fatal("OAuth clear backup is missing")
	}
	routes, err := fixture.store.ReadRoutes(context.Background())
	if err != nil || routes["alice@example.com"] != "alpha" {
		t.Fatalf("OAuth clear routes = %#v err=%v", routes, err)
	}
}

func TestManagerClearAuthDrainFailureDoesNotTouchOAuthOrRuntime(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	fixture.drainer.err = errors.New("stream still active")
	_, err := fixture.manager.ClearAuth(context.Background(), "alpha")
	if !errors.Is(err, ErrAccountDrainTimeout) {
		t.Fatalf("ClearAuth error = %v, want ErrAccountDrainTimeout", err)
	}
	if got := readTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json")); got != "oauth-secret" {
		t.Fatalf("OAuth changed before drain: %q", got)
	}
	if fixture.runtime.restartCalls != 0 {
		t.Fatalf("runtime restarted before drain: %d", fixture.runtime.restartCalls)
	}
	routes, readError := fixture.store.ReadRoutes(context.Background())
	if readError != nil || routes["alice@example.com"] != "alpha" || fixture.snapshots.calls != 2 {
		t.Fatalf("routes after OAuth drain failure = %#v snapshots=%d err=%v", routes, fixture.snapshots.calls, readError)
	}
}

func TestManagerClearAuthWithoutRoutesDoesNotDrainOrPublishSnapshot(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	if _, err := fixture.store.ApplyRoutesExpected(
		context.Background(),
		map[string]string{"alice@example.com": "beta"},
		map[string]string{"alice@example.com": "alpha"},
	); err != nil {
		t.Fatalf("move route away from account: %v", err)
	}
	if _, err := fixture.manager.ClearAuth(context.Background(), "alpha"); err != nil {
		t.Fatalf("ClearAuth: %v", err)
	}
	if fixture.runtime.restartCalls != 1 || fixture.drainer.calls != 0 || fixture.snapshots.calls != 0 {
		t.Fatalf(
			"route-free OAuth clear: restart=%d drainer=%d snapshots=%d",
			fixture.runtime.restartCalls, fixture.drainer.calls, fixture.snapshots.calls,
		)
	}
}

func TestManagerRecoverAcceptedOperationDoesNotRestartActiveAccount(t *testing.T) {
	fixture := newManagerFixture(t)
	if _, err := fixture.manager.beginOperation(context.Background(), operationClearAuth, "alpha", ""); err != nil {
		t.Fatalf("beginOperation: %v", err)
	}
	if err := fixture.manager.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if fixture.runtime.restartCalls != 0 || fixture.runtime.reconcileCalls != 0 || fixture.snapshots.calls != 0 {
		t.Fatalf(
			"accepted recovery touched runtime: restart=%d reconcile=%d snapshots=%d",
			fixture.runtime.restartCalls, fixture.runtime.reconcileCalls, fixture.snapshots.calls,
		)
	}
}

func TestManagerRecoverAcceptedCreateCleansPreparedDirectoriesWithoutRuntime(t *testing.T) {
	fixture := newManagerFixture(t)
	operation, err := fixture.manager.beginOperation(context.Background(), operationCreate, "gamma", "")
	if err != nil {
		t.Fatalf("beginOperation: %v", err)
	}
	if _, err := fixture.manager.files.PrepareCreate(operation.ID, "gamma"); err != nil {
		t.Fatalf("PrepareCreate: %v", err)
	}
	if !directory(filepath.Join(fixture.root, "auth", "gamma")) ||
		!directory(filepath.Join(fixture.root, "logs", "gamma")) {
		t.Fatal("prepared account directories are missing")
	}
	if err := fixture.manager.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if exists(filepath.Join(fixture.root, "auth", "gamma")) ||
		exists(filepath.Join(fixture.root, "logs", "gamma")) {
		t.Fatal("accepted account creation files remain after recovery")
	}
	if fixture.runtime.restartCalls != 0 || fixture.runtime.reconcileCalls != 0 || fixture.snapshots.calls != 0 {
		t.Fatalf(
			"accepted create recovery touched runtime: restart=%d reconcile=%d snapshots=%d",
			fixture.runtime.restartCalls, fixture.runtime.reconcileCalls, fixture.snapshots.calls,
		)
	}
}

func TestManagerRecoverAcceptedDeleteBackupDoesNotRestartActiveAccount(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	stored, rows, err := fixture.store.ReadAccountLifecycle(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("ReadAccountLifecycle: %v", err)
	}
	operation, err := fixture.manager.beginOperation(context.Background(), operationDelete, "alpha", "")
	if err != nil {
		t.Fatalf("beginOperation: %v", err)
	}
	transition, err := fixture.manager.files.PrepareDelete(
		operation.ID, "alpha", BackupData{Account: stored.Account, Keys: rows},
	)
	if err != nil {
		t.Fatalf("PrepareDelete: %v", err)
	}
	if transition.BackupPath() == "" || !directory(filepath.Join(fixture.root, filepath.FromSlash(transition.BackupPath()))) {
		t.Fatalf("prepared delete backup = %q", transition.BackupPath())
	}
	if err := fixture.manager.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := readTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json")); got != "oauth-secret" {
		t.Fatalf("active OAuth changed during accepted delete recovery: %q", got)
	}
	if fixture.runtime.restartCalls != 0 || fixture.runtime.reconcileCalls != 0 || fixture.snapshots.calls != 0 {
		t.Fatalf(
			"accepted delete recovery touched runtime: restart=%d reconcile=%d snapshots=%d",
			fixture.runtime.restartCalls, fixture.runtime.reconcileCalls, fixture.snapshots.calls,
		)
	}
}

func TestManagerRecoverRoutesPreparedRestoresRouteWithoutRestart(t *testing.T) {
	fixture := newManagerFixture(t)
	operation, err := fixture.manager.beginOperation(context.Background(), operationClearAuth, "alpha", "")
	if err != nil {
		t.Fatalf("beginOperation: %v", err)
	}
	operation.EvacuatedRoutes = map[string]string{"alice@example.com": "beta"}
	if err := fixture.manager.advanceOperation(context.Background(), &operation, phaseRoutesPrepared, ""); err != nil {
		t.Fatalf("advanceOperation: %v", err)
	}
	if _, err := fixture.store.ApplyRoutesExpected(
		context.Background(),
		map[string]string{"alice@example.com": "beta"},
		map[string]string{"alice@example.com": "alpha"},
	); err != nil {
		t.Fatalf("evacuate route before interruption: %v", err)
	}
	if err := fixture.manager.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	routes, err := fixture.store.ReadRoutes(context.Background())
	if err != nil || routes["alice@example.com"] != "alpha" {
		t.Fatalf("early recovery routes = %#v err=%v", routes, err)
	}
	if fixture.runtime.restartCalls != 0 || fixture.runtime.reconcileCalls != 0 || fixture.snapshots.calls != 1 {
		t.Fatalf(
			"early recovery runtime: restart=%d reconcile=%d snapshots=%d",
			fixture.runtime.restartCalls, fixture.runtime.reconcileCalls, fixture.snapshots.calls,
		)
	}
}

func TestManagerRecoverInterruptedRenameUsesCanonicalStateAndPreservesKeys(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	writeTestFile(t, filepath.Join(fixture.root, "logs", "alpha", "main.log"), "runtime-log")
	stored, rows, err := fixture.store.ReadAccountLifecycle(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("ReadAccountLifecycle: %v", err)
	}
	operation, err := fixture.manager.beginOperation(context.Background(), operationUpdate, "alpha", "gamma")
	if err != nil {
		t.Fatalf("beginOperation: %v", err)
	}
	operation.EvacuatedRoutes = map[string]string{"alice@example.com": "beta"}
	if err := fixture.manager.advanceOperation(context.Background(), &operation, phaseRoutesPrepared, ""); err != nil {
		t.Fatalf("journal route evacuation: %v", err)
	}
	if _, err := fixture.store.ApplyRoutesExpected(
		context.Background(),
		map[string]string{"alice@example.com": "beta"},
		map[string]string{"alice@example.com": "alpha"},
	); err != nil {
		t.Fatalf("evacuate route before interruption: %v", err)
	}
	transition, err := fixture.manager.files.PrepareUpdate(
		operation.ID, "alpha", "gamma", BackupData{Account: stored.Account, Keys: rows},
	)
	if err != nil {
		t.Fatalf("PrepareUpdate: %v", err)
	}
	// Simulate a process stop after only one of the two directory moves became
	// durable and after SQLite committed the rename.
	if err := os.Rename(
		filepath.Join(fixture.root, "logs", "gamma"),
		filepath.Join(fixture.root, "logs", "alpha"),
	); err != nil {
		t.Fatalf("simulate partial directory move: %v", err)
	}
	if _, err := fixture.store.ApplyAccountUpdate(context.Background(), controlplane.AccountUpdateRequest{
		AccountID: "alpha", NewAccountID: "gamma", Email: "gamma@accounts.example.com",
		ProxyMode: "inherit",
	}); err != nil {
		t.Fatalf("ApplyAccountUpdate: %v", err)
	}
	if err := fixture.manager.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if transition.BackupPath() == "" || fixture.runtime.reconcilePrevious != "alpha" ||
		fixture.runtime.reconcileDesired == nil || fixture.runtime.reconcileDesired.ID != "gamma" {
		t.Fatalf("recovery runtime = previous=%q desired=%#v backup=%q", fixture.runtime.reconcilePrevious, fixture.runtime.reconcileDesired, transition.BackupPath())
	}
	if got := readTestFile(t, filepath.Join(fixture.root, "auth", "gamma", "oauth.json")); got != "oauth-secret" {
		t.Fatalf("recovered OAuth = %q", got)
	}
	if got := readTestFile(t, filepath.Join(fixture.root, "logs", "gamma", "main.log")); got != "runtime-log" {
		t.Fatalf("recovered log = %q", got)
	}
	routes, err := fixture.store.ReadRoutes(context.Background())
	if err != nil || routes["alice@example.com"] != "gamma" {
		t.Fatalf("recovered routes = %#v err=%v", routes, err)
	}
	var journal Operation
	if found, err := fixture.store.ReadRuntimeState(context.Background(), lifecycleJournalStateName, &journal); err != nil || found {
		t.Fatalf("lifecycle journal remained after recovery: found=%v err=%v journal=%#v", found, err, journal)
	}
	assertUnifiedKeyMatrix(t, fixture.store, map[string]string{
		"alice@example.com": "cpa_external_alice",
		"bob@example.com":   "cpa_external_bob",
	}, 2)
}

func TestManagerRecoverInterruptedDeleteActivatesSnapshotBeforeCleanup(t *testing.T) {
	fixture := newManagerFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "auth", "alpha", "oauth.json"), "oauth-secret")
	writeTestFile(t, filepath.Join(fixture.root, "logs", "alpha", "main.log"), "runtime-log")
	stored, rows, err := fixture.store.ReadAccountLifecycle(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("ReadAccountLifecycle: %v", err)
	}
	operation, err := fixture.manager.beginOperation(context.Background(), operationDelete, "alpha", "")
	if err != nil {
		t.Fatalf("beginOperation: %v", err)
	}
	if _, err := fixture.manager.files.PrepareDelete(
		operation.ID, "alpha", BackupData{Account: stored.Account, Keys: rows},
	); err != nil {
		t.Fatalf("PrepareDelete: %v", err)
	}
	if _, err := fixture.store.ApplyAccountDeletion(context.Background(), "alpha", "beta", true); err != nil {
		t.Fatalf("ApplyAccountDeletion: %v", err)
	}
	if err := fixture.manager.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if fixture.snapshots.calls != 1 || fixture.drainer.calls != 1 || fixture.runtime.reconcileDesired != nil || fixture.runtime.reconcilePrevious != "alpha" {
		t.Fatalf(
			"delete recovery ordering calls: snapshots=%d drainer=%d previous=%q desired=%#v",
			fixture.snapshots.calls, fixture.drainer.calls, fixture.runtime.reconcilePrevious, fixture.runtime.reconcileDesired,
		)
	}
	for _, path := range []string{
		filepath.Join(fixture.root, "configs", "alpha.yaml"),
		filepath.Join(fixture.root, "auth", "alpha"),
		filepath.Join(fixture.root, "logs", "alpha"),
	} {
		if exists(path) {
			t.Fatalf("deleted recovery path remains: %s", path)
		}
	}
	assertUnifiedKeyMatrix(t, fixture.store, map[string]string{
		"alice@example.com": "cpa_external_alice",
		"bob@example.com":   "cpa_external_bob",
	}, 1)
}

func TestManagerRecoverInterruptedDisableLeavesEvacuatedRoutesOnFallback(t *testing.T) {
	fixture := newManagerFixture(t)
	operation, err := fixture.manager.beginOperation(context.Background(), operationUpdate, "alpha", "alpha")
	if err != nil {
		t.Fatalf("beginOperation: %v", err)
	}
	operation.EvacuatedRoutes = map[string]string{"alice@example.com": "beta"}
	if err := fixture.manager.advanceOperation(context.Background(), &operation, phaseRoutesEvacuated, ""); err != nil {
		t.Fatalf("journal route evacuation: %v", err)
	}
	if _, err := fixture.store.ApplyRoutesExpected(
		context.Background(),
		map[string]string{"alice@example.com": "beta"},
		map[string]string{"alice@example.com": "alpha"},
	); err != nil {
		t.Fatalf("evacuate route before interruption: %v", err)
	}
	disabled := false
	notDefault := false
	if _, err := fixture.store.ApplyAccountUpdate(context.Background(), controlplane.AccountUpdateRequest{
		AccountID: "alpha", NewAccountID: "alpha", Email: "alpha@accounts.example.com",
		ProxyMode: "inherit", GroupEnabled: &disabled, DefaultGroup: &notDefault, FallbackAccount: "beta",
	}); err != nil {
		t.Fatalf("ApplyAccountUpdate: %v", err)
	}
	if err := fixture.manager.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	routes, err := fixture.store.ReadRoutes(context.Background())
	if err != nil || routes["alice@example.com"] != "beta" {
		t.Fatalf("disabled recovery routes = %#v err=%v", routes, err)
	}
	accounts, err := fixture.store.ReadAccounts(context.Background())
	if err != nil || accounts[0].GroupEnabled {
		t.Fatalf("disabled recovery account = %#v err=%v", accounts, err)
	}
	assertUnifiedKeyMatrix(t, fixture.store, map[string]string{
		"alice@example.com": "cpa_external_alice",
		"bob@example.com":   "cpa_external_bob",
	}, 2)
}

func TestManagerRecoveryFailsClosedWhenCustomProxySecretIsUnavailable(t *testing.T) {
	fixture := newManagerFixture(t)
	operation, err := fixture.manager.beginOperation(context.Background(), operationCreate, "gamma", "")
	if err != nil {
		t.Fatalf("beginOperation: %v", err)
	}
	if _, err := fixture.manager.files.PrepareCreate(operation.ID, "gamma"); err != nil {
		t.Fatalf("PrepareCreate: %v", err)
	}
	if _, err := fixture.store.ApplyAccountCreation(context.Background(), controlplane.Account{
		ID: "gamma", Email: "gamma@accounts.example.com", Port: 18320, ProxyMode: "custom",
	}); err != nil {
		t.Fatalf("ApplyAccountCreation: %v", err)
	}
	err = fixture.manager.Recover(context.Background())
	if !errors.Is(err, ErrLifecycleRecoveryRequired) {
		t.Fatalf("Recover error = %v, want ErrLifecycleRecoveryRequired", err)
	}
	var journal Operation
	if found, readError := fixture.store.ReadRuntimeState(context.Background(), lifecycleJournalStateName, &journal); readError != nil || !found {
		t.Fatalf("failed recovery journal was not retained: found=%v err=%v", found, readError)
	}
	assertUnifiedKeyMatrix(t, fixture.store, map[string]string{
		"alice@example.com": "cpa_external_alice",
		"bob@example.com":   "cpa_external_bob",
	}, 3)
}

type managerFixture struct {
	root      string
	store     *controlplane.Store
	runtime   *fakeAccountRuntime
	snapshots *fakeAccountSnapshots
	drainer   *fakeAccountDrainer
	manager   *Manager
}

func newManagerFixture(t *testing.T) managerFixture {
	t.Helper()
	root := t.TempDir()
	store, err := controlplane.Open(context.Background(), root, controlplane.Options{
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("open lifecycle store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []controlplane.Account{
		{ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318, ProxyMode: "inherit", CreatedAt: 10, GroupEnabled: true, DefaultGroup: true},
		{ID: "beta", Email: "beta@accounts.example.com", Port: 18321, ProxyMode: "direct", CreatedAt: 20, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("seed lifecycle accounts: %v", err)
	}
	if err := store.WriteKeyRecords(ctx, []controlplane.KeyRecord{
		{Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "alice@example.com", Status: "active", Key: "cpa_external_alice", CreatedAt: 100, UpdatedAt: 100},
		{Label: "alice@example.com:beta", Account: "beta", AccountEmail: "beta@accounts.example.com", User: "alice@example.com", Status: "active", Key: "cpa_external_alice", CreatedAt: 100, UpdatedAt: 100},
		{Label: "bob@example.com:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "bob@example.com", Status: "active", Key: "cpa_external_bob", CreatedAt: 100, UpdatedAt: 100},
		{Label: "bob@example.com:beta", Account: "beta", AccountEmail: "beta@accounts.example.com", User: "bob@example.com", Status: "active", Key: "cpa_external_bob", CreatedAt: 100, UpdatedAt: 100},
	}); err != nil {
		t.Fatalf("seed lifecycle Key records: %v", err)
	}
	if err := store.WriteRoutes(ctx, map[string]string{
		"alice@example.com": "alpha", "bob@example.com": "beta",
	}); err != nil {
		t.Fatalf("seed lifecycle routes: %v", err)
	}
	if err := store.WriteSecret(ctx, managementKeySecretName, "management-key-for-tests"); err != nil {
		t.Fatalf("seed lifecycle management key: %v", err)
	}
	projection := &accountprojection.Renderer{Root: root, Store: store}
	if _, err := projection.Render(ctx); err != nil {
		t.Fatalf("render initial lifecycle projection: %v", err)
	}
	runtime := &fakeAccountRuntime{ports: make(map[int]struct{}), transition: &fakeRuntimeTransition{}}
	snapshots := &fakeAccountSnapshots{}
	drainer := &fakeAccountDrainer{}
	manager, err := New(Config{
		Store: store, Files: &FileManager{Root: root}, Projection: projection,
		Snapshots: snapshots, Runtime: runtime, Drainer: drainer, States: fakeAccountStates{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return managerFixture{root: root, store: store, runtime: runtime, snapshots: snapshots, drainer: drainer, manager: manager}
}

type fakeAccountRuntime struct {
	ports             map[int]struct{}
	prepareError      error
	transition        *fakeRuntimeTransition
	lastCreate        controlplane.Account
	lastUpdateBefore  controlplane.Account
	lastUpdateAfter   controlplane.Account
	lastDelete        controlplane.Account
	restartErrors     []error
	restartCalls      int
	reconcileCalls    int
	reconcilePrevious string
	reconcileDesired  *controlplane.Account
}

func (runtime *fakeAccountRuntime) ReservedHostPorts(context.Context) (map[int]struct{}, error) {
	result := make(map[int]struct{}, len(runtime.ports))
	for port := range runtime.ports {
		result[port] = struct{}{}
	}
	return result, nil
}

func (runtime *fakeAccountRuntime) PrepareCreate(_ context.Context, account controlplane.Account) (RuntimeTransition, error) {
	runtime.lastCreate = account
	return runtime.transitionResult()
}

func (runtime *fakeAccountRuntime) PrepareUpdate(_ context.Context, before, after controlplane.Account) (RuntimeTransition, error) {
	runtime.lastUpdateBefore, runtime.lastUpdateAfter = before, after
	return runtime.transitionResult()
}

func (runtime *fakeAccountRuntime) PrepareDelete(_ context.Context, account controlplane.Account) (RuntimeTransition, error) {
	runtime.lastDelete = account
	return runtime.transitionResult()
}

func (runtime *fakeAccountRuntime) transitionResult() (RuntimeTransition, error) {
	if runtime.prepareError != nil {
		return nil, runtime.prepareError
	}
	return runtime.transition, nil
}

func (runtime *fakeAccountRuntime) RestartAccount(context.Context, string) error {
	index := runtime.restartCalls
	runtime.restartCalls++
	if index < len(runtime.restartErrors) {
		return runtime.restartErrors[index]
	}
	return nil
}

func (runtime *fakeAccountRuntime) ReconcileAccount(
	_ context.Context,
	previous string,
	desired *controlplane.Account,
) error {
	runtime.reconcileCalls++
	runtime.reconcilePrevious = previous
	if desired == nil {
		runtime.reconcileDesired = nil
		return nil
	}
	copy := *desired
	runtime.reconcileDesired = &copy
	return nil
}

type fakeRuntimeTransition struct {
	commits       int
	rollbacks     int
	commitError   error
	rollbackError error
}

func (transition *fakeRuntimeTransition) Commit(context.Context) error {
	transition.commits++
	return transition.commitError
}

func (transition *fakeRuntimeTransition) Rollback(context.Context) error {
	transition.rollbacks++
	return transition.rollbackError
}

type fakeAccountSnapshots struct {
	calls    int
	failures []error
}

type fakeAccountDrainer struct {
	calls int
	err   error
}

type fakeAccountStates struct {
	states map[string]failover.AccountState
}

func (provider fakeAccountStates) AccountStates(context.Context) (map[string]failover.AccountState, error) {
	if provider.states != nil {
		return provider.states, nil
	}
	return map[string]failover.AccountState{
		"alpha": {Account: "alpha", Eligible: true, Headroom: 80},
		"beta":  {Account: "beta", Eligible: true, Headroom: 90},
		"gamma": {Account: "gamma", Eligible: true, Headroom: 70},
	}, nil
}

func (drainer *fakeAccountDrainer) WaitAccountDrained(context.Context, string) error {
	drainer.calls++
	return drainer.err
}

func (publisher *fakeAccountSnapshots) PublishAuthSnapshot(context.Context, bool) (failover.Snapshot, error) {
	publisher.calls++
	if publisher.calls <= len(publisher.failures) && publisher.failures[publisher.calls-1] != nil {
		return failover.Snapshot{}, publisher.failures[publisher.calls-1]
	}
	return failover.Snapshot{Generation: fmt.Sprintf("generation-%d", publisher.calls)}, nil
}

func assertUnifiedKeyMatrix(
	t *testing.T,
	store *controlplane.Store,
	want map[string]string,
	wantAccounts int,
) {
	t.Helper()
	records, err := store.ReadKeyRecords(context.Background())
	if err != nil {
		t.Fatalf("ReadKeyRecords: %v", err)
	}
	counts := make(map[string]int)
	for _, record := range records {
		if record.Status != "active" {
			continue
		}
		if record.Key != want[record.User] {
			t.Fatalf("API Key bytes changed for %s: %q", record.User, record.Key)
		}
		counts[record.User]++
	}
	for user := range want {
		if counts[user] != wantAccounts {
			t.Fatalf("active account rows for %s = %d, want %d", user, counts[user], wantAccounts)
		}
	}
}
