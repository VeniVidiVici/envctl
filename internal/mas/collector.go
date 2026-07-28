package mas

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

type Collector struct {
	runner Runner
}

func NewCollector(runner Runner) Collector {
	return Collector{runner: runner}
}

type record struct {
	AdamID      int64  `json:"adamID"`
	DisplayName string `json:"displayName"`
	Path        string `json:"path"`
	Version     string `json:"version"`
}

func (c Collector) Collect(ctx context.Context) ([]model.InstalledPackage, error) {
	raw, err := c.runner.Output(ctx, "mas", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("inspect Mac App Store packages: %w", err)
	}

	var packages []model.InstalledPackage
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var item record
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, fmt.Errorf("decode Mac App Store inventory: %w", err)
		}
		if item.AdamID <= 0 {
			continue
		}
		packages = append(packages, model.InstalledPackage{
			Manager:     model.ManagerMAS,
			Kind:        model.KindApp,
			Source:      "mac-app-store",
			Package:     strconv.FormatInt(item.AdamID, 10),
			Version:     item.Version,
			Application: item.Path,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Mac App Store inventory: %w", err)
	}

	sort.Slice(packages, func(i, j int) bool {
		return packages[i].Package < packages[j].Package
	})
	return packages, nil
}
