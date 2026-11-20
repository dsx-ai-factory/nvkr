// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/httptrust"
	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
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
	// IsolatedTransport selects a fresh standard TLS transport rather than the
	// process-mutable upstream default. It does not alter registry credentials.
	IsolatedTransport bool
	// CertificateAuthority optionally selects exact PEM trust for the isolated
	// route. Default, non-isolated registry behavior is unchanged.
	CertificateAuthority string
	// Authority restricts authenticated traffic to the selected registry and
	// its token endpoint. It is descriptive and grants no provider authority.
	Authority *RegistryAuthority
}

type RegistryAuthority struct {
	RegistryHost  string
	TokenEndpoint string
	Repositories  []string
}

func (authority *RegistryAuthority) enabled() bool {
	return authority != nil
}

// Push publishes a complete verified OCI layout to a Distribution-compatible
// repository and returns the digest-pinned bootstrap reference. reference must be
// an untagged repository for digest-only publication, or an explicit discovery tag.
func Push(ctx context.Context, layoutPath, reference string, options RegistryOptions) (string, error) {
	return PushWithOptions(ctx, layoutPath, reference, PushOptions{Registry: options})
}

// PushWithOptions publishes immutable release bytes before optionally advancing
// explicit image discovery tags. Tags do not establish signer or release authority.
func PushWithOptions(ctx context.Context, layoutPath, reference string, request PushOptions) (published string, resultErr error) {
	ctx, observer := observeRegistry(ctx)
	publication := &registryPublicationProgress{}
	if ctx != nil {
		ctx = context.WithValue(ctx, registryPublicationProgressKey{}, publication)
	}
	defer func(observedCtx context.Context) {
		resultErr = observer.explain(observedCtx, resultErr)
		if publication.writeAttempted.Load() {
			resultErr = uncertainRegistryPublication(resultErr)
		}
	}(ctx)
	options := request.Registry
	if err := deliveryContext(ctx); err != nil {
		return "", err
	}
	ctx, cancel := registryPublicationContext(ctx, 30*time.Minute)
	defer cancel()
	// PushOptions.Validate includes the identical registry credential validation,
	// followed by tag admission, before Inspect or any registry operation.
	if err := request.Validate(); err != nil {
		return "", err
	}
	// An isolated caller must refuse an insecure endpoint before opening any
	// publication bytes. Keep the established ordering of ordinary callers.
	if options.IsolatedTransport {
		repo, err := name.NewRepository(reference, name.StrictValidation)
		if err != nil {
			tag, tagErr := name.NewTag(reference, name.StrictValidation)
			if tagErr != nil {
				return "", e.New(e.CodeUsage, "isolated registry transport requires an HTTPS reference")
			}
			repo = tag.Context()
		}
		if repo.Scheme() != "https" || (options.Authority.enabled() && repo.RegistryStr() != options.Authority.RegistryHost) {
			return "", e.New(e.CodeUsage, "isolated registry transport requires an HTTPS reference")
		}
	}
	inspection, err := Inspect(ctx, layoutPath)
	if err != nil {
		return "", err
	}
	if request.ExpectedRootDigest != "" && inspection.RootDigest != request.ExpectedRootDigest {
		return "", e.New(e.CodeVerifyFailed, "publication root changed after metadata admission")
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
	if options.IsolatedTransport && (ref.Context().Scheme() != "https" || (options.Authority.enabled() && ref.Context().RegistryStr() != options.Authority.RegistryHost)) {
		return "", e.New(e.CodeUsage, "isolated registry transport requires an HTTPS reference")
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
	if digest.String() != inspection.RootDigest {
		return "", e.New(e.CodeVerifyFailed, "publication index changed after membership inspection")
	}
	if digestOnly {
		ref = ref.Context().Digest(digest.String())
	}
	remoteOptions := registryOptions(ctx, options)
	references, err := publicationReferences(ref, inspection.Root, options.PlainHTTP)
	if err != nil {
		return "", err
	}
	aliases, err := prepareImageAliases(inspection.Root, references, request.ImageTags)
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
		if object.Output != nil && object.Output.ImageIndex != nil {
			if err := pushRegistryImageIndex(ctx, layoutPath, object, child, remoteOptions); err != nil {
				return "", err
			}
			continue
		}
		if object.Output != nil && object.Output.Format == "disk-image" {
			if err := pushRegistryDisk(ctx, layoutPath, object, child, options); err != nil {
				return "", err
			}
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
		childOptions := remoteOptions
		if object.Evidence != nil {
			childOptions = registryOptions(context.WithValue(ctx, registryAttachmentKey{}, true), options)
		}
		if err := remote.Write(child, img, childOptions...); err != nil {
			return "", registryPublishError(ctx, err, "publishing deliverable %q", object.Name)
		}
		if isHelm {
			if err := tagHelmChart(ctx, img, discoveryTag, contentTag, object.Name, remoteOptions); err != nil {
				return "", registryPublishError(ctx, err, "publishing chart discovery tags for %q", object.Name)
			}
		}
		if _, err := ResolveDigest(ctx, child.Name(), options); err != nil {
			return "", registryReadbackError(ctx, err, "reading back deliverable %q", object.Name)
		}
	}
	if err := remote.WriteIndex(ref, index, remoteOptions...); err != nil {
		return "", registryPublishError(ctx, err, "publishing OCI release root")
	}
	pinned := ref.Context().Name() + "@" + digest.String()
	if err := publishImageAliases(ctx, pinned, aliases, options); err != nil {
		return "", fmt.Errorf("image alias publication failed after root upload at %s (readback acceptance is not implied): %w", pinned,
			registryReadbackError(ctx, err, "publishing image aliases"))
	}
	return pinned, nil
}

// Pull accepts only a digest-pinned reference, materializes its complete recursive
// OCI closure, and validates the NVKR publication root before making out visible.
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
	if options.IsolatedTransport && (ref.Context().Scheme() != "https" || (options.Authority.enabled() && ref.Context().RegistryStr() != options.Authority.RegistryHost)) {
		return Inspection{}, e.New(e.CodeUsage, "isolated registry transport requires an HTTPS reference")
	}
	return pullWithRecovery(ctx, out, func(attemptCtx context.Context, stage string) (Inspection, error) {
		return pullToStage(attemptCtx, ref, stage, options)
	})
}

// pullToStage only acquires and authenticates bytes in private staging. Commit
// and post-commit inspection belong to the recovery owner and are never retried.
func pullToStage(ctx context.Context, ref name.Digest, stage string, options RegistryOptions) (Inspection, error) {
	remoteOptions := registryOptions(ctx, options)
	index, err := remote.Index(ref, remoteOptions...)
	if err != nil {
		if ctx.Err() != nil {
			return Inspection{}, e.Wrap(e.CodeTimeout, ctx.Err(), "pulling OCI release root")
		}
		return Inspection{}, e.Wrap(e.CodeUnavailable, err, "pulling OCI release root %s", ref.Name())
	}
	digest, err := index.Digest()
	if err != nil || digest.String() != ref.DigestStr() {
		return Inspection{}, e.New(e.CodeVerifyFailed, "registry publication root does not match requested digest")
	}
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
			if object.Output != nil && object.Output.ImageIndex != nil {
				if err := validateImageIndexObject(object); err != nil {
					return err
				}
				index, err := fetchRegistryImageIndex(ctx, child, object.Size, object.Output.ImageIndex.RequiredPlatforms, remoteOptions)
				if err != nil {
					return err
				}
				importErr := materializeRegistryImageIndex(ctx, stage, object, index)
				closeErr := index.Close()
				if err := registryImageImportResult(ctx, importErr, closeErr); err != nil {
					return err
				}
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
			if err := writePulledImage(ctx, stage, object, img); err != nil {
				var coded *e.Error
				if errors.As(err, &coded) {
					return err
				}
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
// permanent catalog error. Authentication/authorization refusals take
// precedence and use CodeRegistryAuthFailed, including structured denials.
func helmTagPublishError(err error, tag name.Tag, chart string) error {
	var transportErr *transport.Error
	if errors.As(err, &transportErr) && registryResponseCode(transportErr) == e.CodeRegistryAuthFailed {
		return e.Wrap(e.CodeRegistryAuthFailed, err, "registry refused Helm chart tag %q for %q", tag.TagStr(), chart)
	}
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
	remoteOptions := []remote.Option{remote.WithContext(ctx), remote.WithUserAgent("nvkr/delivery")}
	progress, _ := ctx.Value(registryPublicationProgressKey{}).(*registryPublicationProgress)
	base := readbudget.Transport(ctx, registryTransportWithProgress(ctx, options, progress))
	if observer, ok := ctx.Value(registryObserverKey{}).(*registryObserver); ok {
		base = observer.transport(base)
	}
	remoteOptions = append(remoteOptions, remote.WithTransport(base))
	switch {
	case options.Anonymous:
		return append(remoteOptions, remote.WithAuth(authn.Anonymous))
	case options.Password != "":
		return append(remoteOptions, remote.WithAuth(authn.FromConfig(authn.AuthConfig{
			Username: options.Username, Password: options.Password,
		})))
	default:
		return append(remoteOptions, remote.WithAuthFromKeychain(options.AmbientKeychain()))
	}
}

// All remote writes in one Push share this operation-phase observation. A
// later local integrity failure must retain uncertainty from earlier uploads.
type registryPublicationProgressKey struct{}
type registryPublicationProgress struct{ writeAttempted atomic.Bool }
type registryPublicationProgressTransport struct {
	base     http.RoundTripper
	progress *registryPublicationProgress
}

func (tracked registryPublicationProgressTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.Method {
	case http.MethodPost, http.MethodPatch, http.MethodPut:
		tracked.progress.writeAttempted.Store(true)
	}
	return tracked.base.RoundTrip(request)
}

func registryWriteAttempted(ctx context.Context) bool {
	progress, ok := ctx.Value(registryPublicationProgressKey{}).(*registryPublicationProgress)
	return ok && progress.writeAttempted.Load()
}

func registryTransport(ctx context.Context, options RegistryOptions) http.RoundTripper {
	return registryTransportWithProgress(ctx, options, nil)
}

func registryTransportWithProgress(ctx context.Context, options RegistryOptions, progress *registryPublicationProgress) http.RoundTripper {
	base := remote.DefaultTransport
	if options.IsolatedTransport {
		isolated, err := httptrust.NewTransport(ctx, options.CertificateAuthority)
		if err != nil {
			return httpsRegistryTransport{admissionErr: err}
		}
		isolated.DisableKeepAlives = true
		isolated.MaxIdleConnsPerHost = 50
		base = isolated
	}
	if progress != nil {
		// Observe only a request admitted to the network transport. Wrapping
		// outside HTTPS admission would count a locally refused POST as a write.
		base = registryPublicationProgressTransport{base: base, progress: progress}
	}
	if options.IsolatedTransport {
		return newHTTPSRegistryTransport(base, options.Authority)
	}
	return base
}

// Enforce HTTPS for every request, including redirects followed by net/http.
type httpsRegistryTransport struct {
	base               http.RoundTripper
	authority          *RegistryAuthority
	tokenEndpoint      *url.URL
	repositoryPrefixes []string
	allowedScopes      map[string]bool
	admissionErr       error
}

// Freeze and parse the caller's scope once. Request processing only consults
// this immutable snapshot, including when the HTTP client follows redirects.
func newHTTPSRegistryTransport(base http.RoundTripper, authority *RegistryAuthority) httpsRegistryTransport {
	scoped := httpsRegistryTransport{base: base}
	if authority == nil {
		return scoped
	}
	copy := *authority
	copy.Repositories = slices.Clone(authority.Repositories)
	scoped.authority = &copy
	endpoint, err := url.Parse(copy.TokenEndpoint)
	if err != nil {
		scoped.admissionErr = e.New(e.CodeTrustPolicyMissing, "registry token endpoint is invalid")
		return scoped
	}
	scoped.tokenEndpoint = endpoint
	scoped.allowedScopes = make(map[string]bool, 2*len(copy.Repositories))
	for _, selected := range copy.Repositories {
		repo, err := name.NewRepository(selected, name.StrictValidation)
		if err != nil || repo.RegistryStr() != copy.RegistryHost {
			scoped.admissionErr = e.New(e.CodeTrustPolicyMissing, "registry scope is invalid")
			return scoped
		}
		for _, endpoint := range []string{"blobs", "manifests", "referrers", "tags"} {
			scoped.repositoryPrefixes = append(scoped.repositoryPrefixes, "/v2/"+repo.RepositoryStr()+"/"+endpoint+"/")
		}
		scoped.allowedScopes[repo.Scope(transport.PullScope)] = true
		scoped.allowedScopes[repo.Scope(transport.PushScope)] = true
	}
	return scoped
}

func (scoped httpsRegistryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// RoundTripper owns Body even when admission refuses before the base is called.
	refuse := func(err error) (*http.Response, error) {
		if request.Body != nil {
			_ = request.Body.Close()
		}
		return nil, err
	}
	if request.URL == nil || request.URL.Scheme != "https" {
		return refuse(e.New(e.CodeUsage, "isolated registry transport requires HTTPS"))
	}
	if scoped.admissionErr != nil {
		return refuse(scoped.admissionErr)
	}
	if scoped.authority.enabled() {
		// Check redirect history before ordinary registry admission: delegated
		// storage cannot redirect back into the registry's credential authority.
		if request.Response != nil && request.Response.Request != nil {
			source := request.Response.Request.URL
			if source == nil || source.Host != scoped.authority.RegistryHost {
				return refuse(e.New(e.CodeTrustPolicyMissing, "registry storage delegation permits only one redirect"))
			}
		}
		if scoped.blobStorageRedirect(request) {
			// net/http can preserve sensitive headers across a redirect to the
			// same hostname on another port. Storage receives no credentials,
			// regardless of that upstream forwarding policy.
			storage := request.Clone(request.Context())
			for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie", "PRIVATE-TOKEN", "JOB-TOKEN"} {
				storage.Header.Del(header)
			}
			return scoped.base.RoundTrip(storage)
		}
		if request.URL.User != nil || request.URL.Fragment != "" || request.URL.RawPath != "" || (request.Host != "" && request.Host != request.URL.Host) {
			return refuse(e.New(e.CodeTrustPolicyMissing, "registry request authority is invalid"))
		}
		clean := path.Clean(request.URL.Path)
		if request.URL.Path != clean && request.URL.Path != clean+"/" {
			return refuse(e.New(e.CodeTrustPolicyMissing, "registry request path is not canonical"))
		}
		auth := request.Header.Get("Authorization")
		registry := request.URL.Host == scoped.authority.RegistryHost && request.URL.Path == "/v2/"
		for _, prefix := range scoped.repositoryPrefixes {
			if request.URL.Host == scoped.authority.RegistryHost && registryEndpointMatches(request.URL.Path, prefix) {
				registry = true
			}
		}
		token := request.URL.Host == scoped.tokenEndpoint.Host && request.URL.Path == scoped.tokenEndpoint.Path
		if (registry && auth != "" && !strings.HasPrefix(auth, "Bearer ")) || (token && auth != "" && !strings.HasPrefix(auth, "Basic ")) || (!registry && !token) {
			return refuse(e.New(e.CodeTrustPolicyMissing, "registry request is outside its admitted authority"))
		}
		if registry && request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodPost && request.Method != http.MethodPatch && request.Method != http.MethodPut {
			return refuse(e.New(e.CodeTrustPolicyMissing, "registry request method is outside its admitted authority"))
		}
		if token {
			query, err := url.ParseQuery(request.URL.RawQuery)
			if err != nil || request.Method != http.MethodGet || len(query["service"]) != 1 || query.Get("service") != "container_registry" || len(query["scope"]) == 0 || len(query["scope"]) > len(scoped.allowedScopes) {
				return refuse(e.New(e.CodeTrustPolicyMissing, "registry token request exceeds its admitted scope"))
			}
			for _, scope := range query["scope"] {
				if !scoped.allowedScopes[scope] {
					return refuse(e.New(e.CodeTrustPolicyMissing, "registry token request exceeds its admitted scope"))
				}
			}
		}
	}
	return scoped.base.RoundTrip(request)
}

// Match the selected repository's endpoint shape, not a descendant repository
// that happens to start with a reserved endpoint name. Upload sessions are the
// sole two-segment suffix used by the pinned Distribution client.
func registryEndpointMatches(requestPath, prefix string) bool {
	suffix, ok := strings.CutPrefix(requestPath, prefix)
	if !ok {
		return false
	}
	switch {
	case strings.HasSuffix(prefix, "/tags/"):
		return suffix == "list"
	case strings.HasSuffix(prefix, "/blobs/") && strings.HasPrefix(suffix, "uploads/"):
		return !strings.Contains(strings.TrimPrefix(suffix, "uploads/"), "/")
	default:
		return suffix != "" && !strings.Contains(suffix, "/")
	}
}

// blobStorageRedirect admits one HTTPS delegation from a selected registry's
// exact digest-addressed blob GET/HEAD. net/http supplies Response only while
// following a redirect; use its original request rather than widening registry
// authority to arbitrary anonymous URLs. The normal readback owner still checks
// every blob's size and digest before accepting a publication.
func (scoped httpsRegistryTransport) blobStorageRedirect(request *http.Request) bool {
	if request.URL.Host == scoped.authority.RegistryHost || request.URL.User != nil || request.URL.Host == "" || request.URL.Fragment != "" || (request.Host != "" && request.Host != request.URL.Host) ||
		(request.Method != http.MethodGet && request.Method != http.MethodHead) || request.Response == nil || request.Response.Request == nil {
		return false
	}
	response := request.Response
	switch response.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return false
	}
	source := response.Request
	if source.URL == nil || source.URL.Scheme != "https" || source.URL.Host != scoped.authority.RegistryHost || source.URL.User != nil || source.URL.RawPath != "" || source.URL.Fragment != "" ||
		(source.Method != http.MethodGet && source.Method != http.MethodHead) || (source.Host != "" && source.Host != source.URL.Host) {
		return false
	}
	location, err := response.Location()
	if err != nil || location.String() != request.URL.String() {
		return false
	}
	for _, prefix := range scoped.repositoryPrefixes {
		if !strings.HasSuffix(prefix, "/blobs/") {
			continue
		}
		if digest, ok := strings.CutPrefix(source.URL.Path, prefix); ok {
			_, err := v1.NewHash(digest)
			return err == nil
		}
	}
	return false
}

// AmbientKeychain returns the keychain these options consult, or nil when access
// is anonymous or explicit. Other registry readers use it so ambient access
// cannot drift from delivery; explicit secrets are never widened through it.
func (options RegistryOptions) AmbientKeychain() authn.Keychain {
	if options.Anonymous || options.Password != "" {
		return nil
	}
	return authn.DefaultKeychain
}

// Validate admits transport credentials before filesystem or network work.
func (options RegistryOptions) Validate() error {
	if options.IsolatedTransport && options.PlainHTTP {
		return e.New(e.CodeUsage, "isolated registry transport requires HTTPS")
	}
	if options.CertificateAuthority != "" && !options.IsolatedTransport {
		return e.New(e.CodeUsage, "explicit registry certificate authority requires isolated transport")
	}
	if options.IsolatedTransport {
		if err := httptrust.ValidateCertificateAuthority(options.CertificateAuthority); err != nil {
			return err
		}
		if options.CertificateAuthority == "" && (os.Getenv("SSL_CERT_FILE") != "" || os.Getenv("SSL_CERT_DIR") != "") {
			return e.New(e.CodeTrustPolicyMissing, "isolated registry certificate overrides require an explicit certificate authority")
		}
	}
	if options.Authority.enabled() {
		registry, registryErr := name.NewRegistry(options.Authority.RegistryHost, name.StrictValidation)
		endpoint, endpointErr := url.Parse(options.Authority.TokenEndpoint)
		if !options.IsolatedTransport || registryErr != nil || registry.RegistryStr() != options.Authority.RegistryHost || endpointErr != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "/jwt/auth" || endpoint.RawPath != "" || len(options.Authority.Repositories) == 0 || len(options.Authority.Repositories) > maxDeliveryObjects+1 {
			return e.New(e.CodeUsage, "registry authority must select an isolated HTTPS registry and exact token endpoint")
		}
		seen := map[string]bool{}
		for _, selected := range options.Authority.Repositories {
			repo, err := name.NewRepository(selected, name.StrictValidation)
			if err != nil || repo.Name() != selected || repo.RegistryStr() != registry.RegistryStr() || seen[selected] {
				return e.New(e.CodeUsage, "registry authority contains an invalid repository")
			}
			seen[selected] = true
		}
	}
	if options.Anonymous && (options.Username != "" || options.Password != "") {
		return e.New(e.CodeUsage, "anonymous registry access cannot carry explicit credentials")
	}
	if (options.Username == "") != (options.Password == "") {
		return e.New(e.CodeUsage, "explicit registry authentication requires both username and password")
	}
	return nil
}

func validateRegistryOptions(options RegistryOptions) error { return options.Validate() }

// Only sibling repositories in the caller-selected namespace, or retained
// earlier attempts of that same development run, are admitted.
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
		outputParent, err := publicationOutputParent(root, object.Output.Publication, parent)
		if err != nil {
			return nil, err
		}
		target := outputParent + object.Output.Repository
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

// outputImage supplies nondisk outputs to the standard OCI layout reader with a temporary one-image
// index. It copies held, hash-checked blobs; neither the publication index nor
// registry storage gains an extra wrapper or duplicate deliverable.
func outputImage(ctx context.Context, root string, object PublishedObject) (v1.Image, func(), error) {
	noop := func() {}
	stage, err := os.MkdirTemp("", "nvkr-output-push-*")
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

// Keep import classification primary while retaining a simultaneous cleanup failure.
func registryImageImportResult(ctx context.Context, importErr, closeErr error) error {
	if closeErr != nil {
		closeErr = classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, closeErr, "closing registry image index"))
	}
	return errors.Join(importErr, closeErr)
}
