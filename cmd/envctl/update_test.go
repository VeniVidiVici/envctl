package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestResolveEnvctlSourceRootUsesExplicitCheckout(t *testing.T) {
	root := writeEnvctlSourceRoot(t)
	got, err := resolveEnvctlSourceRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("resolveEnvctlSourceRoot() = %q, want %q", got, root)
	}
}

func TestResolveEnvctlSourceRootUsesEnvironment(t *testing.T) {
	root := writeEnvctlSourceRoot(t)
	t.Setenv(sourceEnvironment, root)
	got, err := resolveEnvctlSourceRoot("")
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("resolveEnvctlSourceRoot() = %q, want %q", got, root)
	}
}

func TestRequireEnvctlSourceRootRejectsAnotherGoModule(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "go.mod"),
		[]byte("module example.com/not-envctl\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := requireEnvctlSourceRoot(root); err == nil {
		t.Fatal("requireEnvctlSourceRoot() error = nil, want error")
	}
}

func TestTrustedEnvctlOrigin(t *testing.T) {
	for _, origin := range []string{
		defaultPublicRepo,
		"git@github.com:VeniVidiVici/envctl.git",
		"ssh://git@github.com/VeniVidiVici/envctl.git",
	} {
		if !trustedEnvctlOrigin(origin) {
			t.Fatalf("trustedEnvctlOrigin(%q) = false", origin)
		}
	}
	if trustedEnvctlOrigin("https://example.com/envctl.git") {
		t.Fatal("trustedEnvctlOrigin(untrusted) = true")
	}
}

func TestCleanGoEnvironment(t *testing.T) {
	got := cleanGoEnvironment([]string{
		"PATH=/usr/bin",
		"GOROOT=/broken",
		"GOBIN=/tmp/bin",
		"GOWORK=/tmp/go.work",
	})
	for _, expected := range []string{
		"PATH=/usr/bin",
		"GOENV=off",
		"GOFLAGS=",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
	} {
		if !slices.Contains(got, expected) {
			t.Fatalf("cleanGoEnvironment() missing %q: %#v", expected, got)
		}
	}
	for _, unexpected := range []string{
		"GOROOT=/broken",
		"GOBIN=/tmp/bin",
		"GOWORK=/tmp/go.work",
	} {
		if slices.Contains(got, unexpected) {
			t.Fatalf("cleanGoEnvironment() contains %q: %#v", unexpected, got)
		}
	}
}

func writeEnvctlSourceRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "go.mod"),
		[]byte("module github.com/VeniVidiVici/envctl\n\ngo 1.26\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return root
}
