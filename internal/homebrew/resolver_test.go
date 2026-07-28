package homebrew

import (
	"context"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestResolveFormulaIdentity(t *testing.T) {
	input := []byte(`{
	  "formulae": [{
	    "name": "example",
	    "full_name": "example",
	    "tap": "homebrew/core",
	    "installed": []
	  }],
	  "casks": []
	}`)
	wanted := model.PackageSpec{
		ID: "example", Manager: model.ManagerBrew, Kind: model.KindUnknown,
		Package: "example", UpdatePolicy: model.UpdateManaged,
	}

	got, err := NewResolver(fakeRunner{output: input}).Resolve(context.Background(), wanted)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Kind != model.KindFormula || got.Source != "homebrew/core" {
		t.Fatalf("Resolve() = %#v", got)
	}
}

func TestResolveRejectsFormulaCaskAmbiguity(t *testing.T) {
	input := []byte(`{
	  "formulae": [{"name":"example","tap":"homebrew/core","installed":[]}],
	  "casks": [{"token":"example","tap":"homebrew/cask","installed":null}]
	}`)
	wanted := model.PackageSpec{
		ID: "example", Manager: model.ManagerBrew, Kind: model.KindUnknown,
		Package: "example", UpdatePolicy: model.UpdateManaged,
	}

	_, err := NewResolver(fakeRunner{output: input}).Resolve(context.Background(), wanted)
	if err == nil {
		t.Fatal("Resolve() error = nil, want ambiguity error")
	}
}
