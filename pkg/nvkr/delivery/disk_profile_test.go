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
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, test := range []struct {
			name string
			edit func(*ociManifest, *[]byte)
		}{
			{"standard", nil},
			{"extra manifest annotation", func(m *ociManifest, _ *[]byte) { m.Annotations["example.com/unapproved"] = "changed" }},
			{"missing format", func(m *ociManifest, _ *[]byte) { delete(m.Annotations, diskFormatAnnotation) }},
			{"missing encryption", func(m *ociManifest, _ *[]byte) { delete(m.Annotations, diskEncryptionAnnotation) }},
			{"unsafe title", func(m *ociManifest, _ *[]byte) { m.Layers[0].Annotations[ocispec.AnnotationTitle] = "../../etc/passwd" }},
			{"extra layer annotation", func(m *ociManifest, _ *[]byte) { m.Layers[0].Annotations["example.com/unapproved"] = "changed" }},
			{"alternate config encoding", func(_ *ociManifest, raw *[]byte) { *raw = append([]byte(" "), (*raw)...) }},
		} {
			t.Run(platform+"/"+test.name, func(t *testing.T) {
				root, payload, pin := diskFixture(t, platform)
				if test.edit != nil {
					pin = rewriteDiskFixture(t, root, pin, test.edit)
				}
				// The generic reader still admits valid OCI diagnostics. Its
				// own newly computed pin is not proof of the producer profile.
				artifact, err := OpenDiskArtifact(t.Context(), root, pin, platform, int64(len(payload)))
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
