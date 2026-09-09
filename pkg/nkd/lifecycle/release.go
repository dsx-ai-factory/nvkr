package lifecycle

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/cluster"
	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/factory"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/sign"
)

type materializedRelease struct {
	inspection delivery.Inspection
	path       string
	cleanup    func()
}

type releaseMaterializer func(context.Context, string) (materializedRelease, error)

// ConsumerOptions configures transport and signature verification once. Each
// Verify call then accepts only the Release reference and Verification Policy.
type ConsumerOptions struct {
	CosignBinary  string
	CosignDigest  string
	Credential    sign.Credential
	Registry      delivery.RegistryOptions
	WorkspaceRoot string
}

type Consumer struct {
	verifier    sign.OCISigner
	verifierFor func(VerificationPolicy) (sign.OCISigner, error)
	materialize releaseMaterializer
}

// VerifiedRelease is an opaque, policy-bound live capability. Description is a
// defensive public projection; Close releases its verified local closure.
type VerifiedRelease struct {
	mu      sync.RWMutex
	valid   bool
	release Release
	cleanup func()
}

func NewConsumer(options ConsumerOptions) (*Consumer, error) {
	root := options.WorkspaceRoot
	if root == "" {
		root = filepath.ToSlash(filepath.Join(".nkd", "verified-releases"))
	}
	return &Consumer{verifierFor: func(policy VerificationPolicy) (sign.OCISigner, error) {
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.CosignBinary,
			BinaryDigest: options.CosignDigest, Identity: policy.IdentityRegexp, Issuer: policy.Issuer, Credential: options.Credential})
	}, materialize: func(ctx context.Context, reference string) (materializedRelease, error) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return materializedRelease{}, e.Wrap(e.CodeImmutableViolation, err, "creating release verification root")
		}
		stage, err := os.MkdirTemp(root, ".verify-*")
		if err != nil {
			return materializedRelease{}, e.Wrap(e.CodeImmutableViolation, err, "creating release verification workspace")
		}
		cleanup := func() { _ = os.RemoveAll(stage) }
		inspection, err := delivery.Pull(ctx, reference, filepath.Join(stage, "layout"), options.Registry)
		if err != nil {
			cleanup()
			return materializedRelease{}, err
		}
		return materializedRelease{inspection: inspection, path: filepath.Join(stage, "layout"), cleanup: cleanup}, nil
	}}, nil
}

func (verified *VerifiedRelease) Description() (Release, error) {
	if verified == nil {
		return Release{}, e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	verified.mu.RLock()
	defer verified.mu.RUnlock()
	if !verified.valid {
		return Release{}, e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	description := verified.release
	description.Artifacts = cloneArtifacts(verified.release.Artifacts)
	description.Clusters = cloneClusterRealizations(verified.release.Clusters)
	return description, nil
}

func (verified *VerifiedRelease) Close() error {
	if verified == nil {
		return nil
	}
	verified.mu.Lock()
	defer verified.mu.Unlock()
	if !verified.valid {
		return nil
	}
	verified.valid = false
	if verified.cleanup != nil {
		verified.cleanup()
	}
	return nil
}

// Verify consumes one pinned reference under one explicit policy. The currently
// implemented positive scope authenticates a pipeline-produced publication root
// signature and SLSA provenance attestation, then independently pulls and checks
// every reachable descriptor. Full release and qualified scopes remain closed.
func (consumer *Consumer) Verify(ctx context.Context, reference string, policy VerificationPolicy) (*VerifiedRelease, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "release verification requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "release verification canceled")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	digest := referenceDigest(reference)
	subject := sign.Subject{Ref: reference, Digest: digest}
	if err := subject.Validate(); err != nil {
		return nil, err
	}
	if policy.Lane == LaneQualified {
		return nil, e.New(e.CodeEngineCapability, "qualified Release verification requires authenticated discovery, clearance, trusted-time, status, and publication-commit adapters")
	}
	if policy.Scope == ScopeRelease {
		return nil, e.New(e.CodeEngineCapability, "pipeline-attested full Release verification requires qualification and lifecycle-state adapters")
	}
	if consumer == nil || (consumer.verifier == nil && consumer.verifierFor == nil) || consumer.materialize == nil {
		return nil, e.New(e.CodeEngineCapability, "artifact-publication verification requires configured signature and registry adapters")
	}
	verifier := consumer.verifier
	if verifier == nil {
		configured, configureErr := consumer.verifierFor(policy)
		if configureErr != nil {
			return nil, configureErr
		}
		verifier = configured
	}
	record, err := verifier.Verify(ctx, subject, sign.VerifyPolicy{IdentityRegexp: policy.IdentityRegexp,
		Issuer: policy.Issuer, RequireAttestation: true})
	if err != nil {
		return nil, err
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.DryRun || !record.SignatureVerified || !record.AttestationVerified || record.Ref != reference || record.Digest != digest {
		return nil, e.New(e.CodeSignatureInvalid, "publication signature and provenance do not authenticate the exact requested subject")
	}
	identityPattern, err := regexp.Compile(policy.IdentityRegexp)
	if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "compiling release identity policy")
	}
	if !identityPattern.MatchString(record.ObservedIdentity) || record.ObservedIssuer != policy.Issuer {
		return nil, e.New(e.CodeUntrustedSigner, "observed release signer does not satisfy the requested identity and issuer policy")
	}
	materialized, err := consumer.materialize(ctx, reference)
	if err != nil {
		return nil, err
	}
	inspection, cleanup := materialized.inspection, materialized.cleanup
	failed := true
	defer func() {
		if failed && cleanup != nil {
			cleanup()
		}
	}()
	if inspection.RootDigest != digest {
		return nil, e.New(e.CodeVerifyFailed, "materialized publication root %s differs from requested subject %s", inspection.RootDigest, digest)
	}
	artifacts, contentSubject, clusters, err := artifactsFromBinding(ctx, materialized.path, inspection)
	if err != nil {
		return nil, err
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return nil, err
	}
	description := Release{SchemaVersion: ReleaseSchemaVersion, Subject: reference, Lane: policy.Lane,
		Scope: policy.Scope, Artifacts: artifacts, ContentSubject: contentSubject, Clusters: clusters, PolicyDigest: policyDigest}
	if err := description.validate(); err != nil {
		return nil, err
	}
	failed = false
	return &VerifiedRelease{valid: true, release: description, cleanup: cleanup}, nil
}

// InspectReleaseLayout returns exact Artifact descriptions from an already
// materialized publication closure. It authenticates no release lane.
func InspectReleaseLayout(ctx context.Context, reference, layoutPath string) ([]Artifact, error) {
	digest := referenceDigest(reference)
	if err := (sign.Subject{Ref: reference, Digest: digest}).Validate(); err != nil {
		return nil, err
	}
	inspection, err := delivery.Inspect(ctx, layoutPath)
	if err != nil {
		return nil, err
	}
	if inspection.RootDigest != digest {
		return nil, e.New(e.CodeVerifyFailed, "publication layout does not match requested reference")
	}
	return artifactsFromInspection(inspection)
}

func artifactsFromInspection(inspection delivery.Inspection) ([]Artifact, error) {
	artifacts := make([]Artifact, 0, len(inspection.Root.Objects))
	for _, object := range inspection.Root.Objects {
		artifact := Artifact{Name: object.Name, Kind: object.Kind, Digest: object.BlobDigest}
		if err := artifact.validate(); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Name != artifacts[j].Name {
			return artifacts[i].Name < artifacts[j].Name
		}
		return artifacts[i].Kind < artifacts[j].Kind
	})
	return artifacts, nil
}

func artifactsFromBinding(ctx context.Context, layoutPath string, inspection delivery.Inspection) ([]Artifact, string, []clusterRealization, error) {
	if layoutPath == "" {
		return nil, "", nil, e.New(e.CodeVerifyFailed, "verified publication has no held layout path")
	}
	object, raw, err := delivery.ReadObject(ctx, layoutPath, "nkd-lifecycle")
	if err != nil {
		return nil, "", nil, err
	}
	if object.Kind != "lifecycle-binding" || object.ArtifactType != "application/vnd.nkd.lifecycle-binding.v1+json" {
		return nil, "", nil, e.New(e.CodeVerifyFailed, "publication lifecycle binding has an unsupported type")
	}
	var binding factory.LifecycleBinding
	if err := canonical.UnmarshalExact(raw, &binding); err != nil {
		return nil, "", nil, e.Wrap(e.CodeVerifyFailed, err, "decoding lifecycle publication binding")
	}
	semantic, err := canonical.Digest(canonical.KindLifecycleBinding, binding)
	if err != nil || semantic != object.SemanticDigest || binding.SchemaVersion != factory.LifecycleBindingSchema ||
		!canonical.IsDigest(binding.DefinitionDigest) || !canonical.IsDigest(binding.EnvironmentDigest) {
		return nil, "", nil, e.New(e.CodeVerifyFailed, "lifecycle publication binding identity is not exact")
	}
	objects := make(map[string]delivery.PublishedObject, len(inspection.Root.Objects))
	for _, candidate := range inspection.Root.Objects {
		if _, exists := objects[candidate.Name]; exists {
			return nil, "", nil, e.New(e.CodeVerifyFailed, "publication repeats object name %q", candidate.Name)
		}
		objects[candidate.Name] = candidate
	}
	artifacts := make([]Artifact, 0, len(binding.Outputs))
	for _, output := range binding.Outputs {
		if !namePattern.MatchString(output.Name) || (output.Kind != OutputExecutable && output.Kind != OutputAPT && output.Kind != OutputHelm) || !canonical.IsDigest(output.Digest) ||
			!canonical.IsDigest(output.RecipeDigest) || (output.Kind != OutputHelm && len(output.Components) == 0) {
			return nil, "", nil, e.New(e.CodeVerifyFailed, "lifecycle output binding is malformed")
		}
		for _, component := range output.Components {
			if !namePattern.MatchString(component) {
				return nil, "", nil, e.New(e.CodeVerifyFailed, "lifecycle output %q has an unsafe component", output.Name)
			}
		}
	}
	for _, output := range binding.Outputs {
		published, ok := objects[output.Name]
		if !ok || published.Kind != lifecyclePublishedKind(output.Kind) ||
			(output.Kind != OutputAPT && published.BlobDigest != output.Digest) || (output.Kind == OutputAPT && published.SemanticDigest != output.Digest) {
			return nil, "", nil, e.New(e.CodeVerifyFailed, "lifecycle output %q does not match its publication object", output.Name)
		}
		extracted, err := os.MkdirTemp(filepath.Dir(layoutPath), ".inspect-*")
		if err != nil {
			return nil, "", nil, e.Wrap(e.CodeImmutableViolation, err, "creating held bundle inspection workspace")
		}
		defer func() { _ = os.RemoveAll(extracted) }()
		components := append([]string(nil), output.Components...)
		switch output.Kind {
		case OutputExecutable:
			destination := filepath.Join(extracted, "bundle")
			bundle, err := delivery.ExtractBinaryBundle(ctx, layoutPath, output.Name, destination)
			if err != nil {
				return nil, "", nil, err
			}
			components = components[:0]
			for _, entry := range bundle.Entries {
				components = append(components, entry.Name)
			}
		case OutputAPT:
			expectedPath := filepath.ToSlash(filepath.Join(".nkd", "factory", "packages", output.Name))
			if output.TransportPath != expectedPath {
				return nil, "", nil, e.New(e.CodeVerifyFailed, "APT output %q has an unsafe transport binding", output.Name)
			}
			archive := filepath.Join(extracted, "repository.tar")
			if _, err := delivery.ExtractObject(ctx, layoutPath, output.Name, archive); err != nil {
				return nil, "", nil, err
			}
			unpacked := filepath.Join(extracted, "unpacked")
			if err := pipeline.UnpackTransport(ctx, archive, unpacked, []string{output.TransportPath}); err != nil {
				return nil, "", nil, err
			}
			repository, err := delivery.InspectAPTRepository(ctx, filepath.Join(unpacked, filepath.FromSlash(output.TransportPath)))
			if err != nil || repository.Digest != output.Digest {
				return nil, "", nil, e.New(e.CodeVerifyFailed, "APT output %q differs from its binding", output.Name)
			}
			components = components[:0]
			for _, pkg := range repository.Repository.Packages {
				components = append(components, pkg.Name)
			}
			sort.Strings(components)
		case OutputHelm:
			if output.TransportPath != "" {
				return nil, "", nil, e.New(e.CodeVerifyFailed, "Helm output %q has an unexpected transport path", output.Name)
			}
		}
		if !equalStrings(components, output.Components) {
			return nil, "", nil, e.New(e.CodeVerifyFailed, "lifecycle output %q component bytes differ from its binding", output.Name)
		}
		artifact := Artifact{Name: output.Name, Kind: output.Kind, Digest: output.Digest,
			RecipeDigest: output.RecipeDigest, Components: append([]string(nil), output.Components...)}
		if err := artifact.validate(); err != nil {
			return nil, "", nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
	contentSubject, err := canonical.Digest(canonical.KindReleaseContent, releaseContent{Artifacts: cloneArtifacts(artifacts)})
	if err != nil || contentSubject != binding.ContentSubject {
		return nil, "", nil, e.New(e.CodeVerifyFailed, "release content subject differs from authenticated output payloads")
	}
	clusters := make([]clusterRealization, 0, len(binding.Clusters))
	compositionPlans := make([]factory.CompositionPlan, 0, len(binding.Clusters))
	clusterPlans := make([]factory.LifecycleClusterPlan, 0, len(binding.Clusters))
	for _, bound := range binding.Clusters {
		composition, ok := objects[bound.Name]
		if !ok || composition.Kind != "cluster-composition" || composition.SemanticDigest != bound.CompositionDigest || bound.ContentSubject != contentSubject {
			return nil, "", nil, e.New(e.CodeVerifyFailed, "cluster %q does not match its published composition", bound.Name)
		}
		_, compositionRaw, err := delivery.ReadObject(ctx, layoutPath, bound.Name)
		if err != nil {
			return nil, "", nil, err
		}
		var compositionValue cluster.ClusterComposition
		canonicalComposition := bytes.TrimSuffix(compositionRaw, []byte{'\n'})
		if err := canonical.UnmarshalExact(canonicalComposition, &compositionValue); err != nil {
			return nil, "", nil, e.Wrap(e.CodeVerifyFailed, err, "decoding cluster %q composition", bound.Name)
		}
		compositionPlans = append(compositionPlans, factory.CompositionPlan{Name: bound.Name,
			Digest: bound.CompositionDigest, Composition: compositionValue})
		planned := factory.LifecycleClusterPlan{Name: bound.Name, CompositionDigest: bound.CompositionDigest}
		realization := clusterRealization{SchemaVersion: bound.SchemaVersion, Name: bound.Name,
			CompositionDigest: bound.CompositionDigest, ContentSubject: bound.ContentSubject}
		for _, member := range bound.Members {
			planned.Members = append(planned.Members, factory.LifecycleClusterMember{Output: member.Output, Role: member.Role})
			var artifact *Artifact
			for index := range artifacts {
				if artifacts[index].Name == member.Output {
					artifact = &artifacts[index]
					break
				}
			}
			if artifact == nil || artifact.Digest != member.ArtifactDigest || artifact.RecipeDigest != member.RecipeDigest ||
				!equalStrings(artifact.Components, member.Components) {
				return nil, "", nil, e.New(e.CodeVerifyFailed, "cluster %q member %q differs from authenticated release content", bound.Name, member.Output)
			}
			realization.Members = append(realization.Members, clusterRealizedMember{Output: member.Output,
				ArtifactDigest: member.ArtifactDigest, RecipeDigest: member.RecipeDigest,
				Components: append([]string(nil), member.Components...), Role: member.Role})
		}
		if bound.SchemaVersion != clusterRealizationSchema || len(realization.Members) == 0 {
			return nil, "", nil, e.New(e.CodeVerifyFailed, "cluster %q realization is incomplete", bound.Name)
		}
		if _, err := canonical.Digest(canonical.KindClusterRealization, realization); err != nil {
			return nil, "", nil, err
		}
		clusters = append(clusters, realization)
		clusterPlans = append(clusterPlans, planned)
	}
	expectedOutputs := make([]factory.LifecycleOutputPlan, 0, len(binding.Outputs))
	boundOutputs := make([]factory.LifecycleBoundOutput, 0, len(binding.Outputs))
	for _, output := range binding.Outputs {
		expectedOutputs = append(expectedOutputs, factory.LifecycleOutputPlan{Name: output.Name,
			RecipeDigest: output.RecipeDigest, Components: append([]string(nil), output.Components...)})
		boundOutputs = append(boundOutputs, output)
	}
	if err := factory.ValidateLifecycleRealizations(compositionPlans, expectedOutputs, clusterPlans, boundOutputs); err != nil {
		return nil, "", nil, err
	}
	if len(objects) != len(artifacts)+len(clusters)+1 {
		return nil, "", nil, e.New(e.CodeVerifyFailed, "publication closure contains objects outside its lifecycle binding")
	}
	return artifacts, contentSubject, clusters, nil
}

func lifecyclePublishedKind(kind string) string {
	switch kind {
	case OutputExecutable:
		return factory.PackageBinaryBundle
	case OutputAPT:
		return factory.PackageAPT
	case OutputHelm:
		return factory.PackageHelm
	default:
		return ""
	}
}

func (release Release) validate() error {
	if release.SchemaVersion != ReleaseSchemaVersion || release.Lane != LanePipeline || release.Scope != ScopeArtifactPublication ||
		!canonical.IsDigest(release.PolicyDigest) || !canonical.IsDigest(release.ContentSubject) || len(release.Artifacts) == 0 {
		return e.New(e.CodeGateUnmet, "Release does not contain a supported policy-bound verified closure")
	}
	digest := referenceDigest(release.Subject)
	if err := (sign.Subject{Ref: release.Subject, Digest: digest}).Validate(); err != nil {
		return err
	}
	previous := ""
	for _, artifact := range release.Artifacts {
		if err := artifact.validate(); err != nil {
			return err
		}
		key := artifact.Name + "\x00" + artifact.Kind
		if key <= previous {
			return e.New(e.CodeVerifyFailed, "Release artifacts must be sorted and unique")
		}
		previous = key
	}
	return nil
}

func referenceDigest(reference string) string {
	if strings.Count(reference, "@") != 1 {
		return ""
	}
	return reference[strings.LastIndexByte(reference, '@')+1:]
}
