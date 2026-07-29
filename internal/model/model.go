package model

import "time"

type Manager string

const (
	ManagerBrew   Manager = "brew"
	ManagerMAS    Manager = "mas"
	ManagerMise   Manager = "mise"
	ManagerBun    Manager = "bun"
	ManagerCustom Manager = "custom"
	ManagerManual Manager = "manual"
)

type PackageKind string

const (
	KindFormula PackageKind = "formula"
	KindCask    PackageKind = "cask"
	KindApp     PackageKind = "app"
	KindTool    PackageKind = "tool"
	KindUnknown PackageKind = "unknown"
)

type UpdatePolicy string

const (
	UpdateManaged     UpdatePolicy = "managed"
	UpdateInstallOnly UpdatePolicy = "install-only"
	UpdatePinned      UpdatePolicy = "pinned"
	UpdateExternal    UpdatePolicy = "external"
)

type PackageSpec struct {
	ID           string       `json:"id" yaml:"id,omitempty"`
	Manager      Manager      `json:"manager" yaml:"manager"`
	Kind         PackageKind  `json:"kind" yaml:"kind"`
	Source       string       `json:"source,omitempty" yaml:"source,omitempty"`
	Package      string       `json:"package" yaml:"package"`
	Version      string       `json:"version,omitempty" yaml:"version,omitempty"`
	UpdatePolicy UpdatePolicy `json:"update_policy" yaml:"update_policy"`
}

type InstalledPackage struct {
	Manager     Manager     `json:"manager"`
	Kind        PackageKind `json:"kind"`
	Source      string      `json:"source,omitempty"`
	Package     string      `json:"package"`
	Version     string      `json:"version,omitempty"`
	Requested   *bool       `json:"requested,omitempty"`
	Application string      `json:"application,omitempty"`
}

type LinkKind string

const (
	LinkKindFile      LinkKind = "file"
	LinkKindDirectory LinkKind = "directory"
)

type LinkSpec struct {
	ID     string   `json:"id" yaml:"id,omitempty"`
	Source string   `json:"source" yaml:"source"`
	Target string   `json:"target" yaml:"target"`
	Kind   LinkKind `json:"kind" yaml:"kind"`
	Digest string   `json:"digest,omitempty" yaml:"-"`
}

type RecoveryKind string

const (
	RecoveryKindSOPSFile   RecoveryKind = "sops-file"
	RecoveryKindAgeArchive RecoveryKind = "age-archive"
	RecoveryKindGPGKeyring RecoveryKind = "gpg-keyring"
)

type RecoverySpec struct {
	ID          string            `json:"id" yaml:"id,omitempty"`
	Kind        RecoveryKind      `json:"kind" yaml:"kind"`
	Source      string            `json:"source,omitempty" yaml:"source,omitempty"`
	Sources     map[string]string `json:"sources,omitempty" yaml:"sources,omitempty"`
	Target      string            `json:"target" yaml:"target"`
	Format      string            `json:"format,omitempty" yaml:"format,omitempty"`
	Mode        string            `json:"mode" yaml:"mode"`
	Members     []string          `json:"members,omitempty" yaml:"members,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty" yaml:"fingerprint,omitempty"`
}

type AppSettingKind string

const (
	AppSettingTailscaleStartOnLogin AppSettingKind = "tailscale-start-on-login"
)

type AppSettingSpec struct {
	ID                 string         `json:"id" yaml:"id,omitempty"`
	Kind               AppSettingKind `json:"kind" yaml:"kind"`
	PackageID          string         `json:"package_id" yaml:"package_id"`
	VerifyAfterRestart bool           `json:"verify_after_restart,omitempty" yaml:"verify_after_restart,omitempty"`
}

type RecoveryFindingStatus string

const (
	RecoveryFindingSatisfied     RecoveryFindingStatus = "satisfied"
	RecoveryFindingMissing       RecoveryFindingStatus = "missing"
	RecoveryFindingDrifted       RecoveryFindingStatus = "drifted"
	RecoveryFindingBlocked       RecoveryFindingStatus = "blocked"
	RecoveryFindingToolMissing   RecoveryFindingStatus = "tool-missing"
	RecoveryFindingSourceMissing RecoveryFindingStatus = "source-missing"
	RecoveryFindingSourceUnsafe  RecoveryFindingStatus = "source-unsafe"
)

type RecoveryFinding struct {
	Status     RecoveryFindingStatus `json:"status"`
	RecoveryID string                `json:"recovery_id"`
	Kind       RecoveryKind          `json:"kind"`
	Target     string                `json:"target"`
	Detail     string                `json:"detail"`
}

type RecoveryPlanSummary struct {
	Satisfied     int `json:"satisfied"`
	Missing       int `json:"missing"`
	Drifted       int `json:"drifted"`
	Blocked       int `json:"blocked"`
	ToolMissing   int `json:"tool_missing"`
	SourceMissing int `json:"source_missing"`
	SourceUnsafe  int `json:"source_unsafe"`
}

type RecoveryPlan struct {
	Summary  RecoveryPlanSummary `json:"summary"`
	Findings []RecoveryFinding   `json:"findings"`
	Ready    bool                `json:"ready"`
}

type LinkObservation struct {
	ID             string `json:"id"`
	Source         string `json:"source"`
	Target         string `json:"target"`
	SourceType     string `json:"source_type"`
	SourceDigest   string `json:"source_digest,omitempty"`
	TargetType     string `json:"target_type"`
	LinkTarget     string `json:"link_target,omitempty"`
	ResolvedTarget string `json:"resolved_target,omitempty"`
}

type Inventory struct {
	CollectedAt time.Time          `json:"collected_at"`
	Collectors  []string           `json:"collectors"`
	Packages    []InstalledPackage `json:"packages"`
	Links       []LinkObservation  `json:"links,omitempty"`
	Recoveries  []RecoveryFinding  `json:"recoveries,omitempty"`
	Errors      []CollectorError   `json:"errors,omitempty"`
}

type CollectorError struct {
	Collector string `json:"collector"`
	Message   string `json:"message"`
}

type FindingStatus string

const (
	FindingSatisfied    FindingStatus = "satisfied"
	FindingMissing      FindingStatus = "missing"
	FindingSourceDrift  FindingStatus = "source-drift"
	FindingKindDrift    FindingStatus = "kind-drift"
	FindingVersionDrift FindingStatus = "version-drift"
	FindingAmbiguous    FindingStatus = "ambiguous"
	FindingExtra        FindingStatus = "extra"
	FindingNotChecked   FindingStatus = "not-checked"
)

type Finding struct {
	Status    FindingStatus      `json:"status"`
	PackageID string             `json:"package_id,omitempty"`
	Desired   *PackageSpec       `json:"desired,omitempty"`
	Resolved  *PackageSpec       `json:"resolved,omitempty"`
	Installed []InstalledPackage `json:"installed,omitempty"`
	Detail    string             `json:"detail"`
}

type ActionType string

const (
	ActionInstall             ActionType = "package.install"
	ActionAdopt               ActionType = "package.adopt"
	ActionRemove              ActionType = "package.remove"
	ActionReinstallFromSource ActionType = "package.reinstall-from-source"
	ActionReview              ActionType = "manual.review"
)

type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskManual Risk = "manual"
)

type Action struct {
	Sequence          int         `json:"sequence"`
	Type              ActionType  `json:"type"`
	PackageID         string      `json:"package_id"`
	Manager           Manager     `json:"manager"`
	Kind              PackageKind `json:"kind"`
	Source            string      `json:"source,omitempty"`
	Package           string      `json:"package"`
	Version           string      `json:"version,omitempty"`
	Risk              Risk        `json:"risk"`
	Reversible        bool        `json:"reversible"`
	RequiresPrivilege bool        `json:"requires_privilege"`
	RequiresReview    bool        `json:"requires_review"`
	Reason            string      `json:"reason"`
}

type PlanSummary struct {
	Satisfied  int `json:"satisfied"`
	Missing    int `json:"missing"`
	Drifted    int `json:"drifted"`
	Ambiguous  int `json:"ambiguous"`
	Extra      int `json:"extra"`
	NotChecked int `json:"not_checked"`
	Actions    int `json:"actions"`
}

type Plan struct {
	Summary      PlanSummary      `json:"summary"`
	Findings     []Finding        `json:"findings"`
	Actions      []Action         `json:"actions"`
	LinkSummary  *LinkPlanSummary `json:"link_summary,omitempty"`
	LinkFindings []LinkFinding    `json:"link_findings,omitempty"`
	Warnings     []string         `json:"warnings,omitempty"`
}

type LinkFindingStatus string

const (
	LinkFindingSatisfied    LinkFindingStatus = "satisfied"
	LinkFindingMissing      LinkFindingStatus = "missing"
	LinkFindingWrongTarget  LinkFindingStatus = "wrong-target"
	LinkFindingOccupied     LinkFindingStatus = "target-occupied"
	LinkFindingSourceAbsent LinkFindingStatus = "source-missing"
	LinkFindingSourceDrift  LinkFindingStatus = "source-drift"
	LinkFindingNotChecked   LinkFindingStatus = "not-checked"
)

type LinkFinding struct {
	Status   LinkFindingStatus `json:"status"`
	LinkID   string            `json:"link_id"`
	Desired  LinkSpec          `json:"desired"`
	Observed *LinkObservation  `json:"observed,omitempty"`
	Detail   string            `json:"detail"`
}

type LinkPlanSummary struct {
	Satisfied  int `json:"satisfied"`
	Missing    int `json:"missing"`
	Drifted    int `json:"drifted"`
	NotChecked int `json:"not_checked"`
}
