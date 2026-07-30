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

	"github.com/VeniVidiVici/envctl/internal/contentdigest"
	"github.com/VeniVidiVici/envctl/internal/model"
	"go.yaml.in/yaml/v3"
)

type rootFile struct {
	Version      int    `yaml:"version"`
	Catalog      string `yaml:"catalog"`
	Profiles     string `yaml:"profiles"`
	Machines     string `yaml:"machines"`
	RecoveryRoot string `yaml:"recovery_root,omitempty"`
	State        struct {
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
	machines, err := Machines(root)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(machines))
	for _, machine := range machines {
		ids = append(ids, machine.ID)
	}
	return ids, nil
}

func Machines(root string) ([]Machine, error) {
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
	machines := make([]Machine, 0, len(files))
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
		if err := validateMachine(machineFile.Machine); err != nil {
			return nil, fmt.Errorf("validate %s: %w", relative, err)
		}
		seen[machineFile.ID] = true
		machines = append(machines, machineFile.Machine)
	}
	sort.Slice(machines, func(i, j int) bool {
		return machines[i].ID < machines[j].ID
	})
	return machines, nil
}

func ProfileNames(root string) ([]string, error) {
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
	if rootConfig.Profiles == "" {
		return nil, errors.New("envctl.yaml must define profiles")
	}
	files, err := yamlFiles(absoluteRoot, rootConfig.Profiles)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	names := make([]string, 0, len(files))
	for _, relative := range files {
		path, err := safePath(absoluteRoot, relative)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", relative, err)
		}
		var profileFile versionedProfile
		if err := decodeStrict(raw, &profileFile); err != nil {
			return nil, fmt.Errorf("decode %s: %w", relative, err)
		}
		if profileFile.Version != 1 {
			return nil, fmt.Errorf("%s version is %d; expected 1", relative, profileFile.Version)
		}
		if profileFile.Name == "" {
			return nil, fmt.Errorf("%s has no profile name", relative)
		}
		if seen[profileFile.Name] {
			return nil, fmt.Errorf("duplicate profile %q", profileFile.Name)
		}
		seen[profileFile.Name] = true
		names = append(names, profileFile.Name)
	}
	sort.Strings(names)
	return names, nil
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
		relativeSource, err := filepath.Rel(absoluteRoot, sourcePath)
		if err != nil {
			return Loaded{}, fmt.Errorf("name link %q source: %w", link.ID, err)
		}
		sourceDigest, err := digestLinkSource(sourcePath, link.Kind)
		if err != nil {
			return Loaded{}, fmt.Errorf("digest link %q source: %w", link.ID, err)
		}
		hasher.Write([]byte(filepath.ToSlash(relativeSource)))
		hasher.Write([]byte{0})
		hasher.Write([]byte(sourceDigest))
		hasher.Write([]byte{0})
		loadedFiles = append(loadedFiles, filepath.ToSlash(relativeSource))
		targetPath, err := expandHome(link.Target)
		if err != nil {
			return Loaded{}, fmt.Errorf("expand link %q target: %w", link.ID, err)
		}
		desired.Links[index].Source = sourcePath
		desired.Links[index].Target = targetPath
		desired.Links[index].Digest = sourceDigest
	}
	recoveryRoot := ""
	if len(catalogFile.Recoveries) > 0 {
		if rootConfig.RecoveryRoot == "" {
			return Loaded{}, errors.New(
				"envctl.yaml must define recovery_root when recoveries are configured",
			)
		}
		recoveryRoot, err = expandHome(rootConfig.RecoveryRoot)
		if err != nil {
			return Loaded{}, fmt.Errorf("expand recovery root: %w", err)
		}
		if err := validateRecoveryRoot(recoveryRoot); err != nil {
			return Loaded{}, err
		}
	}
	for index, recovery := range desired.Recoveries {
		target, err := expandHome(recovery.Target)
		if err != nil {
			return Loaded{}, fmt.Errorf(
				"expand recovery %q target: %w", recovery.ID, err,
			)
		}
		desired.Recoveries[index].Target = target
		switch recovery.Kind {
		case model.RecoveryKindSOPSFile:
			sourcePath, err := safePath(absoluteRoot, recovery.Source)
			if err != nil {
				return Loaded{}, fmt.Errorf(
					"resolve recovery %q source: %w", recovery.ID, err,
				)
			}
			raw, err := os.ReadFile(sourcePath)
			if err != nil {
				return Loaded{}, fmt.Errorf(
					"read recovery %q source: %w", recovery.ID, err,
				)
			}
			relativeSource, err := filepath.Rel(absoluteRoot, sourcePath)
			if err != nil {
				return Loaded{}, fmt.Errorf(
					"name recovery %q source: %w", recovery.ID, err,
				)
			}
			hasher.Write([]byte(filepath.ToSlash(relativeSource)))
			hasher.Write([]byte{0})
			hasher.Write(raw)
			hasher.Write([]byte{0})
			loadedFiles = append(loadedFiles, filepath.ToSlash(relativeSource))
			desired.Recoveries[index].Source = sourcePath
		case model.RecoveryKindAgeArchive:
			desired.Recoveries[index].Source = filepath.Join(
				recoveryRoot,
				recovery.Source,
			)
		case model.RecoveryKindGPGKeyring:
			resolvedSources := make(map[string]string, len(recovery.Sources))
			for role, source := range recovery.Sources {
				resolvedSources[role] = filepath.Join(recoveryRoot, source)
			}
			desired.Recoveries[index].Sources = resolvedSources
		default:
			return Loaded{}, fmt.Errorf(
				"recovery %q has unsupported kind %q",
				recovery.ID,
				recovery.Kind,
			)
		}
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
		if item.Manager == model.ManagerMise {
			if item.Kind != model.KindTool {
				return fmt.Errorf(
					"catalog Mise package %q must declare tool kind", id,
				)
			}
			if item.Source != "" || item.Version == "" {
				return fmt.Errorf(
					"catalog Mise package %q must declare a version and no source", id,
				)
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
		if item.Kind != model.LinkKindFile &&
			item.Kind != model.LinkKindDirectory {
			return fmt.Errorf(
				"catalog link %q must declare file or directory kind", id,
			)
		}
		sourcePath, err := safePath(root, item.Source)
		if err != nil {
			return fmt.Errorf("catalog link %q source: %w", id, err)
		}
		info, err := os.Stat(sourcePath)
		if err != nil {
			return fmt.Errorf("catalog link %q source: %w", id, err)
		}
		switch item.Kind {
		case model.LinkKindFile:
			if !info.Mode().IsRegular() {
				return fmt.Errorf(
					"catalog link %q source is not a regular file", id,
				)
			}
		case model.LinkKindDirectory:
			if !info.IsDir() {
				return fmt.Errorf(
					"catalog link %q source is not a directory", id,
				)
			}
		}
		if err := validateLinkTarget(item.Target); err != nil {
			return fmt.Errorf("catalog link %q target: %w", id, err)
		}
		catalog.Links[id] = item
	}
	recoveryTargets := make(map[string]string, len(catalog.Recoveries))
	for id, item := range catalog.Recoveries {
		if !safeIdentifier(id) {
			return fmt.Errorf("catalog recovery has unsafe id %q", id)
		}
		if item.ID != "" && item.ID != id {
			return fmt.Errorf(
				"catalog recovery %q declares mismatched id %q", id, item.ID,
			)
		}
		item.ID = id
		if err := validateRecoverySpec(root, item); err != nil {
			return fmt.Errorf("catalog recovery %q: %w", id, err)
		}
		cleanedTarget := filepath.Clean(item.Target)
		if existing := recoveryTargets[cleanedTarget]; existing != "" {
			return fmt.Errorf(
				"catalog recoveries %q and %q share target %q",
				existing,
				id,
				item.Target,
			)
		}
		recoveryTargets[cleanedTarget] = id
		catalog.Recoveries[id] = item
	}
	for id, item := range catalog.AppSettings {
		if !safeIdentifier(id) {
			return fmt.Errorf("catalog app setting has unsafe id %q", id)
		}
		if item.ID != "" && item.ID != id {
			return fmt.Errorf(
				"catalog app setting %q declares mismatched id %q", id, item.ID,
			)
		}
		item.ID = id
		switch item.Kind {
		case model.AppSettingTailscaleStartOnLogin:
		default:
			return fmt.Errorf(
				"catalog app setting %q has unsupported kind %q", id, item.Kind,
			)
		}
		pkg, ok := catalog.Packages[item.PackageID]
		if !ok {
			return fmt.Errorf(
				"catalog app setting %q references unknown package %q",
				id,
				item.PackageID,
			)
		}
		if item.Kind == model.AppSettingTailscaleStartOnLogin &&
			(pkg.Manager != model.ManagerBrew || pkg.Kind != model.KindCask ||
				pkg.Package != "tailscale-app") {
			return fmt.Errorf(
				"catalog app setting %q must reference the tailscale-app Homebrew cask",
				id,
			)
		}
		catalog.AppSettings[id] = item
	}
	return nil
}

func digestLinkSource(path string, kind model.LinkKind) (string, error) {
	switch kind {
	case model.LinkKindFile:
		return contentdigest.File(path)
	case model.LinkKindDirectory:
		return contentdigest.Directory(path)
	default:
		return "", fmt.Errorf("unsupported portable link kind %q", kind)
	}
}

func validateRecoverySpec(root string, item model.RecoverySpec) error {
	if err := validateRecoveryTarget(item.Kind, item.Target); err != nil {
		return err
	}
	switch item.Kind {
	case model.RecoveryKindSOPSFile:
		if item.Mode != "0600" {
			return errors.New("sops-file mode must be quoted 0600")
		}
		switch item.Format {
		case "dotenv", "ini", "yaml", "json":
		default:
			return fmt.Errorf("sops-file has unsupported format %q", item.Format)
		}
		if len(item.Sources) != 0 || len(item.Members) != 0 ||
			item.Fingerprint != "" {
			return errors.New("sops-file has fields for another recovery kind")
		}
		sourcePath, err := safePath(root, item.Source)
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("source must be a regular non-symlink file")
		}
	case model.RecoveryKindAgeArchive:
		if item.Mode != "0600" {
			return errors.New("age-archive member mode must be quoted 0600")
		}
		if !safeRecoverySourceName(item.Source) {
			return errors.New("age-archive source must be a safe recovery filename")
		}
		if item.Format != "" || len(item.Sources) != 0 ||
			item.Fingerprint != "" {
			return errors.New("age-archive has fields for another recovery kind")
		}
		if len(item.Members) == 0 || len(item.Members) > 100 {
			return errors.New("age-archive must declare between 1 and 100 members")
		}
		seen := make(map[string]bool, len(item.Members))
		for _, member := range item.Members {
			if !safeRecoverySourceName(member) || seen[member] {
				return fmt.Errorf("age-archive has unsafe or duplicate member %q", member)
			}
			seen[member] = true
		}
	case model.RecoveryKindGPGKeyring:
		if item.Mode != "0700" {
			return errors.New("gpg-keyring mode must be quoted 0700")
		}
		if item.Source != "" || item.Format != "" || len(item.Members) != 0 {
			return errors.New("gpg-keyring has fields for another recovery kind")
		}
		if len(item.Fingerprint) != 40 && len(item.Fingerprint) != 64 {
			return errors.New("gpg-keyring fingerprint must contain 40 or 64 hex characters")
		}
		if _, err := hex.DecodeString(item.Fingerprint); err != nil ||
			item.Fingerprint != strings.ToUpper(item.Fingerprint) {
			return errors.New("gpg-keyring fingerprint must be uppercase hexadecimal")
		}
		expectedRoles := []string{"ownertrust", "private", "public"}
		if len(item.Sources) != len(expectedRoles) {
			return errors.New("gpg-keyring must declare public, private, and ownertrust sources")
		}
		for _, role := range expectedRoles {
			if !safeRecoverySourceName(item.Sources[role]) {
				return fmt.Errorf(
					"gpg-keyring %s source must be a safe recovery filename", role,
				)
			}
		}
	default:
		return fmt.Errorf("unsupported recovery kind %q", item.Kind)
	}
	return nil
}

func validateRecoveryTarget(kind model.RecoveryKind, target string) error {
	if !strings.HasPrefix(target, "~/") {
		return fmt.Errorf(
			"recovery target must begin with ~/: %q",
			target,
		)
	}
	cleaned := filepath.Clean(strings.TrimPrefix(target, "~/"))
	if cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("recovery target escapes or replaces the home directory: %q", target)
	}
	switch kind {
	case model.RecoveryKindAgeArchive:
		if cleaned != ".ssh" {
			return errors.New("age-archive recovery target must be ~/.ssh")
		}
	case model.RecoveryKindGPGKeyring:
		if cleaned != ".gnupg" {
			return errors.New("gpg-keyring recovery target must be ~/.gnupg")
		}
	case model.RecoveryKindSOPSFile:
		if cleaned == ".ssh" || strings.HasPrefix(cleaned, ".ssh/") ||
			cleaned == ".gnupg" || strings.HasPrefix(cleaned, ".gnupg/") ||
			cleaned == ".local/state" ||
			strings.HasPrefix(cleaned, ".local/state/") {
			return fmt.Errorf("sops-file target enters protected state area %q", cleaned)
		}
	}
	return nil
}

func validateRecoveryRoot(root string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	relative, err := filepath.Rel(home, root)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("recovery_root must be a directory inside the home directory")
	}
	return nil
}

func safeRecoverySourceName(value string) bool {
	if value == "" || filepath.Base(value) != value || value == "." || value == ".." {
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
	allowedGPGConfig := map[string]bool{
		filepath.Join(".gnupg", "dirmngr.conf"):   true,
		filepath.Join(".gnupg", "gpg-agent.conf"): true,
		filepath.Join(".gnupg", "gpg.conf"):       true,
	}
	allowedPortableState := map[string]bool{
		filepath.Join(".ssh", "envctl-hosts.conf"): true,
	}
	if allowedGPGConfig[cleaned] || allowedPortableState[cleaned] {
		return nil
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
	if fingerprint := machine.Match.HardwareUUIDSHA256; fingerprint != "" {
		if len(fingerprint) != sha256.Size*2 {
			return fmt.Errorf(
				"machine %q has invalid hardware UUID fingerprint length",
				machine.ID,
			)
		}
		if _, err := hex.DecodeString(fingerprint); err != nil ||
			fingerprint != strings.ToLower(fingerprint) {
			return fmt.Errorf(
				"machine %q has invalid hardware UUID fingerprint",
				machine.ID,
			)
		}
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

func ValidateMachine(machine Machine) error {
	return validateMachine(machine)
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
