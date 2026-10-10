// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"errors"
	"os"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestVerifyImageLayerAdmitsDescriptorBeforePayloadAccess(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Descriptor, *string)
	}{
		{"invalid layer digest", func(layer *Descriptor, _ *string) { layer.Digest = "sha256:invalid" }},
		{"invalid diff ID", func(_ *Descriptor, diffID *string) { *diffID = "sha256:invalid" }},
		{"negative size", func(layer *Descriptor, _ *string) { layer.Size = -1 }},
		{"oversized layer", func(layer *Descriptor, _ *string) { layer.Size = maxDeliveryFileBytes + 1 }},
		{"unsupported media", func(layer *Descriptor, _ *string) { layer.MediaType = "application/octet-stream" }},
		{"valid descriptor with missing payload", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			layer := Descriptor{Digest: digestBytes([]byte("missing image layer")), Size: 19, MediaType: MediaImageLayer}
			diffID := layer.Digest
			if test.edit != nil {
				test.edit(&layer, &diffID)
			}
			if _, err := os.Stat(fixtureBlobPath(root, layer.Digest)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("fixture must have no payload file: %v", err)
			}

			err := verifyImageLayerFrom(t.Context(), layer, diffID, pathImageBlobs(root))
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			want := "container image layer has an invalid identity, size or media type"
			if test.edit == nil {
				want = "opening OCI blob"
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("wrong admission failure: %v, want %q", err, want)
			}
			if errors.Is(err, os.ErrNotExist) != (test.edit == nil) {
				t.Fatalf("payload-open failure must occur only for the valid descriptor: %v", err)
			}
		})
	}
}
