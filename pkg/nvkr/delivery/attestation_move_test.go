// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func TestAttestPreparedEvidenceTransfersDiskWithoutCopy(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, arch)
			parent := t.TempDir()
			source, out := filepath.Join(parent, "prepared"), filepath.Join(parent, "attested")
			if _, err := BuildPublicationRoot(t.Context(), source, request); err != nil {
				t.Fatal(err)
			}
			pin := request.Objects[0].Output.Disk.PayloadDigest
			before, err := os.Stat(fixtureBlobPath(source, pin))
			if err != nil {
				t.Fatal(err)
			}
			result, err := AttestPreparedEvidence(t.Context(), source, out, func(_ context.Context, subject string, document []byte) ([]byte, error) {
				if _, err := os.Stat(source); err != nil {
					t.Fatal("preparation consumed before signing completed", err)
				}
				return diskFixtureBundle(t, subject, document), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(fixtureBlobPath(out, pin))
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("attestation copied unchanged disk payload: %v", err)
			}
			if _, err := os.Lstat(source); !os.IsNotExist(err) {
				t.Fatalf("successful transfer retained preparation: %v", err)
			}
			inspection, err := Inspect(t.Context(), out)
			if err != nil || inspection.RootDigest != result.RootDigest {
				t.Fatalf("final closure differs from result: %v", err)
			}
			if err := RequireAttestedEvidence(inspection); err != nil {
				t.Fatal(err)
			}
			assertNoAttestationStage(t, parent)
		})
	}
}

func TestAttestPreparedEvidenceRejectsBeforeClaim(t *testing.T) {
	for _, test := range []struct {
		name   string
		before func(*testing.T, *string, *string)
		code   e.Code
	}{
		{"existing destination", func(t *testing.T, _, out *string) { mustMkdir(t, *out) }, e.CodeImmutableViolation},
		{"nested destination", func(_ *testing.T, source, out *string) { *out = filepath.Join(*source, "nested", "out") }, e.CodeUsage},
		{"source symlink", func(t *testing.T, source, _ *string) {
			original := *source
			*source = original + "-link"
			if err := os.Symlink(original, *source); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
		{"unexpected directory", func(t *testing.T, source, _ *string) { mustMkdir(t, filepath.Join(*source, "extra")) }, e.CodeVerifyFailed},
		{"unexpected file", func(t *testing.T, source, _ *string) {
			if err := os.WriteFile(filepath.Join(*source, "extra"), []byte("not disposable metadata"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
		{"hard link", func(t *testing.T, source, _ *string) {
			if err := os.Link(filepath.Join(*source, "index.json"), filepath.Join(t.TempDir(), "alias")); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, _ := releasePublicationFixture(t)
			parent := t.TempDir()
			out := filepath.Join(parent, "attested")
			test.before(t, &source, &out)
			called := false
			_, err := AttestPreparedEvidence(t.Context(), source, out, func(_ context.Context, subject string, document []byte) ([]byte, error) {
				called = true
				return imageIndexFixtureAttestation(subject, document)
			})
			assertRegistryCode(t, err, test.code)
			if called {
				t.Fatal("rejected layout reached signing callback")
			}
			if _, err := os.Lstat(source); err != nil {
				t.Fatal("admission failure consumed source", err)
			}
			assertNoAttestationStage(t, parent)
		})
	}
}

func TestAttestPreparedEvidenceFailureLifetime(t *testing.T) {
	for _, test := range []struct {
		name     string
		code     e.Code
		consumed bool
	}{
		{"signer failure", e.CodeSignatureInvalid, false},
		{"substituted document", e.CodeVerifyFailed, false},
		{"mutated payload", e.CodeVerifyFailed, false},
		{"same-metadata payload mutation", e.CodeVerifyFailed, true},
		{"replaced source", e.CodeVerifyFailed, false},
		{"canceled before claim", e.CodeTimeout, false},
		{"canceled after claim", e.CodeTimeout, true},
		{"destination appeared", e.CodeImmutableViolation, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "amd64")
			parent := t.TempDir()
			source, out := filepath.Join(parent, "prepared"), filepath.Join(parent, "attested")
			if _, err := BuildPublicationRoot(t.Context(), source, request); err != nil {
				t.Fatal(err)
			}
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := base
			if test.name == "canceled after claim" {
				ctx = &cancelAfterPreparedClaim{Context: base, source: source, cancel: cancel}
			}
			first := true
			result, err := AttestPreparedEvidence(ctx, source, out, func(_ context.Context, subject string, document []byte) ([]byte, error) {
				if first {
					first = false
					switch test.name {
					case "signer failure":
						return nil, e.New(e.CodeSignatureInvalid, "fixture signer failed")
					case "substituted document":
						var bom sbom.BOM
						if err := json.Unmarshal(document, &bom); err != nil {
							t.Fatal(err)
						}
						bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "zzfixture", Value: "substituted"})
						var err error
						document, err = bom.Marshal()
						if err != nil {
							t.Fatal(err)
						}
					case "mutated payload":
						if err := os.WriteFile(fixtureBlobPath(source, request.Objects[0].Output.Disk.PayloadDigest), []byte("substitution"), 0o600); err != nil {
							t.Fatal(err)
						}
					case "same-metadata payload mutation":
						mutateAttestationPayloadKeepingMetadata(t, fixtureBlobPath(source, request.Objects[0].Output.Disk.PayloadDigest))
					case "replaced source":
						if err := os.Rename(source, source+"-original"); err != nil {
							t.Fatal(err)
						}
						mustMkdir(t, source)
					case "canceled before claim":
						cancel()
					case "destination appeared":
						mustMkdir(t, out)
					}
				}
				return diskFixtureBundle(t, subject, document), nil
			})
			assertRegistryCode(t, err, test.code)
			if result != (Result{}) {
				t.Fatal("failed transition issued a result")
			}
			_, sourceErr := os.Lstat(source)
			if test.consumed != os.IsNotExist(sourceErr) {
				t.Fatalf("wrong source lifetime: consumed=%t stat=%v", test.consumed, sourceErr)
			}
			if test.name == "destination appeared" {
				entries, err := os.ReadDir(out)
				if err != nil || len(entries) != 0 {
					t.Fatalf("concurrent destination changed: %v", err)
				}
			} else if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("failure exposed final layout: %v", err)
			}
			assertNoAttestationStage(t, parent)
		})
	}
}

// Cancellation is delivered at the filesystem ownership boundary, without a
// timing race or a production hook. Cancel closes Done before Err is returned.
type cancelAfterPreparedClaim struct {
	context.Context
	source string
	cancel context.CancelFunc
}

func (ctx *cancelAfterPreparedClaim) Err() error {
	if _, err := os.Lstat(ctx.source); os.IsNotExist(err) {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestAttestedLayoutCommitSyncFailureRollsBack(t *testing.T) {
	stage, _ := releasePublicationFixture(t)
	out := filepath.Join(filepath.Dir(stage), "attested")
	err := publishAttestedLayoutWithSync(t.Context(), stage, out, func(*os.File) error {
		return errors.New("fixture parent fsync failure")
	})
	assertRegistryCode(t, err, e.CodeInternal)
	if _, err := os.Stat(stage); err != nil {
		t.Fatal("commit failure did not restore private stage for cleanup", err)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatal("commit failure exposed final layout", err)
	}
	if _, err := Inspect(t.Context(), stage); err != nil {
		t.Fatal("rollback changed layout bytes", err)
	}
}

func assertNoAttestationStage(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".nvkr-delivery-") {
			t.Fatal("attestation retained private staging directory", entry.Name())
		}
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mutateAttestationPayloadKeepingMetadata(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
		t.Fatal(err)
	}
	last[0] ^= 0xff
	if _, err := file.WriteAt(last[:], info.Size()-1); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
}
