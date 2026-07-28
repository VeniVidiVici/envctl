package mise

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type fakeRunner struct {
	output []byte
	err    error
}

func (r fakeRunner) Output(
	context.Context,
	string,
	...string,
) ([]byte, error) {
	return r.output, r.err
}

func TestCollectorReturnsActiveInstalledRequestedVersions(t *testing.T) {
	raw := []byte(`{
		"node": [
			{"version":"24.14.0","requested_version":"24","installed":true,"active":true},
			{"version":"25.8.1","installed":true,"active":false}
		],
		"rust": [
			{"version":"stable","requested_version":"stable","installed":true,"active":true}
		]
	}`)
	got, err := NewCollector(fakeRunner{output: raw}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []model.InstalledPackage{
		{Manager: model.ManagerMise, Kind: model.KindTool, Package: "node", Version: "24"},
		{Manager: model.ManagerMise, Kind: model.KindTool, Package: "rust", Version: "stable"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packages = %#v, want %#v", got, want)
	}
}

func TestCollectorRejectsCommandAndSchemaFailures(t *testing.T) {
	for _, runner := range []fakeRunner{
		{err: errors.New("missing")},
		{output: []byte(`{"node":[`)},
	} {
		if _, err := NewCollector(runner).Collect(context.Background()); err == nil {
			t.Fatal("Collect() error = nil, want failure")
		}
	}
}
