package bun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type Collector struct {
	packageFile string
}

func NewCollector(packageFile string) Collector {
	return Collector{packageFile: packageFile}
}

func DefaultCollector() (Collector, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Collector{}, fmt.Errorf("find home directory: %w", err)
	}
	return NewCollector(filepath.Join(home, ".bun", "install", "global", "package.json")), nil
}

type packageManifest struct {
	Dependencies map[string]string `json:"dependencies"`
}

func (c Collector) Collect(context.Context) ([]model.InstalledPackage, error) {
	raw, err := os.ReadFile(c.packageFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Bun global package manifest: %w", err)
	}

	var manifest packageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decode Bun global package manifest: %w", err)
	}

	names := make([]string, 0, len(manifest.Dependencies))
	for name := range manifest.Dependencies {
		names = append(names, name)
	}
	sort.Strings(names)

	packages := make([]model.InstalledPackage, 0, len(names))
	for _, name := range names {
		packages = append(packages, model.InstalledPackage{
			Manager: model.ManagerBun,
			Kind:    model.KindTool,
			Package: name,
			Version: strings.TrimLeft(manifest.Dependencies[name], "^~>=< "),
		})
	}
	return packages, nil
}
