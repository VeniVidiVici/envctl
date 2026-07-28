package customtool

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

func TestCollectsHealthyStandaloneAndExtensionTools(t *testing.T) {
	runner := fakeRunner{
		paths: map[string]string{
			"claude":   "/Users/example/.local/bin/claude",
			"gh":       "/opt/homebrew/bin/gh",
			"opencode": "/Users/example/.opencode/bin/opencode",
		},
		outputs: map[string][]byte{
			"claude --version":   []byte("2.1.220 (Claude Code)\n"),
			"gh dash --version":  []byte("gh-dash version dev\nmodule version: v4.25.2\n"),
			"opencode --version": []byte("1.18.4\n"),
		},
	}
	result := NewCollector(runner).Collect(context.Background())
	if len(result.Issues) != 0 || len(result.Packages) != 3 {
		t.Fatalf("result = %#v", result)
	}
	got := []string{
		result.Packages[0].Package + "=" + result.Packages[0].Version,
		result.Packages[1].Package + "=" + result.Packages[1].Version,
		result.Packages[2].Package + "=" + result.Packages[2].Version,
	}
	want := []string{"claude=2.1.220", "gh-dash=4.25.2", "opencode=1.18.4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packages = %#v, want %#v", got, want)
	}
}

func TestMissingAndUnhealthyToolsBecomeFindingsNotCollectorFailure(t *testing.T) {
	runner := fakeRunner{
		paths: map[string]string{
			"claude":   "/Users/example/.local/bin/claude",
			"opencode": "/Users/example/.opencode/bin/opencode",
		},
		outputs: map[string][]byte{
			"claude --version": []byte("2.1.220\n"),
		},
		outputErrors: map[string]error{
			"opencode --version": errors.New("exit status 1"),
		},
	}
	result := NewCollector(runner).Collect(context.Background())
	if len(result.Packages) != 1 || result.Packages[0].Package != "claude" {
		t.Fatalf("packages = %#v", result.Packages)
	}
	if len(result.Issues) != 1 ||
		result.Issues[0].Tool != "opencode" ||
		result.Issues[0].Message != "version probe failed" {
		t.Fatalf("issues = %#v", result.Issues)
	}
}

type fakeRunner struct {
	paths        map[string]string
	outputs      map[string][]byte
	outputErrors map[string]error
}

func (r fakeRunner) LookPath(name string) (string, error) {
	if path := r.paths[name]; path != "" {
		return path, nil
	}
	return "", exec.ErrNotFound
}

func (r fakeRunner) Output(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	key := name
	for _, arg := range args {
		key += " " + arg
	}
	if err := r.outputErrors[key]; err != nil {
		return nil, err
	}
	value, ok := r.outputs[key]
	if !ok {
		return nil, errors.New("unexpected command")
	}
	return value, nil
}
