package planner

import "github.com/VeniVidiVici/envctl/internal/model"

func BuildLinks(
	desired []model.LinkSpec,
	observed []model.LinkObservation,
	collected bool,
) (model.LinkPlanSummary, []model.LinkFinding) {
	byID := make(map[string]model.LinkObservation, len(observed))
	for _, item := range observed {
		byID[item.ID] = item
	}
	var summary model.LinkPlanSummary
	findings := make([]model.LinkFinding, 0, len(desired))
	for _, wanted := range desired {
		finding := model.LinkFinding{
			LinkID:  wanted.ID,
			Desired: wanted,
		}
		if !collected {
			finding.Status = model.LinkFindingNotChecked
			finding.Detail = "portable-link inventory was not collected"
			summary.NotChecked++
			findings = append(findings, finding)
			continue
		}
		actual, ok := byID[wanted.ID]
		if !ok {
			finding.Status = model.LinkFindingNotChecked
			finding.Detail = "portable-link collector returned no observation"
			summary.NotChecked++
			findings = append(findings, finding)
			continue
		}
		finding.Observed = linkObservationPointer(actual)
		switch {
		case (wanted.Kind != model.LinkKindDirectory &&
			actual.SourceType != "file") ||
			(wanted.Kind == model.LinkKindDirectory &&
				actual.SourceType != "directory"):
			finding.Status = model.LinkFindingSourceAbsent
			finding.Detail = "portable source does not match the declared kind on the target machine"
			summary.Drifted++
		case wanted.Digest == "" || actual.SourceDigest != wanted.Digest:
			finding.Status = model.LinkFindingSourceDrift
			finding.Detail = "portable source content does not match the desired digest"
			summary.Drifted++
		case actual.TargetType == "absent":
			finding.Status = model.LinkFindingMissing
			finding.Detail = "portable target does not exist"
			summary.Missing++
		case actual.TargetType != "symlink":
			finding.Status = model.LinkFindingOccupied
			finding.Detail = "portable target is occupied by a non-symlink object"
			summary.Drifted++
		case actual.ResolvedTarget != wanted.Source:
			finding.Status = model.LinkFindingWrongTarget
			finding.Detail = "portable target points to a different source"
			summary.Drifted++
		default:
			finding.Status = model.LinkFindingSatisfied
			finding.Detail = "portable target links to the desired source"
			summary.Satisfied++
		}
		findings = append(findings, finding)
	}
	return summary, findings
}

func linkObservationPointer(item model.LinkObservation) *model.LinkObservation {
	copy := item
	return &copy
}
