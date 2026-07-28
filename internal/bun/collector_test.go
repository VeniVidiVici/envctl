package bun

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectsGlobalManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package.json")
	if err := os.WriteFile(path, []byte(`{
	  "dependencies": {
	    "example-tool": "^1.2.3",
	    "@example/scoped": "~4.5.6"
	  }
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := NewCollector(path).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Collect() returned %d packages, want 2", len(got))
	}
	if got[0].Package != "@example/scoped" || got[0].Version != "4.5.6" {
		t.Fatalf("first package = %#v", got[0])
	}
}

func TestMissingManifestMeansNoPackages(t *testing.T) {
	got, err := NewCollector(filepath.Join(t.TempDir(), "missing.json")).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Collect() returned %#v, want empty", got)
	}
}
