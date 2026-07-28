package planner

import (
	"testing"

	"github.com/VeniVidiVici/envctl/internal/model"
)

func TestBuildLinksClassifiesSafeReadOnlyFindings(t *testing.T) {
	desired := []model.LinkSpec{
		{ID: "good", Source: "/repo/good", Target: "/home/good", Digest: "good"},
		{ID: "missing", Source: "/repo/missing", Target: "/home/missing", Digest: "missing"},
		{ID: "wrong", Source: "/repo/wrong", Target: "/home/wrong", Digest: "wrong"},
		{ID: "occupied", Source: "/repo/occupied", Target: "/home/occupied", Digest: "occupied"},
		{ID: "source", Source: "/repo/source", Target: "/home/source", Digest: "source"},
		{ID: "stale", Source: "/repo/stale", Target: "/home/stale", Digest: "new"},
	}
	observed := []model.LinkObservation{
		{ID: "good", SourceType: "file", SourceDigest: "good", TargetType: "symlink", ResolvedTarget: "/repo/good"},
		{ID: "missing", SourceType: "file", SourceDigest: "missing", TargetType: "absent"},
		{ID: "wrong", SourceType: "file", SourceDigest: "wrong", TargetType: "symlink", ResolvedTarget: "/other"},
		{ID: "occupied", SourceType: "file", SourceDigest: "occupied", TargetType: "file"},
		{ID: "source", SourceType: "absent", TargetType: "symlink", ResolvedTarget: "/repo/source"},
		{ID: "stale", SourceType: "file", SourceDigest: "old", TargetType: "symlink", ResolvedTarget: "/repo/stale"},
	}
	summary, findings := BuildLinks(desired, observed, true)
	if summary.Satisfied != 1 || summary.Missing != 1 || summary.Drifted != 4 ||
		summary.NotChecked != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	statuses := []model.LinkFindingStatus{
		model.LinkFindingSatisfied,
		model.LinkFindingMissing,
		model.LinkFindingWrongTarget,
		model.LinkFindingOccupied,
		model.LinkFindingSourceAbsent,
		model.LinkFindingSourceDrift,
	}
	for index, status := range statuses {
		if findings[index].Status != status {
			t.Fatalf("finding %d = %#v, want %s", index, findings[index], status)
		}
	}
}

func TestBuildLinksMarksUncollectedInventory(t *testing.T) {
	summary, findings := BuildLinks(
		[]model.LinkSpec{{ID: "example"}}, nil, false,
	)
	if summary.NotChecked != 1 ||
		findings[0].Status != model.LinkFindingNotChecked {
		t.Fatalf("summary = %#v findings = %#v", summary, findings)
	}
}
