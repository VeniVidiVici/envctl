package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeniVidiVici/envctl/internal/model"
	"go.yaml.in/yaml/v3"
)

type rootFile struct {
	Version  int    `yaml:"version"`
	Catalog  string `yaml:"catalog"`
	Profiles string `yaml:"profiles"`
	Machines string `yaml:"machines"`
	State    struct {
		Database string `yaml:"database"`
	} `yaml:"state,omitempty"`
}

type versionedCatalog struct {
	Version int `yaml:"version"`
	Catalog `yaml:",inline"`
}

type versionedProfile struct {
	Version int `yaml:"version"`
	Profile `yaml:",inline"`
}

type versionedMachine struct {
	Version int `yaml:"version"`
	Machine `yaml:",inline"`
}

type Loaded struct {
	Root        string       `json:"root"`
	Database    string       `json:"database,omitempty"`
	Digest      string       `json:"digest"`
	Catalog     Catalog      `json:"catalog"`
	Profiles    []Profile    `json:"profiles"`
	Machine     Machine      `json:"machine"`
	Desired     DesiredState `json:"desired"`
	LoadedFiles []string     `json:"loaded_files"`
}

func MachineIDs(root string) ([]string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve config root: %w", err)
	}
	var rootConfig rootFile
	raw, err := os.ReadFile(filepath.Join(absoluteRoot, "envctl.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read envctl.yaml: %w", err)
	}
	if err := decodeStrict(raw, &rootConfig); err != nil {
		return nil, fmt.Errorf("decode envctl.yaml: %w", err)
	}
	if rootConfig.Version != 1 {
		return nil, fmt.Errorf("envctl.yaml version is %d; expected 1", rootConfig.Version)
	}
	if rootConfig.Machines == "" {
		return nil, errors.New("envctl.yaml must define machines")
	}

	files, err := yamlFiles(absoluteRoot, rootConfig.Machines)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	ids := make([]string, 0, len(files))
	for _, relative := range files {
		path, err := safePath(absoluteRoot, relative)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", relative, err)
		}
		var machineFile versionedMachine
		if err := decodeStrict(raw, &machineFile); err != nil {
			return nil, fmt.Errorf("decode %s: %w", relative, err)
		}
		if machineFile.Version != 1 {
			return nil, fmt.Errorf("%s version is %d; expected 1", relative, machineFile.Version)
		}
		if machineFile.ID == "" {
			return nil, fmt.Errorf("%s has no machine id", relative)
		}
		if seen[machineFile.ID] {
			return nil, fmt.Errorf("duplicate machine %q", machineFile.ID)
		}
		seen[machineFile.ID] = true
		ids = append(ids, machineFile.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

func Load(root, machineID string) (Loaded, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return Loaded{}, fmt.Errorf("resolve config root: %w", err)
	}
	rootInfo, err := os.Stat(absoluteRoot)
	if err != nil {
		return Loaded{}, fmt.Errorf("inspect config root: %w", err)
	}
	if !rootInfo.IsDir() {
		return Loaded{}, fmt.Errorf("config root is not a directory: %s", absoluteRoot)
	}

	hasher := sha256.New()
	var loadedFiles []string
	load := func(relative string, target any) error {
		path, err := safePath(absoluteRoot, relative)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", relative, err)
		}
		if err := decodeStrict(raw, target); err != nil {
			return fmt.Errorf("decode %s: %w", relative, err)
		}
		hasher.Write([]byte(filepath.ToSlash(relative)))
		hasher.Write([]byte{0})
		hasher.Write(raw)
		hasher.Write([]byte{0})
		loadedFiles = append(loadedFiles, filepath.ToSlash(relative))
		return nil
	}

	var rootConfig rootFile
	if err := load("envctl.yaml", &rootConfig); err != nil {
		return Loaded{}, err
	}
	if rootConfig.Version != 1 {
		return Loaded{}, fmt.Errorf("envctl.yaml version is %d; expected 1", rootConfig.Version)
	}
	if rootConfig.Catalog == "" || rootConfig.Profiles == "" || rootConfig.Machines == "" {
		return Loaded{}, errors.New("envctl.yaml must define catalog, profiles, and machines")
	}

	var catalogFile versionedCatalog
	if err := load(rootConfig.Catalog, &catalogFile); err != nil {
		return Loaded{}, err
	}
	if catalogFile.Version != 1 {
		return Loaded{}, fmt.Errorf("%s version is %d; expected 1",
			rootConfig.Catalog, catalogFile.Version)
	}
	if err := validateCatalog(absoluteRoot, &catalogFile.Catalog); err != nil {
		return Loaded{}, err
	}

	profileFiles, err := yamlFiles(absoluteRoot, rootConfig.Profiles)
	if err != nil {
		return Loaded{}, err
	}
	profiles := make(map[string]Profile)
	for _, relative := range profileFiles {
		var profileFile versionedProfile
		if err := load(relative, &profileFile); err != nil {
			return Loaded{}, err
		}
		if profileFile.Version != 1 {
			return Loaded{}, fmt.Errorf("%s version is %d; expected 1", relative, profileFile.Version)
		}
		if profileFile.Name == "" {
			return Loaded{}, fmt.Errorf("%s has no profile name", relative)
		}
		if _, exists := profiles[profileFile.Name]; exists {
			return Loaded{}, fmt.Errorf("duplicate profile %q", profileFile.Name)
		}
		profiles[profileFile.Name] = profileFile.Profile
	}

	machineFiles, err := yamlFiles(absoluteRoot, rootConfig.Machines)
	if err != nil {
		return Loaded{}, err
	}
	machines := make(map[string]Machine)
	for _, relative := range machineFiles {
		var machineFile versionedMachine
		if err := load(relative, &machineFile); err != nil {
			return Loaded{}, err
		}
		if machineFile.Version != 1 {
			return Loaded{}, fmt.Errorf("%s version is %d; expected 1", relative, machineFile.Version)
		}
		if machineFile.ID == "" {
			return Loaded{}, fmt.Errorf("%s has no machine id", relative)
		}
		if _, exists := machines[machineFile.ID]; exists {
			return Loaded{}, fmt.Errorf("duplicate machine %q", machineFile.ID)
		}
		machines[machineFile.ID] = machineFile.Machine
	}
	machine, ok := machines[machineID]
	if !ok {
		return Loaded{}, fmt.Errorf("unknown machine %q", machineID)
	}
	if err := validateMachine(machine); err != nil {
		return Loaded{}, err
	}

	desired, err := Resolve(catalogFile.Catalog, profiles, machine)
	if err != nil {
		return Loaded{}, err
	}
	for index, link := range desired.Links {
		sourcePath, err := safePath(absoluteRoot, link.Source)
		if err != nil {
			return Loaded{}, fmt.Errorf("resolve link %q source: %w", link.ID, err)
		}
		raw, err := os.ReadFile(sourcePath)
		if err != nil {
			return Loaded{}, fmt.Errorf("read link %q source: %w", link.ID, err)
		}
		relativeSource, err := filepath.Rel(absoluteRoot, sourcePath)
		if err != nil {
			return Loaded{}, fmt.Errorf("name link %q source: %w", link.ID, err)
		}
		hasher.Write([]byte(filepath.ToSlash(relativeSource)))
		hasher.Write([]byte{0})
		hasher.Write(raw)
		hasher.Write([]byte{0})
		loadedFiles = append(loadedFiles, filepath.ToSlash(relativeSource))
		targetPath, err := expandHome(link.Target)
		if err != nil {
			return Loaded{}, fmt.Errorf("expand link %q target: %w", link.ID, err)
		}
		desired.Links[index].Source = sourcePath
		desired.Links[index].Target = targetPath
		sourceDigest := sha256.Sum256(raw)
		desired.Links[index].Digest = hex.EncodeToString(sourceDigest[:])
	}

	profileNames := make([]string, 0, len(profiles))
	for name := range profiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	profileList := make([]Profile, 0, len(profileNames))
	for _, name := range profileNames {
		profileList = append(profileList, profiles[name])
	}
	sort.Strings(loadedFiles)

	database, err := expandHome(rootConfig.State.Database)
	if err != nil {
		return Loaded{}, err
	}
	return Loaded{
		Root:        absoluteRoot,
		Database:    database,
		Digest:      hex.EncodeToString(hasher.Sum(nil)),
		Catalog:     catalogFile.Catalog,
		Profiles:    profileList,
		Machine:     machine,
		Desired:     desired,
		LoadedFiles: loadedFiles,
	}, nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	err := decoder.Decode(&extra)
	if err == nil {
		return errors.New("multiple YAML documents are not supported")
	}
	if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func validateCatalog(root string, catalog *Catalog) error {
	if len(catalog.Packages) == 0 {
		return errors.New("catalog contains no packages")
	}
	for id, item := range catalog.Packages {
		if id == "" {
			return errors.New("catalog contains an empty package id")
		}
		if item.ID != "" && item.ID != id {
			return fmt.Errorf("catalog package %q declares mismatched id %q", id, item.ID)
		}
		item.ID = id
		if item.Package == "" {
			return fmt.Errorf("catalog package %q has no package name", id)
		}
		if !validManager(item.Manager) {
			return fmt.Errorf("catalog package %q has unsupported manager %q", id, item.Manager)
		}
		if !validPolicy(item.UpdatePolicy) {
			return fmt.Errorf("catalog package %q has unsupported update policy %q",
				id, item.UpdatePolicy)
		}
		if item.Manager == "brew" {
			if item.Kind != "formula" && item.Kind != "cask" {
				return fmt.Errorf("catalog Homebrew package %q must declare formula or cask kind", id)
			}
			if item.Source == "" {
				return fmt.Errorf("catalog Homebrew package %q must declare its source", id)
			}
		}
		catalog.Packages[id] = item
	}
	for id, item := range catalog.Links {
		if !safeIdentifier(id) {
			return fmt.Errorf("catalog link has unsafe id %q", id)
		}
		if item.ID != "" && item.ID != id {
			return fmt.Errorf("catalog link %q declares mismatched id %q", id, item.ID)
		}
		item.ID = id
		if item.Kind != model.LinkKindFile {
			return fmt.Errorf("catalog link %q must declare file kind", id)
		}
		sourcePath, err := safePath(root, item.Source)
		if err != nil {
			return fmt.Errorf("catalog link %q source: %w", id, err)
		}
		info, err := os.Stat(sourcePath)
		if err != nil {
			return fmt.Errorf("catalog link %q source: %w", id, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("catalog link %q source is not a regular file", id)
		}
		if err := validateLinkTarget(item.Target); err != nil {
			return fmt.Errorf("catalog link %q target: %w", id, err)
		}
		catalog.Links[id] = item
	}
	return nil
}

func validateLinkTarget(target string) error {
	if !strings.HasPrefix(target, "~/") {
		return fmt.Errorf("must be a home-relative path beginning with ~/: %q", target)
	}
	relative := strings.TrimPrefix(target, "~/")
	cleaned := filepath.Clean(relative)
	if cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("escapes or replaces the home directory: %q", target)
	}
	blocked := []string{
		".cache",
		filepath.Join(".local", "share"),
		filepath.Join(".local", "state"),
		".ssh",
		".gnupg",
	}
	for _, prefix := range blocked {
		if cleaned == prefix ||
			strings.HasPrefix(cleaned, prefix+string(filepath.Separator)) {
			return fmt.Errorf("enters machine-local state area %q", prefix)
		}
	}
	return nil
}

func validateMachine(machine Machine) error {
	if !safeIdentifier(machine.ID) {
		return fmt.Errorf("machine has unsafe id %q", machine.ID)
	}
	switch machine.Access.Type {
	case "local":
		if machine.Access.Host != "" {
			return fmt.Errorf("local machine %q must not declare an SSH host", machine.ID)
		}
	case "ssh":
		if machine.Access.Host == "" {
			return fmt.Errorf("SSH machine %q must declare a host", machine.ID)
		}
		for _, character := range machine.Access.Host {
			if (character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') ||
				strings.ContainsRune("._-", character) {
				continue
			}
			return fmt.Errorf("SSH machine %q has an unsafe host %q",
				machine.ID, machine.Access.Host)
		}
	default:
		return fmt.Errorf("machine %q has unsupported access type %q",
			machine.ID, machine.Access.Type)
	}
	return nil
}

func safeIdentifier(value string) bool {
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

func validManager(manager model.Manager) bool {
	switch string(manager) {
	case "brew", "mas", "mise", "bun", "custom", "manual":
		return true
	default:
		return false
	}
}

func validPolicy(policy model.UpdatePolicy) bool {
	switch string(policy) {
	case "managed", "install-only", "pinned", "external":
		return true
	default:
		return false
	}
}

func yamlFiles(root, relativeDirectory string) ([]string, error) {
	directory, err := safePath(root, relativeDirectory)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relativeDirectory, err)
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension != ".yaml" && extension != ".yml" {
			continue
		}
		files = append(files, filepath.Join(relativeDirectory, entry.Name()))
	}
	sort.Strings(files)
	return files, nil
}

func safePath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("config path must be relative: %q", relative)
	}
	cleaned := filepath.Clean(relative)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("config path escapes the root: %q", relative)
	}
	path := filepath.Join(root, cleaned)
	relativeToRoot, err := filepath.Rel(root, path)
	if err != nil || relativeToRoot == ".." ||
		strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("config path escapes the root: %q", relative)
	}
	return path, nil
}

func expandHome(path string) (string, error) {
	if path == "" || path == "~" {
		if path == "" {
			return "", nil
		}
		home, err := os.UserHomeDir()
		return home, err
	}
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}
