// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"go.yaml.in/yaml/v3"
)

func machineImageFixture(arch string) machineImageCandidate {
	digest := "sha256:" + strings.Repeat("a", 64)
	return machineImageCandidate{
		Metadata: MachineImageMetadata{Output: "node", Family: "ubuntu", Version: "24.04", BuildDate: "2026-11-17"},
		Disk: delivery.BootAssetBinding{Output: "node", Kind: "disk-image", Platform: "linux/" + arch, Publication: "registry.example.test/node@" + digest,
			DescriptorDigest: digest, BuildDigest: digest, SHA256: digest, Size: 12345},
		Imager: delivery.BootAssetBinding{Output: "imager", Kind: "executable", Platform: "linux/" + arch, Member: "qcow-imager.efi", Profile: "synthetic-strict-v1",
			Publication: "registry.example.test/imager@" + digest, DescriptorDigest: digest, BuildDigest: digest, SHA256: digest, Size: 4096},
		Destination: "primary", ServingDigest: digest, DiskURL: "https://boot.example.test/disks/" + strings.Repeat("a", 64) + ".qcow2",
		ImagerURL: "https://boot.example.test/imagers/" + strings.Repeat("a", 64) + ".efi", Profile: "synthetic-strict-v1",
		SourceCommit: strings.Repeat("1", 40), TrustProfile: "importer-verification-tls-candidate-v1",
	}
}

func TestMachineImageProductionAdmissionDisabled(t *testing.T) {
	for _, scenario := range []string{"zero", "descriptive-inputs", "secure-boot", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			request := MachineImageRenderRequest{}
			ctx := t.Context()
			if scenario == "descriptive-inputs" {
				request.Inputs = &BootInputExpectation{}
				request.Prepared = []*PreparedBootAssets{{}}
				request.Metadata = []MachineImageMetadata{machineImageFixture("amd64").Metadata}
				request.Profile = "synthetic-strict-v1"
			}
			if scenario == "secure-boot" {
				request.TrustProfile = "secure-boot"
			}
			if scenario == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			// A nil consumer makes any accidental inspection/I/O path fail this
			// test; capability refusal must happen before touching dependencies.
			var consumer *Consumer
			manifests, err := consumer.RenderMachineImages(ctx, request)
			requireLifecycleCode(t, err, e.CodeEngineCapability)
			if manifests != nil {
				t.Fatal("disabled capability released artifact bytes")
			}
		})
	}
}

func TestMachineImageCandidateSchemaAndScript(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			candidate := machineImageFixture(arch)
			manifest, err := renderMachineImageCandidate(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if len(manifest.Name) != 55 || !machineImageLabel.MatchString(manifest.Name) {
				t.Fatal("invalid immutable name", manifest.Name)
			}
			var document machineImageDocument
			if err := yaml.Unmarshal(manifest.YAML, &document); err != nil {
				t.Fatal(err)
			}
			if document.APIVersion != "internal.nvke.nvidia.com/v1" || document.Kind != "MachineImage" || document.Metadata.Name != manifest.Name {
				t.Fatal("incomplete CR")
			}
			validateMachineImagePinnedSchema(t, manifest.YAML)
			script := document.Spec.NICo.IPXEScript
			if !strings.HasPrefix(script, "#!ipxe\nchain "+candidate.ImagerURL+" ") || !strings.Contains(script, "image_sha="+strings.Repeat("a", 64)+" ") ||
				!strings.Contains(script, "disk_strategy={{ .DiskSelectionStrategy }}") || !strings.Contains(script, "${cloudinit-url}") ||
				strings.Contains(script, "sha256:") || strings.Contains(script, "luks") || strings.Count(script, "{{") != strings.Count(script, "}}") {
				t.Fatal("script changed downstream placeholders or raw checksum", script)
			}
			if arch == "amd64" && !strings.Contains(script, "console=ttyS1,115200") || arch == "arm64" && (!strings.Contains(script, "console=ttyAMA0,115200") || strings.Contains(script, "intel_iommu") || strings.Contains(script, "reboot=pci")) {
				t.Fatal("architecture console/kernel arguments mixed", script)
			}
			parsed, err := template.New("ipxe").Option("missingkey=error").Parse(script)
			if err != nil {
				t.Fatal(err)
			}
			for _, strategy := range []string{"smallest", "largest"} {
				var rendered bytes.Buffer
				if err := parsed.Execute(&rendered, struct{ DiskSelectionStrategy string }{strategy}); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(rendered.String(), "disk_strategy="+strategy+" ") || !strings.Contains(rendered.String(), "${cloudinit-url}") {
					t.Fatal("runtime substitution changed")
				}
			}
			var binding struct {
				Material       json.RawMessage   `json:"material"`
				Inputs         material.Manifest `json:"inputs"`
				MaterialDigest string            `json:"materialDigest"`
				ManifestSHA256 string            `json:"manifestSHA256"`
			}
			if err := json.Unmarshal(manifest.DependencyBinding, &binding); err != nil {
				t.Fatal(err)
			}
			digest, err := binding.Inputs.Digest()
			if err != nil || digest != binding.MaterialDigest || binding.ManifestSHA256 != pipeline.FileDigest(manifest.YAML) {
				t.Fatal("binding does not cover exact material/YAML", err)
			}
			candidateBytes, err := canonical.Marshal(binding.Material)
			if err != nil || len(binding.Inputs.Materials) != 1 || binding.Inputs.Materials[0].Digest != pipeline.FileDigest(candidateBytes) {
				t.Fatal("typed material manifest lost candidate bytes", err)
			}
			if !bytes.Contains(manifest.DependencyBinding, []byte(machineImageSchemaCommit)) || bytes.Contains(manifest.DependencyBinding, []byte("aggregate")) || bytes.Contains(manifest.DependencyBinding, []byte("inventory")) {
				t.Fatal("missing schema pin or circular dependency")
			}
		})
	}
}

func TestMachineImageCandidatesDeterminismAndIdentity(t *testing.T) {
	var candidates []machineImageCandidate
	for _, disk := range []string{"first", "second"} {
		for _, destination := range []string{"one", "two"} {
			candidate := machineImageFixture("amd64")
			candidate.Disk.Output, candidate.Metadata.Output, candidate.Destination = disk, disk, destination
			candidates = append(candidates, candidate)
		}
	}
	forward, err := renderMachineImageCandidates(candidates)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(candidates)
	reverse, err := renderMachineImageCandidates(candidates)
	if err != nil || len(forward) != 4 || !reflect.DeepEqual(forward, reverse) {
		t.Fatal("ordering changes exact bytes", err)
	}
	original := machineImageFixture("amd64")
	baseline, err := renderMachineImageCandidate(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*machineImageCandidate)
	}{
		{"URL", func(c *machineImageCandidate) { c.DiskURL += "/changed" }},
		{"metadata", func(c *machineImageCandidate) { c.Metadata.Version = "24.10" }},
		{"architecture", func(c *machineImageCandidate) { c.Disk.Platform = "linux/arm64"; c.Imager.Platform = c.Disk.Platform }},
		{"profile", func(c *machineImageCandidate) { c.Profile = "synthetic-strict-v2"; c.Imager.Profile = c.Profile }},
		{"source", func(c *machineImageCandidate) { c.SourceCommit = strings.Repeat("2", 40) }},
		{"sibling", func(c *machineImageCandidate) { c.Disk.Output = "sibling"; c.Metadata.Output = "sibling" }},
		{"raw-digest", func(c *machineImageCandidate) { c.Disk.SHA256 = "sha256:" + strings.Repeat("b", 64) }},
		{"serving", func(c *machineImageCandidate) { c.ServingDigest = "sha256:" + strings.Repeat("b", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := original
			tc.change(&changed)
			actual, err := renderMachineImageCandidate(changed)
			if err != nil {
				t.Fatal(err)
			}
			if actual.Name == baseline.Name || bytes.Equal(actual.DependencyBinding, baseline.DependencyBinding) {
				t.Fatal("changed material reused identity")
			}
			if changed.Disk.BuildDigest != original.Disk.BuildDigest {
				t.Fatal("render changes altered source Build")
			}
		})
	}
	for _, bad := range [][]machineImageCandidate{nil, {original, original}, make([]machineImageCandidate, 257)} {
		result, err := renderMachineImageCandidates(bad)
		requireLifecycleCode(t, err, e.CodeVerifyFailed)
		if result != nil {
			t.Fatal("invalid set released partial manifests")
		}
	}
}

func TestMachineImageCandidateRejections(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*machineImageCandidate)
		code   e.Code
	}{
		{"missing-family", func(c *machineImageCandidate) { c.Metadata.Family = "" }, e.CodeVerifyFailed},
		{"family-bound", func(c *machineImageCandidate) { c.Metadata.Family = strings.Repeat("a", 64) }, e.CodeVerifyFailed},
		{"version", func(c *machineImageCandidate) { c.Metadata.Version = "latest" }, e.CodeVerifyFailed},
		{"variant", func(c *machineImageCandidate) { c.Metadata.Variant = "bad variant" }, e.CodeVerifyFailed},
		{"date", func(c *machineImageCandidate) { c.Metadata.BuildDate = "2026-02-29" }, e.CodeVerifyFailed},
		{"absent-date", func(c *machineImageCandidate) { c.Metadata.BuildDate = "" }, e.CodeVerifyFailed},
		{"metadata-owner", func(c *machineImageCandidate) { c.Metadata.Output = "sibling" }, e.CodeVerifyFailed},
		{"architecture", func(c *machineImageCandidate) { c.Imager.Platform = "linux/arm64" }, e.CodeVerifyFailed},
		{"profile", func(c *machineImageCandidate) { c.Imager.Profile = "other" }, e.CodeVerifyFailed},
		{"source", func(c *machineImageCandidate) { c.SourceCommit = "main" }, e.CodeVerifyFailed},
		{"raw-digest", func(c *machineImageCandidate) { c.Disk.SHA256 = "invalid" }, e.CodeVerifyFailed},
		{"size", func(c *machineImageCandidate) { c.Disk.Size = 0 }, e.CodeVerifyFailed},
		{"output-name", func(c *machineImageCandidate) { c.Disk.Output = "bad\x00name"; c.Metadata.Output = c.Disk.Output }, e.CodeVerifyFailed},
		{"destination-bound", func(c *machineImageCandidate) { c.Destination = strings.Repeat("a", 64) }, e.CodeVerifyFailed},
		{"kind", func(c *machineImageCandidate) { c.Disk.Kind = "executable" }, e.CodeVerifyFailed},
		{"member", func(c *machineImageCandidate) { c.Imager.Member = "../loader" }, e.CodeVerifyFailed},
		{"publication", func(c *machineImageCandidate) { c.Imager.Publication = "registry.example.test/imager:latest" }, e.CodeVerifyFailed},
		{"same-url", func(c *machineImageCandidate) { c.ImagerURL = c.DiskURL }, e.CodeVerifyFailed},
		{"same-output", func(c *machineImageCandidate) { c.Imager.Output = c.Disk.Output }, e.CodeVerifyFailed},
		{"disk-member", func(c *machineImageCandidate) { c.Disk.Member = "disk.qcow2" }, e.CodeVerifyFailed},
		{"disk-profile", func(c *machineImageCandidate) { c.Disk.Profile = "other" }, e.CodeVerifyFailed},
		{"imager-kind", func(c *machineImageCandidate) { c.Imager.Kind = "disk-image" }, e.CodeVerifyFailed},
		{"destination", func(c *machineImageCandidate) { c.Destination = "bad destination" }, e.CodeVerifyFailed},
		{"serving", func(c *machineImageCandidate) { c.ServingDigest = "invalid" }, e.CodeVerifyFailed},
		{"platform", func(c *machineImageCandidate) { c.Disk.Platform = "linux/riscv64"; c.Imager.Platform = c.Disk.Platform }, e.CodeVerifyFailed},
		{"empty-profile", func(c *machineImageCandidate) { c.Profile = ""; c.Imager.Profile = "" }, e.CodeVerifyFailed},
		{"script-bound", func(c *machineImageCandidate) { c.DiskURL += "/" + strings.Repeat("a", 16384) }, e.CodeVerifyFailed},
		{"secure-boot", func(c *machineImageCandidate) { c.TrustProfile = "secure-boot" }, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := machineImageFixture("amd64")
			tc.change(&candidate)
			first := machineImageFixture("arm64")
			first.Disk.Output, first.Metadata.Output = "aaa", "aaa"
			result, err := renderMachineImageCandidates([]machineImageCandidate{first, candidate})
			requireLifecycleCode(t, err, tc.code)
			if result != nil {
				t.Fatal("invalid input released partial manifests")
			}
		})
	}
	for _, location := range []string{"http://boot.example.test/image", "https://reader:secret@boot.example.test/image", "https:///image", "https://boot.example.test", "https://boot.example.test/image?", "https://boot.example.test/../image", "https://boot.example.test//image", "https://boot.example.test:65536/image"} {
		t.Run(location, func(t *testing.T) {
			candidate := machineImageFixture("amd64")
			candidate.ImagerURL = location
			_, err := renderMachineImageCandidate(candidate)
			requireLifecycleCode(t, err, e.CodeVerifyFailed)
		})
	}
	for _, suffix := range []string{"?token=x", "#fragment", "%20x", ";reboot", "\nboot", "{{ .Other }}", "${injected}", "\\x", "\"x"} {
		t.Run("URL"+suffix, func(t *testing.T) {
			candidate := machineImageFixture("amd64")
			candidate.DiskURL += suffix
			_, err := renderMachineImageCandidate(candidate)
			requireLifecycleCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestMachineImageScriptBoundaries(t *testing.T) {
	candidate := machineImageFixture("amd64")
	baseline, err := renderMachineImageCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	var document machineImageDocument
	if err := yaml.Unmarshal(baseline.YAML, &document); err != nil {
		t.Fatal(err)
	}
	for _, length := range []int{machineImageMaxScriptBytes, machineImageMaxScriptBytes + 1} {
		changed := candidate
		changed.DiskURL += strings.Repeat("x", length-len(document.Spec.NICo.IPXEScript))
		result, err := renderMachineImageCandidate(changed)
		if length > machineImageMaxScriptBytes {
			requireLifecycleCode(t, err, e.CodeVerifyFailed)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		validateMachineImagePinnedSchema(t, result.YAML)
	}
}

// This checks required fields, types and string bounds/patterns against the
// unchanged pinned CRD fixture. Script CEL equivalents are tested above. It is
// not an API-server/CEL admission or EFI execution test.
func validateMachineImagePinnedSchema(t *testing.T, raw []byte) {
	t.Helper()
	crd, err := os.ReadFile("testdata/machineimage/machineimages-crd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema map[string]any `yaml:"openAPIV3Schema"`
				} `yaml:"schema"`
			} `yaml:"versions"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(crd, &schema); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	var check func(map[string]any, any)
	check = func(rule map[string]any, value any) {
		switch rule["type"] {
		case "object":
			object, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("not object: %T", value)
			}
			if required, ok := rule["required"].([]any); ok {
				for _, name := range required {
					if _, ok := object[name.(string)]; !ok {
						t.Fatalf("missing %s", name)
					}
				}
			}
			if properties, ok := rule["properties"].(map[string]any); ok {
				for key, value := range object {
					if child, ok := properties[key].(map[string]any); ok {
						check(child, value)
					}
				}
			}
		case "string":
			text, ok := value.(string)
			if !ok {
				t.Fatalf("not string: %T", value)
			}
			if rule["format"] == "date" {
				if _, err := time.Parse("2006-01-02", text); err != nil {
					t.Fatal(err)
				}
			}
			if min, ok := rule["minLength"].(int); ok && len(text) < min {
				t.Fatal("below schema minimum")
			}
			if max, ok := rule["maxLength"].(int); ok && len(text) > max {
				t.Fatal("above schema maximum")
			}
			if pattern, ok := rule["pattern"].(string); ok && !regexp.MustCompile(pattern).MatchString(text) {
				t.Fatal("schema pattern mismatch")
			}
		}
	}
	if len(schema.Spec.Versions) != 1 {
		t.Fatal("pinned CRD version set changed")
	}
	root := schema.Spec.Versions[0].Schema.OpenAPIV3Schema
	property := func(root map[string]any, names ...string) map[string]any {
		for _, name := range names {
			root = root["properties"].(map[string]any)[name].(map[string]any)
		}
		return root
	}
	if property(root, "spec", "operatingSystem", "family")["pattern"] != machineImageLabel.String() ||
		property(root, "spec", "operatingSystem", "version")["pattern"] != machineImageVersion.String() ||
		property(root, "spec", "variant")["pattern"] != machineImageVariant.String() ||
		property(root, "spec", "nico", "ipxeScript")["maxLength"] != machineImageMaxScriptBytes {
		t.Fatal("candidate constraints differ from pinned CRD")
	}
	check(root, document)
}
