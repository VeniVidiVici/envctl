package mise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"

	"github.com/VeniVidiVici/envctl/internal/model"
)

const maxOutput = 4 << 20

type Runner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Output(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

type Collector struct {
	runner Runner
}

func NewCollector(runner Runner) Collector {
	return Collector{runner: runner}
}

type toolVersion struct {
	Version          string `json:"version"`
	RequestedVersion string `json:"requested_version"`
	Installed        bool   `json:"installed"`
	Active           bool   `json:"active"`
}

func (c Collector) Collect(ctx context.Context) ([]model.InstalledPackage, error) {
	if c.runner == nil {
		return nil, errors.New("mise collector runner is required")
	}
	raw, err := c.runner.Output(ctx, "mise", "ls", "--json")
	if err != nil {
		return nil, fmt.Errorf("run mise ls --json: %w", err)
	}
	if len(raw) > maxOutput {
		return nil, errors.New("mise inventory exceeds safety limit")
	}
	var decoded map[string][]toolVersion
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("parse mise inventory: %w", err)
	}
	packages := make([]model.InstalledPackage, 0, len(decoded))
	for tool, versions := range decoded {
		if tool == "" {
			return nil, errors.New("mise inventory contains an empty tool name")
		}
		for _, version := range versions {
			if !version.Installed || !version.Active {
				continue
			}
			requested := version.RequestedVersion
			if requested == "" {
				requested = version.Version
			}
			packages = append(packages, model.InstalledPackage{
				Manager: model.ManagerMise,
				Kind:    model.KindTool,
				Package: tool,
				Version: requested,
			})
		}
	}
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Package != packages[j].Package {
			return packages[i].Package < packages[j].Package
		}
		return packages[i].Version < packages[j].Version
	})
	return packages, nil
}
