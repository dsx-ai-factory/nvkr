// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestBoundedTrustAuthenticatesTUFWithoutAmbientCache(t *testing.T) {
	root, files, poisonedTimestamp := boundedTUFRepository(t)
	for _, scenario := range []string{"exact", "tampered-target", "read-budget", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			opts := tuf.DefaultOptions().WithRoot(root)
			ambient := filepath.Join(opts.CachePath, tuf.URLToPath(opts.RepositoryBaseURL))
			if err := os.MkdirAll(ambient, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ambient, "timestamp.json"), poisonedTimestamp, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			allowance := int64(1 << 20)
			if scenario == "read-budget" {
				allowance = 64
			}
			ctx, budget, err := readbudget.WithLimit(ctx, allowance)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "canceled" {
				cancel()
			}
			var delivered int64
			var paths []string
			transport := trustTransport(func(request *http.Request) (*http.Response, error) {
				if err := request.Context().Err(); err != nil {
					return nil, err
				}
				paths = append(paths, request.URL.Path)
				raw, exists := files[request.URL.Path]
				status := http.StatusOK
				if !exists {
					status, raw = http.StatusNotFound, []byte("missing next root")
				}
				if scenario == "tampered-target" && strings.HasSuffix(request.URL.Path, "/trusted_root.json") {
					raw = []byte("wrong trusted target")
				}
				delivered += int64(len(raw))
				return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(raw)), Header: http.Header{}}, nil
			})
			opts.WithFetcher(&boundedTrustFetcher{ctx: ctx, client: &http.Client{Transport: readbudget.Transport(ctx, transport)}})
			raw, err := loadBoundedTrust(ctx, budget, opts)
			switch scenario {
			case "exact":
				if err != nil || !bytes.Equal(raw, files["/targets/trusted_root.json"]) || len(paths) != 5 || paths[0] != "/2.root.json" || budget.Used() != delivered {
					t.Fatal("authenticated TUF read/rotation/budget mismatch", err, paths, budget.Used(), delivered)
				}
			case "tampered-target":
				requireSignCode(t, err, e.CodeTrustPolicyMissing)
			case "read-budget":
				requireSignCode(t, err, e.CodeEngineCapability)
			case "canceled":
				requireSignCode(t, err, e.CodeTimeout)
			}
			if err != nil && len(raw) != 0 {
				t.Fatal("failed TUF load released a root")
			}
			entries, err := os.ReadDir(ambient)
			if err != nil || len(entries) != 1 || entries[0].Name() != "timestamp.json" {
				t.Fatal("TUF wrote into ambient cache", entries, err)
			}
			got, err := os.ReadFile(filepath.Join(ambient, "timestamp.json"))
			if err != nil || !bytes.Equal(got, poisonedTimestamp) {
				t.Fatal("ambient trusted timestamp changed", err)
			}
		})
	}
}

// These keys sign an in-memory synthetic TUF repository, not Sigstore artifact
// evidence. A real TUF client must authenticate every role before target use.
func boundedTUFRepository(t *testing.T) ([]byte, map[string][]byte, []byte) {
	t.Helper()
	expires := time.Now().Add(time.Hour)
	root, targets := metadata.Root(expires), metadata.Targets(expires)
	snapshot, timestamp := metadata.Snapshot(expires), metadata.Timestamp(expires)
	root.Signed.ConsistentSnapshot = false
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := metadata.KeyFromPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
		if err := root.Signed.AddKey(key, role); err != nil {
			t.Fatal(err)
		}
	}
	signer, err := signature.LoadSigner(private, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	encode := func(document interface {
		Sign(signature.Signer) (*metadata.Signature, error)
		ToBytes(bool) ([]byte, error)
	}) []byte { if _, err := document.Sign(signer); err != nil {
		t.Fatal(err)
	}; raw, err := document.ToBytes(false); if err != nil {
		t.Fatal(err)
	}; return raw }
	target := []byte(`{"fixture":"authenticated trust target"}`)
	info, err := metadata.TargetFile().FromBytes("trusted_root.json", target, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	targets.Signed.Targets["trusted_root.json"] = info
	snapshot.Signed.Meta["targets.json"] = metadata.MetaFile(1)
	timestamp.Signed.Meta["snapshot.json"] = metadata.MetaFile(1)
	files := map[string][]byte{"/targets.json": encode(targets), "/snapshot.json": encode(snapshot), "/timestamp.json": encode(timestamp), "/targets/trusted_root.json": target}
	timestamp.Signed.Version = 999
	timestamp.ClearSignatures()
	return encode(root), files, encode(timestamp)
}
