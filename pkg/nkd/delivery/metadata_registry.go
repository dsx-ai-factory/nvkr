package delivery

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// PullMetadata fetches bounded manifests, configs and evidence, never output
// layers. Its result is an integrity description, not a verified publication.
func PullMetadata(ctx context.Context, reference, out string, options RegistryOptions) (Inspection, error) {
	if err := deliveryContext(ctx); err != nil {
		return Inspection{}, err
	}
	if err := options.Validate(); err != nil {
		return Inspection{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parse...)
	if err != nil {
		return Inspection{}, e.Wrap(e.CodeUnpinnedSource, err, "parse metadata reference")
	}
	remoteOptions := registryOptions(ctx, options)
	fetched, err := remote.Get(ref, remoteOptions...)
	if err != nil {
		return Inspection{}, classifyContext(ctx, e.Wrap(e.CodeFetchFailed, err, "fetch metadata index"))
	}
	if len(fetched.Manifest) > 8<<20 || digestBytes(fetched.Manifest) != ref.DigestStr() {
		return Inspection{}, e.New(e.CodeVerifyFailed, "metadata index identity/size differs")
	}
	var index ociIndex
	if err := json.Unmarshal(fetched.Manifest, &index); err != nil || index.ArtifactType != MediaPublicationRoot || len(index.Manifests) != 1 {
		return Inspection{}, e.New(e.CodeVerifyFailed, "metadata index must contain one root")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return Inspection{}, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage, plain: true}
	var total int64
	put := func(raw []byte, d Descriptor, max int64) error {
		total += int64(len(raw))
		if int64(len(raw)) > max || total > maxMetadataBytes || d.Size != int64(len(raw)) || digestBytes(raw) != d.Digest {
			return e.New(e.CodeVerifyFailed, "metadata object exceeds bounds or changes identity")
		}
		_, err := writer.addBlobBytes(raw, d.MediaType)
		return err
	}
	manifest := func(repo name.Repository, d Descriptor) (ociManifest, error) {
		got, err := remote.Get(repo.Digest(d.Digest), remoteOptions...)
		if err != nil {
			return ociManifest{}, classifyContext(ctx, e.Wrap(e.CodeFetchFailed, err, "fetch metadata manifest"))
		}
		if err := put(got.Manifest, d, 8<<20); err != nil {
			return ociManifest{}, err
		}
		var m ociManifest
		if err := json.Unmarshal(got.Manifest, &m); err != nil {
			return m, e.Wrap(e.CodeVerifyFailed, err, "decode metadata manifest")
		}
		return m, nil
	}
	blob := func(repo name.Repository, d Descriptor, max int64) error {
		if d.Size < 0 || d.Size > max {
			return e.New(e.CodeVerifyFailed, "oversized metadata blob")
		}
		layer, err := remote.Layer(repo.Digest(d.Digest), remoteOptions...)
		if err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeFetchFailed, err, "fetch metadata blob"))
		}
		reader, err := layer.Compressed()
		if err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeFetchFailed, err, "open metadata blob"))
		}
		defer reader.Close()
		raw, err := io.ReadAll(io.LimitReader(reader, max+1))
		if err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeFetchFailed, err, "read metadata blob"))
		}
		return put(raw, d, max)
	}
	m, err := manifest(ref.Context(), index.Manifests[0])
	if err != nil {
		return Inspection{}, err
	}
	if m.ArtifactType != MediaPublicationRoot || len(m.Layers) != 0 || m.Config.MediaType != MediaPublicationRootConfig {
		return Inspection{}, e.New(e.CodeVerifyFailed, "invalid metadata root")
	}
	if err := blob(ref.Context(), m.Config, 8<<20); err != nil {
		return Inspection{}, err
	}
	config, err := readBlob(ctx, stage, m.Config, 8<<20)
	if err != nil {
		return Inspection{}, err
	}
	var root PublicationRoot
	if err := canonical.UnmarshalExact(config, &root); err != nil {
		return Inspection{}, e.Wrap(e.CodeVerifyFailed, err, "decode metadata root")
	}
	if err := root.Validate(); err != nil {
		return Inspection{}, err
	}
	if root.Release == nil {
		return Inspection{}, e.New(e.CodeVerifyFailed, "metadata release context missing")
	}
	refs, err := publicationReferences(ref, root, options.PlainHTTP)
	if err != nil {
		return Inspection{}, err
	}
	for _, o := range root.Objects {
		child, ok := refs[o.Name]
		if !ok {
			return Inspection{}, e.New(e.CodeVerifyFailed, "metadata member has no repository")
		}
		d := Descriptor{MediaType: publishedManifestMediaType(o), Digest: o.DescriptorDigest, Size: o.Size, ArtifactType: o.ArtifactType}
		m, err := manifest(child.Context(), d)
		if err != nil {
			return Inspection{}, err
		}
		if err := blob(child.Context(), m.Config, 8<<20); err != nil {
			return Inspection{}, err
		}
		if o.Evidence != nil {
			if len(m.Layers) != 1 {
				return Inspection{}, e.New(e.CodeVerifyFailed, "evidence requires one bounded layer")
			}
			if err := blob(child.Context(), m.Layers[0], sbom.MaxBundleBytes); err != nil {
				return Inspection{}, err
			}
		}
	}
	got, err := writer.writeIndex(index)
	if err != nil {
		return Inspection{}, err
	}
	if got != ref.DigestStr() {
		return Inspection{}, e.New(e.CodeVerifyFailed, "noncanonical metadata index")
	}
	inspection, err := InspectMetadata(ctx, stage)
	if err != nil {
		return Inspection{}, err
	}
	if err := commit(); err != nil {
		return Inspection{}, err
	}
	return inspection, nil
}

// PushMetadata publishes only the release index/config after checking every
// referenced manifest exists. Caller-owned signature/provenance gates authorize
// release assembly; manifest existence alone is not acceptance.
func PushMetadata(ctx context.Context, source, repository string, options RegistryOptions) (string, error) {
	if err := deliveryContext(ctx); err != nil {
		return "", err
	}
	if err := options.Validate(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	inspection, err := InspectMetadata(ctx, source)
	if err != nil {
		return "", err
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	repo, err := name.NewRepository(repository, parse...)
	if err != nil || repo.Name() != repository {
		return "", e.New(e.CodeUsage, "metadata publication requires canonical repository")
	}
	ref := repo.Digest(inspection.RootDigest)
	refs, err := publicationReferences(ref, inspection.Root, options.PlainHTTP)
	if err != nil {
		return "", err
	}
	for _, o := range inspection.Root.Objects {
		child, ok := refs[o.Name]
		if !ok {
			return "", e.New(e.CodeVerifyFailed, "metadata member lacks repository")
		}
		if _, err := ResolveDigest(ctx, child.Name(), options); err != nil {
			return "", err
		}
	}
	path, err := layout.FromPath(source)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "open metadata layout")
	}
	index, err := path.ImageIndex()
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "open metadata index")
	}
	if err := remote.WriteIndex(ref, index, registryOptions(ctx, options)...); err != nil {
		return "", classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "publish metadata root"))
	}
	return ref.Name(), nil
}
