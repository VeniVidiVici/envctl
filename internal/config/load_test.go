package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadResolvesNativeConfiguration(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, "envctl.yaml", `
version: 1
catalog: catalog/packages.yaml
profiles: profiles
machines: machines
state:
  database: /tmp/example-envctl.db
`)
	writeConfigFile(t, root, "catalog/packages.yaml", `
version: 1
packages:
  fzf:
    manager: brew
    kind: formula
    source: homebrew/core
    package: fzf
    update_policy: managed
  example-app:
    manager: mas
    kind: app
    source: mac-app-store
    package: "123456789"
    update_policy: managed
`)
	writeConfigFile(t, root, "profiles/base.yaml", `
version: 1
name: base
packages:
  - fzf
`)
	writeConfigFile(t, root, "profiles/development.yaml", `
version: 1
name: development
extends:
  - base
packages:
  - example-app
`)
	writeConfigFile(t, root, "machines/example-mac.yaml", `
version: 1
id: example-mac
profiles:
  - development
access:
  type: local
`)

	got, err := Load(root, "example-mac")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Machine.ID != "example-mac" {
		t.Fatalf("machine = %#v", got.Machine)
	}
	if len(got.Desired.Packages) != 2 {
		t.Fatalf("desired packages = %#v", got.Desired.Packages)
	}
	if got.Digest == "" {
		t.Fatal("digest is empty")
	}
	if got.Database != "/tmp/example-envctl.db" {
		t.Fatalf("database = %q", got.Database)
	}
	ids, err := MachineIDs(root)
	if err != nil {
		t.Fatalf("MachineIDs() error = %v", err)
	}
	if strings.Join(ids, ",") != "example-mac" {
		t.Fatalf("machine ids = %v", ids)
	}
}

func TestLoadRejectsUnknownYAMLField(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, "envctl.yaml", `
version: 1
catalog: catalog.yaml
profiles: profiles
machines: machines
surprise: true
`)
	writeConfigFile(t, root, "catalog.yaml", "version: 1\npackages: {}\n")
	if err := os.MkdirAll(filepath.Join(root, "profiles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "machines"), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := Load(root, "example")
	if err == nil || !strings.Contains(err.Error(), "field surprise") {
		t.Fatalf("Load() error = %v, want unknown field error", err)
	}
}

func TestLoadRejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, "envctl.yaml", `
version: 1
catalog: ../outside.yaml
profiles: profiles
machines: machines
`)

	_, err := Load(root, "example")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Load() error = %v, want path escape error", err)
	}
}

func TestLoadRejectsUnsafeSSHHost(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, "envctl.yaml", `
version: 1
catalog: catalog.yaml
profiles: profiles
machines: machines
`)
	writeConfigFile(t, root, "catalog.yaml", `
version: 1
packages:
  example:
    manager: brew
    kind: formula
    source: homebrew/core
    package: example
    update_policy: managed
`)
	writeConfigFile(t, root, "profiles/base.yaml", `
version: 1
name: base
packages:
  - example
`)
	writeConfigFile(t, root, "machines/example.yaml", `
version: 1
id: example
profiles:
  - base
access:
  type: ssh
  host: "example; touch /tmp/unsafe"
`)

	_, err := Load(root, "example")
	if err == nil || !strings.Contains(err.Error(), "unsafe host") {
		t.Fatalf("Load() error = %v, want unsafe host error", err)
	}
}

func TestLoadResolvesPortableLinkSourcesAndTargets(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, "portable/example/config.json", "{}")
	writeConfigFile(t, root, "envctl.yaml", `
version: 1
catalog: catalog.yaml
profiles: profiles
machines: machines
`)
	writeConfigFile(t, root, "catalog.yaml", `
version: 1
packages:
  example:
    manager: brew
    kind: formula
    source: homebrew/core
    package: example
    update_policy: managed
links:
  example-config:
    source: portable/example/config.json
    target: ~/.config/example/config.json
    kind: file
`)
	writeConfigFile(t, root, "profiles/base.yaml", `
version: 1
name: base
packages: [example]
links: [example-config]
`)
	writeConfigFile(t, root, "machines/example.yaml", `
version: 1
id: example
profiles: [base]
access:
  type: local
`)

	got, err := Load(root, "example")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.Desired.Links) != 1 ||
		got.Desired.Links[0].Source != filepath.Join(root, "portable/example/config.json") ||
		len(got.Desired.Links[0].Digest) != 64 {
		t.Fatalf("desired links = %#v", got.Desired.Links)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got.Desired.Links[0].Target != filepath.Join(
		home, ".config/example/config.json",
	) {
		t.Fatalf("link target = %q", got.Desired.Links[0].Target)
	}
	if !containsString(got.LoadedFiles, "portable/example/config.json") {
		t.Fatalf("loaded files = %v", got.LoadedFiles)
	}
}

func TestLoadRejectsPortableLinkIntoRuntimeState(t *testing.T) {
	root := t.TempDir()
	writeConfigFile(t, root, "portable/config.json", "{}")
	writeConfigFile(t, root, "envctl.yaml", `
version: 1
catalog: catalog.yaml
profiles: profiles
machines: machines
`)
	writeConfigFile(t, root, "catalog.yaml", `
version: 1
packages:
  example:
    manager: brew
    kind: formula
    source: homebrew/core
    package: example
    update_policy: managed
links:
  unsafe:
    source: portable/config.json
    target: ~/.local/share/opencode/auth.json
    kind: file
`)
	writeConfigFile(t, root, "profiles/base.yaml", `
version: 1
name: base
packages: [example]
links: [unsafe]
`)
	writeConfigFile(t, root, "machines/example.yaml", `
version: 1
id: example
profiles: [base]
access:
  type: local
`)

	_, err := Load(root, "example")
	if err == nil || !strings.Contains(err.Error(), "machine-local state area") {
		t.Fatalf("Load() error = %v, want runtime-state rejection", err)
	}
}

func TestLoadResolvesRecoverySourcesAndTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "env-config")
	writeConfigFile(t, root, "secrets/example.sops.env", "encrypted")
	writeConfigFile(t, root, "envctl.yaml", `
version: 1
catalog: catalog.yaml
profiles: profiles
machines: machines
recovery_root: ~/Library/Recovery
`)
	writeConfigFile(t, root, "catalog.yaml", `
version: 1
packages:
  example:
    manager: brew
    kind: formula
    source: homebrew/core
    package: example
    update_policy: managed
recoveries:
  example-secret:
    kind: sops-file
    source: secrets/example.sops.env
    target: ~/.config/example/env
    format: dotenv
    mode: "0600"
  ssh-private:
    kind: age-archive
    source: ssh-private.tar.age
    target: ~/.ssh
    mode: "0600"
    members:
      - id_example
`)
	writeConfigFile(t, root, "profiles/base.yaml", `
version: 1
name: base
packages: [example]
recoveries: [example-secret, ssh-private]
`)
	writeConfigFile(t, root, "machines/example.yaml", `
version: 1
id: example
profiles: [base]
access:
  type: local
`)

	got, err := Load(root, "example")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.Desired.Recoveries) != 2 {
		t.Fatalf("recoveries = %#v", got.Desired.Recoveries)
	}
	if got.Desired.Recoveries[0].Source !=
		filepath.Join(root, "secrets", "example.sops.env") ||
		got.Desired.Recoveries[0].Target !=
			filepath.Join(home, ".config", "example", "env") {
		t.Fatalf("sops recovery = %#v", got.Desired.Recoveries[0])
	}
	if got.Desired.Recoveries[1].Source !=
		filepath.Join(home, "Library", "Recovery", "ssh-private.tar.age") ||
		got.Desired.Recoveries[1].Target != filepath.Join(home, ".ssh") {
		t.Fatalf("archive recovery = %#v", got.Desired.Recoveries[1])
	}
	if !containsString(got.LoadedFiles, "secrets/example.sops.env") {
		t.Fatalf("loaded files = %v", got.LoadedFiles)
	}
}

func TestLoadRejectsUnsafeRecoveryArchiveMember(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "env-config")
	writeConfigFile(t, root, "envctl.yaml", `
version: 1
catalog: catalog.yaml
profiles: profiles
machines: machines
recovery_root: ~/Library/Recovery
`)
	writeConfigFile(t, root, "catalog.yaml", `
version: 1
packages:
  example:
    manager: brew
    kind: formula
    source: homebrew/core
    package: example
    update_policy: managed
recoveries:
  ssh-private:
    kind: age-archive
    source: ssh-private.tar.age
    target: ~/.ssh
    mode: "0600"
    members:
      - ../escape
`)
	writeConfigFile(t, root, "profiles/base.yaml", `
version: 1
name: base
packages: [example]
recoveries: [ssh-private]
`)
	writeConfigFile(t, root, "machines/example.yaml", `
version: 1
id: example
profiles: [base]
access:
  type: local
`)

	_, err := Load(root, "example")
	if err == nil || !strings.Contains(err.Error(), "unsafe or duplicate member") {
		t.Fatalf("Load() error = %v", err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func writeConfigFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
