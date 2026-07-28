package legacy

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type brewEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (e *brewEntry) UnmarshalJSON(data []byte) error {
	var name string
	if json.Unmarshal(data, &name) == nil {
		e.Name = name
		return nil
	}
	type entry brewEntry
	var decoded entry
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*e = brewEntry(decoded)
	return nil
}

type sourceConfig struct {
	Homebrew struct {
		InstallOnce  []brewEntry `json:"install_once"`
		AlwaysUpdate []brewEntry `json:"always_update"`
	} `json:"homebrew"`
	MacAppStore struct {
		Apps []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"apps"`
	} `json:"mac_app_store"`
	Bun struct {
		GlobalPackages []string `json:"global_packages"`
	} `json:"bun"`
	CustomInstallers struct {
		Apps []CustomInstaller `json:"apps"`
	} `json:"custom_installers"`
}

type CustomInstaller struct {
	Name           string `json:"name"`
	CheckCommand   string `json:"check_command"`
	InstallCommand string `json:"install_command"`
	ReviewRequired bool   `json:"review_required"`
}

type Draft struct {
	Packages         []model.PackageSpec `json:"packages"`
	CustomInstallers []CustomInstaller   `json:"custom_installers,omitempty"`
	Warnings         []string            `json:"warnings,omitempty"`
}

func LoadFile(path string) (Draft, error) {
	file, err := os.Open(path)
	if err != nil {
		return Draft{}, err
	}
	defer file.Close()
	return Load(file)
}

func Load(reader io.Reader) (Draft, error) {
	var source sourceConfig
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return Draft{}, fmt.Errorf("decode legacy configuration: %w", err)
	}

	var draft Draft
	for _, entry := range source.Homebrew.InstallOnce {
		draft.Packages = append(draft.Packages, brewPackage(entry, model.UpdateInstallOnly))
	}
	for _, entry := range source.Homebrew.AlwaysUpdate {
		draft.Packages = append(draft.Packages, brewPackage(entry, model.UpdateManaged))
	}
	for _, app := range source.MacAppStore.Apps {
		draft.Packages = append(draft.Packages, model.PackageSpec{
			ID:           slug(app.Name),
			Manager:      model.ManagerMAS,
			Kind:         model.KindApp,
			Source:       "mac-app-store",
			Package:      app.ID,
			UpdatePolicy: model.UpdateManaged,
		})
	}
	for _, item := range source.Bun.GlobalPackages {
		draft.Packages = append(draft.Packages, model.PackageSpec{
			ID:           slug(item),
			Manager:      model.ManagerBun,
			Kind:         model.KindTool,
			Package:      item,
			UpdatePolicy: model.UpdateManaged,
		})
	}
	for _, item := range source.CustomInstallers.Apps {
		item.ReviewRequired = true
		draft.CustomInstallers = append(draft.CustomInstallers, item)
	}
	if len(draft.CustomInstallers) > 0 {
		draft.Warnings = append(draft.Warnings,
			"legacy custom installer commands require manual review before they can be executed")
	}
	return draft, nil
}

func brewPackage(entry brewEntry, policy model.UpdatePolicy) model.PackageSpec {
	source, name := splitBrewName(entry.Name)
	kind := model.KindUnknown
	switch entry.Type {
	case "formula":
		kind = model.KindFormula
	case "cask":
		kind = model.KindCask
	}
	if source == "homebrew/cask" {
		kind = model.KindCask
	}

	return model.PackageSpec{
		ID:           slug(name),
		Manager:      model.ManagerBrew,
		Kind:         kind,
		Source:       source,
		Package:      name,
		UpdatePolicy: policy,
	}
}

func splitBrewName(value string) (string, string) {
	parts := strings.Split(value, "/")
	if len(parts) >= 3 {
		return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1]
	}
	return "", value
}

func slug(value string) string {
	value = strings.ToLower(filepath.Base(value))
	replacer := strings.NewReplacer(" ", "-", "_", "-", ".", "-", "@", "-")
	return strings.Trim(replacer.Replace(value), "-")
}
