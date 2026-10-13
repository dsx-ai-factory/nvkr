// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestVerifiedReleaseExportRequiresOriginAndLiveCapability(t *testing.T) {
	const invocation = "https://github.com/example/nvkr/actions/runs/123/attempts/2"
	for _, tc := range []struct {
		name       string
		invocation string
		closed     bool
		cancel     bool
		code       e.Code
	}{
		{"exact invocation", invocation, false, false, ""},
		{"earlier aggregate attempt", "https://github.com/example/nvkr/actions/runs/123/attempts/1", false, false, e.CodeVerifyFailed},
		{"wrong run", "https://github.com/example/nvkr/actions/runs/124/attempts/2", false, false, e.CodeVerifyFailed},
		{"closed capability", invocation, true, false, e.CodeGateUnmet},
		{"canceled", invocation, false, true, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout, digest := testPreparedPublicationLayoutForInvocation(t, invocation)
			publication, err := delivery.OpenPublication(t.Context(), layout, digest)
			if err != nil {
				t.Fatal(err)
			}
			// The signature policy boundary is covered by Consumer tests. This
			// fixture isolates the authenticated-byte export and origin boundary.
			verified := &VerifiedRelease{valid: true, publication: publication,
				release: Release{Subject: "registry.example.test/product@" + digest}}
			defer verified.Close()
			if tc.closed {
				_ = verified.Close()
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			out := filepath.Join(t.TempDir(), "exported")
			err = verified.ExportForInvocation(ctx, tc.invocation, out)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if _, err := os.Lstat(out); !os.IsNotExist(err) {
					t.Fatal("failed export exposed a destination", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := delivery.Inspect(t.Context(), out)
			if err != nil || got.RootDigest != digest {
				t.Fatalf("exported root changed: %+v %v", got, err)
			}
			assertCode(t, verified.ExportForInvocation(ctx, invocation, out), e.CodeVerifyFailed)
		})
	}
}
