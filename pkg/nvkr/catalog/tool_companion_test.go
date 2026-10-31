// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func companionArchive(t *testing.T, format string, members map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	names := []string{"aicr", "aicr-attestation.sigstore.json"}
	switch format {
	case "tar-gzip":
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		for _, name := range names {
			if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(members[name])), Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(members[name]); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	case "zip":
		zw := zip.NewWriter(&b)
		for _, name := range names {
			f, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write(members[name]); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}

func TestToolPinArchiveMember(t *testing.T) {
	members := map[string][]byte{"aicr": []byte("executable"), "aicr-attestation.sigstore.json": []byte(`{"bundle":true}`)}
	for _, tc := range []struct {
		name, format, member string
		maxBytes             int64
		change               func(*ToolPin, []byte) []byte
		want                 e.Code
	}{
		{name: "tar companion", format: "tar-gzip", member: "aicr-attestation.sigstore.json", maxBytes: 1 << 20},
		{name: "zip companion", format: "zip", member: "aicr-attestation.sigstore.json", maxBytes: 1 << 20},
		{name: "substituted archive", format: "tar-gzip", member: "aicr-attestation.sigstore.json", maxBytes: 1 << 20,
			change: func(_ *ToolPin, raw []byte) []byte { return append(raw, 0) }, want: e.CodeVerifyFailed},
		{name: "missing member", format: "tar-gzip", member: "absent.json", maxBytes: 1 << 20, want: e.CodeVerifyFailed},
		{name: "oversized member", format: "tar-gzip", member: "aicr-attestation.sigstore.json", maxBytes: 4, want: e.CodeVerifyFailed},
		{name: "executable is not a companion", format: "tar-gzip", member: "aicr", maxBytes: 1 << 20, want: e.CodeInvalidCatalog},
		{name: "unsafe member", format: "tar-gzip", member: "../aicr-attestation.sigstore.json", maxBytes: 1 << 20, want: e.CodeInvalidCatalog},
		{name: "unbounded", format: "tar-gzip", member: "aicr-attestation.sigstore.json", want: e.CodeInvalidCatalog},
		{name: "raw tool has no archive", format: "tar-gzip", member: "aicr-attestation.sigstore.json", maxBytes: 1 << 20,
			change: func(pin *ToolPin, raw []byte) []byte {
				pin.Format, pin.ExecutableDigest = "raw", pin.ArchiveDigest
				return raw
			}, want: e.CodeInvalidCatalog},
		{name: "unpinned archive", format: "tar-gzip", member: "aicr-attestation.sigstore.json", maxBytes: 1 << 20,
			change: func(pin *ToolPin, raw []byte) []byte { pin.ArchiveDigest = ""; return raw }, want: e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := companionArchive(t, tc.format, members)
			pin := ToolPin{Format: tc.format, URL: "https://example.invalid/aicr.tar.gz", Platform: "linux/amd64", ExecutablePath: "aicr",
				ArchiveDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), ExecutableDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(members["aicr"]))}
			if tc.change != nil {
				raw = tc.change(&pin, raw)
			}
			got, err := pin.ArchiveMember(t.Context(), raw, tc.member, tc.maxBytes)
			if pinErrorCode(err) != tc.want {
				t.Fatalf("%v, want %q", err, tc.want)
			}
			if tc.want == "" && !bytes.Equal(got, members[tc.member]) {
				t.Fatalf("selected %q, want %q", got, members[tc.member])
			}
		})
	}
}

func TestPrebuiltEvidenceSignerAndArchiveRefs(t *testing.T) {
	const identity = "https://github.com/NVIDIA/aicr/.github/workflows/on-tag.yaml@refs/tags/v1.1.2"
	signed := func() *ExternalEvidence {
		return &ExternalEvidence{ProvenanceRef: "archive:aicr-attestation.sigstore.json", ProvenanceDigest: goodArtifactDigest,
			SBOMRef: "https://example.invalid/aicr.sbom.json", SBOMDigest: goodArtifactDigest,
			Signer: &EvidenceSigner{Issuer: "https://token.actions.githubusercontent.com", Identity: identity}}
	}
	for _, tc := range []struct {
		name       string
		change     func(*Component)
		code       e.Code
		executable bool
	}{
		{name: "signed archive provenance", executable: true},
		{name: "archive ref without release executable", code: e.CodeInvalidCatalog},
		{name: "archive ref names the executable", executable: true, code: e.CodeInvalidCatalog,
			change: func(c *Component) { c.Prebuilt.Evidence.ProvenanceRef = "archive:aicr" }},
		{name: "archive ref escapes", executable: true, code: e.CodeInvalidCatalog,
			change: func(c *Component) { c.Prebuilt.Evidence.SBOMRef = "archive:../aicr.sbom.json" }},
		{name: "raw tool archive ref", executable: true, code: e.CodeInvalidCatalog,
			change: func(c *Component) {
				c.Prebuilt.Executable.Format = "raw"
				c.Prebuilt.Executable.ArchiveDigest = c.Prebuilt.Executable.ExecutableDigest
			}},
		{name: "signer without provenance", executable: true, code: e.CodeTrustPolicyMissing,
			change: func(c *Component) { c.Prebuilt.Evidence.ProvenanceRef, c.Prebuilt.Evidence.ProvenanceDigest = "", "" }},
		{name: "plain HTTP issuer", executable: true, code: e.CodeTrustPolicyMissing,
			change: func(c *Component) { c.Prebuilt.Evidence.Signer.Issuer = "http://token.actions.githubusercontent.com" }},
		{name: "empty identity", executable: true, code: e.CodeTrustPolicyMissing,
			change: func(c *Component) { c.Prebuilt.Evidence.Signer.Identity = "" }},
		{name: "unrendered identity", executable: true, code: e.CodeTrustPolicyMissing,
			change: func(c *Component) { c.Prebuilt.Evidence.Signer.Identity = identity + "{{.Arch}}" }},
		{name: "none with signer", executable: true, code: e.CodeInvalidCatalog,
			change: func(c *Component) {
				c.Prebuilt.Evidence = &ExternalEvidence{None: true, Signer: c.Prebuilt.Evidence.Signer}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			component := Component{OutputType: "host-package", Platforms: []string{"linux/amd64"},
				Prebuilt: &Prebuilt{URL: "https://example.invalid/aicr", Digests: map[string]string{"amd64": goodArtifactDigest}, Evidence: signed()}}
			if tc.executable {
				tool := ToolPin{Format: "tar-gzip", URL: "https://example.invalid/aicr.tar.gz", Platform: "linux/amd64", ExecutablePath: "aicr",
					ArchiveDigest: "sha256:" + strings.Repeat("b", 64), ExecutableDigest: goodArtifactDigest}
				component.Prebuilt.URL, component.Prebuilt.Executable = tool.URL, &tool
			}
			if tc.change != nil {
				tc.change(&component)
			}
			if err := validatePrebuilt("aicr", component); pinErrorCode(err) != tc.code {
				t.Fatalf("%v, want %q", err, tc.code)
			}
		})
	}
}

func TestPrebuiltEvidenceSignerIdentityRendersFromPin(t *testing.T) {
	tool := ToolSource{Format: "tar-gzip", URL: "https://example.invalid/{{.Tag}}/aicr.tar.gz", Platform: "linux/amd64", ExecutablePath: "aicr"}
	pin := Pin{Tag: "v1.1.2", Tool: &ToolPin{Format: tool.Format, URL: "https://example.invalid/v1.1.2/aicr.tar.gz", Platform: tool.Platform,
		ExecutablePath: tool.ExecutablePath, ArchiveDigest: goodArtifactDigest, ExecutableDigest: goodArtifactDigest}}
	authored := &ExternalEvidence{ProvenanceRef: "archive:aicr-attestation.sigstore.json", ProvenanceDigest: goodArtifactDigest,
		Signer: &EvidenceSigner{Issuer: "https://token.actions.githubusercontent.com",
			Identity: "https://github.com/NVIDIA/aicr/.github/workflows/on-tag.yaml@refs/tags/{{.Tag}}"}}
	raw := rawComponent{Prebuilt: &Prebuilt{ReleaseExecutable: true, Evidence: authored}}
	resolved, err := resolvePrebuilt("aicr", raw, Source{Repo: "https://github.com/NVIDIA/aicr", Tool: &tool}, pin, &PinLock{}, Component{})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Prebuilt.Evidence.Signer.Identity; got != "https://github.com/NVIDIA/aicr/.github/workflows/on-tag.yaml@refs/tags/v1.1.2" {
		t.Fatalf("rendered identity %q", got)
	}
	if !strings.Contains(authored.Signer.Identity, "{{.Tag}}") {
		t.Fatal("rendering mutated the authored declaration")
	}
	authored.Signer.Identity = "{{.Unknown}}"
	if _, err := resolvePrebuilt("aicr", raw, Source{Repo: "https://github.com/NVIDIA/aicr", Tool: &tool}, pin, &PinLock{}, Component{}); pinErrorCode(err) != e.CodeInvalidCatalog {
		t.Fatalf("unknown identity token: %v", err)
	}
}
