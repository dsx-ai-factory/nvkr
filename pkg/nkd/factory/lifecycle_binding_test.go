package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func lifecyclePackageFixture(t *testing.T, kind string) (*Plan, map[string]Artifact) {
	t.Helper()
	root := t.TempDir()
	rawPath := filepath.Join(root, "tool")
	if err := os.WriteFile(rawPath, []byte("tool payload\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := digestFactoryPath(context.Background(), rawPath)
	if err != nil {
		t.Fatal(err)
	}
	record, err := canonical.Marshal(pipeline.ArtifactResult{Component: "tool", SHA256: digest, Size: 13})
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "result.json")
	if err := os.WriteFile(recordPath, record, 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts := map[string]Artifact{
		"raw":     {ID: "raw", Name: "tool", Kind: "host-package", BuildTarget: "tools", Component: "tool", Path: rawPath},
		"result":  {ID: "result", Kind: "producer-result", BuildTarget: "tools", Component: "tool", Path: recordPath},
		"package": {ID: "package", Name: "tools", Kind: lifecyclePackageKind(kind), Producer: "package", Path: filepath.Join(root, "package")},
	}
	node := Node{ID: "package", Kind: NodePackage, PackageKind: lifecyclePackageKind(kind), Inputs: []string{"raw"}, Outputs: []string{"package"}, Distribution: "stable", PackageComponent: "main"}
	buildDigest := "sha256:" + strings.Repeat("a", 64)
	plan := &Plan{Builds: []BuildPlan{{Name: "tools", BuildDigest: buildDigest, Platform: "linux/amd64",
		Graph: &ir.Graph{Nodes: []ir.Node{{ID: "build", Kind: ir.KindBuild, Component: "tool", Version: "1.0.0", Platform: "linux/amd64", Env: []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1733939955"}}}}}}},
		Graph:            ArtifactGraph{Nodes: []Node{node}},
		LifecycleOutputs: []LifecycleOutputPlan{{Name: "tools", Kind: kind, ArtifactID: "package", BuildDigest: buildDigest, Components: []string{"tool"}}},
	}
	for _, id := range []string{"raw", "result", "package"} {
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, artifacts[id])
	}
	if err := executePackage(context.Background(), plan, node, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	return plan, artifacts
}

func TestLifecyclePackageBindsExactProducerEvidence(t *testing.T) {
	for _, kind := range []string{"executable", "apt-repository"} {
		t.Run(kind, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				mutate func(*Plan, map[string]Artifact)
				code   e.Code
			}{
				{name: "exact"},
				{name: "equal digest other use", mutate: func(p *Plan, _ map[string]Artifact) {
					duplicate := p.Builds[0]
					duplicate.Name = "another-output"
					p.Builds = append(p.Builds, duplicate)
				}},
				{name: "missing package", mutate: func(_ *Plan, a map[string]Artifact) { delete(a, "package") }, code: e.CodeReleaseIncomplete},
				{name: "wrong package kind", mutate: func(_ *Plan, a map[string]Artifact) { v := a["package"]; v.Kind = "unknown"; a["package"] = v }, code: e.CodeReleaseIncomplete},
				{name: "missing package node", mutate: func(p *Plan, _ map[string]Artifact) { p.Graph.Nodes = nil }, code: e.CodeReleaseIncomplete},
				{name: "missing build", mutate: func(p *Plan, _ map[string]Artifact) { p.Builds = nil }, code: e.CodeReleaseIncomplete},
				{name: "ambiguous build", mutate: func(p *Plan, _ map[string]Artifact) { p.Builds = append(p.Builds, p.Builds[0]) }, code: e.CodeVerifyFailed},
				{name: "wrong component", mutate: func(p *Plan, _ map[string]Artifact) { p.LifecycleOutputs[0].Components = []string{"other"} }, code: e.CodeVerifyFailed},
				{name: "missing component", mutate: func(p *Plan, _ map[string]Artifact) { p.LifecycleOutputs[0].Components = nil }, code: e.CodeVerifyFailed},
			} {
				t.Run(tc.name, func(t *testing.T) {
					plan, artifacts := lifecyclePackageFixture(t, kind)
					if tc.mutate != nil {
						tc.mutate(plan, artifacts)
					}
					got, err := ObserveLifecyclePayload(context.Background(), plan, plan.LifecycleOutputs[0], artifacts)
					if tc.code != "" {
						assertFactoryErrorCode(t, err, tc.code)
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if !canonical.IsDigest(got.Digest) || !reflect.DeepEqual(got.Components, []string{"tool"}) {
						t.Fatalf("observation = %+v", got)
					}
				})
			}
		})
	}
}

func TestLifecyclePublicationRequiresDeclaredEvidence(t *testing.T) {
	for _, kind := range []string{"executable", "apt-repository"} {
		t.Run(kind, func(t *testing.T) {
			plan, artifacts := lifecyclePackageFixture(t, kind)
			ctx := context.Background()
			node := Node{Inputs: []string{"raw", "result", "package"}}
			binding, err := lifecyclePublicationBinding(ctx, plan, node, artifacts)
			if err != nil {
				t.Fatal(err)
			}
			if binding.SchemaVersion != LifecycleBindingSchema || len(binding.Outputs) != 1 || !canonical.IsDigest(binding.ContentSubject) {
				t.Fatalf("binding = %+v", binding)
			}

			observed, err := ObserveLifecyclePayload(ctx, plan, plan.LifecycleOutputs[0], artifacts)
			if err != nil {
				t.Fatal(err)
			}
			if binding.Outputs[0].Digest != observed.Digest {
				t.Fatal("publication changed observed digest")
			}
			if kind == "apt-repository" && binding.Outputs[0].TransportPath != artifacts["package"].Path {
				t.Fatal("APT transport path lost")
			}
			node.Inputs = []string{"raw", "package"}
			_, err = lifecyclePublicationBinding(ctx, plan, node, artifacts)
			want := e.CodeVerifyFailed
			if kind == "apt-repository" {
				want = e.CodeReleaseIncomplete
			}
			assertFactoryErrorCode(t, err, want)
		})
	}
}

func TestLifecycleOutputsRejectChangedJoins(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Plan, *[]LifecycleBoundOutput)
		code   e.Code
	}{
		{name: "exact"},
		{name: "duplicate output", change: func(_ *Plan, o *[]LifecycleBoundOutput) { *o = append(*o, (*o)[0]) }, code: e.CodeVerifyFailed},
		{name: "missing output", change: func(_ *Plan, o *[]LifecycleBoundOutput) { *o = nil }, code: e.CodeReleaseIncomplete},
		{name: "wrong output name", change: func(_ *Plan, o *[]LifecycleBoundOutput) { (*o)[0].Name = "other" }, code: e.CodeVerifyFailed},
		{name: "wrong build", change: func(_ *Plan, o *[]LifecycleBoundOutput) { (*o)[0].BuildDigest = "sha256:" + strings.Repeat("b", 64) }, code: e.CodeVerifyFailed},
		{name: "wrong component", change: func(_ *Plan, o *[]LifecycleBoundOutput) { (*o)[0].Components = []string{"other"} }, code: e.CodeVerifyFailed},
		{name: "invalid digest", change: func(_ *Plan, o *[]LifecycleBoundOutput) { (*o)[0].Digest = "mutable" }, code: e.CodeVerifyFailed},
		{name: "unknown kind", change: func(_ *Plan, o *[]LifecycleBoundOutput) { (*o)[0].Kind = "unknown" }, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &Plan{Builds: []BuildPlan{{BuildDigest: "sha256:" + strings.Repeat("a", 64)}}, LifecycleOutputs: []LifecycleOutputPlan{{Name: "tools", Kind: "executable", BuildDigest: "sha256:" + strings.Repeat("a", 64), Components: []string{"tool"}}}}
			observed := []LifecycleBoundOutput{{Name: "tools", Kind: "executable", Digest: "sha256:" + strings.Repeat("c", 64), BuildDigest: plan.Builds[0].BuildDigest, Components: []string{"tool"}}}
			if tc.change != nil {
				tc.change(plan, &observed)
			}
			err := ValidateLifecycleOutputs(plan.LifecycleOutputs, observed)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
