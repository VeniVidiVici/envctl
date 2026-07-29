package bootstrapverify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	output string
	err    error
}

func (r fakeRunner) Run(
	context.Context,
	string,
	...string,
) ([]byte, error) {
	return []byte(r.output), r.err
}

func TestCurrentBootIDRequiresNonemptyValue(t *testing.T) {
	got, err := CurrentBootID(
		context.Background(),
		fakeRunner{output: "{ sec = 123, usec = 4 }\n"},
	)
	if err != nil || got != "{ sec = 123, usec = 4 }" {
		t.Fatalf("boot id = %q, %v", got, err)
	}
	if _, err := CurrentBootID(
		context.Background(),
		fakeRunner{output: " \n"},
	); err == nil {
		t.Fatal("empty boot id was accepted")
	}
}

func TestCheckpointRoundTripUsesProtectedRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap", "checkpoint.json")
	want := Checkpoint{
		MachineID:    "example",
		ConfigDigest: strings.Repeat("a", 64),
		BootID:       "boot-one",
		CreatedAt:    time.Unix(123, 0).UTC(),
	}
	if err := SaveCheckpoint(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != Version || got.MachineID != want.MachineID ||
		got.BootID != want.BootID {
		t.Fatalf("checkpoint = %#v", got)
	}
}

func TestCheckpointRefusesSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "checkpoint.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	err := SaveCheckpoint(path, Checkpoint{})
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("error = %v", err)
	}
}
