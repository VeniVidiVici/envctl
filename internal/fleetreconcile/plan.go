package fleetreconcile

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	envconfig "github.com/VeniVidiVici/envctl/internal/config"
	"github.com/VeniVidiVici/envctl/internal/model"
	"github.com/VeniVidiVici/envctl/internal/store"
)

const (
	StatusPlanned = "planned"
	StatusNoop    = "no-op"
	StatusIgnored = "ignored"
	StatusBlocked = "blocked"
)

var (
	packagePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@+._-]*$`)
	bunPackagePattern = regexp.MustCompile(
		`^(?:@[A-Za-z0-9][A-Za-z0-9._-]*/)?[A-Za-z0-9][A-Za-z0-9._-]*$`,
	)
	miseToolPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]*$`)
	miseVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]*$`)
	masIDPattern       = regexp.MustCompile(`^[1-9][0-9]*$`)
)

type Command struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
}

type Action struct {
	Sequence     int                    `json:"sequence,omitempty"`
	MachineID    string                 `json:"machine_id"`
	InventoryKey string                 `json:"inventory_key"`
	Decision     string                 `json:"decision"`
	Status       string                 `json:"status"`
	Detail       string                 `json:"detail"`
	Profile      string                 `json:"profile,omitempty"`
	Installed    model.InstalledPackage `json:"installed"`
	PackageID    string                 `json:"package_id,omitempty"`
	Spec         *model.PackageSpec     `json:"spec,omitempty"`
	CatalogAdd   bool                   `json:"catalog_add,omitempty"`
	ProfileAdd   bool                   `json:"profile_add,omitempty"`
	Command      *Command               `json:"command,omitempty"`
}

type Plan struct {
	MachineID string   `json:"machine_id"`
	Profile   string   `json:"profile"`
	Actions   []Action `json:"actions"`
	Ready     bool     `json:"ready"`
	Blockers  []string `json:"blockers,omitempty"`
}

func Build(
	machineID, profileName string,
	decisions []store.Decision,
	inventory model.Inventory,
	catalog map[string]model.PackageSpec,
	profile envconfig.Profile,
) Plan {
	plan := Plan{
		MachineID: machineID,
		Profile:   profileName,
		Actions:   make([]Action, 0),
		Ready:     true,
	}
	inventoryByKey := make(map[string]model.InstalledPackage, len(inventory.Packages))
	for _, item := range inventory.Packages {
		inventoryByKey[InventoryKey(item)] = item
	}
	profilePackages := make(map[string]bool, len(profile.Packages))
	for _, id := range profile.Packages {
		profilePackages[id] = true
	}
	reservedIDs := make(map[string]bool, len(catalog))
	for id := range catalog {
		reservedIDs[id] = true
	}

	sort.SliceStable(decisions, func(i, j int) bool {
		return decisions[i].InventoryKey < decisions[j].InventoryKey
	})
	nextSequence := 1
	for _, decision := range decisions {
		if decision.MachineID != machineID {
			continue
		}
		action := Action{
			MachineID: machineID, InventoryKey: decision.InventoryKey,
			Decision: decision.Value, Profile: profileName,
		}
		if decision.Value == "keep" || decision.Value == "ignore" {
			action.Status = StatusIgnored
			action.Detail = "decision is informational; no change will be made"
			action.Installed = inventoryByKey[decision.InventoryKey]
			plan.Actions = append(plan.Actions, action)
			continue
		}
		item, ok := inventoryByKey[decision.InventoryKey]
		if !ok {
			action.Status = StatusBlocked
			action.Detail = "reviewed package is absent from the saved inventory; refresh the fleet snapshot"
			plan.Blockers = append(plan.Blockers,
				decision.InventoryKey+": "+action.Detail)
			plan.Ready = false
			plan.Actions = append(plan.Actions, action)
			continue
		}
		action.Installed = item
		switch decision.Value {
		case "adopt":
			spec, err := inferredSpec(item)
			if err != nil {
				action.Status = StatusBlocked
				action.Detail = err.Error()
				plan.Blockers = append(plan.Blockers,
					decision.InventoryKey+": "+action.Detail)
				plan.Ready = false
				break
			}
			id := matchingCatalogID(catalog, spec)
			if id == "" {
				id = availableID(suggestedID(item), item.Manager, reservedIDs)
				reservedIDs[id] = true
				action.CatalogAdd = true
			}
			action.PackageID = id
			spec.ID = id
			action.Spec = &spec
			action.ProfileAdd = !profilePackages[id]
			if action.ProfileAdd {
				profilePackages[id] = true
			}
			if !action.CatalogAdd && !action.ProfileAdd {
				action.Status = StatusNoop
				action.Detail = "package is already present in the catalog and target profile"
			} else {
				action.Sequence = nextSequence
				nextSequence++
				action.Status = StatusPlanned
				action.Detail = fmt.Sprintf(
					"add %s to the catalog and %s profile for fleet convergence",
					id, profileName,
				)
			}
		case "remove":
			command, err := removalCommand(item)
			if err != nil {
				action.Status = StatusBlocked
				action.Detail = err.Error()
				plan.Blockers = append(plan.Blockers,
					decision.InventoryKey+": "+action.Detail)
				plan.Ready = false
				break
			}
			action.Sequence = nextSequence
			nextSequence++
			action.Status = StatusPlanned
			action.Command = &command
			action.Detail = "uninstall from this Mac and verify it is absent"
		default:
			action.Status = StatusBlocked
			action.Detail = fmt.Sprintf("unsupported review decision %q", decision.Value)
			plan.Blockers = append(plan.Blockers,
				decision.InventoryKey+": "+action.Detail)
			plan.Ready = false
		}
		plan.Actions = append(plan.Actions, action)
	}
	return plan
}

func InventoryKey(item model.InstalledPackage) string {
	return strings.Join([]string{
		string(item.Manager), string(item.Kind), item.Source, item.Package,
	}, "|")
}

func ProfileByName(profiles []envconfig.Profile, name string) (envconfig.Profile, error) {
	for _, profile := range profiles {
		if profile.Name == name {
			return profile, nil
		}
	}
	return envconfig.Profile{}, fmt.Errorf("unknown profile %q", name)
}

func inferredSpec(item model.InstalledPackage) (model.PackageSpec, error) {
	spec := model.PackageSpec{
		Manager: item.Manager,
		Kind:    item.Kind,
		Source:  item.Source,
		Package: item.Package,
	}
	switch item.Manager {
	case model.ManagerBrew:
		if item.Kind != model.KindFormula && item.Kind != model.KindCask {
			return model.PackageSpec{}, fmt.Errorf(
				"cannot adopt Homebrew package with kind %q", item.Kind,
			)
		}
		if item.Source == "" {
			return model.PackageSpec{}, fmt.Errorf(
				"cannot adopt Homebrew package without its tap source",
			)
		}
		if item.Kind == model.KindCask {
			spec.UpdatePolicy = model.UpdateInstallOnly
		} else {
			spec.UpdatePolicy = model.UpdateManaged
		}
	case model.ManagerMAS:
		if item.Kind != model.KindApp || item.Source != "mac-app-store" ||
			!masIDPattern.MatchString(item.Package) {
			return model.PackageSpec{}, fmt.Errorf(
				"cannot adopt invalid Mac App Store identity %q", item.Package,
			)
		}
		spec.UpdatePolicy = model.UpdateManaged
	case model.ManagerBun:
		if item.Kind != model.KindTool || item.Source != "" ||
			!bunPackagePattern.MatchString(item.Package) {
			return model.PackageSpec{}, fmt.Errorf(
				"cannot adopt invalid Bun package identity %q", item.Package,
			)
		}
		spec.UpdatePolicy = model.UpdateManaged
	case model.ManagerMise:
		if item.Kind != model.KindTool || item.Source != "" ||
			!miseToolPattern.MatchString(item.Package) ||
			!miseVersionPattern.MatchString(item.Version) {
			return model.PackageSpec{}, fmt.Errorf(
				"cannot adopt invalid Mise tool identity %q@%q",
				item.Package, item.Version,
			)
		}
		spec.Version = item.Version
		spec.UpdatePolicy = model.UpdateManaged
	default:
		return model.PackageSpec{}, fmt.Errorf(
			"%s packages need a hand-written catalog entry before they can be adopted",
			item.Manager,
		)
	}
	return spec, nil
}

func removalCommand(item model.InstalledPackage) (Command, error) {
	switch item.Manager {
	case model.ManagerBrew:
		if !packagePattern.MatchString(item.Package) {
			return Command{}, fmt.Errorf("unsafe Homebrew package identity %q", item.Package)
		}
		var kindFlag string
		switch item.Kind {
		case model.KindFormula:
			kindFlag = "--formula"
		case model.KindCask:
			kindFlag = "--cask"
		default:
			return Command{}, fmt.Errorf(
				"cannot remove unsupported Homebrew kind %q", item.Kind,
			)
		}
		return Command{
			Name: "brew", Args: []string{"uninstall", kindFlag, item.Package},
		}, nil
	case model.ManagerMAS:
		if item.Kind != model.KindApp || item.Source != "mac-app-store" ||
			!masIDPattern.MatchString(item.Package) {
			return Command{}, fmt.Errorf(
				"unsafe Mac App Store identity %q", item.Package,
			)
		}
		return Command{
			Name: "sudo", Args: []string{"mas", "uninstall", item.Package},
		}, nil
	case model.ManagerBun:
		if item.Kind != model.KindTool || item.Source != "" ||
			!bunPackagePattern.MatchString(item.Package) {
			return Command{}, fmt.Errorf("unsafe Bun package identity %q", item.Package)
		}
		return Command{
			Name: "bun", Args: []string{"remove", "--global", item.Package},
		}, nil
	case model.ManagerMise:
		if item.Kind != model.KindTool || item.Source != "" ||
			!miseToolPattern.MatchString(item.Package) ||
			!miseVersionPattern.MatchString(item.Version) {
			return Command{}, fmt.Errorf(
				"unsafe Mise tool identity %q@%q", item.Package, item.Version,
			)
		}
		return Command{
			Name: "mise",
			Args: []string{
				"uninstall", "--yes", item.Package + "@" + item.Version,
			},
		}, nil
	default:
		return Command{}, fmt.Errorf(
			"%s removal is not supported automatically", item.Manager,
		)
	}
}

func matchingCatalogID(
	catalog map[string]model.PackageSpec,
	spec model.PackageSpec,
) string {
	var matches []string
	for id, candidate := range catalog {
		if candidate.Manager != spec.Manager ||
			candidate.Kind != spec.Kind ||
			candidate.Source != spec.Source ||
			candidate.Package != spec.Package {
			continue
		}
		if spec.Manager == model.ManagerMise &&
			candidate.Version != spec.Version {
			continue
		}
		matches = append(matches, id)
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func suggestedID(item model.InstalledPackage) string {
	value := item.Package
	if item.Manager == model.ManagerMAS && item.Application != "" {
		value = strings.TrimSuffix(filepath.Base(item.Application), filepath.Ext(item.Application))
	}
	value = strings.TrimPrefix(value, "@")
	value = strings.ReplaceAll(value, "/", "-")
	var result strings.Builder
	lastDash := false
	for _, character := range strings.ToLower(value) {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' {
			result.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash {
			result.WriteByte('-')
			lastDash = true
		}
	}
	id := strings.Trim(result.String(), "-.")
	if id == "" {
		id = string(item.Manager) + "-" + item.Package
	}
	return id
}

func availableID(base string, manager model.Manager, reserved map[string]bool) string {
	if !reserved[base] {
		return base
	}
	candidate := string(manager) + "-" + base
	if !reserved[candidate] {
		return candidate
	}
	for suffix := 2; ; suffix++ {
		candidate = string(manager) + "-" + base + "-" + strconv.Itoa(suffix)
		if !reserved[candidate] {
			return candidate
		}
	}
}
