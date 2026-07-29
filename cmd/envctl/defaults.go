package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
	"github.com/VeniVidiVici/envctl/internal/model"
	"github.com/VeniVidiVici/envctl/internal/onboard"
)

const (
	configEnvironment    = "ENVCTL_CONFIG"
	inventoryEnvironment = "ENVCTL_INVENTORY_DIR"
)

func resolveConfigRoot(explicit string) (string, error) {
	if explicit != "" {
		return requireConfigRoot(explicit)
	}
	if configured := os.Getenv(configEnvironment); configured != "" {
		return requireConfigRoot(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	candidates := []string{
		filepath.Join(home, ".local", "share", "envctl", "repos", "env-config"),
	}
	if workingDirectory, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			workingDirectory,
			filepath.Join(workingDirectory, "..", "env-config"),
		)
	}
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil || seen[absolute] {
			continue
		}
		seen[absolute] = true
		if info, err := os.Stat(filepath.Join(absolute, "envctl.yaml")); err == nil &&
			info.Mode().IsRegular() {
			return absolute, nil
		}
	}
	return "", fmt.Errorf(
		"could not find env-config; set %s or pass --config",
		configEnvironment,
	)
}

func requireConfigRoot(value string) (string, error) {
	expanded, err := expandHome(value)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve config root: %w", err)
	}
	info, err := os.Stat(filepath.Join(absolute, "envctl.yaml"))
	if err != nil {
		return "", fmt.Errorf("inspect config root %s: %w", absolute, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("config root has no regular envctl.yaml: %s", absolute)
	}
	return absolute, nil
}

func resolveInventoryDirectory(explicit string) (string, error) {
	value := explicit
	if value == "" {
		value = os.Getenv(inventoryEnvironment)
	}
	if value == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		value = filepath.Join(home, ".local", "state", "envctl", "inventory")
	}
	expanded, err := expandHome(value)
	if err != nil {
		return "", err
	}
	return filepath.Abs(expanded)
}

func detectConfiguredLocalMachine(
	ctx context.Context,
	configRoot string,
) (string, error) {
	identity, err := onboard.Detect(ctx)
	if err != nil {
		return "", err
	}
	machines, err := envconfig.Machines(configRoot)
	if err != nil {
		return "", err
	}
	return matchConfiguredMachine(identity, machines)
}

func matchConfiguredMachine(
	identity onboard.Identity,
	machines []envconfig.Machine,
) (string, error) {
	if identity.HardwareUUIDSHA256 == "" {
		return "", errors.New("local hardware identity is unavailable")
	}
	var match string
	for _, machine := range machines {
		if machine.Match.HardwareUUIDSHA256 != identity.HardwareUUIDSHA256 {
			continue
		}
		if match != "" {
			return "", fmt.Errorf(
				"local hardware identity matches both %q and %q",
				match, machine.ID,
			)
		}
		match = machine.ID
	}
	if match == "" {
		return "", errors.New(
			"this Mac is not registered in env-config; run envctl onboard",
		)
	}
	return match, nil
}

func writeInventory(path string, inventory model.Inventory) error {
	if path == "" {
		return errors.New("inventory path is empty")
	}
	raw, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return fmt.Errorf("encode inventory: %w", err)
	}
	raw = append(raw, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create inventory directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary inventory: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary inventory: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary inventory: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary inventory: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary inventory: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace inventory: %w", err)
	}
	return nil
}
