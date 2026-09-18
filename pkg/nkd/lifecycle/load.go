package lifecycle

import (
	"context"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
)

func LoadDefinition(ctx context.Context, path string) (Definition, error) {
	return definition.LoadDefinition(ctx, path)
}
func LoadEnvironment(ctx context.Context, path string) (Environment, error) {
	return definition.LoadEnvironment(ctx, path)
}
