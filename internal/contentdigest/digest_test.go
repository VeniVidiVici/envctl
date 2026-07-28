package contentdigest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryDigestChangesWithRelativePathAndContent(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, directory := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(directory, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(directory, "nested", "config"), []byte("one"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	firstDigest, err := Directory(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := Directory(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("equivalent trees differ: %s != %s", firstDigest, secondDigest)
	}
	if err := os.WriteFile(
		filepath.Join(second, "nested", "config"), []byte("two"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	changedDigest, err := Directory(second)
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest == firstDigest {
		t.Fatal("content change did not change directory digest")
	}
}

func TestDirectoryDigestRejectsSymlinkEntries(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(root, "outside"),
		filepath.Join(source, "escape"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := Directory(source); err == nil {
		t.Fatal("Directory() error = nil, want symlink rejection")
	}
}
