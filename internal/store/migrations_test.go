package store

import (
	"io/fs"
	"testing"
)

func TestMigrationsAreEmbedded(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations() error = %v", err)
	}
	for _, name := range []string{
		"001_initial.sql",
		"002_apply_verification.sql",
		"003_portable_links.sql",
	} {
		data, err := fs.ReadFile(migrations, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
}
