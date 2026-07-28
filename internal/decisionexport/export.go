package decisionexport

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeniVidiVici/envctl/internal/store"
	"go.yaml.in/yaml/v3"
)

type Entry struct {
	Machine      string `yaml:"machine" json:"machine"`
	InventoryKey string `yaml:"inventory_key" json:"inventory_key"`
	Decision     string `yaml:"decision" json:"decision"`
	Profile      string `yaml:"profile,omitempty" json:"profile,omitempty"`
}

type Document struct {
	Version   int     `yaml:"version" json:"version"`
	Decisions []Entry `yaml:"decisions" json:"decisions"`
}

type Result struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

func Write(
	configRoot, relativePath string,
	decisions []store.Decision,
	knownMachines map[string]bool,
) (Result, error) {
	path, err := safeOutputPath(configRoot, relativePath)
	if err != nil {
		return Result{}, err
	}

	document := Document{Version: 1, Decisions: make([]Entry, 0, len(decisions))}
	for _, decision := range decisions {
		if !knownMachines[decision.MachineID] {
			continue
		}
		document.Decisions = append(document.Decisions, Entry{
			Machine: decision.MachineID, InventoryKey: decision.InventoryKey,
			Decision: decision.Value, Profile: decision.Profile,
		})
	}
	sort.Slice(document.Decisions, func(i, j int) bool {
		if document.Decisions[i].Machine != document.Decisions[j].Machine {
			return document.Decisions[i].Machine < document.Decisions[j].Machine
		}
		return document.Decisions[i].InventoryKey < document.Decisions[j].InventoryKey
	})

	raw, err := yaml.Marshal(document)
	if err != nil {
		return Result{}, fmt.Errorf("encode decision export: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Result{}, fmt.Errorf("create decision export directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("create decision export: %w", err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return Result{}, fmt.Errorf("secure decision export: %w", err)
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return Result{}, fmt.Errorf("write decision export: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return Result{}, fmt.Errorf("sync decision export: %w", err)
	}
	if err := file.Close(); err != nil {
		return Result{}, fmt.Errorf("close decision export: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return Result{}, fmt.Errorf("replace decision export: %w", err)
	}
	return Result{Path: path, Count: len(document.Decisions)}, nil
}

func safeOutputPath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("decision export path must be relative: %q", relative)
	}
	cleaned := filepath.Clean(relative)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("decision export path escapes config root: %q", relative)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve config root: %w", err)
	}
	path := filepath.Join(absoluteRoot, cleaned)
	relativeToRoot, err := filepath.Rel(absoluteRoot, path)
	if err != nil || relativeToRoot == ".." ||
		strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("decision export path escapes config root: %q", relative)
	}
	return path, nil
}
