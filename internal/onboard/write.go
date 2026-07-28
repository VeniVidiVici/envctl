package onboard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
	"go.yaml.in/yaml/v3"
)

type machineFile struct {
	Version           int `yaml:"version"`
	envconfig.Machine `yaml:",inline"`
}

func WriteMachine(
	configRoot string,
	machine envconfig.Machine,
	replaceExisting bool,
) (string, error) {
	if err := envconfig.ValidateMachine(machine); err != nil {
		return "", err
	}
	root, err := filepath.Abs(configRoot)
	if err != nil {
		return "", fmt.Errorf("resolve config root: %w", err)
	}
	machinesDirectory := filepath.Join(root, "machines")
	if info, err := os.Stat(machinesDirectory); err != nil {
		return "", fmt.Errorf("inspect machines directory: %w", err)
	} else if !info.IsDir() {
		return "", errors.New("configured machines path is not a directory")
	}
	target := filepath.Join(machinesDirectory, machine.ID+".yaml")
	relative, err := filepath.Rel(root, target)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("machine path escapes the configuration root")
	}

	mode := os.FileMode(0o644)
	var original []byte
	replacing := false
	info, statErr := os.Lstat(target)
	switch {
	case statErr == nil:
		if !replaceExisting {
			return "", fmt.Errorf("machine file already exists: %s", relative)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("machine file is not a regular file: %s", relative)
		}
		if err := requireCleanTrackedFile(root, relative); err != nil {
			return "", err
		}
		mode = info.Mode().Perm()
		original, err = os.ReadFile(target)
		if err != nil {
			return "", fmt.Errorf("read existing machine file: %w", err)
		}
		replacing = true
	case errors.Is(statErr, os.ErrNotExist):
		if replaceExisting {
			return "", fmt.Errorf("machine file no longer exists: %s", relative)
		}
	default:
		return "", fmt.Errorf("inspect machine file: %w", statErr)
	}

	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(machineFile{Version: 1, Machine: machine}); err != nil {
		return "", fmt.Errorf("encode machine file: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("close machine encoder: %w", err)
	}
	raw := encoded.Bytes()
	temporary, err := os.CreateTemp(machinesDirectory, "."+machine.ID+".yaml.*")
	if err != nil {
		return "", fmt.Errorf("create temporary machine file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return "", fmt.Errorf("secure temporary machine file: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return "", fmt.Errorf("write temporary machine file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("sync temporary machine file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close temporary machine file: %w", err)
	}
	if !replacing {
		if err := os.Link(temporaryPath, target); err != nil {
			if errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf(
					"machine file appeared while writing; refusing overwrite: %s",
					relative,
				)
			}
			return "", fmt.Errorf("install new machine file: %w", err)
		}
		if err := os.Remove(temporaryPath); err != nil {
			return "", fmt.Errorf("remove temporary machine link: %w", err)
		}
		return filepath.ToSlash(relative), nil
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("re-read existing machine file: %w", err)
	}
	if !bytes.Equal(current, original) {
		return "", fmt.Errorf(
			"machine file changed while writing; refusing overwrite: %s",
			relative,
		)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return "", fmt.Errorf("replace machine file: %w", err)
	}
	return filepath.ToSlash(relative), nil
}

func requireCleanTrackedFile(root, relative string) error {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("refuse replacing a machine file outside a Git checkout")
		}
		return fmt.Errorf("inspect Git checkout: %w", err)
	}
	command := exec.Command(
		"git", "-C", root, "status", "--porcelain", "--", relative,
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf(
			"inspect machine file Git state: %w: %s",
			err,
			strings.TrimSpace(stderr.String()),
		)
	}
	if strings.TrimSpace(stdout.String()) != "" {
		return fmt.Errorf(
			"refuse replacing locally modified machine file: %s",
			relative,
		)
	}
	return nil
}
