package portablelink

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/VeniVidiVici/envctl/internal/contentdigest"
	"github.com/VeniVidiVici/envctl/internal/model"
)

func Collect(specs []model.LinkSpec) []model.LinkObservation {
	observations := make([]model.LinkObservation, 0, len(specs))
	for _, spec := range specs {
		observation := model.LinkObservation{
			ID:     spec.ID,
			Source: spec.Source,
			Target: spec.Target,
		}
		observation.SourceType, _, _ = inspect(spec.Source)
		var digest string
		var err error
		switch spec.Kind {
		case model.LinkKindFile:
			digest, err = contentdigest.File(spec.Source)
		case model.LinkKindDirectory:
			digest, err = contentdigest.Directory(spec.Source)
		default:
			err = os.ErrInvalid
		}
		if err != nil {
			observation.SourceType = "unreadable"
		} else {
			observation.SourceDigest = digest
		}
		observation.TargetType, observation.LinkTarget,
			observation.ResolvedTarget = inspect(spec.Target)
		observations = append(observations, observation)
	}
	sort.Slice(observations, func(i, j int) bool {
		return observations[i].ID < observations[j].ID
	})
	return observations
}

func inspect(path string) (kind, linkTarget, resolvedTarget string) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "absent", "", ""
	}
	if err != nil {
		return "unreadable", "", ""
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "symlink", "unreadable", ""
		}
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(path), resolved)
		}
		return "symlink", target, filepath.Clean(resolved)
	}
	if info.Mode().IsRegular() {
		return "file", "", ""
	}
	if info.IsDir() {
		return "directory", "", ""
	}
	return "other", "", ""
}
