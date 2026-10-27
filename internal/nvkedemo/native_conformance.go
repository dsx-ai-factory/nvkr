// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/conformance"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validation"
	"go.yaml.in/yaml/v3"
)

// The demo supplies descriptions to the external NVKR CLI. Only lifecycle's
// concrete local owner admits the complete captured Run and its held bytes.
type nativeConformanceConfig struct{ repository, nvkr, run string }

func selectNativeConformance(cfg Config) (*nativeConformanceConfig, error) {
	if !cfg.Conformance {
		return nil, nil
	}
	if !configString(cfg.Repository) || !configString(cfg.NVKR) {
		return nil, configUsage("conformance requires repository and nvkr")
	}
	repo, err := resolveExisting(cfg.Repository)
	if err != nil {
		return nil, err
	}
	root, err := resolveExisting(cfg.Runs.OS)
	if err != nil || cfg.Runs.OS == "" {
		return nil, configUsage("conformance requires a complete combined artifact Run")
	}
	relative, err := filepath.Rel(repo, root)
	if err != nil || !securepath.WorkspaceRelative(relative) || filepath.Clean(relative) != relative {
		return nil, configUsage("conformance Run must be inside its source repository")
	}
	// Do not accidentally test one Run while deploying a different retained Run.
	for _, view := range []string{cfg.Runs.Components, cfg.Runs.Providers, cfg.Runs.Site, cfg.Runs.Profiles, cfg.Runs.Runtime} {
		if view == "" {
			continue
		}
		absolute, err := resolveExisting(view)
		if err != nil || absolute != root {
			return nil, configUsage("conformance requires one combined artifact Run for every deployment view")
		}
	}
	binary, err := filepath.Abs(cfg.NVKR)
	if err != nil {
		return nil, err
	}
	return &nativeConformanceConfig{repository: repo, nvkr: binary, run: filepath.ToSlash(relative)}, nil
}

// Only read-only API selection happens here. The conformance owner subsequently
// requires the selected containers' runtime manifests to match the held output,
// and checks the same pod/container/node identities again after the suite.
func conformanceOVNAlias(raw []byte, nodes []tenantNode) (string, error) {
	var pods struct {
		Items []struct {
			Spec struct {
				NodeName   string
				Containers []struct{ Name, Image string }
			}
		}
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, &pods); err != nil || len(nodes) != nativeTenantNodeCount(true) || len(pods.Items) != len(nodes) {
		return "", nvkrerr.New(nvkrerr.CodeVerifyFailed, "conformance requires an OVN node pod on each of the three requested tenant nodes")
	}
	expected, seen := map[string]bool{}, map[string]bool{}
	for _, node := range nodes {
		if node.Metadata.Name == "" || expected[node.Metadata.Name] {
			return "", nvkrerr.New(nvkrerr.CodeVerifyFailed, "conformance tenant nodes are ambiguous")
		}
		expected[node.Metadata.Name] = true
	}
	alias := ""
	for _, pod := range pods.Items {
		if !expected[pod.Spec.NodeName] || seen[pod.Spec.NodeName] {
			return "", nvkrerr.New(nvkrerr.CodeVerifyFailed, "conformance OVN pods must cover distinct requested tenant nodes")
		}
		seen[pod.Spec.NodeName] = true
		matches := 0
		for _, container := range pod.Spec.Containers {
			if container.Name != "ovnkube-controller" {
				continue
			}
			matches++
			if !configString(container.Image) || (alias != "" && alias != container.Image) {
				return "", nvkrerr.New(nvkrerr.CodeVerifyFailed, "tenant OVN image aliases disagree")
			}
			alias = container.Image
		}
		if matches != 1 {
			return "", nvkrerr.New(nvkrerr.CodeVerifyFailed, "tenant OVN controller selection is ambiguous")
		}
	}
	return alias, nil
}

func (s *nativeSiteAttempt) runConformance(ctx context.Context, runner Runner, tools, out string) (result error) {
	cfg := s.conformance
	if cfg == nil {
		return nil
	}
	stage := beginProgress(runner.Progress, "native-conformance", PhaseStarted, PhaseFinished, PhaseFailed)
	defer func() { stage.finish(result) }()
	kubernetes, err := nativeKubernetesVersion(ctx, s.assets)
	if err != nil {
		return err
	}
	kubeconfig := filepath.Join(out, "tenant.kubeconfig")
	kubeBytes, err := boundedRegularRead(kubeconfig, 1<<20)
	if err != nil {
		return err
	}
	var kube struct {
		Current string `yaml:"current-context"`
	}
	if err := yaml.Unmarshal(kubeBytes, &kube); err != nil || !configString(kube.Current) {
		return configUsage("tenant kubeconfig requires its exact current context")
	}
	kubeDigest, err := DigestFile(ctx, kubeconfig)
	if err != nil {
		return err
	}
	raw, err := runner.Capture(ctx, []string{filepath.Join(tools, "kubectl"), "--kubeconfig", kubeconfig, "get", "pods", "-n", "ovn-kubernetes", "-l", "app=ovnkube-node", "-o", "json"}, "conformance-select-tenant-ovn", time.Minute)
	if err != nil {
		return err
	}
	alias, err := conformanceOVNAlias(raw, s.nodes)
	if err != nil {
		return err
	}
	_, ovnOutput, _, err := selectedOVNOutputs(s.network)
	if err != nil {
		return err
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	hydrophoneOutput, kubectlOutput := "hydrophone-arm64", "kubectl-arm64"
	if platform == "darwin/arm64" {
		hydrophoneOutput, kubectlOutput = "hydrophone-darwin-arm64", "kubectl-darwin-arm64"
	}
	local := conformance.LocalConfiguration{ToolsWorkspace: cfg.run, Runner: conformance.Options{
		Hydrophone: conformance.ExecutableSelector{Output: hydrophoneOutput, Member: "hydrophone", Platform: platform},
		Kubectl:    conformance.ExecutableSelector{Output: kubectlOutput, Member: "kubectl", Platform: platform},
		Kubeconfig: conformance.Tool{Path: kubeconfig, Digest: kubeDigest}, Context: kube.Current,
		CNINamespace: "ovn-kubernetes", CNISelector: "app=ovnkube-node", ImageVersion: kubernetes, SuiteName: "kubernetes-conformance", Role: "local", DisposableCluster: true,
		ConformanceImage: conformance.ImageSelector{Output: "conformance-image-arm64", Platform: "linux/arm64"},
		Busybox:          conformance.ImageSelector{Output: "busybox-image-arm64", Platform: "linux/arm64"},
		Targets:          []conformance.DeploymentTarget{{Output: ovnOutput, Namespace: "ovn-kubernetes", LabelSelector: "app=ovnkube-node", Container: "ovnkube-controller", RequiredReplicas: nativeTenantNodeCount(true)}},
	}, Bindings: conformance.LocalBindings{ProductImages: []conformance.LocalImageBinding{{Output: ovnOutput, Reference: alias}}}}
	timeout, err := remainingWork(ctx)
	if err != nil {
		return err
	}
	local.Timeouts.Run, local.Timeouts.Startup = timeout.String(), timeout.String()
	_, run, err := lifecycle.InspectLocalRunInputs(ctx, filepath.Join(cfg.repository, cfg.run))
	if err != nil {
		return err
	}
	for _, tool := range []struct{ output, repository string }{{"conformance-image-arm64", "registry.k8s.io/conformance"}, {"busybox-image-arm64", "registry.k8s.io/e2e-test-images/busybox"}} {
		observed, err := selectedRunOutput(run, tool.output, "oci")
		if err != nil {
			return err
		}
		local.Bindings.ToolImages = append(local.Bindings.ToolImages, conformance.LocalImageBinding{Output: tool.output, Reference: tool.repository + "@" + observed.Digest})
	}
	configPath := filepath.Join(out, "conformance-config.json")
	profilePath := filepath.Join(out, "validation-profile.json")
	if err := privateJSON(configPath, local); err != nil {
		return err
	}
	profile := validation.Configuration{Source: validation.Source{Kind: "local", Workspace: cfg.run}, Checks: []validation.Check{{Name: "tenant-conformance", Suite: "kube-conformance", Harness: "hydrophone", Config: configPath}}}
	if err := privateJSON(profilePath, profile); err != nil {
		return err
	}
	reports := filepath.Join(out, "conformance")
	if err := os.Mkdir(reports, 0700); err != nil {
		return err
	}
	if runner.Progress != nil {
		fmt.Fprintln(runner.Progress, "Starting conformance validation for this tenant; artifact/tool admission precedes test execution. Original JUnit and before/after API observations will be retained.")
	}
	// Run capture snapshots retained disks before starting the harness. Keep its
	// temporary files on the attempt's output filesystem; the factory still
	// admits disk backing and headroom instead of trusting the path alone.
	scratch := filepath.Join(out, "conformance-tmp")
	if err := os.Mkdir(scratch, 0700); err != nil {
		return err
	}
	invocation := nativeConformanceInvocation(runner, cfg.repository, scratch)
	raw, runErr := invocation.Capture(ctx, []string{cfg.nvkr, "validate", "--config", profilePath, "--workspace", reports}, "tenant-conformance", 0)
	// Capture stdout even when the harness exits unsuccessfully. The original
	// reports are retained by NVKR; this wrapper does not rewrite their verdicts.
	if len(raw) > 0 {
		if err := privateWrite(filepath.Join(out, "conformance-output.json"), raw); err != nil {
			return errors.Join(runErr, err)
		}
	}
	if runErr != nil {
		return runErr
	}
	if err := checkConformanceCapture(raw); err != nil {
		return err
	}
	if runner.Progress != nil {
		fmt.Fprintf(runner.Progress, "Conformance execution and report capture completed. Original results: %s\n", reports)
	}
	return nil
}

func nativeConformanceInvocation(runner Runner, repository, scratch string) Runner {
	runner.directory = repository
	runner.Env = SetEnv(runner.Env, map[string]string{"TMPDIR": scratch})
	return runner
}

func checkConformanceCapture(raw []byte) error {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeVerifyFailed, err, "conformance output requires one JSON document")
	}
	var outputs []testresults.Output
	if err := json.Unmarshal(raw, &outputs); err != nil || len(outputs) != 1 {
		return nvkrerr.New(nvkrerr.CodeVerifyFailed, "conformance output requires one captured suite")
	}
	output := outputs[0]
	if err := output.Validate(); err != nil {
		return err
	}
	if output.CaptureStatus != testresults.CaptureComplete || len(output.Reports) == 0 || output.Binding == nil || len(output.Binding.Subjects) == 0 {
		return nvkrerr.New(nvkrerr.CodeVerifyFailed, "conformance capture is incomplete")
	}
	observedExit := false
	for _, fact := range output.Environment {
		if fact.Scope == "harness" && fact.Name == "process.exitCode" && fact.Phase == "after" {
			if observedExit || fact.Metadata.Status != "observed" || fact.Metadata.Value != "0" {
				return nvkrerr.New(nvkrerr.CodeVerifyFailed, "conformance harness did not exit successfully")
			}
			observedExit = true
		}
	}
	if !observedExit {
		return nvkrerr.New(nvkrerr.CodeVerifyFailed, "conformance harness exit is unavailable")
	}
	for _, report := range output.Reports {
		if report.Format != testresults.FormatJUnit || report.ByteLength <= 0 || strings.TrimSpace(report.Reference) == "" {
			return nvkrerr.New(nvkrerr.CodeVerifyFailed, "original conformance JUnit unavailable")
		}
	}
	// Capture and process exit are diagnostic facts. Suite interpretation remains
	// with the original upstream report; no qualification or signed verdict exists.
	return nil
}
