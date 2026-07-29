package main

import (
	"os"
	"path/filepath"
	"testing"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
	"github.com/VeniVidiVici/envctl/internal/model"
	"github.com/VeniVidiVici/envctl/internal/onboard"
)

func TestResolveConfigRootPrefersBootstrapCheckoutThenDocuments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(configEnvironment, "")
	bootstrap := filepath.Join(
		home, ".local", "share", "envctl", "repos", "env-config",
	)
	documents := filepath.Join(home, "Documents", "env-config")
	writeRootMarker(t, documents)
	got, err := resolveConfigRoot("")
	if err != nil || got != documents {
		t.Fatalf("resolveConfigRoot() = %q, %v; want %q", got, err, documents)
	}
	writeRootMarker(t, bootstrap)
	got, err = resolveConfigRoot("")
	if err != nil || got != bootstrap {
		t.Fatalf("resolveConfigRoot() = %q, %v; want %q", got, err, bootstrap)
	}
}

func TestResolveConfigRootHonorsEnvironment(t *testing.T) {
	root := t.TempDir()
	writeRootMarker(t, root)
	t.Setenv(configEnvironment, root)
	got, err := resolveConfigRoot("")
	if err != nil || got != root {
		t.Fatalf("resolveConfigRoot() = %q, %v; want %q", got, err, root)
	}
}

func TestResolveInventoryDirectoryUsesStableStateDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(inventoryEnvironment, "")
	got, err := resolveInventoryDirectory("")
	want := filepath.Join(home, ".local", "state", "envctl", "inventory")
	if err != nil || got != want {
		t.Fatalf(
			"resolveInventoryDirectory() = %q, %v; want %q", got, err, want,
		)
	}
}

func TestMatchConfiguredMachineUsesHardwareFingerprint(t *testing.T) {
	got, err := matchConfiguredMachine(
		onboard.Identity{HardwareUUIDSHA256: "fingerprint"},
		[]envconfig.Machine{
			{ID: "other", Match: envconfig.Match{HardwareUUIDSHA256: "other"}},
			{ID: "current", Match: envconfig.Match{HardwareUUIDSHA256: "fingerprint"}},
		},
	)
	if err != nil || got != "current" {
		t.Fatalf("matchConfiguredMachine() = %q, %v", got, err)
	}
}

func TestWriteInventoryAtomicallyCreatesPrivateJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "machine.json")
	if err := writeInventory(path, model.Inventory{
		Collectors: []string{"homebrew"},
	}); err != nil {
		t.Fatalf("writeInventory() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("inventory mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := loadInventory(path); err == nil {
		t.Fatal("loadInventory() accepted inventory without collection time")
	}
}

func writeRootMarker(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "envctl.yaml"), []byte("version: 1\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
}
