// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

func artifactDescriptionFixture() lifecycle.ArtifactCheckDescription {
	digest := "sha256:" + strings.Repeat("a", 64)
	return lifecycle.ArtifactCheckDescription{Origin: qualificationorigin.Description{InvocationID: "https://github.com/example/nvkr/actions/runs/1/attempts/1", Repository: "example/nvkr", Commit: strings.Repeat("a", 40), Workflow: ".github/workflows/run.yml"}, Inputs: lifecycle.ExactInputDescription{ReleaseSubject: "registry.example.test/release@" + digest, PolicyDigest: digest, DefinitionDigest: digest, EnvironmentDigest: digest, PlanDigest: digest, ContentSubject: digest}, Outputs: []lifecycle.ArtifactCheckOutput{{Name: "image", Kind: lifecycle.OutputOCI, Reference: "registry.example.test/nvke@" + digest, Subject: digest, Platform: "linux/amd64"}}}
}
func deploymentFixture(t *testing.T) ([]deploymentNode, [][]deploymentPod) {
	t.Helper()
	var nodes []deploymentNode
	var pods [][]deploymentPod
	if err := json.Unmarshal([]byte(`[{"metadata":{"name":"node","uid":"node-uid"},"status":{"nodeInfo":{"operatingSystem":"linux","architecture":"amd64"}}}]`), &nodes); err != nil {
		t.Fatal(err)
	}
	raw := `[[{"metadata":{"uid":"pod-uid","namespace":"workload"},"spec":{"nodeName":"node","containers":[{"name":"nvke","image":"registry.example.test/nvke@DIGEST"}]},"status":{"phase":"Running","containerStatuses":[{"name":"nvke","ready":true,"imageID":"docker-pullable://registry.example.test/nvke@DIGEST","containerID":"containerd://container-id","restartCount":0,"state":{"running":{}}}]}}]]`
	if err := json.Unmarshal([]byte(strings.ReplaceAll(raw, "DIGEST", "sha256:"+strings.Repeat("a", 64))), &pods); err != nil {
		t.Fatal(err)
	}
	return nodes, pods
}
func TestArtifactGateRejectsCallerAuthorityBeforeIO(t *testing.T) {
	for _, receipt := range []*lifecycle.ArtifactCheckReceipt{nil, {}} {
		o := optionsFixture()
		o.Artifacts = receipt
		p, err := New(o)
		requireCode(t, err, e.CodeGateUnmet)
		if p != nil {
			t.Fatal("forged receipt admitted")
		}
		p = &Producer{options: o}
		out, err := p.Produce(t.Context())
		requireCode(t, err, e.CodeGateUnmet)
		if out.SchemaVersion != "" {
			t.Fatal("unverified receipt did work")
		}
	}
}
func TestDeploymentOutputAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*lifecycle.ArtifactCheckDescription)
		code e.Code
	}{
		{"unknown output", func(d *lifecycle.ArtifactCheckDescription) { d.Outputs[0].Name = "sibling" }, e.CodeVerifyFailed},
		{"binary", func(d *lifecycle.ArtifactCheckDescription) { d.Outputs[0].Kind = lifecycle.OutputExecutable }, e.CodeEngineCapability},
		{"missing platform", func(d *lifecycle.ArtifactCheckDescription) { d.Outputs[0].Platform = "" }, e.CodeEngineCapability},
		{"wrong subject", func(d *lifecycle.ArtifactCheckDescription) {
			d.Outputs[0].Subject = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeVerifyFailed},
		{"incomplete index", func(d *lifecycle.ArtifactCheckDescription) {
			d.Outputs[0].ImageIndex = &delivery.ImageIndexBinding{RequiredPlatforms: []string{"linux/amd64"}}
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := artifactDescriptionFixture()
			tc.edit(&d)
			_, err := selectOutputs(d, optionsFixture().Targets)
			requireCode(t, err, tc.code)
		})
	}
}
func TestDeploymentIdentityCoverage(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]deploymentNode, [][]deploymentPod)
	}{
		{"success", nil},
		{"missing replica", func(_ []deploymentNode, p [][]deploymentPod) { p[0] = nil }},
		{"PodSpec tag", func(_ []deploymentNode, p [][]deploymentPod) {
			p[0][0].Spec.Containers[0].Image = "registry.example.test/nvke:latest"
		}},
		{"runtime index or config", func(_ []deploymentNode, p [][]deploymentPod) {
			p[0][0].Status.ContainerStatuses[0].ImageID = "registry.example.test/nvke@sha256:" + strings.Repeat("b", 64)
		}},
		{"wrong repository", func(_ []deploymentNode, p [][]deploymentPod) {
			p[0][0].Status.ContainerStatuses[0].ImageID = "registry.example.test/sibling@sha256:" + strings.Repeat("a", 64)
		}},
		{"unknown scheme", func(_ []deploymentNode, p [][]deploymentPod) {
			p[0][0].Status.ContainerStatuses[0].ImageID = "containerd://sha256:" + strings.Repeat("a", 64)
		}},
		{"bare digest", func(_ []deploymentNode, p [][]deploymentPod) {
			p[0][0].Status.ContainerStatuses[0].ImageID = "sha256:" + strings.Repeat("a", 64)
		}},
		{"not ready", func(_ []deploymentNode, p [][]deploymentPod) { p[0][0].Status.ContainerStatuses[0].Ready = false }},
		{"unsafe container ID", func(_ []deploymentNode, p [][]deploymentPod) {
			p[0][0].Status.ContainerStatuses[0].ContainerID = "bad\nID"
		}},
		{"oversized pod UID", func(_ []deploymentNode, p [][]deploymentPod) { p[0][0].Metadata.UID = strings.Repeat("x", 257) }},
		{"missing restart count", func(_ []deploymentNode, p [][]deploymentPod) { p[0][0].Status.ContainerStatuses[0].RestartCount = nil }},
		{"no running state", func(_ []deploymentNode, p [][]deploymentPod) { p[0][0].Status.ContainerStatuses[0].State.Running = nil }},
		{"missing node UID", func(n []deploymentNode, _ [][]deploymentPod) { n[0].Metadata.UID = "" }},
		{"wrong native platform", func(n []deploymentNode, _ [][]deploymentPod) { n[0].Status.NodeInfo.Architecture = "arm64" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes, pods := deploymentFixture(t)
			if tc.edit != nil {
				tc.edit(nodes, pods)
			}
			outputs, err := selectOutputs(artifactDescriptionFixture(), optionsFixture().Targets)
			if err != nil {
				t.Fatal(err)
			}
			observed, subjects, err := evaluateDeployments(optionsFixture().Targets, outputs, nodes, pods)
			if tc.edit != nil {
				requireCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil || len(observed) != 1 || len(subjects) != 1 {
				t.Fatalf("observations=%v subjects=%v error=%v", observed, subjects, err)
			}
		})
	}
}
func TestDeploymentIndexNativeChild(t *testing.T) {
	d := artifactDescriptionFixture()
	out := &d.Outputs[0]
	digest := out.Subject
	out.Subject = "sha256:" + strings.Repeat("b", 64)
	out.Reference = "registry.example.test/nvke@" + out.Subject
	out.Platform = ""
	out.ImageIndex = &delivery.ImageIndexBinding{RequiredPlatforms: []string{"linux/amd64"}, Children: []delivery.ImageIndexChildBinding{{Platform: "linux/amd64", ManifestDigest: digest, Size: 100, BuildDigest: digest, Component: "nvke", BuildSubject: digest}}}
	outputs, err := selectOutputs(d, optionsFixture().Targets)
	if err != nil {
		t.Fatal(err)
	}
	nodes, pods := deploymentFixture(t)
	_, _, err = evaluateDeployments(optionsFixture().Targets, outputs, nodes, pods)
	if err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{out.Subject, "sha256:" + strings.Repeat("c", 64)} {
		wrong := "registry.example.test/nvke@" + digest
		nodes, wrongPods := deploymentFixture(t)
		wrongPods[0][0].Spec.Containers[0].Image = wrong
		wrongPods[0][0].Status.ContainerStatuses[0].ImageID = "docker-pullable://" + wrong
		_, _, err = evaluateDeployments(optionsFixture().Targets, outputs, nodes, wrongPods)
		requireCode(t, err, e.CodeVerifyFailed)
		if !strings.Contains(err.Error(), "PodSpec image differs") {
			t.Fatalf("index/config mismatch not distinguished: %v", err)
		}
	}
	nodes[0].Status.NodeInfo.Architecture = "arm64"
	_, _, err = evaluateDeployments(optionsFixture().Targets, outputs, nodes, pods)
	requireCode(t, err, e.CodeVerifyFailed)
}
func TestDeploymentDrift(t *testing.T) {
	initial := deploymentObservation{Target: 0, KubeSystemNamespaceUID: "cluster", PodUID: "pod", ContainerID: "container", NodeUID: "node", Platform: "linux/amd64", Reference: "ref", ImageID: "image", Restarts: 0}
	for _, edit := range []func(*deploymentObservation){func(o *deploymentObservation) { o.PodUID = "other" }, func(o *deploymentObservation) { o.ContainerID = "other" }, func(o *deploymentObservation) { o.NodeUID = "other" }, func(o *deploymentObservation) { o.KubeSystemNamespaceUID = "other" }, func(o *deploymentObservation) { o.Restarts++ }} {
		changed := initial
		edit(&changed)
		requireCode(t, compareDeployments([]deploymentObservation{initial}, []deploymentObservation{changed}), e.CodeVerifyFailed)
	}
	if err := compareDeployments([]deploymentObservation{initial}, []deploymentObservation{initial}); err != nil {
		t.Fatal(err)
	}
}
func TestDeploymentFailureBeforeHarnessAndDriftAfterCleanup(t *testing.T) {
	for _, mode := range []string{"deployment-wrong-image", "deployment-missing", "deployment-drift", "cluster-drift"} {
		t.Run(mode, func(t *testing.T) {
			o := buildFixture(t, mode)
			_, err := produce(t, o)
			requireCode(t, err, e.CodeVerifyFailed)
			if mode == "deployment-wrong-image" || mode == "deployment-missing" {
				if _, err := os.Stat(filepath.Join(filepath.Dir(o.Kubeconfig.Path), "state.json.startup")); !os.IsNotExist(err) {
					t.Fatalf("rejected deployment started harness: %v", err)
				}
			}
		})
	}
}

func TestDeploymentOverlappingSelectorsRefuseDuplicateCoverage(t *testing.T) {
	targets := optionsFixture().Targets
	other := targets[0]
	other.LabelSelector = "app=other"
	targets = append(targets, other)
	outputs, err := selectOutputs(artifactDescriptionFixture(), targets)
	if err != nil {
		t.Fatal(err)
	}
	nodes, pods := deploymentFixture(t)
	pods = append(pods, pods[0])
	_, _, err = evaluateDeployments(targets, outputs, nodes, pods)
	requireCode(t, err, e.CodeVerifyFailed)
}
