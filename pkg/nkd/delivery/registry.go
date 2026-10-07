// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
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
	if err := options.Validate(); err != nil {
		return "", err
	}
	inspection, err := Inspect(ctx, layoutPath)
	if err != nil {
		return "", err
	}
	parseOptions := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parseOptions = append(parseOptions, name.Insecure)
	}
	var ref name.Reference
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
	references, err := publicationReferences(ref, inspection.Root, options.PlainHTTP)
	if err != nil {
		return "", err
	}
	// Publish payloads first; the release index becomes discoverable only after all
	// referenced manifests are uploaded and checked at their own immutable refs.
	objects := append([]PublishedObject(nil), inspection.Root.Objects...)
	sort.SliceStable(objects, func(i, j int) bool { return objects[i].Evidence == nil && objects[j].Evidence != nil })
	for _, object := range objects {
		child, ok := references[object.Name]
		if !ok {
			continue
		}
		img, cleanup, err := outputImage(ctx, layoutPath, object)
		if err != nil {
			return "", err
		}
		defer cleanup()
		isHelm := object.Output != nil && object.Output.Format == outputFormatHelm
		var discoveryTag, contentTag name.Tag
		if isHelm {
			// Derive and validate both Helm tags before the manifest write below:
			// helmChartTags reads only the already-built local img (no registry
			// I/O), so a chart whose derived tag can never be valid (e.g. an
			// over-length version) fails closed here instead of after an
			// otherwise-successful manifest publication leaves a chart reachable
			// only by digest.
			discoveryTag, contentTag, err = helmChartTags(child, img, parseOptions...)
			if err != nil {
				return "", err
			}
		}
		if err := remote.Write(child, img, remoteOptions...); err != nil {
			return "", e.Wrap(e.CodeUnavailable, err, "publishing deliverable %q", object.Name)
		}
		if isHelm {
			if err := tagHelmChart(ctx, img, discoveryTag, contentTag, object.Name, remoteOptions); err != nil {
				return "", err
			}
		}
		if _, err := ResolveDigest(ctx, child.Name(), options); err != nil {
			return "", err
		}
	}
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
	if err := options.Validate(); err != nil {
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
	inspection, err := inspectPublication(ctx, stage, func(publication PublicationRoot) error {
		references, err := publicationReferences(ref, publication, options.PlainHTTP)
		if err != nil {
			return err
		}
		for _, object := range publication.Objects {
			child, ok := references[object.Name]
			if !ok {
				continue
			}
			descriptor, err := remote.Get(child, remoteOptions...)
			if err != nil {
				return e.Wrap(e.CodeUnavailable, err, "fetching deliverable %q", object.Name)
			}
			if digestBytes(descriptor.Manifest) != object.DescriptorDigest || int64(len(descriptor.Manifest)) != object.Size || string(descriptor.MediaType) != publishedManifestMediaType(object) {
				return e.New(e.CodeVerifyFailed, "registry substituted deliverable %q", object.Name)
			}
			img, err := descriptor.Image()
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "opening delivered manifest")
			}
			if err := layout.Path(stage).WriteImage(img); err != nil {
				return e.Wrap(e.CodeUnavailable, err, "materializing deliverable %q", object.Name)
			}
		}
		return nil
	})
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
	if err := options.Validate(); err != nil {
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

// tagHelmChart pushes both the plain-version discovery tag and the digest-
// suffixed content tag for a published Helm chart manifest (see
// helmChartTags), after the manifest itself is already durably published at
// its digest (remote.Write, called by the caller before this). discovery and
// content are derived and validated by the caller before that write, so a
// chart whose tag can never be valid fails before any bytes are published.
func tagHelmChart(ctx context.Context, img v1.Image, discovery, content name.Tag, chart string, remoteOptions []remote.Option) error {
	manifestDigest, err := img.Digest()
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "digesting Helm chart manifest")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "tagging Helm chart %q", chart)
	}
	// Both tags are pure functions of the chart's own bytes and version, so a
	// rerun that finds either tag already resolved to this manifest's digest
	// is a no-op rather than a re-push; this keeps reruns working against a
	// registry that forbids overwriting an existing tag (e.g. ECR IMMUTABLE,
	// Harbor/Artifactory immutable-tag rules), which would otherwise reject
	// every rerun after the first. The discovery tag is still re-pushed (and
	// re-pointed) when it currently resolves to a different manifest, so a
	// later publication under the same nominal version still moves it,
	// matching normal mutable Helm publish semantics.
	if err := publishHelmTag(content, img, manifestDigest, chart, remoteOptions); err != nil {
		return err
	}
	if err := publishHelmTag(discovery, img, manifestDigest, chart, remoteOptions); err != nil {
		return err
	}
	return nil
}

// publishHelmTag skips the PUT when tag already resolves to manifestDigest.
// A definitive not-found Head means the tag must be written; any other Head
// failure (5xx, timeout, throttling) is inconclusive and reported as an
// availability error rather than risking a re-push into a registry that
// forbids overwriting an existing immutable tag because of a transient hiccup.
func publishHelmTag(tag name.Tag, img v1.Image, manifestDigest v1.Hash, chart string, remoteOptions []remote.Option) error {
	existing, headErr := remote.Head(tag, remoteOptions...)
	switch {
	case headErr == nil:
		if existing.Digest == manifestDigest {
			return nil
		}
	case isNotFoundError(headErr):
		// The tag does not exist yet; proceed to write it below.
	default:
		return e.Wrap(e.CodeUnavailable, headErr, "checking Helm chart tag %q for %q", tag.TagStr(), chart)
	}
	if err := remote.Tag(tag, img, remoteOptions...); err != nil {
		return helmTagPublishError(err, tag, chart)
	}
	return nil
}

// isNotFoundError reports whether err is a definitive registry 404, as
// opposed to a transient or ambiguous Head failure.
func isNotFoundError(err error) bool {
	var transportErr *transport.Error
	return errors.As(err, &transportErr) && transportErr.StatusCode == http.StatusNotFound
}

// helmTagPublishError distinguishes a permanent, registry-side rejection (a
// 4xx response, e.g. an OCI tag-grammar or policy violation the registry
// enforces beyond this package's own local name.NewTag validation) from a
// transient failure. A caller or retry policy that treats every tagging
// failure as CodeUnavailable would keep retrying a chart that can never
// succeed instead of surfacing a coded input error. 408 and 429 are
// transient in the pinned registry transport even though they are 4xx
// responses, so both are reported as CodeUnavailable rather than a
// permanent catalog error.
func helmTagPublishError(err error, tag name.Tag, chart string) error {
	var transportErr *transport.Error
	if errors.As(err, &transportErr) && transportErr.StatusCode >= 400 && transportErr.StatusCode < 500 &&
		transportErr.StatusCode != http.StatusRequestTimeout && transportErr.StatusCode != http.StatusTooManyRequests {
		return e.Wrap(e.CodeInvalidCatalog, err, "registry rejected Helm chart tag %q for %q", tag.TagStr(), chart)
	}
	return e.Wrap(e.CodeUnavailable, err, "tagging Helm chart %q", chart)
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

// Validate admits transport credentials before filesystem or network work.
func (options RegistryOptions) Validate() error {
	if options.Anonymous && (options.Username != "" || options.Password != "") {
		return e.New(e.CodeUsage, "anonymous registry access cannot carry explicit credentials")
	}
	if (options.Username == "") != (options.Password == "") {
		return e.New(e.CodeUsage, "explicit registry authentication requires both username and password")
	}
	return nil
}

// Only sibling repositories in the caller-selected namespace are admitted.
// Metadata cannot redirect registry credentials to another host or namespace.
func outputReferences(root name.Reference, publication PublicationRoot, insecure bool) (map[string]name.Digest, error) {
	result := map[string]name.Digest{}
	repository := root.Context().Name()
	parent := repository[:strings.LastIndexByte(repository, '/')+1]
	options := []name.Option{name.StrictValidation}
	if insecure {
		options = append(options, name.Insecure)
	}
	for _, object := range publication.Objects {
		if object.Output == nil {
			continue
		}
		if !siblingRepositoryPattern.MatchString(object.Output.Repository) {
			return nil, e.New(e.CodeVerifyFailed, "invalid output repository")
		}
		target := parent + object.Output.Repository
		if target == repository {
			return nil, e.New(e.CodeVerifyFailed, "deliverable repository overlaps release repository")
		}
		ref, err := name.NewDigest(target+"@"+object.DescriptorDigest, options...)
		if err != nil || ref.Context().Name() != target {
			return nil, e.New(e.CodeVerifyFailed, "noncanonical deliverable repository")
		}
		result[object.Name] = ref
	}
	return result, nil
}

// outputImage supplies the standard OCI layout reader with a temporary one-image
// index. It copies held, hash-checked blobs; neither the publication index nor
// registry storage gains an extra wrapper or duplicate deliverable.
func outputImage(ctx context.Context, root string, object PublishedObject) (v1.Image, func(), error) {
	noop := func() {}
	stage, err := os.MkdirTemp("", "nkd-output-push-*")
	if err != nil {
		return nil, noop, e.Wrap(e.CodeInternal, err, "creating output upload workspace")
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	descriptor := Descriptor{MediaType: publishedManifestMediaType(object), Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}
	writer := layoutWriter{root: stage}
	if err := writer.copyClosure(ctx, root, descriptor); err != nil {
		cleanup()
		return nil, noop, err
	}
	if _, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}}); err != nil {
		cleanup()
		return nil, noop, err
	}
	hash, err := v1.NewHash(object.DescriptorDigest)
	if err != nil {
		cleanup()
		return nil, noop, e.Wrap(e.CodeVerifyFailed, err, "parsing output digest")
	}
	img, err := layout.Path(stage).Image(hash)
	if err != nil {
		cleanup()
		return nil, noop, e.Wrap(e.CodeVerifyFailed, err, "opening release deliverable")
	}
	return img, cleanup, nil
}

// OutputReferences returns descriptive transport references from the release's
// exact sibling bindings. It authenticates no signature or release policy.
func OutputReferences(reference string, inspection Inspection) (map[string]string, error) {
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil || ref.DigestStr() != inspection.RootDigest {
		return nil, e.New(e.CodeUnpinnedSource, "output references require the exact release digest")
	}
	if err := inspection.Root.Validate(); err != nil {
		return nil, err
	}
	refs, err := outputReferences(ref, inspection.Root, false)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(refs))
	for output, ref := range refs {
		result[output] = ref.Name()
	}
	return result, nil
}
