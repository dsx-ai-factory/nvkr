// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestStandaloneDiskRegistryRoundTrip(t *testing.T) {
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			root, payload, manifest := diskFixture(t, platform)
			owner, err := OpenDiskArtifact(t.Context(), root, manifest, platform, int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			repo := strings.TrimPrefix(server.URL, "http://") + "/local-disk-" + strings.TrimPrefix(platform, "linux/")
			reference, err := PushDiskArtifact(t.Context(), owner, repo, t.TempDir(), options)
			if err != nil {
				t.Fatal(err)
			}
			if reference != repo+"@"+manifest {
				t.Fatal("manifest identity changed", reference)
			}
			for _, mode := range []string{"exact", "too-small", "wrong-platform", "unpinned"} {
				t.Run(mode, func(t *testing.T) {
					out := filepath.Join(t.TempDir(), "pulled")
					pin, target, limit := reference, platform, int64(len(payload))
					code := e.CodeVerifyFailed
					switch mode {
					case "too-small":
						limit--
					case "wrong-platform":
						target = "linux/amd64"
						if platform == target {
							target = "linux/arm64"
						}
					case "unpinned":
						pin = repo + ":latest"
						code = e.CodeUnpinnedSource
					}
					err := PullDiskArtifact(t.Context(), pin, out, target, limit, options)
					if mode != "exact" {
						assertRegistryCode(t, err, code)
						if _, err := os.Stat(out); !os.IsNotExist(err) {
							t.Fatal("failed pull published output")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					pulled, err := OpenDiskArtifact(t.Context(), out, manifest, platform, limit)
					if err != nil {
						t.Fatal(err)
					}
					defer pulled.Close()
					if pulled.Inspection() != owner.Inspection() {
						t.Fatal("round-trip changed transport identity")
					}
				})
			}
		})
	}
}
