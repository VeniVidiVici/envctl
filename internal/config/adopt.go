package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/VeniVidiVici/envctl/internal/model"
	"go.yaml.in/yaml/v3"
)

type PackageAdoption struct {
	ID   string            `json:"id"`
	Spec model.PackageSpec `json:"spec"`
}

type AdoptionResult struct {
	CatalogPath string   `json:"catalog_path"`
	ProfilePath string   `json:"profile_path"`
	Added       []string `json:"added"`
}

// AdoptPackages adds validated package specifications to the catalog and the
// named profile. Both files are restored if the resulting configuration does
// not validate for every registered machine.
func AdoptPackages(
	root, profileName string,
	adoptions []PackageAdoption,
) (AdoptionResult, error) {
	if root == "" || profileName == "" {
		return AdoptionResult{}, errors.New("config root and profile are required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return AdoptionResult{}, fmt.Errorf("resolve config root: %w", err)
	}
	catalogPath, profilePath, err := adoptionPaths(absoluteRoot, profileName)
	if err != nil {
		return AdoptionResult{}, err
	}
	result := AdoptionResult{
		CatalogPath: catalogPath,
		ProfilePath: profilePath,
	}
	if len(adoptions) == 0 {
		return result, nil
	}

	catalogRaw, catalogMode, catalogNode, catalog, err := loadCatalogNode(catalogPath)
	if err != nil {
		return AdoptionResult{}, err
	}
	profileRaw, profileMode, profileNode, profile, err := loadProfileNode(profilePath)
	if err != nil {
		return AdoptionResult{}, err
	}
	if profile.Name != profileName {
		return AdoptionResult{}, fmt.Errorf(
			"profile file %s declares %q, expected %q",
			profilePath, profile.Name, profileName,
		)
	}

	catalogPackages, err := mappingValue(documentMapping(catalogNode), "packages")
	if err != nil {
		return AdoptionResult{}, fmt.Errorf("catalog packages: %w", err)
	}
	if catalogPackages.Kind != yaml.MappingNode {
		return AdoptionResult{}, errors.New("catalog packages must be a mapping")
	}
	profileMapping := documentMapping(profileNode)
	profilePackages, err := mappingValue(profileMapping, "packages")
	if err != nil {
		profilePackages = &yaml.Node{
			Kind: yaml.SequenceNode,
			Tag:  "!!seq",
		}
		profileMapping.Content = append(profileMapping.Content,
			scalarNode("packages"), profilePackages)
	}
	if profilePackages.Kind != yaml.SequenceNode {
		return AdoptionResult{}, errors.New("profile packages must be a sequence")
	}

	profileSet := make(map[string]bool, len(profile.Packages))
	for _, id := range profile.Packages {
		profileSet[id] = true
	}
	catalogChanged := false
	profileChanged := false
	for _, adoption := range adoptions {
		itemChanged := false
		if adoption.ID == "" {
			return AdoptionResult{}, errors.New("adoption package id is empty")
		}
		spec := adoption.Spec
		spec.ID = ""
		if existing, ok := catalog.Packages[adoption.ID]; ok {
			existing.ID = ""
			if existing != spec {
				return AdoptionResult{}, fmt.Errorf(
					"catalog package %q already exists with a different specification",
					adoption.ID,
				)
			}
		} else {
			var specNode yaml.Node
			if err := specNode.Encode(spec); err != nil {
				return AdoptionResult{}, fmt.Errorf(
					"encode catalog package %q: %w", adoption.ID, err,
				)
			}
			specNode.Style = yaml.FlowStyle
			catalogPackages.Content = append(
				catalogPackages.Content,
				scalarNode(adoption.ID),
				&specNode,
			)
			catalog.Packages[adoption.ID] = spec
			catalogChanged = true
			itemChanged = true
		}
		if !profileSet[adoption.ID] {
			profilePackages.Content = append(
				profilePackages.Content,
				scalarNode(adoption.ID),
			)
			profileSet[adoption.ID] = true
			profileChanged = true
			itemChanged = true
		}
		if itemChanged {
			result.Added = append(result.Added, adoption.ID)
		}
	}
	if !catalogChanged && !profileChanged {
		return result, nil
	}

	newCatalogRaw, err := encodeYAMLNode(catalogNode)
	if err != nil {
		return AdoptionResult{}, fmt.Errorf("encode catalog: %w", err)
	}
	newProfileRaw, err := encodeYAMLNode(profileNode)
	if err != nil {
		return AdoptionResult{}, fmt.Errorf("encode profile: %w", err)
	}
	if err := replaceFile(catalogPath, newCatalogRaw, catalogMode); err != nil {
		return AdoptionResult{}, err
	}
	if err := replaceFile(profilePath, newProfileRaw, profileMode); err != nil {
		rollbackErr := replaceFile(catalogPath, catalogRaw, catalogMode)
		if rollbackErr != nil {
			return AdoptionResult{}, fmt.Errorf(
				"write profile: %v; restore catalog: %w", err, rollbackErr,
			)
		}
		return AdoptionResult{}, err
	}
	if err := validateEveryMachine(absoluteRoot); err != nil {
		catalogRollbackErr := replaceFile(catalogPath, catalogRaw, catalogMode)
		profileRollbackErr := replaceFile(profilePath, profileRaw, profileMode)
		if catalogRollbackErr != nil || profileRollbackErr != nil {
			return AdoptionResult{}, fmt.Errorf(
				"validate adopted configuration: %v; rollback catalog: %v; rollback profile: %v",
				err, catalogRollbackErr, profileRollbackErr,
			)
		}
		return AdoptionResult{}, fmt.Errorf(
			"validate adopted configuration (changes restored): %w", err,
		)
	}
	return result, nil
}

func adoptionPaths(root, profileName string) (string, string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "envctl.yaml"))
	if err != nil {
		return "", "", fmt.Errorf("read envctl.yaml: %w", err)
	}
	var rootConfig rootFile
	if err := decodeStrict(raw, &rootConfig); err != nil {
		return "", "", fmt.Errorf("decode envctl.yaml: %w", err)
	}
	catalogPath, err := safePath(root, rootConfig.Catalog)
	if err != nil {
		return "", "", err
	}
	profileFiles, err := yamlFiles(root, rootConfig.Profiles)
	if err != nil {
		return "", "", err
	}
	for _, relative := range profileFiles {
		path, err := safePath(root, relative)
		if err != nil {
			return "", "", err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", "", fmt.Errorf("read %s: %w", relative, err)
		}
		var profile versionedProfile
		if err := decodeStrict(raw, &profile); err != nil {
			return "", "", fmt.Errorf("decode %s: %w", relative, err)
		}
		if profile.Name == profileName {
			return catalogPath, path, nil
		}
	}
	return "", "", fmt.Errorf("unknown profile %q", profileName)
}

func loadCatalogNode(
	path string,
) ([]byte, os.FileMode, *yaml.Node, Catalog, error) {
	raw, mode, node, err := loadYAMLNode(path)
	if err != nil {
		return nil, 0, nil, Catalog{}, err
	}
	var catalog versionedCatalog
	if err := decodeStrict(raw, &catalog); err != nil {
		return nil, 0, nil, Catalog{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return raw, mode, node, catalog.Catalog, nil
}

func loadProfileNode(
	path string,
) ([]byte, os.FileMode, *yaml.Node, Profile, error) {
	raw, mode, node, err := loadYAMLNode(path)
	if err != nil {
		return nil, 0, nil, Profile{}, err
	}
	var profile versionedProfile
	if err := decodeStrict(raw, &profile); err != nil {
		return nil, 0, nil, Profile{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return raw, mode, node, profile.Profile, nil
}

func loadYAMLNode(path string) ([]byte, os.FileMode, *yaml.Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("read %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("inspect %s: %w", path, err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return nil, 0, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return raw, info.Mode().Perm(), &node, nil
}

func documentMapping(document *yaml.Node) *yaml.Node {
	if document == nil || document.Kind != yaml.DocumentNode ||
		len(document.Content) != 1 ||
		document.Content[0].Kind != yaml.MappingNode {
		return &yaml.Node{}
	}
	return document.Content[0]
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, error) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, errors.New("document root must be a mapping")
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1], nil
		}
	}
	return nil, fmt.Errorf("missing %q", key)
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func encodeYAMLNode(node *yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func replaceFile(path string, raw []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary %s permissions: %w", path, err)
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary %s: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary %s: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func validateEveryMachine(root string) error {
	machineIDs, err := MachineIDs(root)
	if err != nil {
		return err
	}
	for _, machineID := range machineIDs {
		if _, err := Load(root, machineID); err != nil {
			return fmt.Errorf("machine %q: %w", machineID, err)
		}
	}
	return nil
}
