// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

func startupTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(t.Context(), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	t.Cleanup(cancel)
	return ctx
}

func startupDescription() NativeSiteStartup {
	cfg := NativeSiteStartup{
		Name: "nvke-native-site", Gateway: "172.30.0.1", KubernetesVersion: "1.34.2",
		ControllerImage: "docker.io/library/nke-controller:latest",
		ProviderImage:   "docker.io/library/cluster-api-provider-provisioned:latest",
		OVNImage:        "localhost/nvke/ovn-kubernetes@sha256:" + strings.Repeat("a", 64),
		ImageImports: map[string]string{
			"controller": "docker.io/library/nke-controller@sha256:" + strings.Repeat("a", 64),
			"profiles":   "docker.io/library/cwe-profiles@sha256:" + strings.Repeat("a", 64),
			"capnico":    "ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller@sha256:" + strings.Repeat("a", 64),
		},
	}
	for _, item := range siteStartupAssetNames {
		cfg.Assets = append(cfg.Assets, StartupAsset{item, "http://172.30.0.1:18080/files/owned/" + item, "sha256:" + strings.Repeat("a", 64)})
	}
	return cfg
}

func addStartupRuntimeImages(cfg *NativeSiteStartup) {
	for _, role := range nativeRuntimeImageRoles() {
		file := role + ".tar"
		cfg.Assets = append(cfg.Assets, StartupAsset{file, "http://172.30.0.1:18080/files/" + file, "sha256:" + strings.Repeat("a", 64)})
		reference := "docker.io/nvkr-local/" + strings.TrimPrefix(role, "runtime-") + "@sha256:" + strings.Repeat("a", 64)
		if role == "workload" {
			reference = "example.invalid/selected-workload@sha256:" + strings.Repeat("b", 64)
		}
		cfg.ImageImports[role] = reference
	}
}

func TestSiteStartupRuntimeImagesRequireTheWholeSelectedClosure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*NativeSiteStartup)
	}{
		{"complete", nil},
		{"missing archive", func(c *NativeSiteStartup) { c.Assets = c.Assets[:len(c.Assets)-1] }},
		{"missing reference", func(c *NativeSiteStartup) { delete(c.ImageImports, "runtime-postgresql") }},
		{"unknown role", func(c *NativeSiteStartup) { c.ImageImports["unexpected"] = c.ImageImports["runtime-postgresql"] }},
		{"sibling image", func(c *NativeSiteStartup) {
			c.ImageImports["runtime-postgresql"] = c.ImageImports["runtime-spire-agent"]
		}},
		{"tag-only reference", func(c *NativeSiteStartup) {
			c.ImageImports["runtime-postgresql"] = "docker.io/nvkr-local/postgresql:latest"
		}},
		{"tag-only workload", func(c *NativeSiteStartup) {
			c.ImageImports["workload"] = "docker.io/library/alpine:latest"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := startupDescription()
			addStartupRuntimeImages(&cfg)
			if tc.change != nil {
				tc.change(&cfg)
			}
			data, err := SiteStartupUserData(startupTestContext(t), cfg)
			if tc.change != nil {
				var coded *nvkrerr.Error
				if data != "" || !errors.As(err, &coded) || coded.Code != nvkrerr.CodeUsage {
					t.Fatalf("incomplete runtime closure escaped admission: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var decoded startupCloudConfig
			if err := yaml.Unmarshal([]byte(data), &decoded); err != nil {
				t.Fatal(err)
			}
			script := decoded.WriteFiles[0].Content
			for role, reference := range cfg.ImageImports {
				if !strings.Contains(script, operatorQuote("downloads/"+role+".tar")) || !strings.Contains(script, "inspecti --quiet "+operatorQuote(reference)) {
					t.Fatalf("selected runtime image lacks exact import and CRI check: %s", role)
				}
			}
			if strings.Count(script, "images import --local --digests --base-name ") != len(cfg.ImageImports)+1 {
				t.Fatal("every selected runtime image and OVN must use the local manifest importer")
			}
			command := exec.CommandContext(t.Context(), "bash", "-n")
			command.Stdin = strings.NewReader(script)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("runtime import shell: %v: %s", err, output)
			}
		})
	}
}

func TestSiteStartupRejectsInvalidHandoffsBeforeReturningUserData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*NativeSiteStartup)
	}{
		{"invalid name", func(c *NativeSiteStartup) { c.Name = "bad\nsite" }},
		{"public gateway", func(c *NativeSiteStartup) { c.Gateway = "8.8.8.8" }},
		{"ipv6 gateway", func(c *NativeSiteStartup) { c.Gateway = "fd00::1" }},
		{"invalid image", func(c *NativeSiteStartup) { c.ControllerImage = "image; command" }},
		{"missing built image reference", func(c *NativeSiteStartup) { delete(c.ImageImports, "controller") }},
		{"tag-only built image reference", func(c *NativeSiteStartup) { c.ImageImports["controller"] = "docker.io/library/nke-controller:latest" }},
		{"tag-only OVN", func(c *NativeSiteStartup) { c.OVNImage = "localhost/nvke/ovn-kubernetes:latest" }},
		{"sibling OVN", func(c *NativeSiteStartup) { c.OVNImage = "localhost/nvke/sibling@sha256:" + strings.Repeat("a", 64) }},
		{"missing asset", func(c *NativeSiteStartup) { c.Assets = c.Assets[1:] }},
		{"duplicate asset", func(c *NativeSiteStartup) { c.Assets[0] = c.Assets[1] }},
		{"unverified bytes", func(c *NativeSiteStartup) { c.Assets[0].Digest = "tag" }},
		{"different gateway", func(c *NativeSiteStartup) { c.Assets[0].URL = "http://172.30.0.2:18080/files/tool" }},
		{"credential URL", func(c *NativeSiteStartup) { c.Assets[0].URL = "http://user:pass@172.30.0.1:18080/files/tool" }},
		{"query", func(c *NativeSiteStartup) { c.Assets[0].URL += "?secret=value" }},
		{"oversized asset", func(c *NativeSiteStartup) { c.Assets[0].URL += strings.Repeat("a", 4096) }},
		{"oversized certificate", func(c *NativeSiteStartup) { c.HandoffCertificate = strings.Repeat("a", 17<<10) }},
		{"partial handoff", func(c *NativeSiteStartup) { c.HandoffURL = "https://172.30.0.1:18443/site-kubeconfig" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := startupDescription()
			tc.change(&cfg)
			data, err := SiteStartupUserData(startupTestContext(t), cfg)
			var coded *nvkrerr.Error
			if data != "" || !errors.As(err, &coded) || coded.Code != nvkrerr.CodeUsage {
				t.Fatalf("unsafe startup escaped admission: data=%d error=%v", len(data), err)
			}
		})
	}
}

func TestSiteStartupOnlyPreparesGuestForHostBootstrap(t *testing.T) {
	cfg := startupDescription()
	data, err := SiteStartupUserData(startupTestContext(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded startupCloudConfig
	if err := yaml.Unmarshal([]byte(data), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.RunCmd) != 1 || len(decoded.RunCmd[0]) != 3 || decoded.RunCmd[0][1] != "-ec" || !strings.Contains(decoded.RunCmd[0][2], "exec /bin/bash /var/lib/nvke-native/prepare.sh") || !strings.Contains(decoded.RunCmd[0][2], "exec timeout --kill-after=5s") {
		t.Fatal("cloud-init must only prepare and queue the site service")
	}
	files := map[string]startupFile{}
	for _, file := range decoded.WriteFiles {
		files[file.Path] = file
	}
	prepare := files["/var/lib/nvke-native/prepare.sh"]
	if prepare.Permissions != "0700" {
		t.Fatal("guest preparation must remain private")
	}
	for _, forbidden := range []string{"nvkectl", "bootstrap site", "kind create", "nvke-native-site.service"} {
		if strings.Contains(prepare.Content, forbidden) {
			t.Fatalf("host orchestration entered guest startup: %s", forbidden)
		}
	}
	if !strings.Contains(prepare.Content, "get_tpm_pubhash") || !strings.Contains(prepare.Content, "X-NVKE-Startup-Status: prepared") {
		t.Fatal("guest startup must return its hardware identity before host bootstrap")
	}
	if !strings.Contains(prepare.Content, "systemctl start --no-block spire-agent.service nvke-agent.service") {
		t.Fatal("agents must start without blocking the host bootstrap")
	}
	// Only stdout/stderr routing may override either source-owned service unit.
	const consoleDropIn = "[Service]\nStandardOutput=journal+console\nStandardError=journal+console\n"
	for _, unit := range []string{"nvke-agent.service", "spire-agent.service"} {
		path := "/etc/systemd/system/" + unit + ".d/10-nvkr-console.conf"
		file, exists := files[path]
		if !exists || file.Permissions != "0600" || file.Content != consoleDropIn {
			t.Fatalf("service diagnostics must use a private logging-only drop-in: %s", unit)
		}
	}
	dropIns := 0
	for _, file := range decoded.WriteFiles {
		if strings.HasPrefix(file.Path, "/etc/systemd/system/") {
			dropIns++
		}
	}
	if dropIns != 2 {
		t.Fatal("startup diagnostics must not add other service overrides")
	}
	// An unconditional reload is needed even if APT performs no installation.
	installed := strings.Index(prepare.Content, "nvke-cni-ovn-charts-")
	reloaded := strings.Index(prepare.Content, "\nsystemctl daemon-reload\n")
	stopped := strings.Index(prepare.Content, "\nsystemctl stop nvke-agent.service spire-agent.service\n")
	started := strings.Index(prepare.Content, "\nsystemctl start --no-block spire-agent.service nvke-agent.service\n")
	if installed < 0 || reloaded <= installed || stopped <= reloaded || started <= stopped || strings.Count(prepare.Content, "systemctl daemon-reload") != 1 {
		t.Fatal("preparation must reload service drop-ins after APT and before starting the agents")
	}
	imported := strings.Index(prepare.Content, "images import --local --digests --base-name "+operatorQuote("localhost/nvke/ovn-kubernetes"))
	configured := strings.Index(prepare.Content, "> /etc/dgx/ovn-kubernetes/values-nke.yaml")
	if imported <= stopped || configured <= imported || started <= configured {
		t.Fatal("site agents must start after selected OVN import and values projection")
	}

	command := exec.CommandContext(t.Context(), "bash", "-n")
	command.Stdin = strings.NewReader(prepare.Content)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("rendered guest preparation shell syntax: %v: %s", err, output)
	}
	if decoded.PhoneHome.URL != "http://172.30.0.1:7777/latest/meta-data/phone_home" || decoded.PhoneHome.Post != "all" {
		t.Fatal("completion must reach the source-owned native metadata gateway")
	}
	if strings.Contains(prepare.Content, "--disable-mtls") || strings.Contains(prepare.Content, " ssh ") {
		t.Fatal("startup must preserve SPIRE and avoid remote shell orchestration")
	}
	// Descriptions are borrowed: rendering must not sort the caller's slice.
	first := cfg.Assets[0]
	cfg.Assets[0], cfg.Assets[1] = cfg.Assets[1], cfg.Assets[0]
	reordered, err := SiteStartupUserData(startupTestContext(t), cfg)
	if err != nil || reordered != data || cfg.Assets[1] != first {
		t.Fatal("startup must be deterministic without mutating descriptions")
	}
}

func TestSiteStartupCanceledContextHasNoOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	data, err := SiteStartupUserData(ctx, startupDescription())
	if data != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled startup returned data=%d error=%v", len(data), err)
	}
}

func TestSiteStartupPinsTheAuthenticatedTLSReturnChannel(t *testing.T) {
	cfg := startupDescription()
	_, certificate, err := nativeHandoffCertificate(netip.MustParseAddr(cfg.Gateway))
	if err != nil {
		t.Fatal(err)
	}
	cfg.HandoffURL = "https://172.30.0.1:18443/site-kubeconfig"
	cfg.HandoffToken = strings.Repeat("b", 64)
	cfg.HandoffCertificate = certificate
	data, err := SiteStartupUserData(startupTestContext(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded startupCloudConfig
	if err := yaml.Unmarshal([]byte(data), &decoded); err != nil {
		t.Fatal(err)
	}
	files := map[string]startupFile{}
	for _, file := range decoded.WriteFiles {
		files[file.Path] = file
	}
	header := files["/var/lib/nvke-native/handoff-header"]
	anchor := files["/var/lib/nvke-native/handoff-ca.pem"]
	if header.Content != "Authorization: Bearer "+cfg.HandoffToken+"\n" || header.Permissions != "0600" || anchor.Content != certificate {
		t.Fatal("startup must privately deliver the exact return-channel credential and trust anchor")
	}
	script := files["/var/lib/nvke-native/prepare.sh"].Content
	if !strings.Contains(script, "--cacert handoff-ca.pem --header @handoff-header") || strings.Contains(script, cfg.HandoffToken) {
		t.Fatal("startup must verify TLS and read its token from the private header file")
	}
	_, wrongCertificate, err := nativeHandoffCertificate(netip.MustParseAddr("172.30.0.2"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.HandoffCertificate = wrongCertificate
	if data, err := SiteStartupUserData(startupTestContext(t), cfg); data != "" || err == nil {
		t.Fatal("startup admitted a trust anchor for a different endpoint")
	}
}

// Package versions must follow the same network selection as the image/chart
// outputs. A historical byte hash would reject a legitimate catalog pin update.
func TestSiteStartupUsesSelectedOVNPackageVersion(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn"} {
		t.Run(network, func(t *testing.T) {
			version, _, _, err := selectedOVNOutputs(network)
			if err != nil {
				t.Fatal(err)
			}
			cfg := startupDescription()
			if network == "upstream-ovn" {
				cfg.OVNVersion = version
			}
			data, err := SiteStartupUserData(startupTestContext(t), cfg)
			if err != nil {
				t.Fatal(err)
			}
			var config startupCloudConfig
			if err := yaml.Unmarshal([]byte(data), &config); err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"nvke-cni-ovn-images-", "nvke-cni-ovn-charts-"} {
				if !strings.Contains(config.WriteFiles[0].Content, prefix+version) {
					t.Fatalf("startup package differs from selected %s output version", network)
				}
			}
		})
	}
}
