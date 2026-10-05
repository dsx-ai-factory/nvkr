package lifecycle

import (
	"context"
	"path/filepath"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sign"
)

// InspectionOptions chooses a local closure or registry materialization.
// Inspection establishes byte integrity and membership, not signature or policy
// verification. Zero Timeout selects a bounded ten-minute inspection.
type InspectionOptions struct {
	LayoutPath    string
	WorkspaceRoot string
	Registry      delivery.RegistryOptions
	Timeout       time.Duration
}

// InspectRelease checks the entire publication before returning a detached view.
// Every named output and evidence binding is checked, even when a caller later
// displays only one output. Temporary registry materialization is cleaned up.
func InspectRelease(ctx context.Context, reference string, options InspectionOptions) (delivery.PublicationDescription, error) {
	if ctx == nil {
		return delivery.PublicationDescription{}, e.New(e.CodeUsage, "release inspection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return delivery.PublicationDescription{}, e.Wrap(e.CodeTimeout, err, "release inspection canceled")
	}
	if options.Timeout < 0 {
		return delivery.PublicationDescription{}, e.New(e.CodeUsage, "release inspection timeout must be positive")
	}
	if err := options.Registry.Validate(); err != nil {
		return delivery.PublicationDescription{}, err
	}
	if options.LayoutPath != "" && (options.WorkspaceRoot != "" || options.Registry != (delivery.RegistryOptions{})) {
		return delivery.PublicationDescription{}, e.New(e.CodeUsage, "local release inspection cannot use registry or workspace options")
	}
	subject := sign.Subject{Ref: reference, Digest: referenceDigest(reference)}
	if err := subject.Validate(); err != nil {
		return delivery.PublicationDescription{}, err
	}
	if err := delivery.ValidatePublicationReference(reference); err != nil {
		return delivery.PublicationDescription{}, err
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var publication *delivery.Publication
	var err error
	if options.LayoutPath != "" {
		publication, err = delivery.OpenPublication(ctx, options.LayoutPath, subject.Digest)
	} else {
		workspace := options.WorkspaceRoot
		if workspace == "" {
			workspace = filepath.Join(".nkd", "inspected-releases")
		}
		publication, err = delivery.PullPublication(ctx, reference, workspace, options.Registry)
	}
	if err != nil {
		if ctx.Err() != nil {
			return delivery.PublicationDescription{}, e.Wrap(e.CodeTimeout, ctx.Err(), "release inspection canceled")
		}
		return delivery.PublicationDescription{}, err
	}
	defer func() { _ = publication.Close() }()
	return publication.Description(reference)
}
