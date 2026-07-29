package legacydeps

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxTextFileBytes = 1 << 20

type FindingKind string

const (
	FindingSymlink FindingKind = "symlink"
	FindingText    FindingKind = "text"
)

type Finding struct {
	Kind   FindingKind `json:"kind"`
	Path   string      `json:"path"`
	Target string      `json:"target,omitempty"`
	Detail string      `json:"detail"`
}

type Report struct {
	LegacyRoot   string    `json:"legacy_root"`
	ConfigRoot   string    `json:"config_root,omitempty"`
	Dependencies int       `json:"dependencies"`
	Ready        bool      `json:"ready"`
	Findings     []Finding `json:"findings,omitempty"`
}

type Auditor struct {
	home       string
	legacyRoot string
	configRoot string
}

func New(home, legacyRoot, configRoot string) (*Auditor, error) {
	absoluteHome, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("resolve home: %w", err)
	}
	absoluteLegacy, err := filepath.Abs(legacyRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve legacy root: %w", err)
	}
	if absoluteLegacy == absoluteHome || !pathWithin(absoluteHome, absoluteLegacy) {
		return nil, errors.New("legacy root must be a directory inside the home directory")
	}
	absoluteConfig := ""
	if configRoot != "" {
		absoluteConfig, err = filepath.Abs(configRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve config root: %w", err)
		}
	}
	return &Auditor{
		home:       filepath.Clean(absoluteHome),
		legacyRoot: filepath.Clean(absoluteLegacy),
		configRoot: filepath.Clean(absoluteConfig),
	}, nil
}

func (a *Auditor) Audit() (Report, error) {
	report := Report{
		LegacyRoot: a.legacyRoot,
		ConfigRoot: a.configRoot,
	}
	var findings []Finding
	seen := make(map[string]bool)
	add := func(finding Finding) {
		key := string(finding.Kind) + "\x00" + finding.Path
		if seen[key] {
			return
		}
		seen[key] = true
		findings = append(findings, finding)
	}

	directFiles := []string{
		".zshrc", ".zprofile", ".zshenv", ".npmrc", ".hydrate",
		filepath.Join(".ssh", "config"),
		filepath.Join(".aws", "config"),
		filepath.Join(".gnupg", "dirmngr.conf"),
		filepath.Join(".gnupg", "gpg-agent.conf"),
		filepath.Join(".gnupg", "gpg.conf"),
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude.json"),
		filepath.Join(".codex", "config.toml"),
	}
	for _, relative := range directFiles {
		if err := a.inspectPath(filepath.Join(a.home, relative), add); err != nil {
			return Report{}, err
		}
	}

	roots := []string{
		filepath.Join(a.home, ".config"),
		filepath.Join(a.home, ".claude", "skills"),
		filepath.Join(a.home, ".codex", "skills"),
		filepath.Join(a.home, "Library", "LaunchAgents"),
	}
	if a.configRoot != "" {
		roots = append(roots, filepath.Join(a.configRoot, "portable"))
	}
	for _, root := range roots {
		if err := a.walk(root, add); err != nil {
			return Report{}, err
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Path == findings[j].Path {
			return findings[i].Kind < findings[j].Kind
		}
		return findings[i].Path < findings[j].Path
	})
	report.Findings = findings
	report.Dependencies = len(findings)
	report.Ready = len(findings) == 0
	return report, nil
}

func (a *Auditor) walk(root string, add func(Finding)) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect audit root %s: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return a.inspectPath(root, add)
	}
	if !info.IsDir() {
		return a.inspectPath(root, add)
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.IsDir() && skipDirectory(path) {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if err := a.inspectPath(path, add); err != nil {
				return err
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			return a.inspectText(path, add)
		}
		return nil
	})
}

func (a *Auditor) inspectPath(path string, add func(Finding)) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if info.Mode().IsRegular() {
			return a.inspectText(path, add)
		}
		return nil
	}
	rawTarget, err := os.Readlink(path)
	if err != nil {
		return fmt.Errorf("read link %s: %w", path, err)
	}
	target := rawTarget
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	target = filepath.Clean(target)
	resolved := target
	if evaluated, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
		resolved = filepath.Clean(evaluated)
	}
	if pathWithin(a.legacyRoot, target) || pathWithin(a.legacyRoot, resolved) {
		add(Finding{
			Kind: FindingSymlink, Path: path, Target: rawTarget,
			Detail: "symbolic link resolves inside the legacy environment root",
		})
		return nil
	}
	if targetInfo, targetErr := os.Stat(path); targetErr == nil &&
		targetInfo.Mode().IsRegular() {
		return a.inspectText(path, add)
	}
	return nil
}

func (a *Auditor) inspectText(path string, add func(Finding)) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxTextFileBytes {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return nil
	}
	legacyRelative := filepath.Join("Documents", "env")
	if !bytes.Contains(raw, []byte(a.legacyRoot)) &&
		!bytes.Contains(raw, []byte(legacyRelative)) &&
		!bytes.Contains(raw, []byte("LEGACY_ENV_HOME")) {
		return nil
	}
	add(Finding{
		Kind: FindingText, Path: path,
		Detail: "text contains a legacy environment path or compatibility variable",
	})
	return nil
}

func skipDirectory(path string) bool {
	base := filepath.Base(path)
	switch base {
	case ".git", ".cache", ".Trash", "node_modules", "__pycache__",
		"history", "logs", "sessions", "shell-snapshots", "backups",
		"archived_sessions", "prompts":
		return true
	}
	return strings.HasPrefix(base, ".env-bootstrap-backups") ||
		strings.HasPrefix(base, ".codex-safety") ||
		strings.HasPrefix(base, ".gnupg.before-")
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
