package mas

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestPreflightReportsAvailabilityPriceCompatibilityAndBlockers(t *testing.T) {
	runner := preflightRunner{responses: map[string][]byte{
		"mas config --json": []byte(
			`{"mas":"7.0.0","store":"GB","region":"GB","macos":"26.5 (25F71)"}`,
		),
		"mas lookup --json 111": lookupJSON(t, lookupRecord{
			AdamID: 111, Name: "Free App", Version: "2.0",
			FormattedPrice: "Free", MinimumOSVersion: "14.0",
		}),
		"mas lookup --json 222": lookupJSON(t, lookupRecord{
			AdamID: 222, Name: "Paid App", Version: "3.0",
			FormattedPrice: "£4.99", Price: 4.99,
			MinimumOSVersion: "27.0",
		}),
	}}
	actions := []model.Action{
		{
			PackageID: "free", Manager: model.ManagerMAS, Kind: model.KindApp,
			Source: "mac-app-store", Package: "111",
		},
		{
			PackageID: "paid", Manager: model.ManagerMAS, Kind: model.KindApp,
			Source: "mac-app-store", Package: "222",
		},
	}
	report, err := Preflight(context.Background(), actions, runner)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if report.Ready || report.NoninteractiveRoot ||
		report.AccountStatus != "unknown" || len(report.Apps) != 2 {
		t.Fatalf("report = %#v", report)
	}
	if !report.Apps[0].Available || !report.Apps[0].Compatible ||
		!reflect.DeepEqual(report.Apps[0].CandidateCommand,
			[]string{"mas", "get", "111"}) {
		t.Fatalf("free app = %#v", report.Apps[0])
	}
	if report.Apps[1].Compatible || !report.Apps[1].RequiresOwnership ||
		len(report.Apps[1].Blockers) != 1 ||
		!reflect.DeepEqual(report.Apps[1].CandidateCommand,
			[]string{"mas", "install", "222"}) {
		t.Fatalf("paid app = %#v", report.Apps[1])
	}
}

func TestPreflightAllowsInteractiveFreeAndOwnedOnlyCandidates(t *testing.T) {
	runner := preflightRunner{responses: map[string][]byte{
		"mas config --json": []byte(
			`{"mas":"7.0.0","store":"GB","region":"GB","macos":"26.5 (25F71)"}`,
		),
		"mas lookup --json 111": lookupJSON(t, lookupRecord{
			AdamID: 111, Name: "Free App", FormattedPrice: "Free",
			MinimumOSVersion: "14.0",
		}),
		"mas lookup --json 222": lookupJSON(t, lookupRecord{
			AdamID: 222, Name: "Paid App", FormattedPrice: "£4.99",
			Price: 4.99, MinimumOSVersion: "14.0",
		}),
	}}
	actions := []model.Action{
		{
			PackageID: "free", Manager: model.ManagerMAS, Kind: model.KindApp,
			Source: "mac-app-store", Package: "111",
		},
		{
			PackageID: "paid", Manager: model.ManagerMAS, Kind: model.KindApp,
			Source: "mac-app-store", Package: "222",
		},
	}

	report, err := Preflight(context.Background(), actions, runner)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || len(report.Blockers) != 0 {
		t.Fatalf("report = %#v", report)
	}
	if len(report.Warnings) < 3 {
		t.Fatalf("warnings = %#v", report.Warnings)
	}
}

func TestVersionComparison(t *testing.T) {
	tests := []struct {
		installed string
		required  string
		want      bool
	}{
		{"26.5", "26.2", true},
		{"14.0", "14", true},
		{"13.6.1", "14.0", false},
		{"27.0", "26.6", true},
		{"invalid", "14", false},
	}
	for _, test := range tests {
		if got := osVersionAtLeast(test.installed, test.required); got != test.want {
			t.Fatalf(
				"osVersionAtLeast(%q, %q) = %v, want %v",
				test.installed, test.required, got, test.want,
			)
		}
	}
}

func lookupJSON(t *testing.T, value lookupRecord) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type preflightRunner struct {
	responses map[string][]byte
}

func (r preflightRunner) Output(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	key := name
	for _, arg := range args {
		key += " " + arg
	}
	if key == "sudo -n true" {
		return nil, errors.New("password required")
	}
	value, ok := r.responses[key]
	if !ok {
		return nil, errors.New("unexpected command")
	}
	return value, nil
}
