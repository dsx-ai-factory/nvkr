// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestExecutableCaptureRechecksStreamDescriptor(t *testing.T) {
	options := buildFixture(t, "success")
	raw, err := os.ReadFile(fixtureToolPath(options))
	if err != nil {
		t.Fatal(err)
	}
	sentinel := e.New(e.CodeFetchFailed, "stream interrupted")
	for _, tc := range []struct {
		name        string
		edit        func(*lifecycle.ExecutableMemberDescription)
		streamError error
		code        e.Code
	}{
		{"exact", nil, nil, ""},
		{"wrong output", func(d *lifecycle.ExecutableMemberDescription) { d.Output = "sibling" }, nil, e.CodeVerifyFailed},
		{"wrong member", func(d *lifecycle.ExecutableMemberDescription) { d.Member = "sibling" }, nil, e.CodeVerifyFailed},
		{"wrong platform", func(d *lifecycle.ExecutableMemberDescription) { d.Platform = "linux/unknown" }, nil, e.CodeVerifyFailed},
		{"wrong digest", func(d *lifecycle.ExecutableMemberDescription) { d.PayloadDigest = "sha256:" + strings.Repeat("a", 64) }, nil, e.CodeVerifyFailed},
		{"wrong size", func(d *lifecycle.ExecutableMemberDescription) { d.Size++ }, nil, e.CodeVerifyFailed},
		{"partial stream", nil, sentinel, e.CodeFetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, err := captureExecutable(t.Context(), options.Hydrophone, func(ctx context.Context, output, member, platform string, limit int64, w io.Writer) (lifecycle.ExecutableMemberDescription, error) {
				if limit != maxToolBytes {
					t.Fatal("owner byte limit lost")
				}
				_, writeErr := w.Write(raw)
				if writeErr != nil {
					return lifecycle.ExecutableMemberDescription{}, writeErr
				}
				d := lifecycle.ExecutableMemberDescription{Output: output, Member: member, Platform: platform, PayloadDigest: blobDigest(raw), Size: int64(len(raw))}
				if tc.edit != nil {
					tc.edit(&d)
				}
				return d, tc.streamError
			})
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = held.Close() }()
				if held.Digest() != blobDigest(raw) {
					t.Fatal("exact bytes changed")
				}
				return
			}
			requireCode(t, err, tc.code)
			if held != nil {
				_ = held.Close()
				t.Fatal("refused member exposed executable")
			}
			if tc.streamError != nil && !errors.Is(err, sentinel) {
				t.Fatal("stream cause lost")
			}
		})
	}
}
func TestImageSelectorsUseAuthenticatedNativeOutput(t *testing.T) {
	options := optionsFixture()
	options.ConformanceImage.Platform = "linux/amd64"
	options.Busybox.Platform = "linux/amd64"
	description := artifactDescriptionFixture()
	first := description.Outputs[0]
	first.Name = "conformance"
	second := first
	second.Name = "busybox"
	second.Reference = "registry.example.test/busybox@" + first.Subject
	description.Outputs = []lifecycle.ArtifactCheckOutput{first, second}
	for _, tc := range []struct {
		name string
		edit func(*lifecycle.ArtifactCheckDescription)
		code e.Code
	}{
		{"exact mirror", func(*lifecycle.ArtifactCheckDescription) {}, ""},
		{"missing output", func(d *lifecycle.ArtifactCheckDescription) { d.Outputs = d.Outputs[:1] }, e.CodeVerifyFailed},
		{"ambiguous output", func(d *lifecycle.ArtifactCheckDescription) { d.Outputs = append(d.Outputs, d.Outputs[0]) }, e.CodeVerifyFailed},
		{"wrong kind", func(d *lifecycle.ArtifactCheckDescription) { d.Outputs[0].Kind = lifecycle.OutputExecutable }, e.CodeEngineCapability},
		{"wrong subject", func(d *lifecycle.ArtifactCheckDescription) {
			d.Outputs[0].Subject = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeVerifyFailed},
		{"wrong platform", func(d *lifecycle.ArtifactCheckDescription) { d.Outputs[0].Platform = "linux/arm64" }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := description
			d.Outputs = append([]lifecycle.ArtifactCheckOutput(nil), description.Outputs...)
			tc.edit(&d)
			image, busybox, err := resolveImages(d, options)
			if tc.code != "" {
				requireCode(t, err, tc.code)
				if image != "" || busybox != "" {
					t.Fatal("partial image graph exposed")
				}
				return
			}
			if err != nil || image != first.Reference || busybox != second.Reference {
				t.Fatalf("wrong mirrored refs %s %s %v", image, busybox, err)
			}
		})
	}
}
func TestExecutableWriterBudgetAndCancellation(t *testing.T) {
	writer := &executableWriter{ctx: t.Context(), destination: io.Discard, written: maxToolBytes - 1}
	n, err := writer.Write([]byte("xx"))
	if n != 0 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("budget admitted: %d %v", n, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	writer.ctx = ctx
	n, err = writer.Write([]byte("x"))
	if n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write admitted: %d %v", n, err)
	}
}
