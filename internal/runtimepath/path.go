package runtimepath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Apply() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory for executable path: %w", err)
	}
	if os.Getenv("XDG_CONFIG_HOME") == "" {
		if err := os.Setenv(
			"XDG_CONFIG_HOME",
			filepath.Join(home, ".config"),
		); err != nil {
			return fmt.Errorf("set default XDG config directory: %w", err)
		}
	}
	return os.Setenv("PATH", Build(home, os.Getenv("PATH")))
}

func Build(home, inherited string) string {
	candidates := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".opencode", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".local", "share", "mise", "shims"),
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/local/sbin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}
	candidates = append(candidates, filepath.SplitList(inherited)...)
	seen := make(map[string]bool)
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		result = append(result, candidate)
		seen[candidate] = true
	}
	return strings.Join(result, string(os.PathListSeparator))
}
