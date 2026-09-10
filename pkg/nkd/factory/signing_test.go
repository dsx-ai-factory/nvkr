package factory

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"sigs.k8s.io/yaml"
)

const testCosignArchiveDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
const testCosignExecutableDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

func dryRunSigningProfile() *SigningProfile {
	return &SigningProfile{Mode: SigningModeDryRun, Identity: "^local$", Issuer: "https://nkd.dev/local"}
}

func cosignSigningProfile() *SigningProfile {
	return &SigningProfile{Mode: SigningModeCosign, CosignTool: "cosign",
		Identity: `^https://github\.com/example/nkd/\.github/workflows/nkd-factory\.yml@refs/heads/main$`,
		Issuer:   "https://token.actions.githubusercontent.com"}
}

// signedTestPlan compiles a plan whose single OCI route publishes an oci-root
// package, optionally under a signing profile. target lets a test point the route
// at an in-process registry.
func signedTestPlan(t *testing.T, signing *SigningProfile, target Target) *Plan {
	t.Helper()
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition := CompileSpec{APIVersion: CompileSpecAPIVersion, Kind: CompileSpecKind,
		Metadata: Metadata{Name: "signed-factory"}, RunsOn: "ubuntu-24.04", Catalog: CatalogStack{Base: "testdata/catalogs/kubernetes-layered"},
		UtilityContainer: "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161",
		UtilityPlatform:  "linux/amd64",
		Recipes: []RecipeTarget{{Name: "worker", Criteria: criteria.Criteria{
			Category: "kubernetes", Role: "worker", K8sVersion: "1.34.0", OS: "ubuntu", Arch: "amd64",
		}}},
		Packages: []PackageSpec{
			{Name: "tools", Kind: PackageBinaryBundle, Inputs: []string{"worker/*"}},
			{Name: "publication", Kind: PackageOCI, Inputs: []string{"package/tools"}},
		},
	}
	if signing != nil && signing.Mode == SigningModeCosign {
		definition.Tools = map[string]Tool{signing.CosignTool: {
			Format:           ToolFormatTarGzip,
			URL:              "https://example.invalid/cosign.tar.gz",
			ArchiveDigest:    testCosignArchiveDigest,
			ExecutablePath:   "cosign",
			ExecutableDigest: testCosignExecutableDigest,
		}}
	}
	delivery := ExecutionSettings{APIVersion: ExecutionSettingsAPIVersion, Kind: ExecutionSettingsKind,
		Metadata: Metadata{Name: "prod"},
		Targets:  map[string]Target{"registry": target},
		Routes:   []Route{{Name: "publication", Input: "package/publication", Target: "registry"}},
		Signing:  signing}
	plan, err := Compile(context.Background(), definition, delivery, stack, "v1.2.3 (deadbeef)")
	if err != nil {
		t.Fatalf("compiling signed plan: %v", err)
	}
	plan.DefinitionPath = "definitions/factory.yaml"
	plan.EnvironmentPath = "definitions/environment.yaml"
	return plan
}

func ghcrTarget() Target {
	return Target{Kind: TargetOCI, Ref: "ghcr.io/example/nkd-tools:latest", CredentialRef: "github:NKD_REGISTRY_AUTH"}
}

func TestCompileEmitsSigningLaneOnlyWhenTheDeliveryConfiguresIt(t *testing.T) {
	unsigned := signedTestPlan(t, nil, ghcrTarget())
	for _, node := range unsigned.Graph.Nodes {
		if signFamilyNode(node.Kind) {
			t.Fatalf("unsigned delivery emitted signing node %q", node.ID)
		}
	}
	for _, artifact := range unsigned.Graph.Artifacts {
		switch artifact.Kind {
		case ArtifactSigningRecord, ArtifactProvenancePredicate, ArtifactVerificationRecord:
			t.Fatalf("unsigned delivery emitted signing artifact %q", artifact.ID)
		}
	}
	if unsigned.CompileSpecDigest != "" {
		t.Fatal("an unsigned plan must keep its exact legacy bytes, including no definition digest")
	}

	signed := signedTestPlan(t, dryRunSigningProfile(), ghcrTarget())
	byID := map[string]Node{}
	for _, node := range signed.Graph.Nodes {
		byID[node.ID] = node
	}
	sign, ok := byID["sign/publication"]
	if !ok {
		t.Fatalf("signed delivery emitted no sign node: %v", byID)
	}
	if !contains(sign.Inputs, "publication/publication") || !contains(sign.Inputs, "package/publication") {
		t.Fatalf("sign node does not consume the published reference AND the routed layout: %+v", sign)
	}
	if !contains(sign.Outputs, "signature/publication") || !contains(sign.Outputs, "predicate/publication") {
		t.Fatalf("sign node does not produce a record and a predicate: %+v", sign)
	}
	verify, ok := byID["verify-publication/publication"]
	if !ok {
		t.Fatal("signed delivery emitted no verify-publication node")
	}
	if !contains(verify.Inputs, "publication/publication") || !contains(verify.Inputs, "signature/publication") ||
		!contains(verify.Outputs, "verification/publication") {
		t.Fatalf("verify node dataflow is incomplete: %+v", verify)
	}
	if !canonical.IsDigest(signed.CompileSpecDigest) {
		t.Fatalf("a signing plan must bind the compile-spec identity, got %q", signed.CompileSpecDigest)
	}
	// Dataflow, not authored order, must place signing after publication.
	order := map[string]int{}
	for index, node := range topologicalFactoryNodes(signed.Graph) {
		order[node.ID] = index
	}
	if !(order["publish/publication"] < order["sign/publication"] &&
		order["sign/publication"] < order["verify-publication/publication"]) {
		t.Fatalf("signing lane is not ordered after publication: %v", order)
	}
}

// The generated plan.json and the workspace-relocated plan both round-trip through
// the canonical codec, so an optional pointer field must survive it exactly.
func TestSignedPlanSurvivesTheCanonicalRoundTrip(t *testing.T) {
	plan := signedTestPlan(t, dryRunSigningProfile(), ghcrTarget())
	relocated, err := relocateFactoryPlan(plan, filepath.Join(".nkd", "factory"))
	if err != nil {
		t.Fatal(err)
	}
	if relocated.ExecutionSettings.Signing == nil {
		t.Fatal("the signing profile was dropped by the canonical round trip")
	}
	if *relocated.ExecutionSettings.Signing != *plan.ExecutionSettings.Signing {
		t.Fatalf("signing settings round-tripped as %+v, want %+v", *relocated.ExecutionSettings.Signing, *plan.ExecutionSettings.Signing)
	}
	if relocated.Qualification != plan.Qualification || relocated.CompileSpecDigest != plan.CompileSpecDigest {
		t.Fatalf("plan identity fields did not round-trip: %q/%q", relocated.Qualification, relocated.CompileSpecDigest)
	}
	before, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	after, err := relocated.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("relocating a signed plan changed its identity")
	}
}

// Relocating into a NON-default workspace genuinely rewrites artifact paths, so the
// relocated plan's byte digest legitimately changes. Its ATTESTED identity must not:
// `--workspace .nkd/a` and `--workspace .nkd/b` build the same release and must attest
// the same planDigest as the compiler and CI report. The sibling test above relocates to
// the default `.nkd/factory`, where relocation is a no-op and this cannot be observed.
func TestRelocationPreservesAttestedPlanIdentity(t *testing.T) {
	plan := signedTestPlan(t, dryRunSigningProfile(), ghcrTarget())
	compiled, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}

	first, err := relocateFactoryPlan(plan, filepath.Join(".nkd", "m1-local"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := relocateFactoryPlan(plan, filepath.Join(".nkd", "other-workspace"))
	if err != nil {
		t.Fatal(err)
	}

	// Precondition: these workspaces really are distinguishable at the byte level,
	// otherwise the assertions below would pass vacuously.
	firstBytes, err := first.Digest()
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := second.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if firstBytes == secondBytes {
		t.Fatal("two different workspaces produced identical plan bytes; the test proves nothing")
	}

	for name, relocated := range map[string]*Plan{".nkd/m1-local": first, ".nkd/other-workspace": second} {
		identity, err := relocated.IdentityDigest()
		if err != nil {
			t.Fatal(err)
		}
		if identity != compiled {
			t.Errorf("workspace %s attests plan digest %s, want the compiled identity %s",
				name, identity, compiled)
		}
	}

	// The predicate is the thing that actually escapes the run, so assert on it too.
	started := time.Unix(1787066575, 0).UTC()
	raw, err := productionPredicateFor(first, first.ExecutionSettings.Signing, started, started)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(compiled)) {
		t.Errorf("production predicate does not carry the compiled plan digest %s: %s", compiled, raw)
	}
}

func TestCompileRejectsCosignSigningWithoutAPinnedTool(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition := CompileSpec{APIVersion: CompileSpecAPIVersion, Kind: CompileSpecKind,
		Metadata: Metadata{Name: "signed-factory"}, RunsOn: "ubuntu-24.04", Catalog: CatalogStack{Base: "testdata/catalogs/kubernetes-layered"},
		UtilityContainer: "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161",
		UtilityPlatform:  "linux/amd64",
		Recipes: []RecipeTarget{{Name: "worker", Criteria: criteria.Criteria{
			Category: "kubernetes", Role: "worker", K8sVersion: "1.34.0", OS: "ubuntu", Arch: "amd64",
		}}},
		Packages: []PackageSpec{{Name: "tools", Kind: PackageBinaryBundle, Inputs: []string{"worker/*"}}},
	}
	delivery := ExecutionSettings{APIVersion: ExecutionSettingsAPIVersion, Kind: ExecutionSettingsKind,
		Metadata: Metadata{Name: "prod"},
		Targets:  map[string]Target{"registry": ghcrTarget()},
		Routes:   []Route{{Name: "tools", Input: "package/tools", Target: "registry"}},
		Signing:  cosignSigningProfile()}
	if _, err := Compile(context.Background(), definition, delivery, stack, "v1.2.3 (deadbeef)"); err == nil {
		t.Fatal("cosign signing was accepted without a pinned cosign tool")
	}
}

func TestExecutionSettingsSigningValidation(t *testing.T) {
	targets := map[string]Target{
		"oci": {Kind: TargetOCI, Ref: "ghcr.io/example/x:v1", Anonymous: true},
		"apt": {Kind: TargetAPT, Ref: "https://apt.example.invalid/nkd", Anonymous: true},
	}
	routes := []Route{{Name: "r", Input: "package/p", Target: "oci"}}
	tests := []struct {
		name    string
		signing *SigningProfile
		routes  []Route
	}{
		{name: "mode", signing: &SigningProfile{Mode: "keyless", Identity: "^x$", Issuer: "https://i"}, routes: routes},
		{name: "identity", signing: &SigningProfile{Mode: SigningModeDryRun, Issuer: "https://i"}, routes: routes},
		{name: "issuer", signing: &SigningProfile{Mode: SigningModeDryRun, Identity: "^x$"}, routes: routes},
		{name: "identity-regexp", signing: &SigningProfile{Mode: SigningModeDryRun, Identity: "^([x$", Issuer: "https://i"}, routes: routes},
		{name: "cosign-tool", signing: &SigningProfile{Mode: SigningModeCosign, Identity: "^x$", Issuer: "https://i"}, routes: routes},
		{name: "no-routes", signing: &SigningProfile{Mode: SigningModeDryRun, Identity: "^x$", Issuer: "https://i"}},
		{name: "non-oci-target", signing: &SigningProfile{Mode: SigningModeDryRun, Identity: "^x$", Issuer: "https://i"},
			routes: []Route{{Name: "r", Input: "package/p", Target: "apt"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.signing.Validate(targets, test.routes); err == nil {
				t.Fatalf("invalid signing profile %+v was accepted", test.signing)
			}
		})
	}
	valid := &SigningProfile{Mode: SigningModeDryRun, Identity: "^x$", Issuer: "https://i"}
	if err := valid.Validate(targets, routes); err != nil {
		t.Fatalf("valid signing profile rejected: %v", err)
	}
	if err := (*SigningProfile)(nil).Validate(targets, routes); err != nil {
		t.Fatalf("absent signing profile rejected: %v", err)
	}
}

// A plan is data: an executor dispatches on its node kinds, so the plan boundary
// must refuse a graph whose signing lane and signing policy disagree.
func TestPlanValidationRejectsAPublicationThatIsNotSigned(t *testing.T) {
	plan := signedTestPlan(t, dryRunSigningProfile(), ghcrTarget())
	stripped := *plan
	stripped.Graph.Nodes = nil
	for _, node := range plan.Graph.Nodes {
		if !signFamilyNode(node.Kind) {
			stripped.Graph.Nodes = append(stripped.Graph.Nodes, node)
		}
	}
	stripped.Graph.Artifacts = nil
	for _, artifact := range plan.Graph.Artifacts {
		switch artifact.Kind {
		case ArtifactSigningRecord, ArtifactProvenancePredicate, ArtifactVerificationRecord:
			continue
		}
		stripped.Graph.Artifacts = append(stripped.Graph.Artifacts, artifact)
	}
	err := stripped.Validate()
	if err == nil {
		t.Fatal("a signing delivery published without any sign node and was accepted")
	}
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTrustPolicyMissing {
		t.Fatalf("rejection = %v, want %s", err, e.CodeTrustPolicyMissing)
	}
}

func TestRenderGitHubSigningJobsCarryModeScopedPermissions(t *testing.T) {
	tests := []struct {
		name    string
		signing *SigningProfile
		sign    map[string]string
		verify  map[string]string
	}{
		{name: "dry-run", signing: dryRunSigningProfile(),
			sign:   map[string]string{"contents": "read"},
			verify: map[string]string{"contents": "read", "packages": "read"}},
		{name: "cosign", signing: cosignSigningProfile(),
			sign:   map[string]string{"contents": "read", "packages": "write", "id-token": "write"},
			verify: map[string]string{"contents": "read", "packages": "read"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := signedTestPlan(t, test.signing, ghcrTarget())
			bundle, err := RenderGitHub(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			var document githubWorkflowDocument
			if err := yaml.UnmarshalStrict(bundle.Files[GitHubWorkflowPath], &document); err != nil {
				t.Fatal(err)
			}
			for jobID, want := range map[string]map[string]string{
				githubJobID("sign/publication"):               test.sign,
				githubJobID("verify-publication/publication"): test.verify,
			} {
				job, ok := document.Jobs[jobID]
				if !ok {
					t.Fatalf("workflow has no job %q", jobID)
				}
				if len(job.Permissions) != len(want) {
					t.Fatalf("job %q permissions = %v, want %v", jobID, job.Permissions, want)
				}
				for name, value := range want {
					if job.Permissions[name] != value {
						t.Fatalf("job %q %s = %q, want %q", jobID, name, job.Permissions[name], value)
					}
				}
				if job.Env["NKD_CREDENTIAL"] != "${{ secrets.NKD_REGISTRY_AUTH }}" {
					t.Fatalf("job %q does not carry the target's configured credential reference: %v", jobID, job.Env)
				}
			}
			// A dry run must not obtain an OIDC identity token anywhere.
			if test.signing.Mode == SigningModeDryRun && bytes.Contains(bundle.Files[GitHubWorkflowPath], []byte("id-token")) {
				t.Fatalf("dry-run signing workflow requests an identity token:\n%s", bundle.Files[GitHubWorkflowPath])
			}
			if err := ValidateGitHubBundle(context.Background(), plan, bundle); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateGitHubBundleRejectsIdentityTokenOutsideTheSigningFamily(t *testing.T) {
	plan := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	original := bundle.Files[GitHubWorkflowPath]
	tests := []struct {
		name string
		old  []byte
		new  []byte
	}{
		{name: "publish-grabs-id-token",
			old: []byte("    permissions:\n      contents: read\n      packages: write\n    env:\n      NKD_CREDENTIAL"),
			new: []byte("    permissions:\n      contents: read\n      id-token: write\n      packages: write\n    env:\n      NKD_CREDENTIAL")},
		{name: "deployment-environment-removed",
			old: []byte("    environment: 'nkd-release'\n"), new: []byte("")},
		{name: "verify-grabs-id-token",
			old: []byte("      contents: read\n      packages: read\n"),
			new: []byte("      contents: read\n      id-token: write\n      packages: read\n")},
		{name: "sign-drops-id-token",
			old: []byte("      contents: read\n      id-token: write\n      packages: write\n"),
			new: []byte("      contents: read\n      packages: write\n")},
		{name: "sign-downgrades-id-token",
			old: []byte("      id-token: write\n      packages: write\n"),
			new: []byte("      id-token: read\n      packages: write\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := *bundle
			mutated.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				mutated.Files[name] = append([]byte(nil), raw...)
			}
			replaced := bytes.Replace(original, test.old, test.new, 1)
			if bytes.Equal(replaced, original) {
				t.Fatalf("mutation %q changed nothing; the rendered workflow no longer has that shape:\n%s", test.name, original)
			}
			mutated.Files[GitHubWorkflowPath] = replaced
			mutated.WorkflowDigest = pipeline.FileDigest(replaced)
			if err := ValidateGitHubBundle(context.Background(), plan, &mutated); err == nil {
				t.Fatal("tampered signing permissions passed static validation")
			}
		})
	}
}

func TestRenderGitLabRefusesSigningBeforeEmittingAnything(t *testing.T) {
	plan := signedTestPlan(t, dryRunSigningProfile(),
		Target{Kind: TargetOCI, Ref: "registry.example.invalid/nkd/tools:dev", CredentialRef: "gitlab:NKD_TOKEN"})
	bundle, err := RenderGitLab(context.Background(), plan)
	if err == nil {
		t.Fatal("GitLab silently rendered a signing plan")
	}
	if bundle != nil {
		t.Fatal("GitLab returned bytes for an unsupported signing plan")
	}
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("GitLab refusal = %v, want %s", err, e.CodeEngineCapability)
	}
	if !strings.Contains(err.Error(), "signing") {
		t.Fatalf("refusal does not name the missing capability: %v", err)
	}
}

func TestRunLocalListsTheSigningLaneAndGatesItBehindPublish(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := signedTestPlan(t, dryRunSigningProfile(), ghcrTarget())
	var withoutPublish bytes.Buffer
	result, err := RunLocal(context.Background(), plan, LocalRunOptions{
		Workspace: ".nkd/factory-dry-run", DryRun: true, Stdout: &withoutPublish,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != len(plan.Graph.Nodes) {
		t.Fatalf("dry-run listed %d of %d nodes", len(result.Nodes), len(plan.Graph.Nodes))
	}
	if result.Qualification != TierLocalExecution {
		t.Fatalf("local run record qualification = %q", result.Qualification)
	}
	listed := map[string]string{}
	for _, node := range result.Nodes {
		listed[node.ID] = node.Status
	}
	for _, id := range []string{"sign/publication", "verify-publication/publication"} {
		if listed[id] != "skipped" {
			t.Fatalf("without --publish, %s status = %q, want skipped", id, listed[id])
		}
		if !strings.Contains(withoutPublish.String(), "[skipped] "+id) {
			t.Fatalf("dry-run output does not list %s:\n%s", id, withoutPublish.String())
		}
	}
	var withPublish bytes.Buffer
	published, err := RunLocal(context.Background(), plan, LocalRunOptions{
		Workspace: ".nkd/factory-dry-run", DryRun: true, Publish: true, Stdout: &withPublish,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range published.Nodes {
		if signFamilyNode(node.Kind) && node.Status != "dry-run" {
			t.Fatalf("with --publish, %s status = %q, want dry-run", node.ID, node.Status)
		}
	}
}
