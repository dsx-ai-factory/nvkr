// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// This opt-in source contract runs the actual pinned reader and a separately
// built native beta CLI. Chart extraction is a fixture: it proves deployment
// handoff syntax and preserved inputs, not package builds or a live install.
func TestPinnedSiteSourceContract(t *testing.T) {
	source := os.Getenv("NVKR_NVKE_SOURCE_CONTRACT")
	if source == "" {
		t.Skip("set NVKR_NVKE_SOURCE_CONTRACT and NVKR_NVKE_SOURCE_CLI to exercise the pinned beta deployment renderer")
	}
	cli, err := resolveExisting(os.Getenv("NVKR_NVKE_SOURCE_CLI"))
	if err != nil {
		t.Fatal(err)
	}
	work := privateTestDir(t)
	runner := Runner{Work: work, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + work, "NVKECTL_RENDER_ALLOW_TEMP_OUT=1"}}
	if err = verifyInstaller(t.Context(), runner, source); err != nil {
		t.Fatal(err)
	}
	capture := sequencedChartCapture(runner)
	fixtureCapture := func(ctx context.Context, args []string, phase string, timeout time.Duration) ([]byte, error) {
		if args[0] == cli && len(args) > 2 && args[1] == "publish" {
			if err := privateWrite(args[len(args)-1], []byte("chart extraction fixture; not a built artifact")); err != nil {
				return nil, err
			}
			return []byte("{}"), nil
		}
		return capture(ctx, args, phase, timeout)
	}
	charts, pins, versions := make(map[string]string), make(map[string]string), make(map[string]string)
	for item := range chartOutputs {
		layout := filepath.Join(work, "layout-"+item)
		if err = os.Mkdir(layout, 0o700); err != nil {
			t.Fatal(err)
		}
		charts[item], pins[item] = layout, "sha256:"+strings.Repeat("a", 64)
		fields, readErr := releaseFields(t.Context(), capture, filepath.Join(source, installerDeployReader), filepath.Join(source, "deploy/nico", item, "release.yaml"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		versions[item] = fields[2]
		if versions[item] == "" {
			versions[item] = "0.22.0-beta.6"
		}
	}
	deploy, err := prepareChartInputs(t.Context(), ChartInputsConfig{NVKECheckout: source, NVKR: cli, Out: filepath.Join(work, "prepared"), Charts: charts, Manifests: pins, Versions: versions}, fixtureCapture)
	if err != nil {
		t.Fatal(err)
	}
	outputs, refs := make(map[string]SelectedOutput), make(map[string]string)
	for _, key := range imageKeys() {
		outputs[key] = SelectedOutput{Observation: lifecycle.Artifact{Digest: "sha256:" + strings.Repeat("a", 64)}}
		refs[key] = "172.18.0.3:5000/nvkr-local/" + key + "@" + outputs[key].Observation.Digest
	}
	if err = prepareRuntimeValues(t.Context(), source, deploy, "172.18.0.3:5000", outputs, capture); err != nil {
		t.Fatal(err)
	}
	if err = projectProviderImage(t.Context(), deploy, refs["provider"]); err != nil {
		t.Fatal(err)
	}
	if err = appendChartValues(t.Context(), capture, filepath.Join(source, installerDeployReader), deploy, "controller", "nvkr-profiles-image.json", profilesImageValues(refs["profiles"])); err != nil {
		t.Fatal(err)
	}
	session := ManagementSession{Name: "nvkr-source-contract", Work: work, Runner: runner}
	if err = bootstrapCA(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	spec, err := prepareStackSiteSpec(t.Context(), session, "172.18.0.2")
	if err != nil {
		t.Fatal(err)
	}
	runner, stack, err := prepareManagementStack(t.Context(), session, refs, SiteConnection{Endpoint: "http://172.30.0.1:18081", Org: "contract-org", Token: "contract-token"})
	if err != nil {
		t.Fatal(err)
	}
	rendered := filepath.Join(work, "rendered")
	if err = runner.Run(t.Context(), []string{cli, "render", "deploy-path", "--deploy-path", deploy, "--sitespec", spec, "--stack-config", stack, "--out", rendered}, "source-render-contract", time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"controller", "nktp", "postgresql", "spire", "spire-crds", "temporal"} {
		fields, readErr := releaseFields(t.Context(), capture, filepath.Join(source, installerDeployReader), filepath.Join(rendered, item, "release.yaml"))
		if readErr != nil || fields[4] != filepath.Join(deploy, "charts", item+".tgz") {
			t.Fatalf("rendered %s lost selected local chart: %v", item, readErr)
		}
	}
	for _, file := range []string{"capnico/kustomization.yaml", "controller/nvkr-profiles-image.json", "spire/nvkr-image-inputs.json", "postgresql/nvkr-image-inputs.json", "temporal/nvkr-image-inputs.json"} {
		before, readErr := os.ReadFile(filepath.Join(deploy, file))
		after, afterErr := os.ReadFile(filepath.Join(rendered, file))
		if readErr != nil || afterErr != nil || string(before) != string(after) {
			t.Fatalf("renderer changed exact input %s", file)
		}
	}
	controller, err := os.ReadFile(filepath.Join(rendered, "controller/site-values.yaml"))
	if err != nil || !strings.Contains(string(controller), "repository: 172.18.0.3:5000/nvkr-local/profiles") || !strings.Contains(string(controller), "digest: sha256:"+strings.Repeat("a", 64)) {
		t.Fatal("renderer lost selected controller/profiles images", err)
	}
	if err = runner.Run(t.Context(), []string{"bash", filepath.Join(source, "deploy/nvke-deploy.sh"), "install", "--deploy-path", rendered, "--kubeconfig", filepath.Join(work, "kubeconfig"), "--dry-run"}, "source-install-dry-run", time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedNICoSimPatchAppliesToSelectedSource(t *testing.T) {
	source := os.Getenv("NVKR_NVKE_SOURCE_CONTRACT")
	if source == "" {
		t.Skip("set NVKR_NVKE_SOURCE_CONTRACT")
	}
	work := privateTestDir(t)
	runner := Runner{Work: work, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + work}}
	if err := verifyInstaller(t.Context(), runner, source); err != nil {
		t.Fatal(err)
	}
	patch, err := filepath.Abs("../../inputs/catalogs/nicosim/patches/version-interface.patch")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), []string{"git", "-C", source, "apply", "--check", patch}, "nicosim-patch-contract", time.Minute); err != nil {
		t.Fatal(err)
	}
}
