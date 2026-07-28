package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestPlannerReportsMatchingSOPSFileWithoutExposingContents(t *testing.T) {
	home := t.TempDir()
	writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "sops", "age", "keys.txt"),
		"identity",
		0o600,
	)
	source := writeRecoveryFile(
		t,
		filepath.Join(home, "config", "credential.sops.env"),
		"encrypted-placeholder",
		0o600,
	)
	target := writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "example", "env"),
		"very-secret-value",
		0o600,
	)
	t.Setenv("SOPS_AGE_KEY", "inherited-secret-must-not-be-used")
	t.Setenv("SOPS_AGE_KEY_FILE", filepath.Join(home, "wrong-identity"))
	sops := writeExecutable(t, home, "sops", `#!/bin/sh
test -z "${SOPS_AGE_KEY:-}" || exit 10
test -f "$SOPS_AGE_KEY_FILE" || exit 11
printf '%s' 'very-secret-value'
`)
	planner := newTestPlanner(t, home, map[string]string{"sops": sops})
	plan := planner.Plan(context.Background(), []model.RecoverySpec{{
		ID: "example", Kind: model.RecoveryKindSOPSFile,
		Source: source, Target: target, Format: "dotenv", Mode: "0600",
	}})
	if !plan.Ready || plan.Summary.Satisfied != 1 ||
		plan.Findings[0].Status != model.RecoveryFindingSatisfied {
		t.Fatalf("plan = %#v", plan)
	}
	if strings.Contains(plan.Findings[0].Detail, "very-secret-value") {
		t.Fatalf("finding exposed plaintext: %#v", plan.Findings[0])
	}
}

func TestPlannerValidatesAgeArchiveAndLocalMembers(t *testing.T) {
	home := t.TempDir()
	identity := writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "sops", "age", "keys.txt"),
		"identity",
		0o600,
	)
	if identity == "" {
		t.Fatal("identity path is empty")
	}
	archive := filepath.Join(home, "recovery", "ssh.tar.age")
	writeTar(t, archive, map[string]string{
		"id_one":   "private-one",
		"id_two":   "private-two",
		"._id_one": "appledouble-metadata",
	})
	sshDirectory := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sshDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRecoveryFile(t, filepath.Join(sshDirectory, "id_one"), "private-one", 0o600)
	writeRecoveryFile(t, filepath.Join(sshDirectory, "id_two"), "private-two", 0o600)
	age := writeExecutable(t, home, "age", `#!/bin/sh
cat "$4"
`)
	planner := newTestPlanner(t, home, map[string]string{"age": age})
	plan := planner.Plan(context.Background(), []model.RecoverySpec{{
		ID: "ssh", Kind: model.RecoveryKindAgeArchive,
		Source: archive, Target: sshDirectory, Mode: "0600",
		Members: []string{"id_one", "id_two"},
	}})
	if !plan.Ready || plan.Summary.Satisfied != 1 ||
		plan.Findings[0].Status != model.RecoveryFindingSatisfied {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlannerBlocksUnsafeRecoverySource(t *testing.T) {
	home := t.TempDir()
	writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "sops", "age", "keys.txt"),
		"identity",
		0o600,
	)
	realSource := writeRecoveryFile(
		t,
		filepath.Join(home, "recovery", "real.sops.env"),
		"encrypted",
		0o600,
	)
	source := filepath.Join(home, "recovery", "linked.sops.env")
	if err := os.Symlink(realSource, source); err != nil {
		t.Fatal(err)
	}
	sops := writeExecutable(t, home, "sops", "#!/bin/sh\nexit 0\n")
	planner := newTestPlanner(t, home, map[string]string{"sops": sops})
	plan := planner.Plan(context.Background(), []model.RecoverySpec{{
		ID: "unsafe", Kind: model.RecoveryKindSOPSFile,
		Source: source, Target: filepath.Join(home, ".config", "example", "env"),
		Format: "dotenv", Mode: "0600",
	}})
	if plan.Ready ||
		plan.Findings[0].Status != model.RecoveryFindingSourceUnsafe {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlannerValidatesExpectedGPGSecretFingerprint(t *testing.T) {
	home := t.TempDir()
	fingerprint := strings.Repeat("A", 40)
	writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "sops", "age", "keys.txt"),
		"identity",
		0o600,
	)
	recoveryDirectory := filepath.Join(home, "recovery")
	public := writeRecoveryFile(
		t,
		filepath.Join(recoveryDirectory, "public.asc"),
		"public",
		0o600,
	)
	private := writeRecoveryFile(
		t,
		filepath.Join(recoveryDirectory, "private.asc.age"),
		"private",
		0o600,
	)
	ownertrust := writeRecoveryFile(
		t,
		filepath.Join(recoveryDirectory, "ownertrust.txt.age"),
		"trust",
		0o600,
	)
	keyring := filepath.Join(home, ".gnupg")
	if err := os.Mkdir(keyring, 0o700); err != nil {
		t.Fatal(err)
	}
	age := writeExecutable(t, home, "age", `#!/bin/sh
cat "$4"
`)
	gpg := writeExecutable(t, home, "gpg", `#!/bin/sh
printf '%s\n' 'fpr:::::::::AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:'
`)
	planner := newTestPlanner(t, home, map[string]string{
		"age": age,
		"gpg": gpg,
	})
	plan := planner.Plan(context.Background(), []model.RecoverySpec{{
		ID: "gpg", Kind: model.RecoveryKindGPGKeyring,
		Target: keyring, Mode: "0700", Fingerprint: fingerprint,
		Sources: map[string]string{
			"public":     public,
			"private":    private,
			"ownertrust": ownertrust,
		},
	}})
	if !plan.Ready || plan.Summary.Satisfied != 1 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlannerPublicGPGInspectionDoesNotCreateKeyring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fingerprint := strings.Repeat("A", 40)
	writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "sops", "age", "keys.txt"),
		"identity",
		0o600,
	)
	recoveryDirectory := filepath.Join(home, "recovery")
	public := writeRecoveryFile(
		t,
		filepath.Join(recoveryDirectory, "public.asc"),
		"public",
		0o600,
	)
	private := writeRecoveryFile(
		t,
		filepath.Join(recoveryDirectory, "private.asc.age"),
		"private",
		0o600,
	)
	ownertrust := writeRecoveryFile(
		t,
		filepath.Join(recoveryDirectory, "ownertrust.txt.age"),
		"trust",
		0o600,
	)
	age := writeExecutable(t, home, "age", `#!/bin/sh
cat "$4"
`)
	gpg := writeExecutable(t, home, "gpg", `#!/bin/sh
scratch=
no_options=
while test "$#" -gt 0; do
  case "$1" in
    --homedir)
      shift
      scratch=$1
      ;;
    --no-options)
      no_options=yes
      ;;
  esac
  shift
done
if test -z "$scratch" || test "$no_options" != yes; then
  mkdir -p "$HOME/.gnupg"
else
  touch "$scratch/inspection-state"
fi
printf '%s\n' 'fpr:::::::::AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:'
`)
	planner := newTestPlanner(t, home, map[string]string{
		"age": age,
		"gpg": gpg,
	})
	target := filepath.Join(home, ".gnupg")
	plan := planner.Plan(context.Background(), []model.RecoverySpec{{
		ID: "gpg", Kind: model.RecoveryKindGPGKeyring,
		Target: target, Mode: "0700", Fingerprint: fingerprint,
		Sources: map[string]string{
			"public":     public,
			"private":    private,
			"ownertrust": ownertrust,
		},
	}})
	if !plan.Ready ||
		plan.Findings[0].Status != model.RecoveryFindingMissing {
		t.Fatalf("plan = %#v", plan)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("read-only planning created GPG keyring: %v", err)
	}
}

func TestPlannerReportsMissingToolWithoutInspectingTarget(t *testing.T) {
	home := t.TempDir()
	source := writeRecoveryFile(
		t,
		filepath.Join(home, "config", "credential.sops.env"),
		"encrypted",
		0o600,
	)
	planner := newTestPlanner(t, home, nil)
	plan := planner.Plan(context.Background(), []model.RecoverySpec{{
		ID: "example", Kind: model.RecoveryKindSOPSFile,
		Source: source, Target: filepath.Join(home, ".config", "example", "env"),
		Format: "dotenv", Mode: "0600",
	}})
	if plan.Ready || plan.Summary.ToolMissing != 1 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlannerRejectsUnsafeAgeIdentityMode(t *testing.T) {
	home := t.TempDir()
	writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "sops", "age", "keys.txt"),
		"identity",
		0o644,
	)
	source := writeRecoveryFile(
		t,
		filepath.Join(home, "config", "credential.sops.env"),
		"encrypted",
		0o600,
	)
	sops := writeExecutable(t, home, "sops", "#!/bin/sh\nexit 0\n")
	planner := newTestPlanner(t, home, map[string]string{"sops": sops})
	plan := planner.Plan(context.Background(), []model.RecoverySpec{{
		ID: "example", Kind: model.RecoveryKindSOPSFile,
		Source: source, Target: filepath.Join(home, ".config", "example", "env"),
		Format: "dotenv", Mode: "0600",
	}})
	if plan.Ready ||
		plan.Findings[0].Status != model.RecoveryFindingSourceUnsafe ||
		!strings.Contains(plan.Findings[0].Detail, "0600") {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestBoundedWriterRejectsExcessPlaintext(t *testing.T) {
	var output strings.Builder
	writer := &boundedWriter{writer: &output, limit: 4}
	written, err := writer.Write([]byte("secret"))
	if err == nil || written != 4 || !writer.exceeded || output.String() != "secr" {
		t.Fatalf(
			"write = %d, %v; exceeded = %v output = %q",
			written,
			err,
			writer.exceeded,
			output.String(),
		)
	}
}

func newTestPlanner(
	t *testing.T,
	home string,
	tools map[string]string,
) *Planner {
	t.Helper()
	planner, err := NewPlanner(home)
	if err != nil {
		t.Fatal(err)
	}
	planner.lookPath = func(name string) (string, error) {
		path, ok := tools[name]
		if !ok {
			return "", exec.ErrNotFound
		}
		return path, nil
	}
	return planner
}

func writeRecoveryFile(
	t *testing.T,
	path, contents string,
	mode os.FileMode,
) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeExecutable(t *testing.T, root, name, contents string) string {
	t.Helper()
	return writeRecoveryFile(t, filepath.Join(root, "bin", name), contents, 0o700)
}

func writeTar(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	for name, contents := range files {
		if err := writer.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o600,
			Size: int64(len(contents)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writeRecoveryFile(t, path, raw.String(), 0o600)
}
