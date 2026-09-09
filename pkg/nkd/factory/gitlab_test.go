package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
)

func gitlabTestPlan(t *testing.T) *Plan {
	t.Helper()
	plan := githubTestPlan(t)
	target := plan.ExecutionSettings.Targets["registry"]
	target.Ref = "registry.gitlab.com/example/nkd-tools:latest"
	target.CredentialRef = "gitlab:NKD_REGISTRY_AUTH"
	plan.ExecutionSettings.Targets["registry"] = target
	digest, err := plan.ExecutionSettings.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.ExecutionSettingsDigest = digest
	plan.EnvironmentPath = "examples/environment-gitlab.yaml"
	return plan
}

func TestRenderGitLabProducesStaticallyValidCompleteBundle(t *testing.T) {
	plan := gitlabTestPlan(t)
	bundle, err := Render(context.Background(), plan, EngineGitLab)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Engine != EngineGitLab || bundle.WorkflowPath != GitLabWorkflowPath ||
		bundle.ManifestPath != GitLabManifestPath {
		t.Fatalf("GitLab bundle identity = %+v", bundle)
	}
	workflow := bundle.Files[GitLabWorkflowPath]
	for _, required := range [][]byte{
		[]byte("qualification: " + TierStaticUnqualified.String()),
		// IR build/fetch nodes share the pipeline emitter's Model-B per-node primitive.
		[]byte("exec-node --launch --runtime podman"),
		[]byte("factory execute-node"),
		[]byte("transport-pack"),
		[]byte("transport-unpack"),
		[]byte("NKD_CREDENTIAL: '$NKD_REGISTRY_AUTH'"),
		[]byte("run generate --engine gitlab"),
	} {
		if !bytes.Contains(workflow, required) {
			t.Fatalf("GitLab workflow omits %q\n%s", required, workflow)
		}
	}
	if err := ValidateGeneratedBundle(context.Background(), plan, bundle); err != nil {
		t.Fatal(err)
	}
}

func TestValidateGitLabBundleRejectsIsolationAndTransportTampering(t *testing.T) {
	plan := gitlabTestPlan(t)
	bundle, err := RenderGitLab(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	original := bundle.Files[GitLabWorkflowPath]
	for _, test := range []struct {
		name string
		old  []byte
		new  []byte
	}{
		{name: "network-runtime", old: []byte("--network none"), new: []byte("--network bridge")},
		{name: "launch", old: []byte("exec-node --launch"), new: []byte("exec-node")},
		{name: "transport", old: []byte("transport-pack"), new: []byte("transport-skip")},
		{name: "unconditional-verify", old: []byte("when: always"), new: []byte("when: on_success")},
		{name: "regeneration-engine", old: []byte("--engine gitlab"), new: []byte("--engine github")},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := *bundle
			mutated.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				mutated.Files[name] = append([]byte(nil), raw...)
			}
			mutated.Files[GitLabWorkflowPath] = bytes.Replace(original, test.old, test.new, 1)
			mutated.WorkflowDigest = pipeline.FileDigest(mutated.Files[GitLabWorkflowPath])
			if err := ValidateGitLabBundle(context.Background(), plan, &mutated); err == nil {
				t.Fatal("tampered GitLab workflow passed semantic validation")
			}
		})
	}
}

func TestWriteGeneratedGitLabBundleIsRerunnableAndDetectsDrift(t *testing.T) {
	plan := gitlabTestPlan(t)
	bundle, err := RenderGitLab(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for iteration := 0; iteration < 2; iteration++ {
		if err := WriteGeneratedBundle(context.Background(), root, bundle); err != nil {
			t.Fatalf("write %d: %v", iteration, err)
		}
		if err := CheckGeneratedBundle(context.Background(), root, bundle); err != nil {
			t.Fatalf("check %d: %v", iteration, err)
		}
	}
	workflow := filepath.Join(root, filepath.FromSlash(GitLabWorkflowPath))
	if err := os.WriteFile(workflow, []byte("hand edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneratedBundle(context.Background(), root, bundle); err == nil {
		t.Fatal("hand-edited GitLab workflow passed drift check")
	}
}

func TestGenerationEngineAdmission(t *testing.T) {
	for _, supported := range []string{EngineGitHub, EngineGitLab} {
		if err := ValidateGenerationEngine(supported); err != nil {
			t.Fatalf("supported engine %s rejected: %v", supported, err)
		}
	}
	for _, unavailable := range []string{EngineTekton, EngineArgo} {
		if err := ValidateGenerationEngine(unavailable); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("unavailable engine %s did not fail honestly: %v", unavailable, err)
		}
	}
	if err := ValidateGenerationEngine("jenkins"); err == nil {
		t.Fatal("unknown engine accepted")
	}
}
