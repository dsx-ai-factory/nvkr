package client

import (
	"context"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/release"
	"github.com/nhensley/nkdctl/pkg/nkd/transport"
)

// AuthorizeReleaseRequest carries only opaque verified receipt capabilities and the
// compatibility inputs consumed by release.Authorize. OutputName optionally commits
// the canonical authorization record through AtomicOutput.
type AuthorizeReleaseRequest struct {
	Authorization release.AuthorizationRequest
	OutputName    string
}

// AuthorizeRelease delegates the complete gate decision to the configured release
// authority; the facade does not reimplement completeness or compatibility policy.
func (c *Client) AuthorizeRelease(ctx context.Context, request AuthorizeReleaseRequest) (*release.Authorization, error) {
	if err := c.ready(ctx, "release authorization"); err != nil {
		return nil, err
	}
	if err := c.requireAtomicOutput(request.OutputName); err != nil {
		return nil, err
	}
	if c.releaseAuthority == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "release authority is not configured")
	}
	authorization, err := c.releaseAuthority.Authorize(ctx, request.Authorization)
	if err != nil {
		return nil, normalizeError(ctx, err, "release authorization")
	}
	if err := contextError(ctx, "release authorization"); err != nil {
		return nil, err
	}
	canonicalAuthorization, err := authorization.Marshal()
	if err != nil {
		return nil, err
	}
	if err := contextError(ctx, "release authorization"); err != nil {
		return nil, err
	}
	if request.OutputName != "" {
		if err := c.commit(ctx, request.OutputName, canonicalAuthorization); err != nil {
			return nil, err
		}
	}
	return authorization, nil
}

// PublishRequest binds local artifact metadata to an opaque release authorization and
// sends it through explicitly injected transport adapters.
type PublishRequest struct {
	Authorization *release.Authorization
	Artifacts     []transport.Artifact
	Targets       transport.Targets
}

// Publish delegates binding and publication to transport. It refuses transport's
// filesystem defaults so embedding applications must make every I/O dependency
// explicit. Adapter commits retain transport's atomic, no-overwrite contract.
func (c *Client) Publish(ctx context.Context, request PublishRequest) error {
	if err := c.ready(ctx, "artifact publication"); err != nil {
		return err
	}
	cleared, err := transport.BindClearedArtifactSet(request.Authorization, request.Artifacts)
	if err != nil {
		return err
	}
	if err := contextError(ctx, "artifact publication"); err != nil {
		return err
	}
	for _, artifact := range request.Artifacts {
		switch artifact.Kind {
		case "oci":
			if c.publication.OCI == nil {
				return e.New(e.CodeUsage, "OCI publication requires an injected OCIRegistry")
			}
		case "package":
			if c.publication.Package == nil {
				return e.New(e.CodeUsage, "package publication requires an injected PackageRepo")
			}
		case "object":
			if c.publication.Object == nil {
				return e.New(e.CodeUsage, "object publication requires an injected ObjectStore")
			}
		}
	}
	return normalizeError(ctx,
		transport.PublishCleared(ctx, c.publication, request.Targets, cleared),
		"artifact publication")
}
