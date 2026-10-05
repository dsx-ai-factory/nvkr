//go:build !linux && !darwin

package factory

import (
	"context"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func admitGitHubRecoveryPlatform() error {
	return e.New(e.CodeEngineCapability, "GitHub artifact recovery is supported only on Linux and Darwin")
}

func writeGitHubRestoreOutcome(context.Context, string, string) error {
	return admitGitHubRecoveryPlatform()
}
