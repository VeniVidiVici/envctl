package bootstrapverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const Version = 1

type Checkpoint struct {
	Version      int        `json:"version"`
	MachineID    string     `json:"machine_id"`
	ConfigDigest string     `json:"config_digest"`
	BootID       string     `json:"boot_id"`
	CreatedAt    time.Time  `json:"created_at"`
	VerifiedAt   *time.Time `json:"verified_at,omitempty"`
}

type CheckStatus string

const (
	StatusPassed CheckStatus = "passed"
	StatusFailed CheckStatus = "failed"
	StatusWarned CheckStatus = "warning"
)

type Check struct {
	ID     string      `json:"id"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
}

type Report struct {
	MachineID      string    `json:"machine_id"`
	ConfigDigest   string    `json:"config_digest"`
	CheckpointPath string    `json:"checkpoint_path"`
	PreviousBootID string    `json:"previous_boot_id"`
	CurrentBootID  string    `json:"current_boot_id"`
	Restarted      bool      `json:"restarted"`
	Checks         []Check   `json:"checks"`
	Ready          bool      `json:"ready"`
	VerifiedAt     time.Time `json:"verified_at"`
}

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

func CurrentBootID(ctx context.Context, runner Runner) (string, error) {
	output, err := runner.Run(ctx, "/usr/sbin/sysctl", "-n", "kern.boottime")
	if err != nil {
		return "", fmt.Errorf("read macOS boot time: %w", err)
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", errors.New("macOS boot time is empty")
	}
	return value, nil
}

func Load(path string) (Checkpoint, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read reboot checkpoint: %w", err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return Checkpoint{}, fmt.Errorf("decode reboot checkpoint: %w", err)
	}
	if checkpoint.Version != Version {
		return Checkpoint{}, fmt.Errorf(
			"reboot checkpoint version is %d; expected %d",
			checkpoint.Version,
			Version,
		)
	}
	if checkpoint.MachineID == "" || checkpoint.ConfigDigest == "" ||
		checkpoint.BootID == "" || checkpoint.CreatedAt.IsZero() {
		return Checkpoint{}, errors.New("reboot checkpoint is incomplete")
	}
	return checkpoint, nil
}

func SaveCheckpoint(path string, checkpoint Checkpoint) error {
	checkpoint.Version = Version
	return saveJSON(path, checkpoint)
}

func SaveReport(path string, report Report) error {
	return saveJSON(path, report)
}

func saveJSON(path string, value any) error {
	if path == "" {
		return errors.New("state path is required")
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse unsafe state file %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect state file: %w", err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(parent, ".envctl-state-*")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("protect temporary state file: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		cleanup()
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temporary state file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close temporary state file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("publish state file: %w", err)
	}
	return nil
}
