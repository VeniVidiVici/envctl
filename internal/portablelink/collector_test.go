package portablelink

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestCollectDescribesRelativeSymlinkAndSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "repo", "config.json")
	target := filepath.Join(root, "home", ".config", "app", "config.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(filepath.Dir(target), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, target); err != nil {
		t.Fatal(err)
	}

	got := Collect([]model.LinkSpec{{
		ID: "app-config", Source: source, Target: target, Kind: model.LinkKindFile,
	}})
	if len(got) != 1 || got[0].SourceType != "file" ||
		got[0].TargetType != "symlink" ||
		got[0].ResolvedTarget != source ||
		got[0].LinkTarget != relative {
		t.Fatalf("observations = %#v", got)
	}
}

func TestCollectReportsAbsentAndOccupiedTargets(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	occupied := filepath.Join(root, "occupied")
	if err := os.WriteFile(occupied, []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Collect([]model.LinkSpec{
		{ID: "absent", Source: source, Target: filepath.Join(root, "missing")},
		{ID: "occupied", Source: source, Target: occupied},
	})
	if got[0].TargetType != "absent" || got[1].TargetType != "file" {
		t.Fatalf("observations = %#v", got)
	}
}
