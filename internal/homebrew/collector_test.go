package homebrew

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type fakeRunner struct {
	output []byte
	err    error
}

func (r fakeRunner) Output(context.Context, string, ...string) ([]byte, error) {
	return r.output, r.err
}

func TestCollectsFormulaeAndCasks(t *testing.T) {
	input := []byte(`{
	  "formulae": [{
	    "name": "example-formula",
	    "full_name": "example-formula",
	    "tap": "homebrew/core",
	    "installed": [{
	      "version": "1.2.3",
	      "installed_on_request": true
	    }]
	  }],
	  "casks": [{
	    "token": "example-app",
	    "full_token": "example-app",
	    "tap": "example/tools",
	    "installed": "4.5.6",
	    "artifacts": [{
	      "app": ["Example.app"],
	      "target": "/Applications/Example.app"
	    }]
	  }]
	}`)

	got, err := NewCollector(fakeRunner{output: input}).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Collect() returned %d packages, want 2", len(got))
	}

	if got[0].Kind != model.KindCask || got[0].Source != "example/tools" {
		t.Fatalf("cask identity = %#v", got[0])
	}
	if got[0].Application != "/Applications/Example.app" {
		t.Fatalf("cask application = %q", got[0].Application)
	}
	if got[1].Kind != model.KindFormula || got[1].Version != "1.2.3" {
		t.Fatalf("formula identity = %#v", got[1])
	}
	if got[1].Requested == nil || !*got[1].Requested {
		t.Fatalf("formula requested = %#v", got[1].Requested)
	}
}

func TestReportsRunnerFailure(t *testing.T) {
	_, err := NewCollector(fakeRunner{err: errors.New("brew unavailable")}).Collect(context.Background())
	if err == nil {
		t.Fatal("Collect() error = nil, want error")
	}
}

func TestReceiptTapUsesInstalledCaskProvenance(t *testing.T) {
	caskroom := t.TempDir()
	metadata := filepath.Join(caskroom, "example-app", ".metadata")
	if err := os.MkdirAll(metadata, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(metadata, "INSTALL_RECEIPT.json"),
		[]byte(`{"source":{"tap":"example/tools"}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if got := receiptTap(caskroom, "example-app"); got != "example/tools" {
		t.Fatalf("receiptTap() = %q, want example/tools", got)
	}
}

func TestReceiptTapRejectsUnsafeToken(t *testing.T) {
	if got := receiptTap(t.TempDir(), "../outside"); got != "" {
		t.Fatalf("receiptTap() = %q, want empty", got)
	}
}
