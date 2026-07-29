package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapPinsSOPSIdentity(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "scripts", "bootstrap-macos")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read bootstrap script: %v", err)
	}
	script := string(raw)
	required := []string{
		`export SOPS_AGE_KEY_FILE="$LOCAL_AGE_IDENTITY"`,
		"SOPS_AGE_KEY_CMD",
		"SOPS_AGE_SSH_PRIVATE_KEY_CMD",
	}
	for _, fragment := range required {
		if !strings.Contains(script, fragment) {
			t.Fatalf("bootstrap script does not pin SOPS identity: missing %q", fragment)
		}
	}
}
