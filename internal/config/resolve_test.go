package config

import (
	"strings"
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestResolveComposesProfilesAndMachineOverlay(t *testing.T) {
	catalog := Catalog{Packages: map[string]model.PackageSpec{
		"base-tool":    {Manager: model.ManagerBrew, Package: "base-tool"},
		"dev-tool":     {Manager: model.ManagerBrew, Package: "dev-tool"},
		"laptop-tool":  {Manager: model.ManagerBrew, Package: "laptop-tool"},
		"removed-tool": {Manager: model.ManagerBrew, Package: "removed-tool"},
	}, Links: map[string]model.LinkSpec{
		"base-link":    {Source: "base", Target: "~/base"},
		"removed-link": {Source: "removed", Target: "~/removed"},
		"machine-link": {Source: "machine", Target: "~/machine"},
	}}
	profiles := map[string]Profile{
		"base": {
			Name:     "base",
			Packages: []string{"base-tool", "removed-tool"},
			Links:    []string{"base-link", "removed-link"},
		},
		"development": {
			Name:     "development",
			Extends:  []string{"base"},
			Packages: []string{"dev-tool"},
		},
	}
	machine := Machine{
		ID:          "example-laptop",
		Profiles:    []string{"development"},
		Add:         []string{"laptop-tool"},
		Remove:      []string{"removed-tool"},
		AddLinks:    []string{"machine-link"},
		RemoveLinks: []string{"removed-link"},
	}

	got, err := Resolve(catalog, profiles, machine)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if strings.Join(got.Profiles, ",") != "base,development" {
		t.Fatalf("resolved profiles = %v", got.Profiles)
	}
	var packageIDs []string
	for _, item := range got.Packages {
		packageIDs = append(packageIDs, item.ID)
	}
	if strings.Join(packageIDs, ",") != "base-tool,dev-tool,laptop-tool" {
		t.Fatalf("resolved packages = %v", packageIDs)
	}
	var linkIDs []string
	for _, item := range got.Links {
		linkIDs = append(linkIDs, item.ID)
	}
	if strings.Join(linkIDs, ",") != "base-link,machine-link" {
		t.Fatalf("resolved links = %v", linkIDs)
	}
}

func TestResolveRejectsProfileCycle(t *testing.T) {
	profiles := map[string]Profile{
		"a": {Name: "a", Extends: []string{"b"}},
		"b": {Name: "b", Extends: []string{"a"}},
	}
	_, err := Resolve(Catalog{}, profiles, Machine{Profiles: []string{"a"}})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Resolve() error = %v, want cycle error", err)
	}
}

func TestResolveRejectsUnknownPackage(t *testing.T) {
	profiles := map[string]Profile{
		"base": {Name: "base", Packages: []string{"missing"}},
	}
	_, err := Resolve(Catalog{}, profiles, Machine{Profiles: []string{"base"}})
	if err == nil || !strings.Contains(err.Error(), "unknown package") {
		t.Fatalf("Resolve() error = %v, want unknown package error", err)
	}
}
