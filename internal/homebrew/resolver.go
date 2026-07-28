package homebrew

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type Resolver struct {
	runner Runner
}

func NewResolver(runner Runner) Resolver {
	return Resolver{runner: runner}
}

func (r Resolver) Resolve(ctx context.Context, wanted model.PackageSpec) (model.PackageSpec, error) {
	raw, err := r.runner.Output(ctx, "brew", "info", "--json=v2", wanted.Package)
	if err != nil {
		return wanted, fmt.Errorf("resolve Homebrew package %q: %w", wanted.Package, err)
	}

	var info brewInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return wanted, fmt.Errorf("decode Homebrew metadata for %q: %w", wanted.Package, err)
	}

	type identity struct {
		kind   model.PackageKind
		source string
	}
	var candidates []identity
	for _, item := range info.Formulae {
		if wanted.Kind != "" && wanted.Kind != model.KindUnknown && wanted.Kind != model.KindFormula {
			continue
		}
		if wanted.Source != "" && item.Tap != wanted.Source {
			continue
		}
		candidates = append(candidates, identity{kind: model.KindFormula, source: item.Tap})
	}
	for _, item := range info.Casks {
		if wanted.Kind != "" && wanted.Kind != model.KindUnknown && wanted.Kind != model.KindCask {
			continue
		}
		if wanted.Source != "" && item.Tap != wanted.Source {
			continue
		}
		candidates = append(candidates, identity{kind: model.KindCask, source: item.Tap})
	}

	if len(candidates) == 0 {
		return wanted, fmt.Errorf("Homebrew has no matching formula or cask for %q", wanted.Package)
	}
	if len(candidates) > 1 {
		return wanted, fmt.Errorf(
			"Homebrew package %q is ambiguous across formulae or casks",
			wanted.Package,
		)
	}

	wanted.Kind = candidates[0].kind
	wanted.Source = candidates[0].source
	return wanted, nil
}
