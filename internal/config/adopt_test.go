package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestAdoptPackagesAddsCatalogEntryAndSharedProfileReference(t *testing.T) {
	root := adoptionFixture(t)
	result, err := AdoptPackages(root, "shared", []PackageAdoption{{
		ID: "firefox",
		Spec: model.PackageSpec{
			Manager: model.ManagerBrew, Kind: model.KindCask,
			Source: "homebrew/cask", Package: "firefox",
			UpdatePolicy: model.UpdateInstallOnly,
		},
	}})
	if err != nil {
		t.Fatalf("AdoptPackages() error = %v", err)
	}
	if len(result.Added) != 1 || result.Added[0] != "firefox" {
		t.Fatalf("result = %#v", result)
	}
	loaded, err := Load(root, "example")
	if err != nil {
		t.Fatalf("Load() after adoption error = %v", err)
	}
	found := false
	for _, spec := range loaded.Desired.Packages {
		if spec.ID == "firefox" {
			found = true
		}
	}
	if !found {
		t.Fatalf("desired packages = %#v, want firefox", loaded.Desired.Packages)
	}
	raw, err := os.ReadFile(filepath.Join(root, "catalog", "packages.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "firefox: {manager: brew") {
		t.Fatalf("catalog does not retain compact package style:\n%s", raw)
	}
}

func TestAdoptPackagesRefusesConflictingCatalogIDWithoutMutation(t *testing.T) {
	root := adoptionFixture(t)
	catalogPath := filepath.Join(root, "catalog", "packages.yaml")
	before, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AdoptPackages(root, "shared", []PackageAdoption{{
		ID: "jq",
		Spec: model.PackageSpec{
			Manager: model.ManagerBrew, Kind: model.KindFormula,
			Source: "homebrew/core", Package: "different",
			UpdatePolicy: model.UpdateManaged,
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "different specification") {
		t.Fatalf("AdoptPackages() error = %v", err)
	}
	after, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("catalog changed after rejected adoption")
	}
}

func adoptionFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"envctl.yaml": `version: 1
catalog: catalog/packages.yaml
profiles: profiles
machines: machines
state:
  database: ~/.local/state/envctl/test.db
`,
		"catalog/packages.yaml": `version: 1
packages:
  jq: {manager: brew, kind: formula, source: homebrew/core, package: jq, update_policy: managed}
`,
		"profiles/shared.yaml": `version: 1
name: shared
packages:
  - jq
`,
		"machines/example.yaml": `version: 1
id: example
profiles:
  - shared
access:
  type: local
`,
	}
	for relative, contents := range files {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
