package fleetrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type runnerCall struct {
	name string
	args []string
}

type fakeRunner struct {
	mu    sync.Mutex
	run   func(name string, args ...string) ([]byte, []byte, error)
	calls []runnerCall
}

func (r *fakeRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, []byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, runnerCall{name: name, args: append([]string(nil), args...)})
	r.mu.Unlock()
	return r.run(name, args...)
}

func TestRefreshWritesLocalInventoryAndStatus(t *testing.T) {
	inventory := model.Inventory{
		CollectedAt: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
		Collectors:  []string{"homebrew"},
	}
	raw, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{run: func(name string, args ...string) ([]byte, []byte, error) {
		if name != "/tmp/envctl" {
			t.Fatalf("command = %q, want /tmp/envctl", name)
		}
		return raw, nil, nil
	}}
	directory := t.TempDir()
	status, err := New("/tmp/envctl", directory, runner).Refresh(
		context.Background(),
		[]Target{{ID: "local-mac", AccessType: "local"}},
	)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if len(status.Results) != 1 || status.Results[0].Status != "ok" {
		t.Fatalf("status = %#v", status)
	}
	info, err := os.Stat(filepath.Join(directory, "local-mac.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("inventory permissions = %o, want 600", info.Mode().Perm())
	}
	if _, err := LoadStatus(directory); err != nil {
		t.Fatalf("LoadStatus() error = %v", err)
	}
}

func TestRefreshRetainsLastGoodSnapshotOnRemoteFailure(t *testing.T) {
	directory := t.TempDir()
	snapshotPath := filepath.Join(directory, "remote-mac.json")
	if err := os.WriteFile(snapshotPath, []byte(`{"existing":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{run: func(string, ...string) ([]byte, []byte, error) {
		return nil, []byte("Host key verification failed"), errors.New("exit status 255")
	}}

	status, err := New("/tmp/envctl", directory, runner).Refresh(
		context.Background(),
		[]Target{{ID: "remote-mac", AccessType: "ssh", Host: "remote-mac"}},
	)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	result := status.Results[0]
	if result.Status != "error" || !result.RetainedLastGood ||
		!strings.Contains(result.Error, "Host key verification failed") {
		t.Fatalf("result = %#v", result)
	}
	raw, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"existing":true}` {
		t.Fatalf("last good snapshot changed to %q", raw)
	}
}

func TestPartialRefreshPreservesOtherMachineStatus(t *testing.T) {
	directory := t.TempDir()
	previous := Status{
		StartedAt:   time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC),
		CompletedAt: time.Date(2026, 7, 28, 10, 1, 0, 0, time.UTC),
		Results: []Result{
			{MachineID: "local-mac", AccessType: "local", Status: "ok"},
			{MachineID: "remote-mac", AccessType: "ssh", Status: "ok"},
		},
	}
	if err := writeJSONAtomically(
		filepath.Join(directory, StatusFilename), previous,
	); err != nil {
		t.Fatal(err)
	}
	inventory := model.Inventory{
		CollectedAt: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
		Collectors:  []string{"homebrew"},
	}
	raw, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{run: func(string, ...string) ([]byte, []byte, error) {
		return raw, nil, nil
	}}
	status, err := New("/tmp/envctl", directory, runner).Refresh(
		context.Background(),
		[]Target{{ID: "local-mac", AccessType: "local"}},
	)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if len(status.Results) != 1 {
		t.Fatalf("returned current results = %#v", status.Results)
	}
	persisted, err := LoadStatus(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Results) != 2 ||
		persisted.Results[0].MachineID != "local-mac" ||
		persisted.Results[1].MachineID != "remote-mac" {
		t.Fatalf("persisted results = %#v", persisted.Results)
	}
}

func TestCollectRemoteIsAgentlessAndCleansUp(t *testing.T) {
	inventory := model.Inventory{
		CollectedAt: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
		Collectors:  []string{"homebrew"},
	}
	raw, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{run: func(name string, args ...string) ([]byte, []byte, error) {
		switch name {
		case "ssh":
			last := args[len(args)-1]
			switch {
			case last == "uname -s; uname -m":
				return []byte("Darwin\narm64\n"), nil, nil
			case strings.Contains(last, " audit --json --no-record"):
				return raw, nil, nil
			case strings.HasPrefix(last, "rm -f /tmp/envctl-fleet-"):
				return nil, nil, nil
			default:
				t.Fatalf("unexpected ssh command %q", last)
			}
		case "scp":
			return nil, nil, nil
		default:
			t.Fatalf("unexpected command %q", name)
		}
		return nil, nil, nil
	}}
	refresher := New("/tmp/envctl", "", runner)
	collected, err := refresher.Collect(context.Background(), Target{
		ID: "remote-mac", AccessType: "ssh", Host: "remote-mac",
		Links: []model.LinkSpec{{
			ID: "example", Source: "/Users/example/repo/config",
			Target: "/Users/example/.config/example", Kind: model.LinkKindFile,
			Digest: strings.Repeat("a", 64),
		}},
	})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if !collected.CollectedAt.Equal(inventory.CollectedAt) {
		t.Fatalf("inventory = %#v", collected)
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 4 {
		t.Fatalf("calls = %#v, want platform, copy, audit, cleanup", runner.calls)
	}
	for _, call := range runner.calls {
		if call.name != "ssh" && call.name != "scp" {
			continue
		}
		joined := strings.Join(call.args, " ")
		if !strings.Contains(joined, "StrictHostKeyChecking=yes") {
			t.Fatalf("strict host key option missing from %#v", call)
		}
		if !strings.Contains(joined, "ControlMaster=no") ||
			!strings.Contains(joined, "ControlPath=none") {
			t.Fatalf("independent connection options missing from %#v", call)
		}
		if call.name == "ssh" &&
			strings.Contains(joined, " audit --json --no-record") &&
			!strings.Contains(joined, " --link-specs ") {
			t.Fatalf("portable link specifications missing from %#v", call)
		}
	}
}

func TestCollectRejectsUnsafeTargetBeforeRunning(t *testing.T) {
	runner := &fakeRunner{run: func(string, ...string) ([]byte, []byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil, nil
	}}
	_, err := New("/tmp/envctl", "", runner).Collect(
		context.Background(),
		Target{ID: "../bad", AccessType: "ssh", Host: "remote-mac"},
	)
	if err == nil {
		t.Fatal("Collect() error = nil, want unsafe target rejection")
	}
}
