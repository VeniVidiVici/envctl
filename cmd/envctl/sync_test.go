package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseConfigSyncStatusSeparatesTrackedAndUntracked(t *testing.T) {
	statuses, err := parseConfigSyncStatus(
		" M portable/ssh/config\x00?? private-key\x00R  new-name\x00old-name\x00",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(configSyncChangedPaths(statuses), ","); got !=
		"portable/ssh/config,new-name" {
		t.Fatalf("tracked paths = %q", got)
	}
	if got := strings.Join(configSyncUntracked(statuses), ","); got != "private-key" {
		t.Fatalf("untracked paths = %q", got)
	}
}

func TestConfirmConfigSyncDefaultsToNo(t *testing.T) {
	var output bytes.Buffer
	confirmed, err := confirmConfigSync(strings.NewReader("\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed {
		t.Fatal("blank confirmation was accepted")
	}
	if !strings.Contains(output.String(), "[y/N]") {
		t.Fatalf("prompt = %q", output.String())
	}
}

func TestConfirmConfigSyncAcceptsYes(t *testing.T) {
	confirmed, err := confirmConfigSync(
		strings.NewReader("yes\n"),
		&bytes.Buffer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Fatal("yes confirmation was rejected")
	}
}

func TestTrustedEnvConfigOrigin(t *testing.T) {
	for _, origin := range []string{
		defaultEnvConfigRepo,
		"https://github.com/VeniVidiVici/env-config.git",
		"ssh://git@github.com/VeniVidiVici/env-config.git",
	} {
		if !trustedEnvConfigOrigin(origin) {
			t.Fatalf("trustedEnvConfigOrigin(%q) = false", origin)
		}
	}
	if trustedEnvConfigOrigin("git@example.com:other/config.git") {
		t.Fatal("untrusted origin was accepted")
	}
}

func TestCompactSyncErrorIsSingleLineAndBounded(t *testing.T) {
	got := compactSyncError(
		&syncTestError{message: strings.Repeat("x", 200) + "\nsecond line"},
	)
	if strings.Contains(got, "\n") || len(got) > 180 {
		t.Fatalf("compact error = %q", got)
	}
}

type syncTestError struct {
	message string
}

func (e *syncTestError) Error() string {
	return e.message
}
