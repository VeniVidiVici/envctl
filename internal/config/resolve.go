package config

import (
	"fmt"
	"sort"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type Catalog struct {
	Packages   map[string]model.PackageSpec  `json:"packages" yaml:"packages"`
	Links      map[string]model.LinkSpec     `json:"links,omitempty" yaml:"links,omitempty"`
	Recoveries map[string]model.RecoverySpec `json:"recoveries,omitempty" yaml:"recoveries,omitempty"`
}

type Profile struct {
	Name       string   `json:"name" yaml:"name"`
	Extends    []string `json:"extends,omitempty" yaml:"extends,omitempty"`
	Packages   []string `json:"packages,omitempty" yaml:"packages,omitempty"`
	Links      []string `json:"links,omitempty" yaml:"links,omitempty"`
	Recoveries []string `json:"recoveries,omitempty" yaml:"recoveries,omitempty"`
}

type Machine struct {
	ID               string   `json:"id" yaml:"id"`
	Match            Match    `json:"match,omitempty" yaml:"match,omitempty"`
	Profiles         []string `json:"profiles" yaml:"profiles"`
	Add              []string `json:"add,omitempty" yaml:"add,omitempty"`
	Remove           []string `json:"remove,omitempty" yaml:"remove,omitempty"`
	AddLinks         []string `json:"add_links,omitempty" yaml:"add_links,omitempty"`
	RemoveLinks      []string `json:"remove_links,omitempty" yaml:"remove_links,omitempty"`
	AddRecoveries    []string `json:"add_recoveries,omitempty" yaml:"add_recoveries,omitempty"`
	RemoveRecoveries []string `json:"remove_recoveries,omitempty" yaml:"remove_recoveries,omitempty"`
	Access           Access   `json:"access" yaml:"access"`
}

type Match struct {
	HardwareUUIDSHA256 string `json:"hardware_uuid_sha256,omitempty" yaml:"hardware_uuid_sha256,omitempty"`
}

type Access struct {
	Type string `json:"type" yaml:"type"`
	Host string `json:"host,omitempty" yaml:"host,omitempty"`
}

type DesiredState struct {
	MachineID  string               `json:"machine_id"`
	Profiles   []string             `json:"profiles"`
	Packages   []model.PackageSpec  `json:"packages"`
	Links      []model.LinkSpec     `json:"links,omitempty"`
	Recoveries []model.RecoverySpec `json:"recoveries,omitempty"`
}

func Resolve(catalog Catalog, profiles map[string]Profile, machine Machine) (DesiredState, error) {
	selected := make(map[string]bool)
	selectedLinks := make(map[string]bool)
	selectedRecoveries := make(map[string]bool)
	var profileOrder []string
	visiting := make(map[string]bool)
	visited := make(map[string]bool)

	var addProfile func(string) error
	addProfile = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("profile inheritance cycle at %q", name)
		}
		if visited[name] {
			return nil
		}
		profile, ok := profiles[name]
		if !ok {
			return fmt.Errorf("unknown profile %q", name)
		}
		visiting[name] = true
		for _, parent := range profile.Extends {
			if err := addProfile(parent); err != nil {
				return err
			}
		}
		delete(visiting, name)
		visited[name] = true
		profileOrder = append(profileOrder, name)
		for _, packageID := range profile.Packages {
			if _, ok := catalog.Packages[packageID]; !ok {
				return fmt.Errorf("profile %q references unknown package %q", name, packageID)
			}
			selected[packageID] = true
		}
		for _, linkID := range profile.Links {
			if _, ok := catalog.Links[linkID]; !ok {
				return fmt.Errorf("profile %q references unknown link %q", name, linkID)
			}
			selectedLinks[linkID] = true
		}
		for _, recoveryID := range profile.Recoveries {
			if _, ok := catalog.Recoveries[recoveryID]; !ok {
				return fmt.Errorf(
					"profile %q references unknown recovery %q",
					name,
					recoveryID,
				)
			}
			selectedRecoveries[recoveryID] = true
		}
		return nil
	}

	for _, name := range machine.Profiles {
		if err := addProfile(name); err != nil {
			return DesiredState{}, err
		}
	}
	for _, packageID := range machine.Add {
		if _, ok := catalog.Packages[packageID]; !ok {
			return DesiredState{}, fmt.Errorf("machine %q adds unknown package %q", machine.ID, packageID)
		}
		selected[packageID] = true
	}
	for _, packageID := range machine.Remove {
		if _, ok := catalog.Packages[packageID]; !ok {
			return DesiredState{}, fmt.Errorf("machine %q removes unknown package %q", machine.ID, packageID)
		}
		delete(selected, packageID)
	}
	for _, linkID := range machine.AddLinks {
		if _, ok := catalog.Links[linkID]; !ok {
			return DesiredState{}, fmt.Errorf(
				"machine %q adds unknown link %q", machine.ID, linkID,
			)
		}
		selectedLinks[linkID] = true
	}
	for _, linkID := range machine.RemoveLinks {
		if _, ok := catalog.Links[linkID]; !ok {
			return DesiredState{}, fmt.Errorf(
				"machine %q removes unknown link %q", machine.ID, linkID,
			)
		}
		delete(selectedLinks, linkID)
	}
	for _, recoveryID := range machine.AddRecoveries {
		if _, ok := catalog.Recoveries[recoveryID]; !ok {
			return DesiredState{}, fmt.Errorf(
				"machine %q adds unknown recovery %q", machine.ID, recoveryID,
			)
		}
		selectedRecoveries[recoveryID] = true
	}
	for _, recoveryID := range machine.RemoveRecoveries {
		if _, ok := catalog.Recoveries[recoveryID]; !ok {
			return DesiredState{}, fmt.Errorf(
				"machine %q removes unknown recovery %q", machine.ID, recoveryID,
			)
		}
		delete(selectedRecoveries, recoveryID)
	}

	packageIDs := make([]string, 0, len(selected))
	for packageID := range selected {
		packageIDs = append(packageIDs, packageID)
	}
	sort.Strings(packageIDs)

	state := DesiredState{
		MachineID: machine.ID,
		Profiles:  profileOrder,
		Packages:  make([]model.PackageSpec, 0, len(packageIDs)),
	}
	for _, packageID := range packageIDs {
		spec := catalog.Packages[packageID]
		if spec.ID == "" {
			spec.ID = packageID
		}
		state.Packages = append(state.Packages, spec)
	}
	linkIDs := make([]string, 0, len(selectedLinks))
	for linkID := range selectedLinks {
		linkIDs = append(linkIDs, linkID)
	}
	sort.Strings(linkIDs)
	state.Links = make([]model.LinkSpec, 0, len(linkIDs))
	for _, linkID := range linkIDs {
		spec := catalog.Links[linkID]
		if spec.ID == "" {
			spec.ID = linkID
		}
		state.Links = append(state.Links, spec)
	}
	recoveryIDs := make([]string, 0, len(selectedRecoveries))
	for recoveryID := range selectedRecoveries {
		recoveryIDs = append(recoveryIDs, recoveryID)
	}
	sort.Strings(recoveryIDs)
	state.Recoveries = make([]model.RecoverySpec, 0, len(recoveryIDs))
	for _, recoveryID := range recoveryIDs {
		spec := catalog.Recoveries[recoveryID]
		if spec.ID == "" {
			spec.ID = recoveryID
		}
		state.Recoveries = append(state.Recoveries, spec)
	}
	return state, nil
}
