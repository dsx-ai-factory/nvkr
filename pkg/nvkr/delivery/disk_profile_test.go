// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestStandardDiskProfileRejectsSelfPinnedMetadataChanges(t *testing.T) {
	for _, test := range []struct {
		name, platform string
		edit           func(*ociManifest, *[]byte)
	}{
		{"standard", "linux/amd64", nil},
		{"standard arm64", "linux/arm64", nil},
		{"extra manifest annotation", "linux/amd64", func(m *ociManifest, _ *[]byte) { m.Annotations["example.com/unapproved"] = "changed" }},
		{"missing format", "linux/amd64", func(m *ociManifest, _ *[]byte) { delete(m.Annotations, diskFormatAnnotation) }},
		{"missing encryption", "linux/amd64", func(m *ociManifest, _ *[]byte) { delete(m.Annotations, diskEncryptionAnnotation) }},
		{"unsafe title", "linux/amd64", func(m *ociManifest, _ *[]byte) { m.Layers[0].Annotations[ocispec.AnnotationTitle] = "../../etc/passwd" }},
		{"extra layer annotation", "linux/amd64", func(m *ociManifest, _ *[]byte) { m.Layers[0].Annotations["example.com/unapproved"] = "changed" }},
		{"alternate config encoding", "linux/amd64", func(_ *ociManifest, raw *[]byte) { *raw = append([]byte(" "), (*raw)...) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, payload, pin := diskFixture(t, test.platform)
			if test.edit != nil {
				pin = rewriteDiskFixture(t, root, pin, test.edit)
			}
			// The generic reader still admits valid OCI diagnostics. Its
			// own newly computed pin is not proof of the producer profile.
			artifact, err := OpenDiskArtifact(t.Context(), root, pin, test.platform, int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = artifact.Close() }()
			err = artifact.RequireStandardProfile(t.Context())
			if test.edit == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestStandardDiskProfileRequiresOpenOwnerAndContext(t *testing.T) {
	root, payload, pin := diskFixture(t, "linux/amd64")
	artifact, err := OpenDiskArtifact(t.Context(), root, pin, "linux/amd64", int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = artifact.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertRegistryCode(t, artifact.RequireStandardProfile(ctx), e.CodeTimeout)
	assertRegistryCode(t, (*DiskArtifact)(nil).RequireStandardProfile(t.Context()), e.CodeGateUnmet)
	if err := artifact.Close(); err != nil {
		t.Fatal(err)
	}
	assertRegistryCode(t, artifact.RequireStandardProfile(t.Context()), e.CodeGateUnmet)
}
