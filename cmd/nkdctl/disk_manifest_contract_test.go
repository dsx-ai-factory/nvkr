package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/urfave/cli/v2"
)

func TestDiskManifestCommandMeasuresExactCanonicalStream(t *testing.T) {
	for _, format := range []string{"raw", "iso"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			path, out := filepath.Join(dir, "disk"), filepath.Join(dir, "manifest.json")
			payload := bytes.Repeat([]byte("disk-content"), 1000)
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"assemble", "--format", format, "--from-raw", path, "--role", "worker", "--block-size", strconv.Itoa(assemble.MinDiskBlockSizeBytes)}
			output, err := invokeLifecycleCLI(t, append(args, "--manifest-out", out)...)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			var manifest assemble.DiskManifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			if err := manifest.Validate(); err != nil {
				t.Fatal(err)
			}
			if manifest.DiskKind != format || manifest.Role != "worker" || manifest.VirtualSizeBytes != int64(len(payload)) || manifest.LogicalDigest != pipeline.FileDigest(payload) {
				t.Fatalf("manifest lost input binding: %+v", manifest)
			}
			if (manifest.BlockManifest != nil) != (format == "raw") {
				t.Fatalf("wrong block manifest for %s", format)
			}
			canonical, err := manifest.Marshal()
			if err != nil || !bytes.Equal(raw, canonical) {
				t.Fatalf("noncanonical manifest: %v", err)
			}
			digest, err := manifest.Digest()
			if err != nil || !strings.Contains(output, digest) {
				t.Fatalf("output omits manifest identity: %q, %v", output, err)
			}
			withoutFile, err := invokeLifecycleCLI(t, args...)
			if err != nil || withoutFile != output {
				t.Fatalf("display-only identity changed: %q, %v", withoutFile, err)
			}
		})
	}
}

func TestDecodeSelectionFailsBeforeDecoderExecution(t *testing.T) {
	for _, tc := range []struct {
		name, selection string
		components      []attest.ComponentDigest
		code            e.Code
	}{
		{"missing named component", "absent", []attest.ComponentDigest{{Name: "disk", OutputType: "raw", SHA256: testArtifactDigest}}, e.CodeVerifyFailed},
		{"no disks", "", []attest.ComponentDigest{{Name: "tool", OutputType: "host-package", SHA256: testArtifactDigest}}, e.CodeVerifyFailed},
		{"ambiguous disks", "", []attest.ComponentDigest{{Name: "a", OutputType: "raw", SHA256: testArtifactDigest}, {Name: "b", OutputType: "iso", SHA256: testArtifactDigest}}, e.CodeUsage},
		{"sole disk requires pinned decoder", "", []attest.ComponentDigest{{Name: "a", OutputType: "qcow2", SHA256: testArtifactDigest}}, e.CodeUnpinnedInput},
		{"explicit disk requires pinned decoder", "b", []attest.ComponentDigest{{Name: "a", OutputType: "raw", SHA256: testArtifactDigest}, {Name: "b", OutputType: "iso", SHA256: testArtifactDigest}}, e.CodeUnpinnedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &cli.App{Flags: []cli.Flag{&cli.StringFlag{Name: "decode-component"}}, Action: func(c *cli.Context) error {
				return verifyDecode(c, &attest.Manifest{Components: tc.components}, "missing-envelope")
			}}
			err := app.RunContext(t.Context(), []string{"decode-test", "--decode-component", tc.selection})
			assertLifecycleCommandCode(t, err, tc.code)
		})
	}
}

func TestDiskManifestFormatAdmissionPrecedesMissingInput(t *testing.T) {
	for _, tc := range []struct {
		name, format, envelope string
		code                   e.Code
	}{
		{"raw with envelope", "raw", "missing.qcow2", e.CodeUsage},
		{"iso with envelope", "iso", "missing.qcow2", e.CodeUsage},
		{"qcow2 without envelope", "qcow2", "", e.CodeUsage},
		{"unsupported format", "vmdk", "", e.CodeUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "new", "manifest.json")
			args := []string{"assemble", "--format", tc.format, "--from-raw", "missing.raw", "--manifest-out", out}
			if tc.envelope != "" {
				args = append(args, "--envelope", tc.envelope)
			}
			_, err := invokeLifecycleCLI(t, args...)
			assertLifecycleCommandCode(t, err, tc.code)
			if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
				t.Fatalf("rejected format created output: %v", err)
			}
		})
	}
}
