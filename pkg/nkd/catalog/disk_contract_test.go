package catalog

import (
	"context"
	"slices"
	"strings"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestDiskImageCatalogSchemaAndIdentity(t *testing.T) {
	const vmToolchain = "example.invalid/nkd-vm-builder@sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72"
	registry := `apiVersion: nkd.dev/catalog/v1
kind: SourceBuildRegistry
sources:
  image-source: {repo: https://github.com/example/node-image, toolchainImage: ` + vmToolchain + `}
recipes:
  - id: node-amd64
    criteria: {category: os, os: ubuntu, arch: amd64}
    components: [node-fde, node-plain]
  - id: node-arm64
    criteria: {category: os, os: ubuntu, arch: arm64}
    components: [node-plain]
components:
  node-fde:
    fromSource: true
    source: image-source
    builderType: vm-image
    outputType: disk-image
    disk: {format: qcow2, encryption: luks2}
    buildCommand: [make, -C, image, image-fde, "ARCH={{.Arch}}"]
    artifactPath: "image/output-fde/node-{{.Arch}}.qcow2"
  node-plain:
    fromSource: true
    source: image-source
    builderType: vm-image
    outputType: disk-image
    disk: {format: qcow2, encryption: none}
    buildCommand: [make, -C, image, image-plain, "ARCH={{.Arch}}"]
    artifactPath: "image/output-plain/node-{{.Arch}}.qcow2"
`
	lock := `apiVersion: nkd.dev/lock/v1
kind: PinLock
pins:
  image-source: {tag: v1.0.0, commit: ` + goodCommit + `, treeHash: ` + goodTree + `, sourceDateEpoch: 1733939955}
`
	load := func(t *testing.T) *Catalog {
		t.Helper()
		cat, err := LoadContext(context.Background(), bytesProvider{
			RegistryFile: []byte(registry), LockFile: []byte(lock),
		})
		if err != nil {
			t.Fatal(err)
		}
		return cat
	}

	cat := load(t)
	plain := cat.Components["node-plain"]
	wantCommand := []string{"make", "-C", "image", "image-plain", "ARCH={{.Arch}}"}
	if plain.BuilderType != BuilderVMImage || plain.Disk == nil || plain.Disk.Format != "qcow2" ||
		plain.Disk.Encryption != "none" || !slices.Equal(plain.BuildCommand, wantCommand) ||
		plain.ArtifactPath != "image/output-plain/node-{{.Arch}}.qcow2" {
		t.Fatalf("loaded disk component lost its authored contract: %#v", plain)
	}

	before, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	changed := load(t)
	component := changed.Components["node-plain"]
	disk := *component.Disk
	disk.Encryption = "luks2"
	component.Disk = &disk
	changed.Components["node-plain"] = component
	after, err := changed.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("disk encryption metadata did not affect catalog identity")
	}
}

func TestDiskImageSchemaValidation(t *testing.T) {
	valid := goodComponent()
	valid.OutputType = "disk-image"
	valid.Disk = &DiskSpec{Format: "qcow2", Encryption: "none"}
	validate := func(component Component) error {
		cat := base(component)
		cat.Recipes[0].Criteria.Category = "os"
		return cat.Validate()
	}
	tests := []struct {
		name     string
		edit     func(*Component)
		wantText string
	}{
		{"missing disk", func(c *Component) { c.Disk = nil }, ""},
		{"unsupported format", func(c *Component) { c.Disk.Format = "raw" }, ""},
		{"unsupported encryption", func(c *Component) { c.Disk.Encryption = "aes" }, ""},
		{"disk on another output", func(c *Component) { c.OutputType = "raw" }, ""},
		{"vm builder on another output", func(c *Component) { c.BuilderType = BuilderVMImage; c.OutputType = "raw"; c.Disk = nil }, ""},
		{"vm builder not from source", func(c *Component) {
			c.BuilderType = BuilderVMImage
			c.FromSource = false
			c.ExclusionReason = "not built"
			c.OutputType = "host-package"
			c.Disk = nil
		}, "vm-image builder requires a from-source component"},
		{"excluded disk", func(c *Component) {
			c.FromSource = false
			c.ExclusionReason = "not built"
		}, ""},
		{"prebuilt disk", func(c *Component) {
			c.FromSource = false
			c.Prebuilt = &Prebuilt{URL: "https://example.test/image.qcow2", Digests: map[string]string{
				"amd64": "sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72",
			}, Evidence: &ExternalEvidence{None: true}}
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := valid
			disk := *valid.Disk
			component.Disk = &disk
			tt.edit(&component)
			err := validate(component)
			if coded, ok := err.(*nkderr.Error); !ok || coded.Code != nkderr.CodeInvalidCatalog {
				t.Fatalf("error = %v, want %s", err, nkderr.CodeInvalidCatalog)
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("error = %v, want text %q", err, tt.wantText)
			}
		})
	}
	if err := validate(valid); err != nil {
		t.Fatalf("disk-image must not require the vm-image builder: %v", err)
	}
}
