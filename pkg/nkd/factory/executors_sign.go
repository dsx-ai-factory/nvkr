package factory

// executors_sign.go owns the two post-publication factory nodes: `sign` binds a
// signature and an SLSA v1 production attestation to the exact digest a route
// published, and `verify-publication` re-resolves that digest at the registry and
// checks the signing record against it.
//
// Both are deliberately thin: every authority-bearing decision belongs to a deep
// module elsewhere — pkg/nkd/delivery owns registry transport and layout identity,
// pkg/nkd/provenance owns the predicate shape and its validation, and pkg/nkd/sign
// owns signing, verification, argv construction, and credential handling. What
// this file owns is the JOIN: proving that the reference file, the routed layout,
// and the registry all name the same bytes before any of them is treated as a
// subject, and refusing to write a record when they do not.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sign"
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
	signing := plan.ExecutionSettings.Signing
	if signing == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "factory node %q has no delivery signing profile", node.ID)
	}
	switch signing.Mode {
	case SigningModeDryRun:
		return sign.NewDryRunSigner(signing.Identity, signing.Issuer)
	case SigningModeCosign:
		tool, ok := artifacts["tool/"+signing.CosignTool]
		if !ok {
			return nil, e.New(e.CodeTrustPolicyMissing,
				"factory node %q signs with cosign but tool %q was never acquired", node.ID, signing.CosignTool)
		}
		credential, err := registryCredential(plan, node)
		if err != nil {
			return nil, err
		}
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{
			Binary:       tool.Path,
			BinaryDigest: toolExecutableDigest(plan, signing.CosignTool),
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
// credential fails closed rather than silently attempting anonymous access.
func registryCredential(plan *Plan, node Node) (sign.Credential, error) {
	target := plan.ExecutionSettings.Targets[node.Target]
	if target.CredentialRef == "" {
		return sign.Credential{}, nil
	}
	username, password := os.Getenv("NKD_REGISTRY_USER"), os.Getenv("NKD_CREDENTIAL")
	if username == "" || password == "" {
		return sign.Credential{}, e.New(e.CodeTrustPolicyMissing,
			"factory signing target %q requires its configured username and credential", node.Target)
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
	signing := plan.ExecutionSettings.Signing
	if signing == nil {
		return e.New(e.CodeTrustPolicyMissing, "factory sign node %q has no delivery signing profile", node.ID)
	}
	reference, err := signNodeInput(node, artifacts, "published-reference")
	if err != nil {
		return err
	}
	layoutArtifact, err := signNodePackageInput(node, artifacts)
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
	// The reference file says WHAT was published; the routed layout is the bytes we
	// believe were published. Re-deriving the index digest from the layout and
	// refusing any disagreement is what keeps a signature from binding an identity
	// nobody in this run actually produced.
	inspection, err := delivery.Inspect(ctx, layoutArtifact.Path)
	if err != nil {
		return err
	}
	if inspection.RootDigest != subject.Digest {
		return e.New(e.CodeVerifyFailed,
			"factory sign node %q: routed layout digest %s does not equal published digest %s",
			node.ID, inspection.RootDigest, subject.Digest)
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
	signing := plan.ExecutionSettings.Signing
	if signing == nil {
		return e.New(e.CodeTrustPolicyMissing, "factory verification node %q has no delivery signing profile", node.ID)
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
	target := plan.ExecutionSettings.Targets[node.Target]
	served, err := delivery.ResolveDigest(ctx, subject.Ref, delivery.RegistryOptions{
		PlainHTTP: target.PlainHTTP, Anonymous: target.Anonymous,
		Username: registryUsername(target), Password: registryPassword(target),
	})
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
	verification, err := signer.Verify(ctx, subject, sign.VerifyPolicy{
		IdentityRegexp: signing.Identity, Issuer: signing.Issuer, RequireAttestation: true,
	})
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
	workspace, err := os.MkdirTemp("", "nkd-verify-release-*")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating release verification workspace")
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	if _, err := delivery.Pull(ctx, subject.Ref, filepath.Join(workspace, "closure"), delivery.RegistryOptions{
		PlainHTTP: target.PlainHTTP, Anonymous: target.Anonymous, Username: registryUsername(target), Password: registryPassword(target),
	}); err != nil {
		return err
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

func registryUsername(target Target) string {
	if target.CredentialRef == "" {
		return ""
	}
	return os.Getenv("NKD_REGISTRY_USER")
}

func registryPassword(target Target) string {
	if target.CredentialRef == "" {
		return ""
	}
	return os.Getenv("NKD_CREDENTIAL")
}

// productionPredicateFor renders the exact SLSA v1 production predicate for this
// run. Every identity in it is taken from the plan; nothing is discovered here.
func productionPredicateFor(plan *Plan, _ *SigningProfile, startedOn, finishedOn time.Time) ([]byte, error) {
	// IdentityDigest, not Digest: this predicate outlives the run, so it must name the
	// plan as compiled rather than as relocated into whatever local workspace produced it.
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		return nil, err
	}
	run, err := factoryRunContext(planDigest, startedOn, finishedOn)
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
	if plan.ExecutionSettings.Signing == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "production predicate requires an admitted signing profile")
	}
	return productionPredicateForRun(plan, plan.ExecutionSettings.Signing, run)
}

// productionPredicateForRun renders the owning production predicate from one
// admitted plan and an authenticated provider run context. Keeping this join in
// factory lets hosted finalization reconstruct and compare the exact predicate
// semantics instead of trusting a matching file hash.
func productionPredicateForRun(plan *Plan, signing *SigningProfile, run provenance.RunContext) ([]byte, error) {
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		return nil, err
	}
	if plan.CompileSpecDigest == "" {
		return nil, e.New(e.CodeProvenanceMissing,
			"run plan carries no compile-spec digest to attest; recompile it with signing configured")
	}
	return provenance.MarshalProduction(provenance.ProductionPredicate(run, provenance.ProductionInputs{
		BuildType:               productionBuildType(plan),
		PlanDigest:              planDigest,
		RunPlanReference:        plan.Name,
		CompileSpecDigest:       plan.CompileSpecDigest,
		ExecutionSettingsDigest: plan.ExecutionSettingsDigest,
		CatalogDigest:           plan.CatalogDigest,
		GeneratorVersion:        plan.GeneratorVersion,
		SigningMode:             signing.Mode,
		ResolvedDependencies:    resolvedDependencies(plan),
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
// prevents a hermetic claim even when another recipe builds offline. A plan
// without build nodes only acquired existing artifacts.
func productionBuildType(plan *Plan) string {
	hasBuild := false
	for _, recipePlan := range plan.Recipes {
		if recipePlan.Graph == nil {
			continue
		}
		for _, node := range recipePlan.Graph.Nodes {
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

// resolvedDependencies lists every pinned input the plan resolved: each recipe's
// source repository at its exact commit (annotated with the tree hash), each
// pinned toolchain image, and each pinned prebuilt artifact. URIs are deduplicated
// because the same toolchain or source legitimately serves many components; the
// first binding wins, and a second binding of the same URI is by construction the
// same pin (recipe compilation would have rejected a conflicting one).
func resolvedDependencies(plan *Plan) []provenance.Dependency {
	byURI := map[string]provenance.Dependency{}
	if plan.DefinitionDigest != "" && plan.EnvironmentDigest != "" {
		byURI["nkd:definition"] = provenance.Dependency{URI: "nkd:definition", Digest: map[string]string{"sha256": strings.TrimPrefix(plan.DefinitionDigest, "sha256:")}}
		byURI["nkd:environment"] = provenance.Dependency{URI: "nkd:environment", Digest: map[string]string{"sha256": strings.TrimPrefix(plan.EnvironmentDigest, "sha256:")}}
	}
	for _, recipePlan := range plan.Recipes {
		if recipePlan.Graph == nil {
			continue
		}
		for _, node := range recipePlan.Graph.Nodes {
			var dependency provenance.Dependency
			switch {
			case node.Kind == ir.KindFetch && node.Repo != "" && node.Commit != "":
				dependency = provenance.SourceDependency(node.Repo, node.Commit, node.TreeHash)
			case node.Kind == ir.KindBuild && node.Toolchain != "":
				dependency = provenance.ToolchainDependency(node.Toolchain)
			case node.Kind == ir.KindPull && node.Artifact != "" && node.Digest != "":
				dependency = provenance.Dependency{URI: node.Artifact,
					Digest: map[string]string{"sha256": strings.TrimPrefix(node.Digest, "sha256:")}}
			default:
				continue
			}
			if _, seen := byURI[dependency.URI]; !seen {
				byURI[dependency.URI] = dependency
			}
		}
		// A fetch node's vendoring image is an executed input too.
		for _, node := range recipePlan.Graph.Nodes {
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
