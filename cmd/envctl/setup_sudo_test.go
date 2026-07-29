package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeniVidiVici/envctl/internal/setupui"
)

func TestAutomaticSetupNeedsSudoForPendingHomebrewWork(t *testing.T) {
	phases := []setupui.Phase{
		{
			ID:      setupui.PhaseHomebrew,
			Status:  setupui.StatusReady,
			Actions: 3,
		},
	}
	if !automaticSetupNeedsSudo(phases) {
		t.Fatal("automaticSetupNeedsSudo() = false, want true")
	}
}

func TestAutomaticSetupDoesNotNeedSudoWithoutPendingHomebrewWork(
	t *testing.T,
) {
	tests := []setupui.Phase{
		{
			ID:      setupui.PhaseHomebrew,
			Status:  setupui.StatusSatisfied,
			Actions: 0,
		},
		{
			ID:      setupui.PhaseHomebrew,
			Status:  setupui.StatusReady,
			Actions: 0,
		},
		{
			ID:      setupui.PhaseMise,
			Status:  setupui.StatusReady,
			Actions: 1,
		},
	}
	for _, phase := range tests {
		if automaticSetupNeedsSudo([]setupui.Phase{phase}) {
			t.Fatalf("automaticSetupNeedsSudo(%#v) = true, want false", phase)
		}
	}
}

func TestAuthorizeAutomaticSetupValidatesAndRefreshesSudo(t *testing.T) {
	binDirectory := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "sudo.log")
	sudoPath := filepath.Join(binDirectory, "sudo")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$ENVCTL_SUDO_LOG\"\n"
	if err := os.WriteFile(sudoPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory)
	t.Setenv("ENVCTL_SUDO_LOG", logPath)

	var stdout, stderr bytes.Buffer
	stop, err := authorizeAutomaticSetup(
		context.Background(),
		&stdout,
		&stderr,
		10*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		contents, readErr := os.ReadFile(logPath)
		if readErr == nil && strings.Contains(string(contents), "-n -v") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"sudo refresh was not recorded: contents=%q err=%v",
				contents,
				readErr,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.FieldsFunc(string(contents), func(r rune) bool {
		return r == '\n'
	})
	if len(lines) < 2 || lines[0] != "-v" {
		t.Fatalf("sudo invocations = %#v", lines)
	}
	if !strings.Contains(stdout.String(), "Enter your Mac password once") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAuthorizeAutomaticSetupReportsInitialSudoFailure(t *testing.T) {
	binDirectory := t.TempDir()
	sudoPath := filepath.Join(binDirectory, "sudo")
	script := "#!/bin/sh\nexit 42\n"
	if err := os.WriteFile(sudoPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory)

	stop, err := authorizeAutomaticSetup(
		context.Background(),
		&bytes.Buffer{},
		&bytes.Buffer{},
		time.Hour,
	)
	if err == nil {
		t.Fatal("authorizeAutomaticSetup() error = nil, want error")
	}
	if stop != nil {
		t.Fatal("authorizeAutomaticSetup() returned a stop function after failure")
	}
	if !strings.Contains(err.Error(), "authorize automatic package setup") {
		t.Fatalf("error = %q", err)
	}
}
