package homebrew

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

type brewInfo struct {
	Formulae []formula `json:"formulae"`
	Casks    []cask    `json:"casks"`
}

type formula struct {
	Name      string             `json:"name"`
	FullName  string             `json:"full_name"`
	Tap       string             `json:"tap"`
	Installed []formulaInstalled `json:"installed"`
}

type formulaInstalled struct {
	Version            string `json:"version"`
	InstalledOnRequest bool   `json:"installed_on_request"`
}

type cask struct {
	Token     string          `json:"token"`
	FullToken string          `json:"full_token"`
	Tap       string          `json:"tap"`
	Installed json.RawMessage `json:"installed"`
	Artifacts []artifact      `json:"artifacts"`
}

type artifact struct {
	App    []string `json:"app"`
	Target string   `json:"target"`
}

func (c Collector) Collect(ctx context.Context) ([]model.InstalledPackage, error) {
	raw, err := c.runner.Output(ctx, "brew", "info", "--json=v2", "--installed")
	if err != nil {
		return nil, fmt.Errorf("inspect Homebrew packages: %w", err)
	}

	var info brewInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("decode Homebrew inventory: %w", err)
	}

	packages := make([]model.InstalledPackage, 0, len(info.Formulae)+len(info.Casks))
	for _, item := range info.Formulae {
		if len(item.Installed) == 0 {
			continue
		}
		installed := item.Installed[len(item.Installed)-1]
		requested := installed.InstalledOnRequest
		name := item.Name
		if name == "" {
			name = item.FullName
		}
		packages = append(packages, model.InstalledPackage{
			Manager:   model.ManagerBrew,
			Kind:      model.KindFormula,
			Source:    item.Tap,
			Package:   name,
			Version:   installed.Version,
			Requested: &requested,
		})
	}

	caskroom := ""
	if len(info.Casks) > 0 {
		if value, err := c.runner.Output(ctx, "brew", "--caskroom"); err == nil {
			caskroom = strings.TrimSpace(string(value))
		}
	}
	for _, item := range info.Casks {
		name := item.Token
		if name == "" {
			name = item.FullToken
		}
		source := item.Tap
		if receiptSource := receiptTap(caskroom, name); receiptSource != "" {
			source = receiptSource
		}
		packages = append(packages, model.InstalledPackage{
			Manager:     model.ManagerBrew,
			Kind:        model.KindCask,
			Source:      source,
			Package:     name,
			Version:     installedVersion(item.Installed),
			Application: applicationPath(item.Artifacts),
		})
	}

	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Kind != packages[j].Kind {
			return packages[i].Kind < packages[j].Kind
		}
		if packages[i].Source != packages[j].Source {
			return packages[i].Source < packages[j].Source
		}
		return packages[i].Package < packages[j].Package
	})
	return packages, nil
}

func installedVersion(raw json.RawMessage) string {
	var version string
	if json.Unmarshal(raw, &version) == nil {
		return version
	}

	var versions []string
	if json.Unmarshal(raw, &versions) == nil && len(versions) > 0 {
		return versions[len(versions)-1]
	}
	return ""
}

func applicationPath(artifacts []artifact) string {
	for _, item := range artifacts {
		if item.Target != "" && len(item.App) > 0 {
			return item.Target
		}
		if len(item.App) > 0 {
			return strings.Join(item.App, ",")
		}
	}
	return ""
}

func receiptTap(caskroom, token string) string {
	if caskroom == "" || token == "" || filepath.Base(token) != token {
		return ""
	}
	path := filepath.Join(caskroom, token, ".metadata", "INSTALL_RECEIPT.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var receipt struct {
		Source struct {
			Tap string `json:"tap"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &receipt) != nil {
		return ""
	}
	return receipt.Source.Tap
}
