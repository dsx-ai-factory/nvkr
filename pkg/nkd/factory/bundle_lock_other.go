//go:build !linux && !darwin

package factory

import (
	"context"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func acquireBundleLock(context.Context, string) (func(), error) {
	return nil, e.New(e.CodeEngineCapability, "factory bundle locking is supported only on Linux and Darwin")
}
