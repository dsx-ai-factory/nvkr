// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

// execute_sign.go owns the two post-publication factory nodes: `sign` binds a
// signature and an SLSA v1 production attestation to the exact digest a route
// published and each bound deliverable manifest. `verify-publication` checks the
// root signing record, retrieves the full closure, and verifies every deliverable.
//
// Both are deliberately thin: every authority-bearing decision belongs to a deep
// module elsewhere — pkg/nvkr/delivery owns registry transport and layout identity,
// pkg/nvkr/provenance owns the predicate shape and its validation, and pkg/nvkr/sign
// owns signing, verification, argv construction, and credential handling. What
// this file owns is the JOIN: proving that the reference file, the routed layout,
// and the registry all name the same bytes before any of them is treated as a
// subject, and refusing to write a record when they do not.

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// maxSigningRecordBytes bounds the small JSON documents this lane reads back
// (a publication reference and a signing record).
const maxSigningRecordBytes = int64(1 << 20)

// signerFor constructs the signing adapter for one node. It is the injection seam
// that lets the executor be tested against a fake signer without ever selecting an
// adapter by inspecting a global or an ambient environment at signing time.
type signerFor func(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact) (sign.OCISigner, error)

// productionOCISigner selects the adapter the delivery profile's signing mode
// names. Credentials are resolved HERE, from the target's configured environment
// (the same convention executePublish uses), and handed to the adapter as data;
// the adapter never reads the ambient environment for a secret.
func productionOCISigner(_ context.Context, plan *Plan, node Node, artifacts map[string]Artifact) (sign.OCISigner, error) {
	if plan.Environment.GitHubDevelopment {
		if _, err := runtimeTarget(plan, node.Target); err != nil {
			return nil, err
		}
	}
	signing := plan.Environment.Signing
	if signing == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "factory node %q has no delivery signing profile", node.ID)
	}
	switch signing.Mode {
	case SigningModeDryRun:
		return sign.NewDryRunSigner(signing.Identity, signing.Issuer)
	case SigningModeCosign:
		tool, ok := artifacts["tool/"+plan.Definition.CosignTool]
		if !ok {
			return nil, e.New(e.CodeTrustPolicyMissing,
				"factory node %q signs with cosign but tool %q was never acquired", node.ID, plan.Definition.CosignTool)
		}
		var credential sign.Credential
		var err error
		if node.Kind != NodeAttestEvidence {
			credential, err = registryCredential(plan, node)
		}
		if err != nil {
			return nil, err
		}
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{
			Binary:       tool.Path,
			BinaryDigest: toolExecutableDigest(plan, plan.Definition.CosignTool),
			Identity:     signing.Identity,
			Issuer:       signing.Issuer,
			Credential:   credential,
		})
	default:
		return nil, e.New(e.CodeEngineCapability, "delivery signing mode %q has no adapter", signing.Mode)
	}
}

// registryCredential reads the credential the target declares it needs from this
// process's environment. A target that declares a credentialRef and finds no
// credential fails closed before anonymous or ambient-keychain access.
func registryCredential(plan *Plan, node Node) (sign.Credential, error) {
	target, err := runtimeTarget(plan, node.Target)
	if err != nil {
		return sign.Credential{}, err
	}
	if target.CredentialRef == "" {
		return sign.Credential{}, nil
	}
	username, password := os.Getenv("NVKR_REGISTRY_USER"), os.Getenv("NVKR_CREDENTIAL")
	if username == "" || password == "" {
		return sign.Credential{}, e.New(e.CodeTrustPolicyMissing,
			"factory registry target %q requires its configured username and credential", node.Target)
	}
	return sign.Credential{Registry: registryHost(target.Ref), Username: username, Password: password}, nil
}

// registryHost is the registry authority of a reference: everything before the
// first path separator. cosign's credential store is keyed by that authority.
func registryHost(reference string) string {
	if slash := strings.IndexByte(reference, '/'); slash > 0 {
		return reference[:slash]
	}
	return reference
}

// toolExecutableDigest returns the exact executable digest the plan pinned for a
// tool, so the stock cosign adapter can refuse anything else.
func toolExecutableDigest(plan *Plan, name string) string {
	for _, node := range plan.Graph.Nodes {
		if node.ID == "tool/"+name {
			return node.ExecutableDigest
		}
	}
	return ""
}

func executeSign(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact,
	newSigner signerFor, stdout io.Writer) error {
	startedOn := time.Now().UTC()
	signing := plan.Environment.Signing
	if signing == nil {
		return e.New(e.CodeTrustPolicyMissing, "factory sign node %q has no delivery signing profile", node.ID)
	}
	reference, err := signNodeInput(node, artifacts, "published-reference")
	if err != nil {
		return err
	}
	metadataOnly := PerOutputPublication(plan) && node.Target != "release"
	selectLayout := signNodePackageInput
	if metadataOnly {
		selectLayout = func(node Node, artifacts map[string]Artifact) (Artifact, error) {
			return signNodeInput(node, artifacts, ArtifactSigningMetadata)
		}
	}
	layoutArtifact, err := selectLayout(node, artifacts)
	if err != nil {
		return err
	}
	predicateOutput, err := signNodeOutput(node, artifacts, ArtifactProvenancePredicate)
	if err != nil {
		return err
	}
	recordOutput, err := signNodeOutput(node, artifacts, ArtifactSigningRecord)
	if err != nil {
		return err
	}
	subject, err := readPublicationSubject(ctx, reference.Path)
	if err != nil {
		return err
	}
	if err := admitDevelopmentSubject(plan, node.Target, subject); err != nil {
		return err
	}
	// Re-derive the root and every signing subject from validated control bytes.
	// Output-local signing metadata is a description; independent remote
	// verification still authenticates the complete published payload closure.
	inspect := delivery.Inspect
	if PerOutputPublication(plan) {
		inspect = delivery.InspectMetadata
	}
	inspection, err := inspect(ctx, layoutArtifact.Path)
	if err != nil {
		return err
	}
	if inspection.RootDigest != subject.Digest {
		return e.New(e.CodeVerifyFailed,
			"factory sign node %q: routed layout digest %s does not equal published digest %s",
			node.ID, inspection.RootDigest, subject.Digest)
	}
	if err := admitDevelopmentMetadata(plan, inspection); err != nil {
		return err
	}
	if metadataOnly {
		if err := admitSigningMetadata(plan, node, subject, inspection); err != nil {
			return err
		}
	}
	if signing.Mode == SigningModeCosign && inspection.Root.Release != nil {
		if err := delivery.RequireAttestedEvidence(inspection); err != nil {
			return err
		}
	}
	predicate, err := productionPredicateFor(plan, signing, startedOn, time.Now().UTC())
	if err != nil {
		return err
	}
	// Terminate the predicate BEFORE it is signed, because the predicate file is the
	// artifact a real `cosign attest --predicate <file>` reads: the bytes on disk are the
	// bytes that get attested. Hashing the unterminated form and then writing a
	// newline-terminated file would make the record's predicateSha256 disagree with both
	// the file and the attestation, so the one link the record exists to establish —
	// "this record describes that predicate" — could never be checked.
	predicate = append(predicate, '\n')

	signer, err := newSigner(ctx, plan, node, artifacts)
	if err != nil {
		return err
	}
	if signer == nil {
		return e.New(e.CodeInternal, "factory sign node %q resolved no signing adapter", node.ID)
	}
	if metadataOnly && signing.Mode == SigningModeCosign {
		if err := verifyMetadataSBOMs(ctx, signer, layoutArtifact.Path, inspection); err != nil {
			return err
		}
	}
	// Each output is independently consumable from its own repository. Derive all
	// subjects from the inspected closure before the first signing side effect.
	if PerOutputPublication(plan) && node.Target == "release" {
		if err := verifyPublicationOutputs(ctx, signer, subject.Ref, inspection, signing, stdout); err != nil {
			return err
		}
	} else if err := signPublicationOutputs(ctx, signer, subject.Ref, inspection, predicate, signing, stdout); err != nil {
		return err
	}
	record, err := signer.SignAndAttest(ctx, subject, predicate)
	if err != nil {
		return err
	}
	raw, err := record.Canonical()
	if err != nil {
		return err
	}
	if err := writeFactoryFile(ctx, predicateOutput.Path, predicate, 0o644); err != nil {
		return err
	}
	if err := writeFactoryFile(ctx, recordOutput.Path, raw, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "signed %s (mode %s, dryRun %t): %s\n", node.ID, signing.Mode, record.DryRun, subject.Ref)
	return nil
}

func executeVerifyPublication(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact,
	newSigner signerFor, stdout io.Writer) error {
	signing := plan.Environment.Signing
	if signing == nil {
		return e.New(e.CodeTrustPolicyMissing, "factory verification node %q has no delivery signing profile", node.ID)
	}
	credential, err := registryCredential(plan, node)
	if err != nil {
		return err
	}
	target, err := runtimeTarget(plan, node.Target)
	if err != nil {
		return err
	}
	registryOptions := delivery.RegistryOptions{
		PlainHTTP: target.PlainHTTP, Anonymous: target.Anonymous,
		Username: credential.Username, Password: credential.Password,
	}
	reference, err := signNodeInput(node, artifacts, "published-reference")
	if err != nil {
		return err
	}
	signature, err := signNodeInput(node, artifacts, ArtifactSigningRecord)
	if err != nil {
		return err
	}
	output, err := signNodeOutput(node, artifacts, ArtifactVerificationRecord)
	if err != nil {
		return err
	}
	subject, err := readPublicationSubject(ctx, reference.Path)
	if err != nil {
		return err
	}
	if err := admitDevelopmentSubject(plan, node.Target, subject); err != nil {
		return err
	}
	signed, err := readSigningRecord(ctx, signature.Path)
	if err != nil {
		return err
	}
	if signed.Ref != subject.Ref || signed.Digest != subject.Digest {
		return e.New(e.CodeVerifyFailed,
			"factory verification node %q: signing record binds %s but the publication reference is %s",
			node.ID, signed.Ref, subject.Ref)
	}
	if signed.DryRun != (signing.Mode == SigningModeDryRun) {
		return e.New(e.CodeVerifyFailed,
			"factory verification node %q: signing record dryRun=%t contradicts delivery signing mode %q",
			node.ID, signed.DryRun, signing.Mode)
	}
	served, err := delivery.ResolveDigest(ctx, subject.Ref, registryOptions)
	if err != nil {
		return err
	}
	if served != subject.Digest {
		return e.New(e.CodeVerifyFailed,
			"factory verification node %q: registry serves %s for %s", node.ID, served, subject.Ref)
	}
	signer, err := newSigner(ctx, plan, node, artifacts)
	if err != nil {
		return err
	}
	if signer == nil {
		return e.New(e.CodeInternal, "factory verification node %q resolved no signing adapter", node.ID)
	}
	policy := sign.VerifyPolicy{IdentityRegexp: signing.Identity, Issuer: signing.Issuer, RequireAttestation: true}
	if PerOutputPublication(plan) {
		raw, err := readSmallFactoryFile(ctx, artifacts["predicate/"+strings.TrimPrefix(node.ID, NodeVerifyPublication+"/")].Path, "production predicate")
		if err != nil {
			return err
		}
		if err := validateHostedProductionPredicate(raw, plan, signing); err != nil {
			return err
		}
		policy.ExpectedPredicate = raw
	}
	verification, err := signer.Verify(ctx, subject, policy)
	if err != nil {
		// Fail closed and write NOTHING: an absent verification record is an honest
		// "unverified", while a written one would be read as a checked publication.
		return err
	}
	if !verification.DryRun && (!verification.SignatureVerified || !verification.AttestationVerified) {
		return e.New(e.CodeVerifyFailed,
			"factory verification node %q: %s was not verified against its identity policy", node.ID, subject.Ref)
	}
	// Authenticate the selected entry point first, then verify all repository-bound
	// deliverables and release evidence before committing a successful record.
	workspace, err := os.MkdirTemp("", "nvkr-verify-release-*")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating release verification workspace")
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	pull := delivery.Pull
	if PerOutputPublication(plan) && node.Target == "release" {
		pull = delivery.PullMetadata
	}
	inspection, err := pull(ctx, subject.Ref, filepath.Join(workspace, "closure"), registryOptions)
	if err != nil {
		return err
	}
	if signing.Mode == SigningModeCosign && inspection.Root.Release != nil {
		verifyEvidence := verifySBOMAttestations
		if PerOutputPublication(plan) && node.Target == "release" {
			verifyEvidence = verifyMetadataSBOMs
		}
		if err := verifyEvidence(ctx, signer, filepath.Join(workspace, "closure"), inspection); err != nil {
			return err
		}
	}
	if err := verifyPublicationOutputs(ctx, signer, subject.Ref, inspection, signing, stdout); err != nil {
		return err
	}
	if PerOutputPublication(plan) {
		route := strings.TrimPrefix(node.ID, NodeVerifyPublication+"/")
		output := artifacts["metadata/"+route]
		if output.Kind != ArtifactPublicationMetadata {
			return e.New(e.CodePipelineDrift, "verification has no metadata output")
		}
		var err error
		if route == "release" {
			sources := map[string]string{}
			for _, object := range inspection.Root.Objects {
				sources[object.Name] = filepath.Join(workspace, "closure")
			}
			_, err = delivery.BuildMetadataRoot(ctx, output.Path, inspection.Root, sources)
		} else {
			_, err = delivery.ExportMetadata(ctx, filepath.Join(workspace, "closure"), output.Path)
		}
		if err != nil {
			return err
		}
	}
	raw, err := verification.Canonical()
	if err != nil {
		return err
	}
	if err := writeFactoryFile(ctx, output.Path, raw, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "verified %s (mode %s, dryRun %t): %s\n", node.ID, signing.Mode, verification.DryRun, subject.Ref)
	return nil
}

// productionPredicateFor renders the exact SLSA v1 production predicate for this
// run. Every identity in it is taken from the plan; nothing is discovered here.
func productionPredicateFor(plan *Plan, _ *definition.Signing, startedOn, finishedOn time.Time) ([]byte, error) {
	// IdentityDigest, not Digest: this predicate outlives the run, so it must name the
	// plan as compiled rather than as relocated into whatever local workspace produced it.
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		return nil, err
	}
	run, err := publicationRunContext(plan, planDigest, startedOn, finishedOn)
	if err != nil {
		return nil, err
	}
	return ProductionPredicate(plan, run)
}

// ProductionPredicate builds the exact descriptive SLSA predicate for one
// admitted plan and provider run. It does not confer authority; only the signing
// module can bind these bytes to a publication subject.
func ProductionPredicate(plan *Plan, run provenance.RunContext) ([]byte, error) {
	if plan == nil {
		return nil, e.New(e.CodeUsage, "production predicate requires a plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if plan.Environment.Signing == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "production predicate requires an admitted signing profile")
	}
	return productionPredicateForRun(plan, plan.Environment.Signing, run)
}

// productionPredicateForRun renders the owning production predicate from one
// admitted plan and an authenticated provider run context. Keeping this join in
// factory lets hosted finalization reconstruct and compare the exact predicate
// semantics instead of trusting a matching file hash.
func productionPredicateForRun(plan *Plan, signing *definition.Signing, run provenance.RunContext) ([]byte, error) {
	if plan.Environment.GitHubDevelopment {
		if _, _, _, err := developmentRun(plan, run); err != nil {
			return nil, err
		}
	}
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		return nil, err
	}
	if plan.DefinitionDigest == "" {
		return nil, e.New(e.CodeProvenanceMissing,
			"run plan carries no definition digest to attest; recompile it with signing configured")
	}
	return provenance.MarshalProduction(provenance.ProductionPredicate(run, provenance.ProductionInputs{
		BuildType:            productionBuildType(plan),
		PlanDigest:           planDigest,
		RunPlanReference:     plan.Name,
		DefinitionDigest:     plan.DefinitionDigest,
		EnvironmentDigest:    plan.EnvironmentDigest,
		CatalogDigest:        plan.CatalogDigest,
		GeneratorVersion:     plan.GeneratorVersion,
		SigningMode:          signing.Mode,
		ResolvedDependencies: resolvedDependencies(plan),
	}))
}

// factoryRunContext picks the run identity from the execution environment: a
// GitHub Actions job is identified by its workflow reference and run attempt, and
// anything else is an explicitly local, unattested builder identity. GITHUB_ACTIONS
// is the runner-set marker; when it is absent nothing about GitHub is assumed.
func factoryRunContext(planDigest string, startedOn, finishedOn time.Time) (provenance.RunContext, error) {
	if strings.TrimSpace(os.Getenv("GITHUB_ACTIONS")) == "" {
		return provenance.NewLocalRunContext(planDigest, startedOn, finishedOn)
	}
	return provenance.NewGitHubRunContext(provenance.OSEnvLookup, startedOn, finishedOn)
}

// productionBuildType classifies the whole plan. A wrapped/networked build
// prevents a hermetic claim even when another build builds offline. A plan
// without build nodes only acquired existing artifacts.
func productionBuildType(plan *Plan) string {
	hasBuild := false
	for _, buildPlan := range plan.Builds {
		if buildPlan.Graph == nil {
			continue
		}
		for _, node := range buildPlan.Graph.Nodes {
			if node.Kind == ir.KindBuild {
				if node.BuilderType == ir.BuilderBuildKit || node.Network != ir.NetworkNone {
					return provenance.BuildTypeWrappedBuild
				}
				hasBuild = true
			}
		}
	}
	if hasBuild {
		return provenance.BuildTypeHermeticBuild
	}
	return provenance.BuildTypeAcquired
}

// resolvedDependencies lists every pinned input the plan resolved: each build's
// source repository at its exact commit (annotated with the tree hash), each
// pinned toolchain image, and each pinned prebuilt artifact. URIs are deduplicated
// because the same toolchain or source legitimately serves many components; the
// first binding wins, and a second binding of the same URI is by construction the
// same pin (build compilation would have rejected a conflicting one).
func resolvedDependencies(plan *Plan) []provenance.Dependency {
	byURI := map[string]provenance.Dependency{}
	if plan.DefinitionDigest != "" && plan.EnvironmentDigest != "" {
		byURI["nvkr:definition"] = provenance.Dependency{URI: "nvkr:definition", Digest: map[string]string{"sha256": strings.TrimPrefix(plan.DefinitionDigest, "sha256:")}}
		byURI["nvkr:environment"] = provenance.Dependency{URI: "nvkr:environment", Digest: map[string]string{"sha256": strings.TrimPrefix(plan.EnvironmentDigest, "sha256:")}}
	}
	for _, buildPlan := range plan.Builds {
		if buildPlan.Graph == nil {
			continue
		}
		for _, node := range buildPlan.Graph.Nodes {
			var dependency provenance.Dependency
			switch {
			case node.Kind == ir.KindFetch && node.Repo != "" && node.Commit != "":
				dependency = provenance.SourceDependency(node.Repo, node.Commit, node.TreeHash)
			case node.Kind == ir.KindBuild && node.Toolchain != "":
				dependency = provenance.ToolchainDependency(node.Toolchain)
			case node.Kind == ir.KindPull && node.Artifact != "" && node.Digest != "":
				digest := node.Digest
				if node.OutputType == "oci-image" && node.ImageParentDigest != "" {
					digest = node.ImageParentDigest
					// The parent URI names index/root bytes. The native delivered
					// subject remains a separate immutable dependency.
					if nativeRef, err := ir.NativeImageReference(node); err == nil {
						byURI[nativeRef] = provenance.Dependency{URI: nativeRef, Digest: map[string]string{"sha256": strings.TrimPrefix(node.Digest, "sha256:")}}
					}
				}
				if node.ExecutableArchive != nil {
					digest = node.ExecutableArchive.Digest
					// The URL identifies the acquired archive bytes, not the
					// executable selected from it. Retain both exact subjects.
					memberURI := "nvkr:prebuilt/" + url.PathEscape(node.Component) + "?" + url.Values{
						"archive": {node.Artifact}, "member": {node.ExecutableArchive.Member},
					}.Encode()
					if _, seen := byURI[memberURI]; !seen {
						byURI[memberURI] = provenance.Dependency{URI: memberURI,
							Digest: map[string]string{"sha256": strings.TrimPrefix(node.Digest, "sha256:")}}
					}
					for _, companion := range node.ExecutableArchive.Companions {
						companionURI := "nvkr:prebuilt/" + url.PathEscape(node.Component) + "?" + url.Values{
							"archive": {node.Artifact}, "member": {companion.Member},
						}.Encode()
						if _, seen := byURI[companionURI]; !seen {
							byURI[companionURI] = provenance.Dependency{URI: companionURI,
								Digest: map[string]string{"sha256": strings.TrimPrefix(companion.Digest, "sha256:")}}
						}
					}
				}
				dependency = provenance.Dependency{URI: node.Artifact,
					Digest: map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}
			default:
				continue
			}
			if _, seen := byURI[dependency.URI]; !seen {
				byURI[dependency.URI] = dependency
			}
		}
		// A fetch node's vendoring image is an executed input too.
		for _, node := range buildPlan.Graph.Nodes {
			if node.Kind == ir.KindFetch && node.VendorImage != "" {
				if _, seen := byURI[node.VendorImage]; !seen {
					byURI[node.VendorImage] = provenance.ToolchainDependency(node.VendorImage)
				}
			}
		}
	}
	dependencies := make([]provenance.Dependency, 0, len(byURI))
	for _, uri := range sortedKeys(byURI) {
		dependencies = append(dependencies, byURI[uri])
	}
	sort.SliceStable(dependencies, func(i, j int) bool { return dependencies[i].URI < dependencies[j].URI })
	return dependencies
}

// signNodeInput selects the single input artifact of a given kind, so the
// executor never depends on input ORDER (which is canonical-sorted, not semantic).
func signNodeInput(node Node, artifacts map[string]Artifact, kind string) (Artifact, error) {
	var found Artifact
	for _, id := range node.Inputs {
		if artifacts[id].Kind == kind {
			if found.ID != "" {
				return Artifact{}, e.New(e.CodePipelineDrift,
					"factory node %q has more than one %s input", node.ID, kind)
			}
			found = artifacts[id]
		}
	}
	if found.ID == "" {
		return Artifact{}, e.New(e.CodePipelineDrift, "factory node %q has no %s input", node.ID, kind)
	}
	return found, nil
}

// signNodePackageInput selects the routed package layout the publication was
// pushed from. It is whichever input is a package artifact.
func signNodePackageInput(node Node, artifacts map[string]Artifact) (Artifact, error) {
	var found Artifact
	for _, id := range node.Inputs {
		if !strings.HasPrefix(id, "package/") {
			continue
		}
		if found.ID != "" {
			return Artifact{}, e.New(e.CodePipelineDrift,
				"factory sign node %q has more than one routed package input", node.ID)
		}
		found = artifacts[id]
	}
	if found.ID == "" {
		return Artifact{}, e.New(e.CodePipelineDrift, "factory sign node %q has no routed package input", node.ID)
	}
	return found, nil
}

func signNodeOutput(node Node, artifacts map[string]Artifact, kind string) (Artifact, error) {
	for _, id := range node.Outputs {
		if artifacts[id].Kind == kind {
			return artifacts[id], nil
		}
	}
	return Artifact{}, e.New(e.CodePipelineDrift, "factory node %q declares no %s output", node.ID, kind)
}

// readPublicationSubject admits the `repository@sha256:<hex>` line publish wrote.
// sign.Subject.Validate is the authority on the form; this only splits it.
func readPublicationSubject(ctx context.Context, path string) (sign.Subject, error) {
	raw, err := readSmallFactoryFile(ctx, path, "factory-publication-reference")
	if err != nil {
		return sign.Subject{}, err
	}
	reference := strings.TrimSpace(string(raw))
	at := strings.LastIndex(reference, "@")
	if at <= 0 {
		return sign.Subject{}, e.New(e.CodeUnpinnedSource,
			"published reference %q is not repository@sha256:<hex>", reference)
	}
	subject := sign.Subject{Ref: reference, Digest: reference[at+1:]}
	if err := subject.Validate(); err != nil {
		return sign.Subject{}, err
	}
	return subject, nil
}

func readSigningRecord(ctx context.Context, path string) (sign.SigningRecord, error) {
	raw, err := readSmallFactoryFile(ctx, path, "factory-signing-record")
	if err != nil {
		return sign.SigningRecord{}, err
	}
	var record sign.SigningRecord
	if err := canonical.UnmarshalExact(raw, &record); err != nil {
		return sign.SigningRecord{}, e.Wrap(e.CodePipelineDrift, err, "decoding signing record %s", path)
	}
	if err := record.Validate(); err != nil {
		return sign.SigningRecord{}, err
	}
	return record, nil
}

func readSmallFactoryFile(ctx context.Context, path, label string) ([]byte, error) {
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: label, MaxBytes: maxSigningRecordBytes})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodePipelineDrift, err, "opening %s %s", label, path))
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxSigningRecordBytes)
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodePipelineDrift, err, "reading %s %s", label, path))
	}
	return raw, nil
}
