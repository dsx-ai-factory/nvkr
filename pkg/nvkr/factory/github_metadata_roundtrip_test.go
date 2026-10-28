// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func metadataTreeBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		result[relative] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// Exercise the public factory boundary, encoded provider record and consumer
// restoration together. No storage token or registry backend is available.
func roundtripProducedGitHubMetadata(t *testing.T, plan *Plan, artifact Artifact) {
	t.Helper()
	before := metadataTreeBytes(t, artifact.Path)
	member := carrierPath(artifact.ID)
	if err := os.MkdirAll(filepath.Dir(member), 0700); err != nil {
		t.Fatal(err)
	}
	if err := transportarchive.Pack(t.Context(), member, ".", []string{artifact.Path}); err != nil {
		t.Fatal(err)
	}
	index := t.TempDir()
	request := cistore.Request{Repository: "example/nvkr", RunID: "12345", Attempt: "1", Name: githubArtifactName(artifact.ID)}
	record, err := UploadGitHubCarrier(t.Context(), plan, cistore.GitHubUpload{Request: request, Root: ".", Paths: member, OutputDirectory: index, IfNoFilesFound: "error"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := cistore.DecodeLocator(raw)
	if err != nil || locator.Metadata == nil || locator.Reference != "" {
		t.Fatalf("not public metadata: %v", err)
	}
	destination := t.TempDir()
	selection := map[string]cistore.NestedArchive{request.Name: {Member: member, Allowed: []string{artifact.Path}}}
	if err := cistore.DownloadGitHubNested(t.Context(), request, index, destination, selection, cistore.Options{}); err != nil {
		t.Fatal(err)
	}
	if after := metadataTreeBytes(t, filepath.Join(destination, artifact.Path)); !reflect.DeepEqual(before, after) {
		t.Fatal("metadata bytes or membership changed in public transport")
	}
}

func TestUploadGitHubCarrierCanceledBeforeAdmission(t *testing.T) {
	root, output, snapshots := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", snapshots)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// Cancellation must win before Plan admission or reading a nonexistent carrier.
	record, err := UploadGitHubCarrier(ctx, nil, cistore.GitHubUpload{
		Request: cistore.Request{Repository: "example/nvkr", RunID: "12345", Attempt: "1", Name: "nvkr-metadata"},
		Root:    root, Paths: "missing.tar", OutputDirectory: output, IfNoFilesFound: "error",
	})
	assertFactoryErrorCode(t, err, e.CodeTimeout)
	if record != "" {
		t.Fatalf("canceled upload returned locator %q", record)
	}
	for _, directory := range []string{root, output, snapshots} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("canceled upload changed %s: %v %v", directory, entries, err)
		}
	}
}

func TestGitHubMetadataProducedControlsRoundtrip(t *testing.T) {
	selected := recompileGitHubStabilityPlan(t, referenceTestPlan(t), func(inputs *githubStabilityInputs) {
		inputs.definition.Outputs = inputs.definition.Outputs[:1]
	})
	plan, artifacts, _ := referenceMetadataFixtureForPlan(t, selected)
	output := plan.LifecycleOutputs[0].Name
	route := outputPublicationRoute(output)
	snapshotRoot := t.TempDir()
	t.Setenv("TMPDIR", snapshotRoot)
	for _, id := range []string{"sbom/" + output, "predicate/" + route, "metadata/" + route, "signing-metadata/" + route} {
		t.Run(id, func(t *testing.T) {
			artifact := artifacts[id]
			if artifact.ID == "" {
				t.Fatal("missing produced fixture artifact")
			}
			if artifact.Kind == ArtifactProvenancePredicate {
				raw, err := os.ReadFile(artifact.Path)
				if err != nil || !bytes.HasSuffix(raw, []byte("\n")) {
					t.Fatal("fixture lost signed predicate terminator")
				}
			}
			roundtripProducedGitHubMetadata(t, plan, artifact)
			entries, err := filepath.Glob(filepath.Join(snapshotRoot, "nvkr-metadata-admission-*"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("snapshot leaked: %v %v", entries, err)
			}
		})
	}
}

func TestGitHubMetadataRejectsMalformedOCIArchiveWithoutReceipt(t *testing.T) {
	// Archive admission needs the expected descriptor, not a signed release.
	artifact := Artifact{ID: "metadata/output", Kind: ArtifactPublicationMetadata, Path: "metadata/output"}
	snapshotRoot := t.TempDir()
	t.Setenv("TMPDIR", snapshotRoot)
	for _, kind := range []string{"parent traversal", "absolute path", "symlink", "hardlink", "duplicate", "invalid layout", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			var buf bytes.Buffer
			archive := tar.NewWriter(&buf)
			root := &tar.Header{Name: artifact.Path, Mode: 0755, Typeflag: tar.TypeDir}
			if err := archive.WriteHeader(root); err != nil {
				t.Fatal(err)
			}
			header := &tar.Header{Name: artifact.Path + "/index.json", Mode: 0644, Typeflag: tar.TypeReg, Size: 2}
			switch kind {
			case "parent traversal":
				header.Name = artifact.Path + "/../../escape.json"
			case "absolute path":
				header.Name = "/escape.json"
			case "symlink":
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "../../outside"
				header.Size = 0
			case "hardlink":
				header.Typeflag = tar.TypeLink
				header.Linkname = "../../outside"
				header.Size = 0
			case "duplicate":
				header = root
			}
			if err := archive.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Size > 0 {
				if _, err := archive.Write([]byte("{}")); err != nil {
					t.Fatal(err)
				}
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			carrier := filepath.Join(t.TempDir(), "carrier.tar")
			if err := os.WriteFile(carrier, buf.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			receipt, err := admitGitHubMetadata(ctx, artifact, carrier)
			if kind == "canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("private admission cancellation = %v, want context.Canceled", err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			}
			if receipt != nil {
				t.Fatal("rejected archive issued publication receipt")
			}
			entries, err := filepath.Glob(filepath.Join(snapshotRoot, "nvkr-metadata-admission-*"))
			if err != nil || len(entries) != 0 {
				t.Fatal("rejected archive leaked snapshot")
			}
		})
	}
}

func TestGitHubMetadataRejectsChangedSBOMMembershipAndDigest(t *testing.T) {
	_, artifacts, _ := publicationSBOMFixture(t)
	var artifact Artifact
	for _, a := range artifacts {
		if a.Kind == "sbom-set" {
			artifact = a
			break
		}
	}
	if artifact.ID == "" {
		t.Fatal("fixture omitted SBOM set")
	}
	for _, kind := range []string{"extra", "missing", "changed digest"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			for name, raw := range metadataTreeBytes(t, artifact.Path) {
				if kind == "missing" && strings.HasSuffix(name, ".cdx.json") {
					continue
				}
				if kind == "changed digest" && name == "index.json" {
					var index sbom.Index
					if err := canonical.UnmarshalExact([]byte(strings.TrimSuffix(raw, "\n")), &index); err != nil {
						t.Fatal(err)
					}
					if len(index.Entries) == 0 {
						t.Fatal("fixture has no indexed SBOM")
					}
					index.Entries[0].SBOMDigest = "sha256:" + strings.Repeat("f", 64)
					changed, err := canonical.Marshal(index)
					if err != nil {
						t.Fatal(err)
					}
					raw = string(changed)
				}
				if err := os.WriteFile(filepath.Join(root, name), []byte(raw), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "extra" {
				if err := os.WriteFile(filepath.Join(root, "extra.json"), []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			copied := artifact
			copied.Path = "sboms"
			workspace := t.TempDir()
			if err := os.Rename(root, filepath.Join(workspace, copied.Path)); err != nil {
				t.Fatal(err)
			}
			carrier := filepath.Join(workspace, "carrier.tar")
			if err := transportarchive.Pack(t.Context(), carrier, workspace, []string{copied.Path}); err != nil {
				t.Fatal(err)
			}
			receipt, err := admitGitHubMetadata(t.Context(), copied, carrier)
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			if receipt != nil {
				t.Fatal("invalid SBOM set issued receipt")
			}
		})
	}
}
