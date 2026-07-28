package recovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type fakeRecoveryJournal struct {
	started    []string
	backups    []string
	completed  []string
	failed     []string
	rolledBack []string
}

func (j *fakeRecoveryJournal) StartRecovery(
	_ context.Context,
	action RecoveryAction,
) error {
	j.started = append(j.started, action.RecoveryID)
	return nil
}

func (j *fakeRecoveryJournal) RecordRecoveryBackup(
	_ context.Context,
	action RecoveryAction,
	_, _ string,
) error {
	j.backups = append(j.backups, action.RecoveryID)
	return nil
}

func (j *fakeRecoveryJournal) CompleteRecovery(
	_ context.Context,
	action RecoveryAction,
) error {
	j.completed = append(j.completed, action.RecoveryID)
	return nil
}

func (j *fakeRecoveryJournal) FailRecovery(
	_ context.Context,
	action RecoveryAction,
	_ string,
) error {
	j.failed = append(j.failed, action.RecoveryID)
	return nil
}

func (j *fakeRecoveryJournal) RollBackRecovery(
	_ context.Context,
	action RecoveryAction,
	_ string,
) error {
	j.rolledBack = append(j.rolledBack, action.RecoveryID)
	return nil
}

func TestTransactionRestoresSOPSAndSSHWithBackups(t *testing.T) {
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
	sopsSource := writeRecoveryFile(
		t,
		filepath.Join(home, "recovery", "application.sops.env"),
		"new-application-secret",
		0o600,
	)
	sopsTarget := writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "application", "env"),
		"old-application-secret",
		0o600,
	)
	archive := filepath.Join(home, "recovery", "ssh.tar.age")
	writeTar(t, archive, map[string]string{
		"id_one":   "new-private-one",
		"id_two":   "new-private-two",
		"._id_one": "metadata",
	})
	sshTarget := filepath.Join(home, ".ssh")
	writeRecoveryFile(
		t,
		filepath.Join(sshTarget, "id_one"),
		"old-private-one",
		0o600,
	)
	writeRecoveryFile(
		t,
		filepath.Join(sshTarget, "known_hosts"),
		"machine-local-state",
		0o600,
	)
	sops := writeExecutable(t, home, "sops", `#!/bin/sh
for argument do source=$argument; done
cat "$source"
`)
	age := writeExecutable(t, home, "age", `#!/bin/sh
for argument do source=$argument; done
cat "$source"
`)
	specs := []model.RecoverySpec{
		{
			ID: "application", Kind: model.RecoveryKindSOPSFile,
			Source: sopsSource, Target: sopsTarget,
			Format: "dotenv", Mode: "0600",
		},
		{
			ID: "ssh", Kind: model.RecoveryKindAgeArchive,
			Source: archive, Target: sshTarget, Mode: "0600",
			Members: []string{"id_one", "id_two"},
		},
	}
	transaction := newRecoveryTransaction(t, home, map[string]string{
		"sops": sops,
		"age":  age,
	})
	plan, statusPlan, err := transaction.Plan(context.Background(), specs)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || len(plan.Actions) != 2 ||
		statusPlan.Summary.Drifted != 2 {
		t.Fatalf("plan = %#v status = %#v", plan, statusPlan)
	}
	journal := &fakeRecoveryJournal{}
	result, err := transaction.Apply(
		context.Background(),
		plan,
		specs,
		journal,
	)
	if err != nil {
		t.Fatalf("Apply() error = %v result = %#v", err, result)
	}
	if !result.Verified || result.RolledBack || len(result.Applied) != 2 {
		t.Fatalf("result = %#v", result)
	}
	assertRecoveryContents(t, sopsTarget, "new-application-secret", 0o600)
	assertRecoveryContents(
		t,
		filepath.Join(sshTarget, "id_one"),
		"new-private-one",
		0o600,
	)
	assertRecoveryContents(
		t,
		filepath.Join(sshTarget, "id_two"),
		"new-private-two",
		0o600,
	)
	assertRecoveryContents(
		t,
		filepath.Join(sshTarget, "known_hosts"),
		"machine-local-state",
		0o600,
	)
	assertRecoveryContents(
		t,
		plan.Actions[0].BackupPath,
		"old-application-secret",
		0o600,
	)
	assertRecoveryContents(
		t,
		filepath.Join(plan.Actions[1].BackupPath, "id_one"),
		"old-private-one",
		0o600,
	)
	if strings.Join(journal.started, ",") != "application,ssh" ||
		strings.Join(journal.backups, ",") != "application,ssh" ||
		strings.Join(journal.completed, ",") != "application,ssh" ||
		len(journal.failed) != 0 ||
		len(journal.rolledBack) != 0 {
		t.Fatalf("journal = %#v", journal)
	}
}

func TestTransactionRollsBackEarlierRecoveryWhenTargetChanges(t *testing.T) {
	home := t.TempDir()
	writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "sops", "age", "keys.txt"),
		"identity",
		0o600,
	)
	sops := writeExecutable(t, home, "sops", `#!/bin/sh
for argument do source=$argument; done
cat "$source"
`)
	firstSource := writeRecoveryFile(
		t,
		filepath.Join(home, "recovery", "first.sops.env"),
		"first-secret",
		0o600,
	)
	secondSource := writeRecoveryFile(
		t,
		filepath.Join(home, "recovery", "second.sops.env"),
		"second-secret",
		0o600,
	)
	firstTarget := filepath.Join(home, ".config", "first", "env")
	secondTarget := filepath.Join(home, ".config", "second", "env")
	specs := []model.RecoverySpec{
		{
			ID: "first", Kind: model.RecoveryKindSOPSFile,
			Source: firstSource, Target: firstTarget,
			Format: "dotenv", Mode: "0600",
		},
		{
			ID: "second", Kind: model.RecoveryKindSOPSFile,
			Source: secondSource, Target: secondTarget,
			Format: "dotenv", Mode: "0600",
		},
	}
	transaction := newRecoveryTransaction(
		t,
		home,
		map[string]string{"sops": sops},
	)
	plan, _, err := transaction.Plan(context.Background(), specs)
	if err != nil {
		t.Fatal(err)
	}
	external := writeRecoveryFile(
		t,
		filepath.Join(home, "external", "appeared"),
		"external",
		0o600,
	)
	transaction.beforeInstall = func(index int) {
		if index != 1 {
			return
		}
		if err := os.MkdirAll(filepath.Dir(secondTarget), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, secondTarget); err != nil {
			t.Fatal(err)
		}
	}
	journal := &fakeRecoveryJournal{}
	result, err := transaction.Apply(
		context.Background(),
		plan,
		specs,
		journal,
	)
	if err == nil || !result.RolledBack || result.Verified {
		t.Fatalf("result = %#v error = %v", result, err)
	}
	if _, err := os.Lstat(firstTarget); !os.IsNotExist(err) {
		t.Fatalf("first recovery was not rolled back: %v", err)
	}
	info, err := os.Lstat(secondTarget)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("external target was changed: %v %v", info, err)
	}
	if strings.Join(journal.started, ",") != "first,second" ||
		strings.Join(journal.completed, ",") != "first" ||
		strings.Join(journal.failed, ",") != "second" ||
		strings.Join(journal.rolledBack, ",") != "first" {
		t.Fatalf("journal = %#v", journal)
	}
}

func TestTransactionAtomicallyRestoresAbsentGPGKeyring(t *testing.T) {
	home := t.TempDir()
	fingerprint := strings.Repeat("A", 40)
	writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "sops", "age", "keys.txt"),
		"identity",
		0o600,
	)
	public := writeRecoveryFile(
		t,
		filepath.Join(home, "recovery", "public.asc"),
		"public",
		0o600,
	)
	private := writeRecoveryFile(
		t,
		filepath.Join(home, "recovery", "private.asc.age"),
		"private",
		0o600,
	)
	ownertrust := writeRecoveryFile(
		t,
		filepath.Join(home, "recovery", "ownertrust.txt.age"),
		"ownertrust",
		0o600,
	)
	age := writeExecutable(t, home, "age", `#!/bin/sh
for argument do source=$argument; done
cat "$source"
`)
	gpg := writeExecutable(t, home, "gpg", `#!/bin/sh
home=
operation=
last=
for argument do
  last=$argument
done
while test "$#" -gt 0; do
  case "$1" in
    --homedir)
      shift
      home=$1
      ;;
    --show-keys)
      operation=show
      ;;
    --list-secret-keys)
      operation=list
      ;;
    --import-ownertrust)
      operation=ownertrust
      ;;
    --import)
      operation=import
      ;;
  esac
  shift
done
case "$operation" in
  show)
    printf '%s\n' 'fpr:::::::::AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:'
    ;;
  list)
    test -f "$home/private-imported" || exit 2
    printf '%s\n' 'fpr:::::::::AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:'
    ;;
  import)
    mkdir -p "$home"
    if test -f "$last"; then
      touch "$home/public-imported"
    else
      cat >/dev/null
      touch "$home/private-imported"
    fi
    ;;
  ownertrust)
    cat >/dev/null
    touch "$home/ownertrust-imported"
    ;;
esac
`)
	gpgconf := writeExecutable(t, home, "gpgconf", "#!/bin/sh\nexit 0\n")
	target := filepath.Join(home, ".gnupg")
	specs := []model.RecoverySpec{{
		ID: "gpg", Kind: model.RecoveryKindGPGKeyring,
		Target: target, Mode: "0700", Fingerprint: fingerprint,
		Sources: map[string]string{
			"public": public, "private": private, "ownertrust": ownertrust,
		},
	}}
	transaction := newRecoveryTransaction(t, home, map[string]string{
		"age":     age,
		"gpg":     gpg,
		"gpgconf": gpgconf,
	})
	plan, _, err := transaction.Plan(context.Background(), specs)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || len(plan.Actions) != 1 ||
		plan.Actions[0].Type != ActionRestore {
		t.Fatalf("plan = %#v", plan)
	}
	result, err := transaction.Apply(context.Background(), plan, specs, nil)
	if err != nil {
		t.Fatalf("Apply() error = %v result = %#v", err, result)
	}
	if !result.Verified || result.RolledBack {
		t.Fatalf("result = %#v", result)
	}
	for _, marker := range []string{
		"public-imported",
		"private-imported",
		"ownertrust-imported",
	} {
		if _, err := os.Stat(filepath.Join(target, marker)); err != nil {
			t.Fatalf("missing %s: %v", marker, err)
		}
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("GPG keyring mode = %v, %v", info, err)
	}
}

func TestTransactionBlocksExistingGPGKeyringWithoutExpectedKey(t *testing.T) {
	home := t.TempDir()
	fingerprint := strings.Repeat("A", 40)
	writeRecoveryFile(
		t,
		filepath.Join(home, ".config", "sops", "age", "keys.txt"),
		"identity",
		0o600,
	)
	public := writeRecoveryFile(
		t,
		filepath.Join(home, "recovery", "public.asc"),
		"public",
		0o600,
	)
	private := writeRecoveryFile(
		t,
		filepath.Join(home, "recovery", "private.asc.age"),
		"private",
		0o600,
	)
	ownertrust := writeRecoveryFile(
		t,
		filepath.Join(home, "recovery", "ownertrust.txt.age"),
		"ownertrust",
		0o600,
	)
	target := filepath.Join(home, ".gnupg")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	age := writeExecutable(t, home, "age", `#!/bin/sh
for argument do source=$argument; done
cat "$source"
`)
	gpg := writeExecutable(t, home, "gpg", `#!/bin/sh
for argument do
  if test "$argument" = "--show-keys"; then
    printf '%s\n' 'fpr:::::::::AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:'
    exit 0
  fi
done
exit 2
`)
	transaction := newRecoveryTransaction(t, home, map[string]string{
		"age": age,
		"gpg": gpg,
	})
	plan, status, err := transaction.Plan(
		context.Background(),
		[]model.RecoverySpec{{
			ID: "gpg", Kind: model.RecoveryKindGPGKeyring,
			Target: target, Mode: "0700", Fingerprint: fingerprint,
			Sources: map[string]string{
				"public": public, "private": private, "ownertrust": ownertrust,
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ready || len(plan.Actions) != 0 || len(plan.Blockers) != 1 ||
		status.Findings[0].Status != model.RecoveryFindingMissing {
		t.Fatalf("plan = %#v status = %#v", plan, status)
	}
}

func TestTransactionRejectsSymlinkTargetParentBeforeStaging(t *testing.T) {
	home := t.TempDir()
	realDirectory := filepath.Join(home, "real-config")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDirectory, filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	transaction := newRecoveryTransaction(t, home, nil)
	_, _, err := transaction.Plan(
		context.Background(),
		[]model.RecoverySpec{{
			ID: "unsafe", Kind: model.RecoveryKindSOPSFile,
			Source: filepath.Join(home, "recovery", "source"),
			Target: filepath.Join(home, ".config", "app", "env"),
			Format: "dotenv", Mode: "0600",
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "parent is unsafe") {
		t.Fatalf("Plan() error = %v", err)
	}
}

func newRecoveryTransaction(
	t *testing.T,
	home string,
	tools map[string]string,
) *Transaction {
	t.Helper()
	transaction, err := NewTransaction(
		home,
		filepath.Join(home, ".local", "state", "envctl", "backups", "recovery"),
		filepath.Join(home, ".local", "state", "envctl", "staging", "recovery"),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction.now = func() time.Time {
		return time.Date(2026, 7, 28, 19, 0, 0, 0, time.UTC)
	}
	transaction.planner.lookPath = func(name string) (string, error) {
		path, ok := tools[name]
		if !ok {
			return "", os.ErrNotExist
		}
		return path, nil
	}
	return transaction
}

func assertRecoveryContents(
	t *testing.T,
	path, expected string,
	mode os.FileMode,
) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != expected || info.Mode().Perm() != mode {
		t.Fatalf(
			"%s contents = %q mode = %o",
			path,
			raw,
			info.Mode().Perm(),
		)
	}
}
