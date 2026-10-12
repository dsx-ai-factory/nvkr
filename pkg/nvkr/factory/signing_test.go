// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"sigs.k8s.io/yaml"
)

const testCosignArchiveDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
const testCosignExecutableDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

func dryRunSigningProfile() *authored.Signing {
	return &authored.Signing{Mode: SigningModeDryRun, Identity: "^local$", Issuer: "https://nvkr.dev/local"}
}

func cosignSigningProfile() *authored.Signing {
	return &authored.Signing{Mode: SigningModeCosign,
		Identity: `^https://github\.com/example/nvkr/\.github/workflows/nvkr-factory\.yml@refs/heads/main$`,
		Issuer:   "https://token.actions.githubusercontent.com"}
}

// signedTestPlan compiles a plan whose single OCI route publishes an oci-root
// package, optionally under a signing profile. target lets a test point the route
// at an in-process registry.
func signedTestPlan(t *testing.T, signing *authored.Signing, target Target) *Plan {
	t.Helper()
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, delivery := fixtureInputs("signed-factory", "worker", "ubuntu-24.04")
	delivery.Publication = &authored.Publication{RepositoryPrefix: strings.Split(target.Ref, ":latest")[0], CredentialRef: target.CredentialRef, Anonymous: target.Anonymous, PlainHTTP: target.PlainHTTP}
	// Tests pass routes by name; public admission owns the single /releases route.
	if i := strings.LastIndex(delivery.Publication.RepositoryPrefix, ":"); i > strings.LastIndex(delivery.Publication.RepositoryPrefix, "/") {
		delivery.Publication.RepositoryPrefix = delivery.Publication.RepositoryPrefix[:i]
	}
	delivery.Signing = signing
	if signing != nil && signing.Mode == SigningModeCosign {
		definition.CosignTool = "cosign"
		if base.Tools == nil {
			base.Tools = map[string]catalog.ToolPin{}
		}
		base.Tools["cosign"] = catalog.ToolPin{Format: ToolFormatTarGzip, URL: "https://example.invalid/cosign.tar.gz", Platform: "linux/amd64", ArchiveDigest: testCosignArchiveDigest, ExecutablePath: "cosign", ExecutableDigest: testCosignExecutableDigest}
		stack, err = catalog.ApplyStack(base)
		if err != nil {
			t.Fatal(err)
		}
	}
	plan, err := Compile(context.Background(), definition, delivery, stack, "v1.2.3 (deadbeef)")
	if err != nil {
		t.Fatalf("compiling signed plan: %v", err)
	}
	plan.DefinitionPath = "definitions/factory.yaml"
	plan.EnvironmentPath = "definitions/environment.yaml"
	return plan
}

func ghcrTarget() Target {
	return Target{Kind: TargetOCI, Ref: "ghcr.io/example/nvkr-tools:latest", CredentialRef: "github:NVKR_REGISTRY_AUTH"}
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
	if !canonical.IsDigest(unsigned.DefinitionDigest) {
		t.Fatal("every plan must bind its admitted Definition")
	}

	signed := signedTestPlan(t, dryRunSigningProfile(), ghcrTarget())
	byID := map[string]Node{}
	for _, node := range signed.Graph.Nodes {
		byID[node.ID] = node
	}
	sign, ok := byID["sign/release"]
	if !ok {
		t.Fatalf("signed delivery emitted no sign node: %v", byID)
	}
	if !contains(sign.Inputs, "publication/release") || !contains(sign.Inputs, "package/release") {
		t.Fatalf("sign node does not consume the published reference AND the routed layout: %+v", sign)
	}
	if !contains(sign.Outputs, "signature/release") || !contains(sign.Outputs, "predicate/release") {
		t.Fatalf("sign node does not produce a record and a predicate: %+v", sign)
	}
	verify, ok := byID["verify-publication/release"]
	if !ok {
		t.Fatal("signed delivery emitted no verify-publication node")
	}
	if !contains(verify.Inputs, "publication/release") || !contains(verify.Inputs, "signature/release") ||
		!contains(verify.Outputs, "verification/release") {
		t.Fatalf("verify node dataflow is incomplete: %+v", verify)
	}
	if !canonical.IsDigest(signed.DefinitionDigest) {
		t.Fatalf("a signing plan must bind the Definition identity, got %q", signed.DefinitionDigest)
	}
	// Dataflow, not authored order, must place signing after publication.
	order := map[string]int{}
	for index, node := range topologicalFactoryNodes(signed.Graph) {
		order[node.ID] = index
	}
	if !(order["publish/release"] < order["sign/release"] &&
		order["sign/release"] < order["verify-publication/release"]) {
		t.Fatalf("signing lane is not ordered after publication: %v", order)
	}
}

// The generated plan.json and the workspace-relocated plan both round-trip through
// the canonical codec, so an optional pointer field must survive it exactly.
func TestSignedPlanSurvivesTheCanonicalRoundTrip(t *testing.T) {
	plan := signedTestPlan(t, dryRunSigningProfile(), ghcrTarget())
	relocated, err := relocateFactoryPlan(plan, filepath.Join(".nvkr", "factory"))
	if err != nil {
		t.Fatal(err)
	}
	if relocated.Environment.Signing == nil {
		t.Fatal("the signing profile was dropped by the canonical round trip")
	}
	if *relocated.Environment.Signing != *plan.Environment.Signing {
		t.Fatalf("signing settings round-tripped as %+v, want %+v", *relocated.Environment.Signing, *plan.Environment.Signing)
	}
	if relocated.Qualification != plan.Qualification || relocated.DefinitionDigest != plan.DefinitionDigest {
		t.Fatalf("plan identity fields did not round-trip: %q/%q", relocated.Qualification, relocated.DefinitionDigest)
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
// `--workspace .nvkr/a` and `--workspace .nvkr/b` build the same release and must attest
// the same planDigest as the compiler and CI report. The sibling test above relocates to
// the default `.nvkr/factory`, where relocation is a no-op and this cannot be observed.
func TestRelocationPreservesAttestedPlanIdentity(t *testing.T) {
	plan := signedTestPlan(t, dryRunSigningProfile(), ghcrTarget())
	compiled, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}

	first, err := relocateFactoryPlan(plan, filepath.Join(".nvkr", "m1-local"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := relocateFactoryPlan(plan, filepath.Join(".nvkr", "other-workspace"))
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

	for name, relocated := range map[string]*Plan{".nvkr/m1-local": first, ".nvkr/other-workspace": second} {
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
	raw, err := productionPredicateFor(first, first.Environment.Signing, started, started)
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
	definition, delivery := fixtureInputs("signed-factory", "worker", "ubuntu-24.04")
	definition.CosignTool = "absent"
	delivery.Publication = &authored.Publication{RepositoryPrefix: "ghcr.io/example/nvkr", Anonymous: true}
	delivery.Signing = cosignSigningProfile()
	if _, err := Compile(context.Background(), definition, delivery, stack, "v1.2.3 (deadbeef)"); err == nil {
		t.Fatal("cosign signing was accepted without a pinned cosign tool")
	}
}

func TestEnvironmentSigningValidation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		signing     *authored.Signing
		publication bool
	}{
		{"mode", &authored.Signing{Mode: "keyless", Identity: "^x$", Issuer: "https://i"}, true},
		{"identity", &authored.Signing{Mode: SigningModeDryRun, Issuer: "https://i"}, true},
		{"issuer", &authored.Signing{Mode: SigningModeDryRun, Identity: "^x$"}, true},
		{"regexp", &authored.Signing{Mode: SigningModeDryRun, Identity: "^([x$", Issuer: "https://i"}, true},
		{"no publication", &authored.Signing{Mode: SigningModeDryRun, Identity: "^x$", Issuer: "https://i"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, env := fixtureInputs("test", "worker", "")
			env.Signing = tc.signing
			if tc.publication {
				env.Publication = &authored.Publication{RepositoryPrefix: "ghcr.io/example/nvkr", Anonymous: true}
			}
			if err := env.Validate(); err == nil {
				t.Fatal("invalid signing policy admitted")
			}
		})
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
		signing *authored.Signing
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
				githubJobID("sign/release"):               test.sign,
				githubJobID("verify-publication/release"): test.verify,
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
				if job.Env["NVKR_CREDENTIAL"] != "${{ secrets.NVKR_REGISTRY_AUTH }}" {
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
			old: []byte("    permissions:\n      contents: read\n      packages: write\n    env:\n      NVKR_CREDENTIAL"),
			new: []byte("    permissions:\n      contents: read\n      id-token: write\n      packages: write\n    env:\n      NVKR_CREDENTIAL")},
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

func TestRunLocalListsTheSigningLaneAndGatesItBehindPublish(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := signedTestPlan(t, dryRunSigningProfile(), ghcrTarget())
	var withoutPublish bytes.Buffer
	result, err := RunLocal(context.Background(), plan, LocalRunOptions{
		Workspace: ".nvkr/factory-dry-run", DryRun: true, Stdout: &withoutPublish,
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
	for _, id := range []string{"sign/release", "verify-publication/release"} {
		if listed[id] != "skipped" {
			t.Fatalf("without --publish, %s status = %q, want skipped", id, listed[id])
		}
		if !strings.Contains(withoutPublish.String(), "[skipped] "+id) {
			t.Fatalf("dry-run output does not list %s:\n%s", id, withoutPublish.String())
		}
	}
	var withPublish bytes.Buffer
	published, err := RunLocal(context.Background(), plan, LocalRunOptions{
		Workspace: ".nvkr/factory-dry-run", DryRun: true, Publish: true, Stdout: &withPublish,
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
