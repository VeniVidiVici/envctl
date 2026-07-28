package mas

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

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

var plainRecordPattern = regexp.MustCompile(
	`^\s*([1-9][0-9]*)\s+.+\s+\(([^()]*)\)\s*$`,
)

func (c Collector) Collect(ctx context.Context) ([]model.InstalledPackage, error) {
	raw, err := c.runner.Output(ctx, "mas", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("inspect Mac App Store packages: %w", err)
	}

	var packages []model.InstalledPackage
	placeholder := false
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var item record
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, fmt.Errorf("decode Mac App Store inventory: %w", err)
		}
		if item.AdamID <= 0 {
			placeholder = true
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
	if placeholder {
		fallback, err := c.runner.Output(ctx, "mas", "list")
		if err != nil {
			return nil, fmt.Errorf(
				"inspect Mac App Store fallback inventory: %w", err,
			)
		}
		plainPackages, err := parsePlainInventory(fallback)
		if err != nil {
			return nil, err
		}
		if len(plainPackages) == 0 {
			return nil, fmt.Errorf(
				"Mac App Store JSON returned placeholders and plain fallback returned no identities",
			)
		}
		packages = mergePackages(packages, plainPackages)
	}

	sort.Slice(packages, func(i, j int) bool {
		return packages[i].Package < packages[j].Package
	})
	return packages, nil
}

func parsePlainInventory(raw []byte) ([]model.InstalledPackage, error) {
	var packages []model.InstalledPackage
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		match := plainRecordPattern.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf(
				"decode Mac App Store fallback inventory line %q", line,
			)
		}
		packages = append(packages, model.InstalledPackage{
			Manager: model.ManagerMAS,
			Kind:    model.KindApp,
			Source:  "mac-app-store",
			Package: match[1],
			Version: match[2],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Mac App Store fallback inventory: %w", err)
	}
	return packages, nil
}

func mergePackages(
	preferred, fallback []model.InstalledPackage,
) []model.InstalledPackage {
	seen := make(map[string]bool, len(preferred))
	result := append([]model.InstalledPackage(nil), preferred...)
	for _, item := range preferred {
		seen[item.Package] = true
	}
	for _, item := range fallback {
		if seen[item.Package] {
			continue
		}
		result = append(result, item)
		seen[item.Package] = true
	}
	return result
}
