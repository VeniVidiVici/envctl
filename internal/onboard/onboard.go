package onboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"unicode"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
	"github.com/VeniVidiVici/envctl/internal/model"
)

type Identity struct {
	Hostname           string `json:"hostname"`
	LocalHostname      string `json:"local_hostname,omitempty"`
	Model              string `json:"model,omitempty"`
	Chip               string `json:"chip,omitempty"`
	Architecture       string `json:"architecture,omitempty"`
	OSVersion          string `json:"os_version,omitempty"`
	HardwareUUIDSHA256 string `json:"hardware_uuid_sha256"`
}

type Status string

const (
	StatusMatched           Status = "matched"
	StatusNeedsConfirmation Status = "needs-confirmation"
	StatusUnmatched         Status = "unmatched"
)

type Result struct {
	Status             Status              `json:"status"`
	Identity           Identity            `json:"identity"`
	MachineID          string              `json:"machine_id,omitempty"`
	SuggestedMachineID string              `json:"suggested_machine_id"`
	ConfiguredMachines []string            `json:"configured_machines"`
	AvailableProfiles  []string            `json:"available_profiles"`
	Proposal           *envconfig.Machine  `json:"proposal,omitempty"`
	ProposalPath       string              `json:"proposal_path,omitempty"`
	Plan               *model.Plan         `json:"plan,omitempty"`
	RecoveryPlan       *model.RecoveryPlan `json:"recovery_plan,omitempty"`
}

type hardwareProfile struct {
	Items []struct {
		Model        string `json:"machine_model"`
		Chip         string `json:"chip_type"`
		PlatformUUID string `json:"platform_UUID"`
	} `json:"SPHardwareDataType"`
}

func Detect(ctx context.Context) (Identity, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return Identity{}, fmt.Errorf("read hostname: %w", err)
	}
	hardwareOutput, err := exec.CommandContext(
		ctx, "system_profiler", "SPHardwareDataType", "-json",
	).Output()
	if err != nil {
		return Identity{}, fmt.Errorf("inspect Mac hardware identity: %w", err)
	}
	identity, err := ParseHardwareProfile(hardwareOutput)
	if err != nil {
		return Identity{}, err
	}
	identity.Hostname = hostname
	identity.LocalHostname = optionalCommand(
		ctx, "scutil", "--get", "LocalHostName",
	)
	identity.Architecture = optionalCommand(ctx, "uname", "-m")
	identity.OSVersion = optionalCommand(ctx, "sw_vers", "-productVersion")
	return identity, nil
}

func ParseHardwareProfile(raw []byte) (Identity, error) {
	var profile hardwareProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return Identity{}, fmt.Errorf("decode hardware profile: %w", err)
	}
	if len(profile.Items) != 1 {
		return Identity{}, fmt.Errorf(
			"hardware profile contains %d devices; expected one",
			len(profile.Items),
		)
	}
	item := profile.Items[0]
	uuid := strings.ToLower(strings.TrimSpace(item.PlatformUUID))
	if uuid == "" {
		return Identity{}, errors.New("hardware profile has no platform UUID")
	}
	fingerprint := sha256.Sum256([]byte(uuid))
	return Identity{
		Model:              strings.TrimSpace(item.Model),
		Chip:               strings.TrimSpace(item.Chip),
		HardwareUUIDSHA256: hex.EncodeToString(fingerprint[:]),
	}, nil
}

func Resolve(
	identity Identity,
	machines []envconfig.Machine,
	profiles []string,
	requestedMachineID string,
	requestedProfiles []string,
) (Result, error) {
	if identity.HardwareUUIDSHA256 == "" {
		return Result{}, errors.New("hardware UUID fingerprint is required")
	}
	configuredIDs := make([]string, 0, len(machines))
	byID := make(map[string]envconfig.Machine)
	var fingerprintMatches []envconfig.Machine
	for _, machine := range machines {
		configuredIDs = append(configuredIDs, machine.ID)
		byID[machine.ID] = machine
		if machine.Match.HardwareUUIDSHA256 == identity.HardwareUUIDSHA256 {
			fingerprintMatches = append(fingerprintMatches, machine)
		}
	}
	sort.Strings(configuredIDs)
	sort.Strings(profiles)
	if len(fingerprintMatches) > 1 {
		return Result{}, fmt.Errorf(
			"hardware UUID fingerprint matches multiple machines: %s",
			joinMachineIDs(fingerprintMatches),
		)
	}
	suggestedID := requestedMachineID
	if suggestedID == "" {
		suggestedID = SuggestMachineID(identity)
	}
	if !SafeIdentifier(suggestedID) {
		return Result{}, fmt.Errorf("unsafe requested machine id %q", suggestedID)
	}
	result := Result{
		Identity: identity, SuggestedMachineID: suggestedID,
		ConfiguredMachines: configuredIDs,
		AvailableProfiles:  append([]string(nil), profiles...),
	}
	if len(fingerprintMatches) == 1 {
		result.Status = StatusMatched
		result.MachineID = fingerprintMatches[0].ID
		return result, nil
	}

	selectedProfiles, err := selectProfiles(profiles, requestedProfiles)
	if err != nil {
		return Result{}, err
	}
	proposal := envconfig.Machine{
		ID: suggestedID,
		Match: envconfig.Match{
			HardwareUUIDSHA256: identity.HardwareUUIDSHA256,
		},
		Profiles: selectedProfiles,
		Access: envconfig.Access{
			Type: "ssh",
			Host: suggestedID,
		},
	}
	result.Proposal = &proposal
	result.ProposalPath = "machines/" + suggestedID + ".yaml"
	if existing, ok := byID[suggestedID]; ok {
		result.Status = StatusNeedsConfirmation
		result.MachineID = existing.ID
		proposal.Profiles = append([]string(nil), existing.Profiles...)
		proposal.Add = append([]string(nil), existing.Add...)
		proposal.Remove = append([]string(nil), existing.Remove...)
		proposal.AddLinks = append([]string(nil), existing.AddLinks...)
		proposal.RemoveLinks = append([]string(nil), existing.RemoveLinks...)
		proposal.Access = existing.Access
		result.Proposal = &proposal
		return result, nil
	}
	result.Status = StatusUnmatched
	return result, nil
}

func AsLocal(
	machine envconfig.Machine,
	identity Identity,
) (envconfig.Machine, error) {
	expected := machine.Match.HardwareUUIDSHA256
	if expected == "" {
		return envconfig.Machine{}, fmt.Errorf(
			"machine %q has no registered hardware identity; run onboard first",
			machine.ID,
		)
	}
	if identity.HardwareUUIDSHA256 == "" {
		return envconfig.Machine{}, errors.New(
			"local hardware UUID fingerprint is unavailable",
		)
	}
	if identity.HardwareUUIDSHA256 != expected {
		return envconfig.Machine{}, fmt.Errorf(
			"this Mac does not match machine %q; refuse local override",
			machine.ID,
		)
	}
	machine.Access = envconfig.Access{Type: "local"}
	return machine, nil
}

func SuggestMachineID(identity Identity) string {
	value := identity.LocalHostname
	if value == "" {
		value = strings.Split(identity.Hostname, ".")[0]
	}
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	previousHyphen := false
	for _, character := range value {
		switch {
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			result.WriteRune(character)
			previousHyphen = false
		case !previousHyphen && result.Len() > 0:
			result.WriteByte('-')
			previousHyphen = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func SafeIdentifier(value string) bool {
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

func optionalCommand(ctx context.Context, name string, args ...string) string {
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func selectProfiles(available, requested []string) ([]string, error) {
	if len(requested) == 0 {
		if len(available) == 1 {
			return append([]string(nil), available[0]), nil
		}
		return nil, nil
	}
	known := make(map[string]bool)
	for _, profile := range available {
		known[profile] = true
	}
	selected := make([]string, 0, len(requested))
	seen := make(map[string]bool)
	for _, profile := range requested {
		if !known[profile] {
			return nil, fmt.Errorf("unknown requested profile %q", profile)
		}
		if !seen[profile] {
			selected = append(selected, profile)
			seen[profile] = true
		}
	}
	return selected, nil
}

func joinMachineIDs(machines []envconfig.Machine) string {
	ids := make([]string, 0, len(machines))
	for _, machine := range machines {
		ids = append(ids, machine.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}
