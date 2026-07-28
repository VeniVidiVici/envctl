package fleetrefresh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/VeniVidiVici/envctl/internal/model"
)

const StatusFilename = "fleet-status.json"

type Target struct {
	ID         string           `json:"id"`
	AccessType string           `json:"access_type"`
	Host       string           `json:"host,omitempty"`
	Links      []model.LinkSpec `json:"links,omitempty"`
}

type Result struct {
	MachineID        string     `json:"machine_id"`
	AccessType       string     `json:"access_type"`
	Host             string     `json:"host,omitempty"`
	Status           string     `json:"status"`
	CollectedAt      *time.Time `json:"collected_at,omitempty"`
	Error            string     `json:"error,omitempty"`
	RetainedLastGood bool       `json:"retained_last_good,omitempty"`
}

type Status struct {
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Results     []Result  `json:"results"`
}

type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

type ExecRunner struct{}

func (ExecRunner) Run(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

type Refresher struct {
	executable     string
	inventoryDir   string
	runner         Runner
	connectTimeout time.Duration
}

func New(executable, inventoryDir string, runner Runner) *Refresher {
	return &Refresher{
		executable:     executable,
		inventoryDir:   inventoryDir,
		runner:         runner,
		connectTimeout: 8 * time.Second,
	}
}

func (r *Refresher) Refresh(ctx context.Context, targets []Target) (Status, error) {
	if len(targets) == 0 {
		return Status{}, errors.New("no fleet targets selected")
	}
	if err := os.MkdirAll(r.inventoryDir, 0o700); err != nil {
		return Status{}, fmt.Errorf("create inventory directory: %w", err)
	}
	if err := os.Chmod(r.inventoryDir, 0o700); err != nil {
		return Status{}, fmt.Errorf("secure inventory directory: %w", err)
	}

	status := Status{StartedAt: time.Now().UTC()}
	results := make(chan Result, len(targets))
	var group sync.WaitGroup
	for _, target := range targets {
		target := target
		group.Add(1)
		go func() {
			defer group.Done()
			results <- r.refreshTarget(ctx, target)
		}()
	}
	group.Wait()
	close(results)
	for result := range results {
		status.Results = append(status.Results, result)
	}
	sort.Slice(status.Results, func(i, j int) bool {
		return status.Results[i].MachineID < status.Results[j].MachineID
	})
	status.CompletedAt = time.Now().UTC()
	persistedStatus := status
	previous, loadErr := LoadStatus(r.inventoryDir)
	if loadErr == nil {
		persistedStatus.Results = mergeResults(previous.Results, status.Results)
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return status, fmt.Errorf("load previous fleet status: %w", loadErr)
	}
	if err := writeJSONAtomically(
		filepath.Join(r.inventoryDir, StatusFilename),
		persistedStatus,
	); err != nil {
		return status, err
	}
	return status, nil
}

func mergeResults(previous, current []Result) []Result {
	byMachine := make(map[string]Result, len(previous)+len(current))
	for _, result := range previous {
		byMachine[result.MachineID] = result
	}
	for _, result := range current {
		byMachine[result.MachineID] = result
	}
	results := make([]Result, 0, len(byMachine))
	for _, result := range byMachine {
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].MachineID < results[j].MachineID
	})
	return results
}

func (r *Refresher) refreshTarget(ctx context.Context, target Target) Result {
	result := Result{
		MachineID: target.ID, AccessType: target.AccessType, Host: target.Host,
	}
	inventory, err := r.Collect(ctx, target)
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		_, statErr := os.Stat(r.inventoryPath(target.ID))
		result.RetainedLastGood = statErr == nil
		return result
	}
	if err := writeJSONAtomically(r.inventoryPath(target.ID), inventory); err != nil {
		result.Status = "error"
		result.Error = err.Error()
		return result
	}
	result.Status = "ok"
	collectedAt := inventory.CollectedAt
	result.CollectedAt = &collectedAt
	return result
}

// Collect gathers a live inventory without writing snapshots or fleet status.
func (r *Refresher) Collect(
	ctx context.Context,
	target Target,
) (model.Inventory, error) {
	if !safeIdentifier(target.ID) {
		return model.Inventory{}, fmt.Errorf("unsafe machine id %q", target.ID)
	}
	switch target.AccessType {
	case "local":
		return r.collectLocal(ctx, target.Links)
	case "ssh":
		if target.Host == "" {
			return model.Inventory{}, errors.New("SSH target has no host")
		}
		return r.collectRemote(ctx, target.Host, target.Links)
	default:
		return model.Inventory{}, fmt.Errorf(
			"unsupported access type %q", target.AccessType,
		)
	}
}

func (r *Refresher) collectLocal(
	ctx context.Context,
	links []model.LinkSpec,
) (model.Inventory, error) {
	args := []string{"audit", "--json", "--no-record"}
	encodedLinks, err := encodeLinkSpecs(links)
	if err != nil {
		return model.Inventory{}, err
	}
	if encodedLinks != "" {
		args = append(args, "--link-specs", encodedLinks)
	}
	stdout, stderr, err := r.runner.Run(
		ctx, r.executable, args...,
	)
	if err != nil {
		return model.Inventory{}, commandError("local audit", err, stderr)
	}
	return decodeInventory(stdout)
}

func (r *Refresher) collectRemote(
	ctx context.Context,
	host string,
	links []model.LinkSpec,
) (model.Inventory, error) {
	timeoutSeconds := max(1, int(r.connectTimeout.Seconds()))
	sshOptions := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", fmt.Sprintf("ConnectTimeout=%d", timeoutSeconds),
	}
	stdout, stderr, err := r.runner.Run(
		ctx,
		"ssh",
		append(sshOptions, host, "uname -s; uname -m")...,
	)
	if err != nil {
		return model.Inventory{}, commandError("verify remote platform", err, stderr)
	}
	platform := strings.Fields(string(stdout))
	if len(platform) < 2 {
		return model.Inventory{}, fmt.Errorf("verify remote platform: unexpected response %q",
			strings.TrimSpace(string(stdout)))
	}
	if strings.ToLower(platform[0]) != runtime.GOOS || platform[1] != runtime.GOARCH {
		return model.Inventory{}, fmt.Errorf(
			"remote platform %s/%s does not match envctl binary %s/%s",
			strings.ToLower(platform[0]), platform[1], runtime.GOOS, runtime.GOARCH,
		)
	}

	suffix, err := randomSuffix()
	if err != nil {
		return model.Inventory{}, err
	}
	remotePath := "/tmp/envctl-fleet-" + suffix
	destination := host + ":" + remotePath
	scpOptions := []string{
		"-q",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", fmt.Sprintf("ConnectTimeout=%d", timeoutSeconds),
	}
	if _, stderr, err := r.runner.Run(
		ctx, "scp", append(scpOptions, r.executable, destination)...,
	); err != nil {
		return model.Inventory{}, commandError("copy envctl to remote", err, stderr)
	}
	defer r.cleanupRemote(host, remotePath, sshOptions)

	encodedLinks, err := encodeLinkSpecs(links)
	if err != nil {
		return model.Inventory{}, err
	}
	remoteCommand := fmt.Sprintf(
		"PATH=$HOME/.local/bin:$HOME/.opencode/bin:$HOME/.bun/bin:"+
			"/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:$PATH "+
			"%s audit --json --no-record",
		remotePath,
	)
	if encodedLinks != "" {
		remoteCommand += " --link-specs " + encodedLinks
	}
	stdout, stderr, err = r.runner.Run(
		ctx, "ssh", append(sshOptions, host, remoteCommand)...,
	)
	if err != nil {
		return model.Inventory{}, commandError("run remote audit", err, stderr)
	}
	return decodeInventory(stdout)
}

func encodeLinkSpecs(specs []model.LinkSpec) (string, error) {
	if len(specs) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(specs)
	if err != nil {
		return "", fmt.Errorf("encode portable link specifications: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (r *Refresher) cleanupRemote(host, remotePath string, sshOptions []string) {
	cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, _ = r.runner.Run(
		cleanupContext,
		"ssh",
		append(sshOptions, host, "rm -f "+remotePath)...,
	)
}

func (r *Refresher) inventoryPath(machineID string) string {
	return filepath.Join(r.inventoryDir, machineID+".json")
}

func decodeInventory(raw []byte) (model.Inventory, error) {
	var inventory model.Inventory
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return model.Inventory{}, fmt.Errorf("decode audit inventory: %w", err)
	}
	if inventory.CollectedAt.IsZero() {
		return model.Inventory{}, errors.New("audit inventory has no collection time")
	}
	return inventory, nil
}

func writeJSONAtomically(path string, value any) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("secure temporary state file: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		file.Close()
		return fmt.Errorf("encode state file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync state file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close state file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}

func commandError(operation string, commandErr error, stderr []byte) error {
	message := strings.TrimSpace(string(stderr))
	if len(message) > 500 {
		message = message[:500] + "..."
	}
	if message == "" {
		return fmt.Errorf("%s: %w", operation, commandErr)
	}
	return fmt.Errorf("%s: %w: %s", operation, commandErr, message)
}

func randomSuffix() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create remote temporary name: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func safeIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func LoadStatus(directory string) (Status, error) {
	raw, err := os.ReadFile(filepath.Join(directory, StatusFilename))
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(raw, &status); err != nil {
		return Status{}, fmt.Errorf("decode fleet status: %w", err)
	}
	return status, nil
}
