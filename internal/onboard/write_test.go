package onboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
)

func TestWriteMachineCreatesNewFileAtomically(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "machines"), 0o700); err != nil {
		t.Fatal(err)
	}
	machine := envconfig.Machine{
		ID: "new-mac",
		Match: envconfig.Match{
			HardwareUUIDSHA256: strings.Repeat("a", 64),
		},
		Profiles: []string{"shared"},
		Access:   envconfig.Access{Type: "ssh", Host: "new-mac"},
	}
	path, err := WriteMachine(root, machine, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != "machines/new-mac.yaml" {
		t.Fatalf("path = %q", path)
	}
	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, expected := range []string{
		"version: 1",
		"id: new-mac",
		"hardware_uuid_sha256: " + strings.Repeat("a", 64),
		"- shared",
		"type: ssh",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("machine file does not contain %q:\n%s", expected, text)
		}
	}
}

func TestWriteMachineRefusesDirtyExistingFile(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "envctl@example.invalid")
	runGit(t, root, "config", "user.name", "envctl test")
	machines := filepath.Join(root, "machines")
	if err := os.Mkdir(machines, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(machines, "existing.yaml")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "machines/existing.yaml")
	runGit(t, root, "commit", "-m", "fixture")
	if err := os.WriteFile(path, []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteMachine(root, envconfig.Machine{
		ID: "existing",
		Match: envconfig.Match{
			HardwareUUIDSHA256: strings.Repeat("b", 64),
		},
		Profiles: []string{"shared"},
		Access:   envconfig.Access{Type: "local"},
	}, true)
	if err == nil || !strings.Contains(err.Error(), "locally modified") {
		t.Fatalf("error = %v", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != "local edit\n" {
		t.Fatalf("dirty content was overwritten: %q", raw)
	}
}

func TestWriteMachineReplacesCleanTrackedFile(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "envctl@example.invalid")
	runGit(t, root, "config", "user.name", "envctl test")
	machines := filepath.Join(root, "machines")
	if err := os.Mkdir(machines, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(machines, "existing.yaml")
	original := `version: 1
id: existing
profiles:
  - shared
access:
  type: local
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "machines/existing.yaml")
	runGit(t, root, "commit", "-m", "fixture")

	relative, err := WriteMachine(root, envconfig.Machine{
		ID: "existing",
		Match: envconfig.Match{
			HardwareUUIDSHA256: strings.Repeat("c", 64),
		},
		Profiles: []string{"shared"},
		Access:   envconfig.Access{Type: "local"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if relative != "machines/existing.yaml" {
		t.Fatalf("path = %q", relative)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		string(raw),
		"hardware_uuid_sha256: "+strings.Repeat("c", 64),
	) {
		t.Fatalf("updated machine file = %s", raw)
	}
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", directory}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
