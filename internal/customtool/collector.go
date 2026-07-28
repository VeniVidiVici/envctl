package customtool

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/VeniVidiVici/envctl/internal/model"
)

const probeTimeout = 5 * time.Second

var versionPattern = regexp.MustCompile(`\bv?([0-9]+(?:\.[0-9]+)+)\b`)

type Runner interface {
	LookPath(name string) (string, error)
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (ExecRunner) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

type Issue struct {
	Tool    string `json:"tool"`
	Message string `json:"message"`
}

type Result struct {
	Packages []model.InstalledPackage `json:"packages"`
	Issues   []Issue                  `json:"issues,omitempty"`
}

type probe struct {
	id         string
	executable string
	args       []string
	source     string
}

var probes = []probe{
	{
		id: "claude", executable: "claude",
		args: []string{"--version"}, source: "executable",
	},
	{
		id: "gh-dash", executable: "gh",
		args: []string{"dash", "--version"}, source: "gh-extension",
	},
	{
		id: "opencode", executable: "opencode",
		args: []string{"--version"}, source: "executable",
	},
}

type Collector struct {
	runner Runner
}

func NewCollector(runner Runner) Collector {
	return Collector{runner: runner}
}

func (c Collector) Collect(ctx context.Context) Result {
	var result Result
	for _, definition := range probes {
		path, err := c.runner.LookPath(definition.executable)
		if errors.Is(err, exec.ErrNotFound) || path == "" {
			continue
		}
		if err != nil {
			result.Issues = append(result.Issues, Issue{
				Tool: definition.id, Message: "executable lookup failed",
			})
			continue
		}
		probeContext, cancel := context.WithTimeout(ctx, probeTimeout)
		rawVersion, err := c.runner.Output(
			probeContext, definition.executable, definition.args...,
		)
		cancel()
		if err != nil {
			message := "version probe failed"
			if errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(probeContext.Err(), context.DeadlineExceeded) {
				message = "version probe timed out"
			}
			result.Issues = append(result.Issues, Issue{
				Tool: definition.id, Message: message,
			})
			continue
		}
		result.Packages = append(result.Packages, model.InstalledPackage{
			Manager:     model.ManagerCustom,
			Kind:        model.KindTool,
			Source:      definition.source,
			Package:     definition.id,
			Version:     parseVersion(string(rawVersion)),
			Application: path,
		})
	}
	sort.Slice(result.Packages, func(i, j int) bool {
		return result.Packages[i].Package < result.Packages[j].Package
	})
	sort.Slice(result.Issues, func(i, j int) bool {
		return result.Issues[i].Tool < result.Issues[j].Tool
	})
	return result
}

func parseVersion(output string) string {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(output))
	if len(match) != 2 {
		return ""
	}
	return match[1]
}
