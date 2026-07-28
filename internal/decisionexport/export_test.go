package decisionexport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/store"
)

func TestWriteProducesStableFilteredYAML(t *testing.T) {
	root := t.TempDir()
	result, err := Write(
		root,
		"reviews/fleet-decisions.yaml",
		[]store.Decision{
			{
				MachineID: "unknown", InventoryKey: "brew|formula||unknown",
				Value: "ignore",
			},
			{
				MachineID: "example", InventoryKey: "brew|formula|homebrew/core|fzf",
				Value: "adopt", Profile: "shared",
			},
		},
		map[string]bool{"example": true},
	)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("result count = %d, want 1", result.Count)
	}
	raw, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, expected := range []string{
		"version: 1",
		"machine: example",
		"inventory_key: brew|formula|homebrew/core|fzf",
		"decision: adopt",
		"profile: shared",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("export does not contain %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "unknown") {
		t.Fatalf("export contains unknown machine:\n%s", text)
	}
	info, err := os.Stat(filepath.Join(root, "reviews", "fleet-decisions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("export permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteRejectsPathEscape(t *testing.T) {
	_, err := Write(t.TempDir(), "../outside.yaml", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Write() error = %v, want path escape error", err)
	}
}
