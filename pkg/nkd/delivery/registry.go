package delivery

import (
	"context"
	"os"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// RegistryOptions selects transport only. PlainHTTP is deliberately explicit and
// intended for a loopback development registry; immutable digest references remain
// mandatory at the consumer boundary.
type RegistryOptions struct {
	PlainHTTP bool
	Anonymous bool
	Username  string
	Password  string
}

// Push publishes a complete verified OCI layout to a Distribution-compatible
// repository and returns the digest-pinned bootstrap reference. reference must be
// an untagged repository for digest-only publication, or an explicit discovery tag.
func Push(ctx context.Context, layoutPath, reference string, options RegistryOptions) (string, error) {
	if err := deliveryContext(ctx); err != nil {
		return "", err
	}
	if err := validateRegistryOptions(options); err != nil {
		return "", err
	}
	if _, err := Inspect(ctx, layoutPath); err != nil {
		return "", err
	}
	parseOptions := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parseOptions = append(parseOptions, name.Insecure)
	}
	var ref name.Reference
	var err error
	digestOnly := !strings.ContainsAny(reference[strings.LastIndex(reference, "/")+1:], ":@")
	if digestOnly {
		var repo name.Repository
		repo, err = name.NewRepository(reference, parseOptions...)
		if err == nil && repo.Name() != reference {
			return "", e.New(e.CodeUsage, "publication repository must be canonical")
		}
		ref = repo.Tag("unused")
	} else {
		ref, err = name.NewTag(reference, parseOptions...)
	}
	if err != nil {
		return "", e.Wrap(e.CodeUsage, err, "parsing OCI publication tag %q", reference)
	}
	path, err := layout.FromPath(layoutPath)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "opening OCI publication layout")
	}
	index, err := path.ImageIndex()
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "opening OCI publication index")
	}
	digest, err := index.Digest()
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "digesting OCI publication index")
	}
	if digestOnly {
		ref = ref.Context().Digest(digest.String())
	}
	remoteOptions := registryOptions(ctx, options)
	if err := remote.WriteIndex(ref, index, remoteOptions...); err != nil {
		if ctx.Err() != nil {
			return "", e.Wrap(e.CodeTimeout, ctx.Err(), "publishing OCI release root")
		}
		return "", e.Wrap(e.CodeUnavailable, err, "publishing OCI release root to %s", ref.Name())
	}
	return ref.Context().Name() + "@" + digest.String(), nil
}

// Pull accepts only a digest-pinned reference, materializes its complete recursive
// OCI closure, and validates the NKD publication root before making out visible.
func Pull(ctx context.Context, reference, out string, options RegistryOptions) (Inspection, error) {
	if err := deliveryContext(ctx); err != nil {
		return Inspection{}, err
	}
	if err := validateRegistryOptions(options); err != nil {
		return Inspection{}, err
	}
	if strings.Count(reference, "@") != 1 {
		return Inspection{}, e.New(e.CodeUnpinnedSource, "publication reference must be repository@sha256:digest")
	}
	parseOptions := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parseOptions = append(parseOptions, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parseOptions...)
	if err != nil {
		return Inspection{}, e.Wrap(e.CodeUnpinnedSource, err, "parsing digest-pinned publication reference")
	}
	remoteOptions := registryOptions(ctx, options)
	index, err := remote.Index(ref, remoteOptions...)
	if err != nil {
		if ctx.Err() != nil {
			return Inspection{}, e.Wrap(e.CodeTimeout, ctx.Err(), "pulling OCI release root")
		}
		return Inspection{}, e.Wrap(e.CodeUnavailable, err, "pulling OCI release root %s", reference)
	}
	digest, err := index.Digest()
	if err != nil || digest.String() != ref.DigestStr() {
		return Inspection{}, e.New(e.CodeVerifyFailed, "registry publication root does not match requested digest")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return Inspection{}, err
	}
	defer cleanup()
	if _, err := layout.Write(stage, index); err != nil {
		if ctx.Err() != nil {
			return Inspection{}, e.Wrap(e.CodeTimeout, ctx.Err(), "materializing OCI release root")
		}
		return Inspection{}, e.Wrap(e.CodeUnavailable, err, "materializing OCI release root")
	}
	inspection, err := Inspect(ctx, stage)
	if err != nil {
		return Inspection{}, err
	}
	if inspection.RootDigest != ref.DigestStr() {
		return Inspection{}, e.New(e.CodeVerifyFailed, "materialized publication root digest does not match requested reference")
	}
	if err := commit(); err != nil {
		return Inspection{}, err
	}
	inspection, err = Inspect(ctx, out)
	if err != nil {
		_ = os.RemoveAll(out)
		return Inspection{}, err
	}
	return inspection, nil
}

// ResolveDigest asks the registry what it actually serves for a digest-pinned
// reference and returns the digest of the exact manifest bytes it returned. It
// materializes nothing: it is the "is this really published, and are these really
// the bytes?" question a verification step must ask before it treats a signing
// record as being about a real publication.
//
// The returned digest is recomputed from the served manifest bytes rather than
// read from the registry's own headers or from the requested reference, so a
// registry that serves different bytes under the requested digest fails closed.
func ResolveDigest(ctx context.Context, reference string, options RegistryOptions) (string, error) {
	if err := deliveryContext(ctx); err != nil {
		return "", err
	}
	if err := validateRegistryOptions(options); err != nil {
		return "", err
	}
	if strings.Count(reference, "@") != 1 {
		return "", e.New(e.CodeUnpinnedSource, "publication reference must be repository@sha256:digest")
	}
	parseOptions := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parseOptions = append(parseOptions, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parseOptions...)
	if err != nil {
		return "", e.Wrap(e.CodeUnpinnedSource, err, "parsing digest-pinned publication reference")
	}
	descriptor, err := remote.Get(ref, registryOptions(ctx, options)...)
	if err != nil {
		if ctx.Err() != nil {
			return "", e.Wrap(e.CodeTimeout, ctx.Err(), "resolving published reference")
		}
		return "", e.Wrap(e.CodeUnavailable, err, "resolving published reference %s", reference)
	}
	served := digestBytes(descriptor.Manifest)
	if served != ref.DigestStr() || served != descriptor.Digest.String() {
		return "", e.New(e.CodeVerifyFailed,
			"registry served %s for %s; the published bytes do not match the requested digest", served, reference)
	}
	return served, nil
}

// registryOptions is the ONE place transport credentials become remote options,
// so anonymous, explicit, and ambient-keychain access cannot drift between the
// push, pull, and resolve paths.
func registryOptions(ctx context.Context, options RegistryOptions) []remote.Option {
	remoteOptions := []remote.Option{remote.WithContext(ctx), remote.WithUserAgent("nkdctl/delivery")}
	switch {
	case options.Anonymous:
		return append(remoteOptions, remote.WithAuth(authn.Anonymous))
	case options.Password != "":
		return append(remoteOptions, remote.WithAuth(authn.FromConfig(authn.AuthConfig{
			Username: options.Username, Password: options.Password,
		})))
	default:
		return append(remoteOptions, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	}
}

func validateRegistryOptions(options RegistryOptions) error {
	if options.Anonymous && (options.Username != "" || options.Password != "") {
		return e.New(e.CodeUsage, "anonymous registry access cannot carry explicit credentials")
	}
	if (options.Username == "") != (options.Password == "") {
		return e.New(e.CodeUsage, "explicit registry authentication requires both username and password")
	}
	return nil
}
