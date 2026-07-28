package recovery

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeniVidiVici/envctl/internal/model"
)

const (
	maxArchiveMemberBytes = 16 << 20
	maxArchiveBytes       = 64 << 20
	maxCommandOutputBytes = 16 << 20
	maxGPGListingBytes    = 4 << 20
)

type Planner struct {
	home     string
	lookPath func(string) (string, error)
	command  func(context.Context, string, ...string) *exec.Cmd
}

func NewPlanner(home string) (*Planner, error) {
	absoluteHome, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	info, err := os.Lstat(absoluteHome)
	if err != nil {
		return nil, fmt.Errorf("inspect home directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("home path must be a real directory")
	}
	return &Planner{
		home:     absoluteHome,
		lookPath: exec.LookPath,
		command:  exec.CommandContext,
	}, nil
}

func (p *Planner) Plan(
	ctx context.Context,
	specs []model.RecoverySpec,
) model.RecoveryPlan {
	ordered := append([]model.RecoverySpec(nil), specs...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})
	plan := model.RecoveryPlan{Ready: true}
	for _, spec := range ordered {
		finding := p.inspect(ctx, spec)
		plan.Findings = append(plan.Findings, finding)
		switch finding.Status {
		case model.RecoveryFindingSatisfied:
			plan.Summary.Satisfied++
		case model.RecoveryFindingMissing:
			plan.Summary.Missing++
		case model.RecoveryFindingDrifted:
			plan.Summary.Drifted++
		case model.RecoveryFindingBlocked:
			plan.Summary.Blocked++
			plan.Ready = false
		case model.RecoveryFindingToolMissing:
			plan.Summary.ToolMissing++
			plan.Ready = false
		case model.RecoveryFindingSourceMissing:
			plan.Summary.SourceMissing++
			plan.Ready = false
		case model.RecoveryFindingSourceUnsafe:
			plan.Summary.SourceUnsafe++
			plan.Ready = false
		}
	}
	return plan
}

func (p *Planner) inspect(
	ctx context.Context,
	spec model.RecoverySpec,
) model.RecoveryFinding {
	finding := model.RecoveryFinding{
		RecoveryID: spec.ID,
		Kind:       spec.Kind,
		Target:     spec.Target,
	}
	var status model.RecoveryFindingStatus
	var detail string
	switch spec.Kind {
	case model.RecoveryKindSOPSFile:
		status, detail = p.inspectSOPSFile(ctx, spec)
	case model.RecoveryKindAgeArchive:
		status, detail = p.inspectAgeArchive(ctx, spec)
	case model.RecoveryKindGPGKeyring:
		status, detail = p.inspectGPGKeyring(ctx, spec)
	default:
		status = model.RecoveryFindingBlocked
		detail = "unsupported recovery kind"
	}
	finding.Status = status
	finding.Detail = detail
	return finding
}

func (p *Planner) inspectSOPSFile(
	ctx context.Context,
	spec model.RecoverySpec,
) (model.RecoveryFindingStatus, string) {
	sops, err := p.lookPath("sops")
	if err != nil {
		return model.RecoveryFindingToolMissing, "sops is unavailable"
	}
	if status, detail := inspectRegularSource(spec.Source); status != "" {
		return status, detail
	}
	identity := filepath.Join(p.home, ".config", "sops", "age", "keys.txt")
	if status, detail := inspectAgeIdentity(identity); status != "" {
		return status, detail
	}
	sourceDigest, err := p.commandDigestWithEnvironment(
		ctx,
		map[string]string{"SOPS_AGE_KEY_FILE": identity},
		sops,
		"decrypt",
		"--output-type",
		spec.Format,
		spec.Source,
	)
	if err != nil {
		return model.RecoveryFindingSourceUnsafe,
			"encrypted source could not be decrypted"
	}
	info, err := os.Lstat(spec.Target)
	if errors.Is(err, os.ErrNotExist) {
		return model.RecoveryFindingMissing, "credential target is absent"
	}
	if err != nil {
		return model.RecoveryFindingBlocked, "credential target could not be inspected"
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return model.RecoveryFindingBlocked,
			"credential target is not a regular machine-local file"
	}
	if info.Mode().Perm() != 0o600 {
		return model.RecoveryFindingDrifted, "credential target mode is not 0600"
	}
	targetDigest, err := digestFile(spec.Target, maxArchiveMemberBytes)
	if err != nil {
		return model.RecoveryFindingBlocked, "credential target could not be read"
	}
	if targetDigest != sourceDigest {
		return model.RecoveryFindingDrifted,
			"credential target differs from the encrypted desired source"
	}
	return model.RecoveryFindingSatisfied,
		"credential target matches the decryptable desired source"
}

func (p *Planner) inspectAgeArchive(
	ctx context.Context,
	spec model.RecoverySpec,
) (model.RecoveryFindingStatus, string) {
	age, err := p.lookPath("age")
	if err != nil {
		return model.RecoveryFindingToolMissing, "age is unavailable"
	}
	identity := filepath.Join(p.home, ".config", "sops", "age", "keys.txt")
	if status, detail := inspectAgeIdentity(identity); status != "" {
		return status, detail
	}
	if status, detail := inspectRegularSource(spec.Source); status != "" {
		return status, detail
	}
	memberDigests, err := p.ageArchiveDigests(
		ctx,
		age,
		identity,
		spec.Source,
		spec.Members,
	)
	if err != nil {
		return model.RecoveryFindingSourceUnsafe,
			"encrypted archive could not be validated"
	}
	info, err := os.Lstat(spec.Target)
	if errors.Is(err, os.ErrNotExist) {
		return model.RecoveryFindingMissing, "credential directory is absent"
	}
	if err != nil {
		return model.RecoveryFindingBlocked,
			"credential directory could not be inspected"
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return model.RecoveryFindingBlocked,
			"credential directory is not a real machine-local directory"
	}
	if info.Mode().Perm() != 0o700 {
		return model.RecoveryFindingDrifted,
			"credential directory mode is not 0700"
	}
	for _, member := range spec.Members {
		target := filepath.Join(spec.Target, member)
		memberInfo, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			return model.RecoveryFindingMissing,
				"one or more credential files are absent"
		}
		if err != nil || !memberInfo.Mode().IsRegular() ||
			memberInfo.Mode()&os.ModeSymlink != 0 {
			return model.RecoveryFindingBlocked,
				"one or more credential files are unsafe"
		}
		if memberInfo.Mode().Perm() != 0o600 {
			return model.RecoveryFindingDrifted,
				"one or more credential file modes are not 0600"
		}
		targetDigest, err := digestFile(target, maxArchiveMemberBytes)
		if err != nil {
			return model.RecoveryFindingBlocked,
				"one or more credential files could not be read"
		}
		if targetDigest != memberDigests[member] {
			return model.RecoveryFindingDrifted,
				"one or more credential files differ from the encrypted archive"
		}
	}
	return model.RecoveryFindingSatisfied,
		"all encrypted archive members match machine-local credential files"
}

func (p *Planner) inspectGPGKeyring(
	ctx context.Context,
	spec model.RecoverySpec,
) (model.RecoveryFindingStatus, string) {
	age, err := p.lookPath("age")
	if err != nil {
		return model.RecoveryFindingToolMissing, "age is unavailable"
	}
	gpg, err := p.lookPath("gpg")
	if err != nil {
		return model.RecoveryFindingToolMissing, "gpg is unavailable"
	}
	identity := filepath.Join(p.home, ".config", "sops", "age", "keys.txt")
	if status, detail := inspectAgeIdentity(identity); status != "" {
		return status, detail
	}
	for _, role := range []string{"public", "private", "ownertrust"} {
		if status, detail := inspectRegularSource(spec.Sources[role]); status != "" {
			return status, detail
		}
	}
	publicFingerprint, err := p.gpgPublicFingerprint(
		ctx,
		gpg,
		spec.Sources["public"],
	)
	if err != nil || publicFingerprint != spec.Fingerprint {
		return model.RecoveryFindingSourceUnsafe,
			"public recovery export does not contain the expected fingerprint"
	}
	for _, role := range []string{"private", "ownertrust"} {
		if _, err := p.commandDigest(
			ctx,
			age,
			"--decrypt",
			"--identity",
			identity,
			spec.Sources[role],
		); err != nil {
			return model.RecoveryFindingSourceUnsafe,
				"encrypted GPG recovery input could not be decrypted"
		}
	}
	info, err := os.Lstat(spec.Target)
	if errors.Is(err, os.ErrNotExist) {
		return model.RecoveryFindingMissing, "GPG keyring is absent"
	}
	if err != nil {
		return model.RecoveryFindingBlocked, "GPG keyring could not be inspected"
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return model.RecoveryFindingBlocked,
			"GPG keyring is not a real machine-local directory"
	}
	installed, err := p.gpgSecretFingerprint(ctx, gpg, spec.Target, spec.Fingerprint)
	if err != nil || installed != spec.Fingerprint {
		return model.RecoveryFindingMissing,
			"expected GPG secret key is not installed"
	}
	if info.Mode().Perm() != 0o700 {
		return model.RecoveryFindingDrifted, "GPG keyring mode is not 0700"
	}
	return model.RecoveryFindingSatisfied,
		"expected GPG secret key is installed in the machine-local keyring"
}

func inspectRegularSource(path string) (model.RecoveryFindingStatus, string) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return model.RecoveryFindingSourceMissing, "recovery source is absent"
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return model.RecoveryFindingSourceUnsafe,
			"recovery source is not a regular non-symlink file"
	}
	return "", ""
}

func inspectAgeIdentity(path string) (model.RecoveryFindingStatus, string) {
	status, _ := inspectRegularSource(path)
	if status == model.RecoveryFindingSourceMissing {
		return status, "local age identity is absent"
	}
	if status != "" {
		return status, "local age identity is unsafe"
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		return model.RecoveryFindingSourceUnsafe,
			"local age identity mode is not 0600"
	}
	return "", ""
}

func (p *Planner) commandDigest(
	ctx context.Context,
	executable string,
	args ...string,
) (string, error) {
	return p.commandDigestWithEnvironment(ctx, nil, executable, args...)
}

func (p *Planner) commandDigestWithEnvironment(
	ctx context.Context,
	environment map[string]string,
	executable string,
	args ...string,
) (string, error) {
	hasher := sha256.New()
	bounded := &boundedWriter{
		writer: hasher,
		limit:  maxCommandOutputBytes,
	}
	command := p.command(ctx, executable, args...)
	if len(environment) > 0 {
		command.Env = filteredEnvironment(
			os.Environ(),
			environment,
			"SOPS_AGE_KEY",
			"SOPS_AGE_KEY_CMD",
			"SOPS_AGE_KEY_FILE",
		)
	}
	command.Stdout = bounded
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return "", err
	}
	if bounded.exceeded {
		return "", errors.New("command output exceeds recovery size limit")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func filteredEnvironment(
	current []string,
	overrides map[string]string,
	remove ...string,
) []string {
	removed := make(map[string]bool, len(remove))
	for _, name := range remove {
		removed[name] = true
	}
	result := make([]string, 0, len(current)+len(overrides))
	for _, entry := range current {
		name, _, found := strings.Cut(entry, "=")
		if !found || removed[name] {
			continue
		}
		result = append(result, entry)
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result = append(result, name+"="+overrides[name])
	}
	return result
}

func (p *Planner) ageArchiveDigests(
	ctx context.Context,
	age, identity, source string,
	members []string,
) (map[string]string, error) {
	expected := make(map[string]bool, len(members))
	for _, member := range members {
		expected[member] = true
	}
	command := p.command(
		ctx,
		age,
		"--decrypt",
		"--identity",
		identity,
		source,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	reader := tar.NewReader(io.LimitReader(stdout, maxArchiveBytes+1))
	digests := make(map[string]string, len(members))
	var readErr error
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			readErr = err
			break
		}
		if strings.HasPrefix(header.Name, "._") &&
			expected[strings.TrimPrefix(header.Name, "._")] &&
			filepath.Base(header.Name) == header.Name &&
			(header.Typeflag == tar.TypeReg ||
				header.Typeflag == tar.TypeRegA) &&
			header.Size >= 0 && header.Size <= maxArchiveMemberBytes {
			if _, err := io.CopyN(io.Discard, reader, header.Size); err != nil {
				readErr = errors.New("truncated archive metadata member")
				break
			}
			continue
		}
		if !expected[header.Name] || filepath.Base(header.Name) != header.Name ||
			(header.Typeflag != tar.TypeReg &&
				header.Typeflag != tar.TypeRegA) || header.Size < 0 ||
			header.Size > maxArchiveMemberBytes {
			readErr = fmt.Errorf("unsafe archive member %q", header.Name)
			break
		}
		if _, exists := digests[header.Name]; exists {
			readErr = fmt.Errorf("duplicate archive member %q", header.Name)
			break
		}
		hasher := sha256.New()
		written, err := io.CopyN(hasher, reader, header.Size)
		if err != nil || written != header.Size {
			readErr = errors.New("truncated archive member")
			break
		}
		digests[header.Name] = hex.EncodeToString(hasher.Sum(nil))
	}
	if readErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, readErr
	}
	if err := command.Wait(); err != nil {
		return nil, err
	}
	if len(digests) != len(expected) {
		return nil, errors.New("archive is missing required members")
	}
	return digests, nil
}

func (p *Planner) gpgPublicFingerprint(
	ctx context.Context,
	gpg, source string,
) (fingerprint string, returnErr error) {
	scratch, err := os.MkdirTemp("", "envctl-gpg-inspect-*")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(scratch, 0o700); err != nil {
		_ = os.RemoveAll(scratch)
		return "", err
	}
	defer func() {
		if err := os.RemoveAll(scratch); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("remove temporary GPG inspection home: %w", err),
			)
		}
	}()
	command := p.command(
		ctx,
		gpg,
		"--batch",
		"--no-options",
		"--homedir",
		scratch,
		"--with-colons",
		"--show-keys",
		"--fingerprint",
		source,
	)
	output, err := boundedCommandOutput(command, maxGPGListingBytes)
	if err != nil {
		return "", err
	}
	return firstFingerprint(output), nil
}

func (p *Planner) gpgSecretFingerprint(
	ctx context.Context,
	gpg, home, fingerprint string,
) (string, error) {
	command := p.command(
		ctx,
		gpg,
		"--batch",
		"--with-colons",
		"--homedir",
		home,
		"--list-secret-keys",
		"--fingerprint",
		fingerprint,
	)
	output, err := boundedCommandOutput(command, maxGPGListingBytes)
	if err != nil {
		return "", err
	}
	return firstFingerprint(output), nil
}

func firstFingerprint(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			return fields[9]
		}
	}
	return ""
}

func digestFile(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if written > limit {
		return "", errors.New("file exceeds recovery size limit")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type boundedWriter struct {
	writer   io.Writer
	limit    int64
	written  int64
	exceeded bool
}

func (w *boundedWriter) Write(raw []byte) (int, error) {
	remaining := w.limit - w.written
	if remaining <= 0 {
		w.exceeded = true
		return 0, errors.New("recovery output size limit exceeded")
	}
	if int64(len(raw)) > remaining {
		w.exceeded = true
		written, err := w.writer.Write(raw[:remaining])
		w.written += int64(written)
		if err != nil {
			return written, err
		}
		return written, errors.New("recovery output size limit exceeded")
	}
	written, err := w.writer.Write(raw)
	w.written += int64(written)
	return written, err
}

func boundedCommandOutput(command *exec.Cmd, limit int64) (string, error) {
	var buffer strings.Builder
	bounded := &boundedWriter{writer: &buffer, limit: limit}
	command.Stdout = bounded
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return "", err
	}
	if bounded.exceeded {
		return "", errors.New("command output exceeds recovery size limit")
	}
	return buffer.String(), nil
}
