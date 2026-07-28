package onboard

import (
	"strings"
	"testing"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
)

func TestParseHardwareProfileHashesUUIDWithoutReturningIt(t *testing.T) {
	raw := []byte(`{
	  "SPHardwareDataType": [{
	    "machine_model": "Mac99,1",
	    "chip_type": "Example Chip",
	    "platform_UUID": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE",
	    "serial_number": "NEVER-RETURN-THIS"
	  }]
	}`)
	got, err := ParseHardwareProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "Mac99,1" || got.Chip != "Example Chip" {
		t.Fatalf("identity = %#v", got)
	}
	if len(got.HardwareUUIDSHA256) != 64 {
		t.Fatalf("fingerprint = %q", got.HardwareUUIDSHA256)
	}
	if strings.Contains(strings.ToLower(got.HardwareUUIDSHA256), "aaaaaaaa") {
		t.Fatalf("fingerprint appears to contain the raw UUID: %q", got.HardwareUUIDSHA256)
	}
}

func TestResolveMatchesFingerprint(t *testing.T) {
	identity := Identity{
		LocalHostname:      "new-mac",
		HardwareUUIDSHA256: strings.Repeat("a", 64),
	}
	machines := []envconfig.Machine{
		{
			ID: "existing",
			Match: envconfig.Match{
				HardwareUUIDSHA256: strings.Repeat("a", 64),
			},
		},
	}
	got, err := Resolve(identity, machines, []string{"shared"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusMatched || got.MachineID != "existing" || got.Proposal != nil {
		t.Fatalf("result = %#v", got)
	}
}

func TestResolveProposesNewMachineWithOnlyAvailableProfile(t *testing.T) {
	identity := Identity{
		Hostname:           "MacBook Air.local",
		LocalHostname:      "Example MacBook Air",
		HardwareUUIDSHA256: strings.Repeat("b", 64),
	}
	got, err := Resolve(identity, nil, []string{"shared"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusUnmatched ||
		got.SuggestedMachineID != "example-macbook-air" ||
		got.ProposalPath != "machines/example-macbook-air.yaml" {
		t.Fatalf("result = %#v", got)
	}
	if got.Proposal == nil ||
		strings.Join(got.Proposal.Profiles, ",") != "shared" ||
		got.Proposal.Access.Type != "ssh" {
		t.Fatalf("proposal = %#v", got.Proposal)
	}
}

func TestResolveExistingIDRequiresConfirmationAndPreservesOverlay(t *testing.T) {
	identity := Identity{
		LocalHostname:      "ai",
		HardwareUUIDSHA256: strings.Repeat("c", 64),
	}
	machines := []envconfig.Machine{{
		ID: "ai", Profiles: []string{"shared"}, Add: []string{"local-only"},
		Access: envconfig.Access{Type: "local"},
	}}
	got, err := Resolve(identity, machines, []string{"shared"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusNeedsConfirmation || got.MachineID != "ai" {
		t.Fatalf("result = %#v", got)
	}
	if got.Proposal == nil ||
		strings.Join(got.Proposal.Add, ",") != "local-only" ||
		got.Proposal.Access.Type != "local" {
		t.Fatalf("proposal = %#v", got.Proposal)
	}
}

func TestResolveRejectsUnknownRequestedProfile(t *testing.T) {
	identity := Identity{
		LocalHostname:      "new-mac",
		HardwareUUIDSHA256: strings.Repeat("d", 64),
	}
	_, err := Resolve(
		identity, nil, []string{"shared"}, "", []string{"work"},
	)
	if err == nil || !strings.Contains(err.Error(), `unknown requested profile "work"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestAsLocalRequiresMatchingRegisteredFingerprint(t *testing.T) {
	machine := envconfig.Machine{
		ID: "example",
		Match: envconfig.Match{
			HardwareUUIDSHA256: strings.Repeat("a", 64),
		},
		Access: envconfig.Access{Type: "ssh", Host: "example"},
	}
	got, err := AsLocal(machine, Identity{
		HardwareUUIDSHA256: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Access.Type != "local" || got.Access.Host != "" {
		t.Fatalf("access = %#v", got.Access)
	}
	_, err = AsLocal(machine, Identity{
		HardwareUUIDSHA256: strings.Repeat("b", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
}
