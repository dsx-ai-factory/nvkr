package ir

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

func TestRecipeIdentityFieldRulesCoverEveryGraphField(t *testing.T) {
	assertRecipeIdentityFieldRules(t, reflect.TypeOf(Node{}), reflect.TypeOf(LogicalNode{}), nodeRecipeIdentityRules)
	assertRecipeIdentityFieldRules(t, reflect.TypeOf(criteria.Criteria{}), reflect.TypeOf(LogicalCriteria{}), criteriaRecipeIdentityRules)
	assertRecipeIdentityFieldRules(t, reflect.TypeOf(Graph{}), reflect.TypeOf(LogicalGraph{}), graphRecipeIdentityRules)

	activation := reflect.TypeOf(AICRActivation{})
	logicalActivation := reflect.TypeOf(LogicalAICRActivation{})
	if activation.NumField() != logicalActivation.NumField() {
		t.Fatalf("AICR logical DTO covers %d of %d fields", logicalActivation.NumField(), activation.NumField())
	}
	for i := 0; i < activation.NumField(); i++ {
		field := activation.Field(i)
		logicalField, ok := logicalActivation.FieldByName(field.Name)
		if !ok || logicalField.Tag.Get("json") != field.Tag.Get("json") {
			t.Fatalf("AICR field %s is not explicitly represented with the same canonical key", field.Name)
		}
	}
}

func assertRecipeIdentityFieldRules(t *testing.T, full, logical reflect.Type,
	rules map[string]recipeIdentityFieldRule,
) {
	t.Helper()
	if len(rules) != full.NumField() {
		t.Fatalf("%s Recipe-identity rules have %d entries for %d fields",
			full.Name(), len(rules), full.NumField())
	}
	for i := 0; i < full.NumField(); i++ {
		field := full.Field(i)
		rule, ok := rules[field.Name]
		if !ok {
			t.Fatalf("new %s field %s has no explicit Recipe-identity rule", full.Name(), field.Name)
		}
		logicalField, represented := logical.FieldByName(field.Name)
		switch rule {
		case includeLogicalValueInRecipeIdentity, includeContentReferenceInRecipeIdentity:
			if !represented {
				t.Fatalf("T1 %s field %s is classified for identity but absent from %s", full.Name(), field.Name, logical.Name())
			}
			if logicalField.Tag.Get("json") != field.Tag.Get("json") {
				t.Fatalf("T1 %s field %s changed canonical key in %s", full.Name(), field.Name, logical.Name())
			}
		case excludeTransportFromRecipeIdentity, excludeCompositionFromRecipeIdentity,
			excludeExecutionMetadataFromRecipeIdentity:
			if represented {
				t.Fatalf("non-T1 %s field %s leaked into %s", full.Name(), field.Name, logical.Name())
			}
		default:
			t.Fatalf("%s field %s has unknown Recipe-identity rule %d", full.Name(), field.Name, rule)
		}
	}
	for name := range rules {
		if _, ok := full.FieldByName(name); !ok {
			t.Fatalf("Recipe-identity rules retain removed %s field %s", full.Name(), name)
		}
	}
}

// TestDigestHostInvariance is the ADR-02 §4 empirical purity guard for process
// environment values related to the host and clock. The IR carries no runtime-
// observed host, clock, or random field, so perturbing hostname, user, home,
// timezone, and a source-date environment value must not move Recipe identity.
// Separate tests cover pinned-input sensitivity and execution-metadata projection.
func TestDigestHostInvariance(t *testing.T) {
	before, err := sample().Digest()
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"HOSTNAME":          "builder-a.local",
		"USER":              "alice",
		"HOME":              "/home/alice",
		"TZ":                "America/Los_Angeles",
		"SOURCE_DATE_EPOCH": "9999999999", // a host env value, not the pinned node Env
		"PWD":               "/tmp/build-xyz",
	} {
		t.Setenv(k, v)
	}
	after, err := sample().Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("recipe identity moved under host/clock env mutation: %s -> %s", before, after)
	}
}

func TestAICRActivationCannotGainIdentityWithoutBothPins(t *testing.T) {
	valid := &AICRActivation{
		Service: "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training",
		RecipeDigest:    "sha256:" + strings.Repeat("a", 64),
		GeneratorDigest: "sha256:" + strings.Repeat("b", 64),
	}
	graph := sample()
	graph.Criteria.Category = "accelerated-runtime"
	graph.AICR = valid
	if _, err := graph.Digest(); err != nil {
		t.Fatalf("valid AICR activation was rejected: %v", err)
	}
	for name, mutate := range map[string]func(*AICRActivation){
		"recipe":    func(a *AICRActivation) { a.RecipeDigest = "" },
		"generator": func(a *AICRActivation) { a.GeneratorDigest = "SHA256:" + strings.Repeat("b", 64) },
		"axis":      func(a *AICRActivation) { a.Accelerator = "future-gpu" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *valid
			mutate(&changed)
			invalid := sample()
			invalid.Criteria.Category = "accelerated-runtime"
			invalid.AICR = &changed
			if _, err := invalid.Digest(); err == nil {
				t.Fatal("invalid caller-composed AICR activation gained a recipe identity")
			}
			if _, err := invalid.CanonicalJSON(); err == nil {
				t.Fatal("invalid caller-composed AICR activation entered executable IR")
			}
		})
	}
}

func sample() Graph {
	commit := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	toolchain := "golang@sha256:" + strings.Repeat("d", 64)
	vendorSpec, _ := VendorSpecIdentity(nil, toolchain)
	source := "kubernetes-" + commit[:12]
	return Graph{
		SchemaVersion: SchemaVersion,
		RecipeID:      "k8s-core-worker",
		Criteria:      criteria.Criteria{Category: "kubernetes", Role: "worker", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"},
		Nodes: []Node{
			{ID: "build-kubelet", Kind: KindBuild, Needs: []string{"fetch-kubernetes-abc"},
				Component: "kubelet", BuilderType: BuilderOCIHermetic, Toolchain: toolchain,
				Platform: "linux/amd64", Network: NetworkNone,
				Env: []EnvVar{
					{Name: "CGO_ENABLED", Value: "0"}, {Name: "GOAMD64", Value: "v1"},
					{Name: "GOARCH", Value: "amd64"}, {Name: "GOFLAGS", Value: "-trimpath -buildvcs=false"},
					{Name: "GOOS", Value: "linux"}, {Name: "GOTOOLCHAIN", Value: "local"},
					{Name: "LC_ALL", Value: "C"}, {Name: "SOURCE_DATE_EPOCH", Value: "1"}, {Name: "TZ", Value: "UTC"},
				},
				Command: []string{"make"}, Workdir: "/src", SourceDest: source,
				SourceCommit: commit, SourceTreeHash: tree, VendorSpecDigest: vendorSpec,
				SourceReadOnly: true, WriteDir: "out", OutputType: "host-package",
				ArtifactPath: "out/kubelet", Version: "1.32.0"},
			{ID: "fetch-kubernetes-abc", Kind: KindFetch, Repo: "https://x/kubernetes",
				Commit: commit, TreeHash: tree, Dest: source, VendorImage: toolchain,
				VendorSpecDigest: vendorSpec, Network: NetworkDefault},
			{ID: "verify", Kind: KindVerify, Needs: []string{"build-kubelet"}},
		},
	}
}

// NKD-FR-RC-017: identical inputs -> byte-identical serialization, regardless of
// the in-memory node/env ordering.
func TestDeterministicMarshal(t *testing.T) {
	a := sample()
	b := sample()
	// A caller must explicitly normalize before crossing the authoritative IR seam.
	b.Nodes[0], b.Nodes[2] = b.Nodes[2], b.Nodes[0]
	if _, err := b.MarshalDeterministic(); err == nil {
		t.Fatal("non-canonical node order silently gained executable bytes")
	}
	b.Normalize()
	ba, err := a.MarshalDeterministic()
	if err != nil {
		t.Fatal(err)
	}
	bb, err := b.MarshalDeterministic()
	if err != nil {
		t.Fatal(err)
	}
	if string(ba) != string(bb) {
		t.Fatalf("marshaling is order-dependent:\n--- a ---\n%s\n--- b ---\n%s", ba, bb)
	}
}

// NKD-FR-RC-018: the digest is stable across runs and changes when a pin changes.
func TestDigestStableAndSensitive(t *testing.T) {
	g := sample()
	d1, err := g.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := sample().Digest()
	if d1 != d2 {
		t.Fatalf("digest not stable: %s vs %s", d1, d2)
	}
	// change a pinned input -> digest must change.
	g2 := sample()
	g2.Nodes[1].Commit = strings.Repeat("e", 40)
	g2.Nodes[0].SourceCommit = g2.Nodes[1].Commit
	d3, _ := g2.Digest()
	if d3 == d1 {
		t.Fatal("digest did not change when a pinned commit changed")
	}
}

func TestExecutionRequirementsChangeFullGraphButNotRecipeIdentity(t *testing.T) {
	base := sample()
	baseDigest, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}
	baseBytes, err := base.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}

	requested := sample()
	requested.Nodes[0].Requirements = &execution.Requirements{
		Resources: &execution.ResourceRequest{
			CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
		},
		AllowedHostPlatforms: []execution.HostPlatform{
			execution.HostPlatformLinuxAMD64, execution.HostPlatformLinuxARM64,
		},
		Capabilities: []execution.CapabilityRequirement{{
			Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
		}},
		TimeoutSeconds: 7200,
	}
	requestedDigest, err := requested.Digest()
	if err != nil {
		t.Fatal(err)
	}
	requestedBytes, err := requested.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if requestedDigest != baseDigest {
		t.Fatalf("execution request changed recipe identity: %s != %s", requestedDigest, baseDigest)
	}
	if string(requestedBytes) == string(baseBytes) {
		t.Fatal("execution request was omitted from the full executable graph")
	}

	bound := requested
	binding, err := execution.Match(bound.Nodes[0].Requirements, []execution.Class{{
		Name: "local", RunsOn: "local", HostPlatform: execution.HostPlatformLinuxAMD64,
		Resources: execution.ResourceCapacity{
			CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 16384,
		},
		Capabilities:      []execution.CapabilityName{execution.CapabilityKVM},
		MaxTimeoutSeconds: 14400,
	}}, execution.MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bound.Nodes[0].ExecutionBinding = binding
	boundDigest, err := bound.Digest()
	if err != nil {
		t.Fatal(err)
	}
	boundBytes, err := bound.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if boundDigest != requestedDigest {
		t.Fatalf("execution binding changed recipe identity: %s != %s", boundDigest, requestedDigest)
	}
	if string(boundBytes) == string(requestedBytes) {
		t.Fatal("execution binding was omitted from the full executable graph")
	}
}

// The patch set is a pinned input: adding/altering a patch must change the digest
// (NKD-FR-RC-018/022).
func TestPatchesAffectDigest(t *testing.T) {
	base := sample()
	d0, _ := base.Digest()

	withPatch := sample()
	withPatch.Nodes[0].Patches = []PatchRef{testPatch("cve.patch", "first patch")}
	d1, _ := withPatch.Digest()
	if d1 == d0 {
		t.Fatal("adding a patch did not change the digest")
	}

	altered := sample()
	altered.Nodes[0].Patches = []PatchRef{testPatch("cve.patch", "changed patch")}
	d2, _ := altered.Digest()
	if d2 == d1 {
		t.Fatal("altering patch content (same name) did not change the digest")
	}
}

func testPatch(name, content string) PatchRef {
	sum := sha256.Sum256([]byte(content))
	return PatchRef{Name: name, SHA256: fmt.Sprintf("sha256:%x", sum), Content: content}
}
