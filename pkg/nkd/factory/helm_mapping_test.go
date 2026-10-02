package factory

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/documentpath"
	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

// Two independent image slots exercise named selection shared with catalog pin
// sets. The chart consumes digests through its original templates.
func mappingFixture(t *testing.T) (string, HelmImageBinding, map[string]string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Chart.yaml":  "apiVersion: v2\nname: controller\nversion: 1.0.0\n",
		"values.yaml": "controller: {repository: upstream/controller, tag: dev}\nhelper: {repository: upstream/helper, tag: dev}\nimages: [{name: controller, digest: ''}, {name: helper, digest: ''}]\n",
		"templates/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata: {name: controller}
spec:
  template:
    spec:
      containers:
        - name: controller
          image: "{{ .Values.controller.repository }}@{{ (index .Values.images 0).digest }}"
        - name: helper
          image: "{{ .Values.helper.repository }}@{{ (index .Values.images 1).digest }}"
`,
	}
	for file, raw := range files {
		if err := os.WriteFile(filepath.Join(root, file), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	binding := HelmImageBinding{Chart: ".", Images: map[string]HelmImage{}}
	digests := map[string]string{}
	for i, slot := range []string{"controller", "helper"} {
		binding.Images[slot] = HelmImage{From: "package/" + slot, Values: catalog.HelmImageValues{Digest: "images[name=" + slot + "].digest"}}
		digests[slot] = "sha256:" + strings.Repeat(string(rune('a'+i)), 64)
	}
	return root, binding, digests
}

func TestHelmMappedBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(string, *HelmImageBinding, map[string]string)
		code   e.Code
	}{
		{name: "multiple images"},
		{name: "missing digest", mutate: func(_ string, _ *HelmImageBinding, d map[string]string) { delete(d, "helper") }, code: e.CodeVerifyFailed},
		{name: "invalid digest", mutate: func(_ string, _ *HelmImageBinding, d map[string]string) { d["helper"] = "latest" }, code: e.CodeVerifyFailed},
		{name: "missing value", mutate: func(_ string, b *HelmImageBinding, _ map[string]string) {
			s := b.Images["helper"]
			s.Values.Digest = "missing.digest"
			b.Images["helper"] = s
		}, code: e.CodeVerifyFailed},
		{name: "same value target", mutate: func(_ string, b *HelmImageBinding, _ map[string]string) {
			s := b.Images["helper"]
			s.Values.Digest = b.Images["controller"].Values.Digest
			b.Images["helper"] = s
		}, code: e.CodeInvalidCatalog},
		{name: "linked template", mutate: func(root string, _ *HelmImageBinding, _ map[string]string) {
			if err := os.Symlink("deployment.yaml", filepath.Join(root, "templates", "alias.yaml")); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, b, d := mappingFixture(t)
			before, _ := os.ReadFile(filepath.Join(root, "templates/deployment.yaml"))
			valuesBefore, _ := os.ReadFile(filepath.Join(root, "values.yaml"))
			if tc.mutate != nil {
				tc.mutate(root, &b, d)
			}
			err := bindHelmImages(t.Context(), root, b, d)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				after, _ := os.ReadFile(filepath.Join(root, "templates/deployment.yaml"))
				valuesAfter, _ := os.ReadFile(filepath.Join(root, "values.yaml"))
				if !bytes.Equal(before, after) || !bytes.Equal(valuesBefore, valuesAfter) {
					t.Fatal("failure partially bound source")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			bound, _ := os.ReadFile(filepath.Join(root, "templates/deployment.yaml"))
			if !bytes.Equal(before, bound) {
				t.Fatal("packaging rewrote the template")
			}
			values, _ := os.ReadFile(filepath.Join(root, "values.yaml"))
			doc, err := documentpath.Decode(values)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := documentpath.Select(doc, []string{"controller.repository", "controller.tag", "helper.repository", "helper.tag", "images[name=controller].digest", "images[name=helper].digest"}, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, slot := range []string{"controller", "helper"} {
				if selected[slot+".repository"].Value != "upstream/"+slot || selected[slot+".tag"].Value != "dev" || selected["images[name="+slot+"].digest"].Value != d[slot] {
					t.Fatalf("incorrect value binding for %s", slot)
				}
			}
		})
	}
}

// Enable explicitly so ordinary contract tests do not claim Helm-client evidence.
func TestMappedBindingsWithHelm(t *testing.T) {
	if os.Getenv("NKD_TEST_HELM_BINDINGS") != "1" {
		t.Skip("set NKD_TEST_HELM_BINDINGS=1 for real Helm rendering")
	}
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal(err)
	}
	root, b, d := mappingFixture(t)
	if err := bindHelmImages(t.Context(), root, b, d); err != nil {
		t.Fatal(err)
	}
	before, err := digestFactoryPath(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, registry := range []string{"registry.operator-a.test/nke", "mirror.operator-b.test/platform"} {
		out, err := exec.CommandContext(t.Context(), helm, "template", "demo", root, "--set-string", "controller.repository="+registry+"/cwe", "--set-string", "helper.repository="+registry+"/helper").CombinedOutput()
		if err != nil {
			t.Fatalf("Helm: %v\n%s", err, out)
		}
		for slot, digest := range d {
			name := slot
			if slot == "controller" {
				name = "cwe"
			}
			if err := verifyRenderedContainerImage(out, slot, registry+"/"+name, digest); err != nil {
				t.Fatal(err)
			}
		}
	}
	after, err := digestFactoryPath(t.Context(), root)
	if err != nil || after != before {
		t.Fatal("repository overrides changed packaged chart contents")
	}
}

// This opt-in test uses an unmodified chart extracted from a recorded NKE commit.
func TestNKEDeclarativeBindingWithHelm(t *testing.T) {
	source := os.Getenv("NKD_NKE_CHART")
	if source == "" {
		t.Skip("set NKD_NKE_CHART to the pinned upstream controller chart")
	}
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal(err)
	}
	def, err := definition.LoadDefinition(t.Context(), filepath.Join("..", "..", "..", "docs", "examples", "nke-controller-bindings", "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	copied := filepath.Join(workspace, "controller")
	if err := os.CopyFS(copied, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(workspace, "source.tar")
	if err := transportarchive.Pack(t.Context(), archive, workspace, []string{"controller"}); err != nil {
		t.Fatal(err)
	}
	root, relative, cleanup, err := helmPackageTree(t.Context(), Artifact{Kind: "tarball", BuildTarget: "chart", Path: archive})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	chart := filepath.Join(root, relative)
	rawRegistry, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "examples", "nke-controller-bindings", "registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Components map[string]struct {
			Helm *catalog.HelmPackaging `json:"helm"`
		} `json:"components"`
	}
	if err := yaml.Unmarshal(rawRegistry, &registry); err != nil {
		t.Fatal(err)
	}
	outputIndex := -1
	for i, output := range def.Outputs {
		if output.Name == "controller-chart" {
			outputIndex = i
			break
		}
	}
	if outputIndex < 0 {
		t.Fatal("example is missing the controller-chart output")
	}
	output := def.Outputs[outputIndex]
	binding, err := resolveHelmBinding(output, []BuildPlan{{Name: output.Name, ResolvedComponents: []catalog.NamedComponent{{Name: "controller-chart", Component: catalog.Component{Helm: registry.Components["controller-chart"].Helm}}}}})
	if err != nil {
		t.Fatal(err)
	}
	templatesBefore, err := digestFactoryPath(t.Context(), filepath.Join(chart, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if err := bindHelmImages(t.Context(), chart, *binding, map[string]string{"controller": digest}); err != nil {
		t.Fatal(err)
	}
	templatesAfter, err := digestFactoryPath(t.Context(), filepath.Join(chart, "templates"))
	if err != nil || templatesBefore != templatesAfter {
		t.Fatal("upstream templates changed")
	}
	before, err := digestFactoryPath(t.Context(), chart)
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"registry.operator-a.test/nke/cwe", "mirror.operator-b.test/platform/cwe"} {
		out, err := exec.CommandContext(t.Context(), helm, "template", "controller", chart, "--set-string", "nodeAgent.spireAttestationMethod=tpm", "--set-string", "image.repository="+repository, "--set-string", "nameOverride=nkd-binding-controller").CombinedOutput()
		if err != nil {
			t.Fatalf("rendering pinned NKE chart: %v\n%s", err, out)
		}
		if err := verifyRenderedControllerImage(out, repository, digest); err != nil {
			t.Fatal(err)
		}
	}
	after, err := digestFactoryPath(t.Context(), chart)
	if err != nil || before != after {
		t.Fatal("registry overrides changed the chart")
	}
	t.Logf("real NKE chart rendered for two repositories with values digest; bound tree %s", before)
}

func TestMappedImageRejectsDecoyDigest(t *testing.T) {
	for _, slot := range []string{"controller", "helper"} {
		t.Run(slot, func(t *testing.T) {
			repository := "registry.example.test/" + slot
			digest := "sha256:" + strings.Repeat("a", 64)
			raw := "kind: Deployment\nmetadata:\n  annotations:\n    expected: '" + repository + "@" + digest + "'\nspec:\n  template:\n    spec:\n      containers:\n      - name: " + slot + "\n        image: " + repository + ":dev\n"
			if err := verifyRenderedContainerImage([]byte(raw), slot, repository, digest); err == nil {
				t.Fatal("digest in annotation accepted as container image")
			}
		})
	}
}
