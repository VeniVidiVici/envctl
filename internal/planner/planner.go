package planner

import (
	"fmt"
	"sort"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func Build(
	desired []model.PackageSpec,
	installed []model.InstalledPackage,
	collectedManagers []model.Manager,
) model.Plan {
	collected := make(map[model.Manager]bool)
	for _, manager := range collectedManagers {
		collected[manager] = true
	}

	sortedDesired := append([]model.PackageSpec(nil), desired...)
	sort.Slice(sortedDesired, func(i, j int) bool {
		if sortedDesired[i].Manager != sortedDesired[j].Manager {
			return sortedDesired[i].Manager < sortedDesired[j].Manager
		}
		if sortedDesired[i].Package != sortedDesired[j].Package {
			return sortedDesired[i].Package < sortedDesired[j].Package
		}
		return sortedDesired[i].ID < sortedDesired[j].ID
	})

	used := make(map[int]bool)
	var plan model.Plan
	for _, wanted := range sortedDesired {
		if !collected[wanted.Manager] {
			plan.Findings = append(plan.Findings, model.Finding{
				Status:    model.FindingNotChecked,
				PackageID: wanted.ID,
				Desired:   packagePointer(wanted),
				Detail:    fmt.Sprintf("%s inventory is not collected yet", wanted.Manager),
			})
			plan.Summary.NotChecked++
			continue
		}

		finding, matched := match(wanted, installed)
		for _, index := range matched {
			used[index] = true
		}
		plan.Findings = append(plan.Findings, finding)
		updateSummary(&plan.Summary, finding.Status)
		if action, ok := actionFor(finding); ok {
			action.Sequence = len(plan.Actions) + 1
			plan.Actions = append(plan.Actions, action)
		}
	}

	for index, item := range installed {
		if used[index] || !collected[item.Manager] || !isUserManaged(item) {
			continue
		}
		plan.Findings = append(plan.Findings, model.Finding{
			Status:    model.FindingExtra,
			Installed: []model.InstalledPackage{item},
			Detail:    "installed on request but absent from desired state",
		})
		plan.Summary.Extra++
	}

	plan.Summary.Actions = len(plan.Actions)
	if plan.Summary.NotChecked > 0 {
		plan.Warnings = append(plan.Warnings,
			"some desired packages were not checked because their manager has no collector yet")
	}
	if plan.Summary.Extra > 0 {
		plan.Warnings = append(plan.Warnings,
			"extra packages are findings only; removal is never planned automatically")
	}
	return plan
}

func match(wanted model.PackageSpec, installed []model.InstalledPackage) (model.Finding, []int) {
	var candidates []int
	for index, item := range installed {
		if item.Manager == wanted.Manager && item.Package == wanted.Package {
			candidates = append(candidates, index)
		}
	}

	base := model.Finding{
		PackageID: wanted.ID,
		Desired:   packagePointer(wanted),
	}
	if len(candidates) == 0 {
		base.Status = model.FindingMissing
		base.Detail = "desired package is not installed"
		return base, nil
	}

	filtered := candidates
	if wanted.Kind != "" && wanted.Kind != model.KindUnknown {
		filtered = filterIndices(filtered, installed, func(item model.InstalledPackage) bool {
			return item.Kind == wanted.Kind
		})
		if len(filtered) == 0 {
			base.Status = model.FindingKindDrift
			base.Installed = packagesAt(installed, candidates)
			base.Detail = fmt.Sprintf("installed as %s instead of %s",
				joinKinds(base.Installed), wanted.Kind)
			return base, candidates
		}
	}

	if wanted.Source != "" {
		sourceMatches := filterIndices(filtered, installed, func(item model.InstalledPackage) bool {
			return item.Source == wanted.Source
		})
		if len(sourceMatches) == 0 {
			base.Status = model.FindingSourceDrift
			base.Installed = packagesAt(installed, filtered)
			base.Detail = fmt.Sprintf("installed from %s instead of %s",
				joinSources(base.Installed), wanted.Source)
			return base, candidates
		}
		filtered = sourceMatches
	}

	if len(filtered) != 1 {
		base.Status = model.FindingAmbiguous
		base.Installed = packagesAt(installed, filtered)
		base.Detail = "more than one installed package matches the desired identity"
		return base, candidates
	}

	actual := installed[filtered[0]]
	if wanted.Version != "" && actual.Version != wanted.Version {
		base.Status = model.FindingVersionDrift
		base.Installed = []model.InstalledPackage{actual}
		base.Detail = fmt.Sprintf(
			"installed version request %q instead of %q",
			actual.Version, wanted.Version,
		)
		return base, candidates
	}
	resolved := wanted
	resolved.Kind = actual.Kind
	resolved.Source = actual.Source
	base.Status = model.FindingSatisfied
	base.Resolved = packagePointer(resolved)
	base.Installed = []model.InstalledPackage{actual}
	if wanted.Kind == model.KindUnknown || wanted.Kind == "" || wanted.Source == "" {
		if wanted.Manager == model.ManagerBrew {
			base.Detail = "installed; type or source inferred from the Homebrew receipt"
		} else {
			base.Detail = "installed; type or source inferred from collected inventory"
		}
	} else {
		base.Detail = "installed with the desired type and source"
	}
	return base, filtered
}

func actionFor(finding model.Finding) (model.Action, bool) {
	if finding.Desired == nil {
		return model.Action{}, false
	}
	wanted := *finding.Desired
	action := model.Action{
		PackageID: wanted.ID,
		Manager:   wanted.Manager,
		Kind:      wanted.Kind,
		Source:    wanted.Source,
		Package:   wanted.Package,
		Version:   wanted.Version,
	}

	switch finding.Status {
	case model.FindingMissing:
		if wanted.Kind == "" || wanted.Kind == model.KindUnknown {
			action.Type = model.ActionReview
			action.Risk = model.RiskManual
			action.RequiresReview = true
			action.Reason = "package is missing, but its Homebrew type is unresolved"
		} else {
			action.Type = model.ActionInstall
			action.Risk = model.RiskLow
			action.Reversible = true
			action.Reason = "install missing desired package"
		}
		return action, true
	case model.FindingSourceDrift:
		action.Type = model.ActionReinstallFromSource
		action.Risk = model.RiskMedium
		action.RequiresReview = true
		action.Reason = "replace the installed package with the configured source"
		return action, true
	case model.FindingVersionDrift:
		action.Type = model.ActionInstall
		action.Risk = model.RiskLow
		action.Reversible = true
		action.Reason = "install the configured version request"
		return action, true
	case model.FindingKindDrift, model.FindingAmbiguous:
		action.Type = model.ActionReview
		action.Risk = model.RiskManual
		action.RequiresReview = true
		action.Reason = finding.Detail
		return action, true
	default:
		return model.Action{}, false
	}
}

func filterIndices(
	indices []int,
	packages []model.InstalledPackage,
	keep func(model.InstalledPackage) bool,
) []int {
	var filtered []int
	for _, index := range indices {
		if keep(packages[index]) {
			filtered = append(filtered, index)
		}
	}
	return filtered
}

func packagesAt(packages []model.InstalledPackage, indices []int) []model.InstalledPackage {
	result := make([]model.InstalledPackage, 0, len(indices))
	for _, index := range indices {
		result = append(result, packages[index])
	}
	return result
}

func isUserManaged(item model.InstalledPackage) bool {
	switch item.Manager {
	case model.ManagerMAS, model.ManagerMise, model.ManagerBun:
		return true
	case model.ManagerBrew:
		if item.Kind == model.KindCask {
			return true
		}
		return item.Kind == model.KindFormula && item.Requested != nil && *item.Requested
	default:
		return false
	}
}

func packagePointer(item model.PackageSpec) *model.PackageSpec {
	copy := item
	return &copy
}

func joinKinds(packages []model.InstalledPackage) string {
	values := make([]string, 0, len(packages))
	seen := make(map[model.PackageKind]bool)
	for _, item := range packages {
		if !seen[item.Kind] {
			values = append(values, string(item.Kind))
			seen[item.Kind] = true
		}
	}
	sort.Strings(values)
	return fmt.Sprintf("%v", values)
}

func joinSources(packages []model.InstalledPackage) string {
	values := make([]string, 0, len(packages))
	seen := make(map[string]bool)
	for _, item := range packages {
		if !seen[item.Source] {
			values = append(values, item.Source)
			seen[item.Source] = true
		}
	}
	sort.Strings(values)
	return fmt.Sprintf("%v", values)
}

func updateSummary(summary *model.PlanSummary, status model.FindingStatus) {
	switch status {
	case model.FindingSatisfied:
		summary.Satisfied++
	case model.FindingMissing:
		summary.Missing++
	case model.FindingSourceDrift, model.FindingKindDrift,
		model.FindingVersionDrift:
		summary.Drifted++
	case model.FindingAmbiguous:
		summary.Ambiguous++
	case model.FindingExtra:
		summary.Extra++
	case model.FindingNotChecked:
		summary.NotChecked++
	}
}
