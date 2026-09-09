package ir

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
)

func TestLogicalIdentityDTOClassifiesEveryExecutionField(t *testing.T) {
	assertIdentityClassification(t, reflect.TypeOf(Node{}), reflect.TypeOf(LogicalNode{}), nodeIdentityFieldClass)
	assertIdentityClassification(t, reflect.TypeOf(criteria.Criteria{}), reflect.TypeOf(LogicalCriteria{}), criteriaIdentityFieldClass)
	assertIdentityClassification(t, reflect.TypeOf(Graph{}), reflect.TypeOf(LogicalGraph{}), graphIdentityFieldClass)

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

func assertIdentityClassification(t *testing.T, execution, logical reflect.Type,
	classification map[string]identityFieldClass,
) {
	t.Helper()
	if len(classification) != execution.NumField() {
		t.Fatalf("%s identity classification has %d entries for %d execution fields",
			execution.Name(), len(classification), execution.NumField())
	}
	for i := 0; i < execution.NumField(); i++ {
		field := execution.Field(i)
		class, ok := classification[field.Name]
		if !ok {
			t.Fatalf("new %s field %s has no explicit recipe-identity classification", execution.Name(), field.Name)
		}
		logicalField, represented := logical.FieldByName(field.Name)
		switch class {
		case identityLogical, identityContentReference:
			if !represented {
				t.Fatalf("T1 %s field %s is classified for identity but absent from %s", execution.Name(), field.Name, logical.Name())
			}
			if logicalField.Tag.Get("json") != field.Tag.Get("json") {
				t.Fatalf("T1 %s field %s changed canonical key in %s", execution.Name(), field.Name, logical.Name())
			}
		case identityTransport, identityComposition:
			if represented {
				t.Fatalf("non-T1 %s field %s leaked into %s", execution.Name(), field.Name, logical.Name())
			}
		default:
			t.Fatalf("%s field %s has unknown identity classification %d", execution.Name(), field.Name, class)
		}
	}
	for name := range classification {
		if _, ok := execution.FieldByName(name); !ok {
			t.Fatalf("identity classification retains removed %s field %s", execution.Name(), name)
		}
	}
}

// TestDigestHostInvariance is the ADR-02 §3 empirical purity guard (the
// "mutate host/clock/builder facts → identity byte-stable" direction). The IR
// carries no host/clock/random field by construction, so perturbing the process
// environment — hostname, user, home, timezone, a fake clock var — must not move
// the recipe identity by a single byte. The complementary "mutate a pinned input
// → identity changes" direction is TestDigestStableAndSensitive.
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
