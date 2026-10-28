// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestPullOutputPackages(t *testing.T) {
	for _, format := range []string{outputFormatHelm, "apt-repository"} {
		scenarios := []string{"exact", "payload corruption", "invalid contents", "existing destination"}
		if format == "apt-repository" {
			scenarios = append(scenarios, "read-only directories")
		}
		for _, scenario := range scenarios {
			t.Run(format+"/"+scenario, func(t *testing.T) {
				fixture := packagePullFixture(t, format, scenario)
				var active atomic.Bool
				var unrelatedGets atomic.Int64
				handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if active.Load() && r.Method == http.MethodGet {
						if strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.otherPayload) {
							unrelatedGets.Add(1)
							http.Error(w, "unselected payload is unavailable", http.StatusForbidden)
							return
						}
						if scenario == "payload corruption" && strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.payloadDigest) {
							bad := append([]byte(nil), fixture.payload...)
							bad[0] ^= 1
							_, _ = w.Write(bad)
							return
						}
					}
					handler.ServeHTTP(w, r)
				}))
				defer server.Close()
				options := RegistryOptions{Anonymous: true, PlainHTTP: true}
				reference, err := Push(t.Context(), fixture.root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
				if err != nil {
					t.Fatal(err)
				}
				active.Store(true)
				parent, workspace := t.TempDir(), t.TempDir()
				out := filepath.Join(parent, "output")
				if scenario == "existing destination" {
					if err := os.Mkdir(out, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				result, err := PullOutput(t.Context(), reference, "package", out, workspace, options)
				assertEmptyPullDirectory(t, workspace)
				if unrelatedGets.Load() != 0 {
					t.Fatal("pull downloaded an unrelated executable payload")
				}
				if scenario != "exact" && scenario != "read-only directories" {
					code := e.CodeVerifyFailed
					if scenario == "existing destination" {
						code = e.CodeImmutableViolation
						assertEmptyPullDirectory(t, out)
					} else {
						assertEmptyPullDirectory(t, parent)
					}
					assertRegistryCode(t, err, code)
					if result.Output.Name != "" {
						t.Fatal("failed pull returned a result")
					}
					return
				}
				if err != nil || result.Output.Name != "package" || result.Output.Kind != format {
					t.Fatalf("wrong package result: %+v %v", result, err)
				}
				if format == outputFormatHelm {
					if len(result.Files) != 1 || result.Files[0].Size != int64(len(fixture.payload)) || result.Files[0].SHA256 != fixture.payloadDigest {
						t.Fatalf("chart file description differs from exact archive: %+v", result.Files)
					}
					if result.Output.ChartVersion != "1.2.3" {
						t.Fatalf("chart version differs from its exact config: %+v", result.Output)
					}
					raw, err := os.ReadFile(filepath.Join(out, "package.tgz"))
					if err != nil || !bytes.Equal(raw, fixture.payload) {
						t.Fatalf("chart archive changed: %v", err)
					}
				} else {
					repository, err := InspectAPTRepository(t.Context(), out)
					if err != nil || repository.Digest != fixture.semantic {
						t.Fatalf("APT projection differs: %+v %v", repository, err)
					}
					for _, name := range []string{"dists/stable/Release", "dists/stable/main/binary-amd64/Packages", "nvkr-apt-repository.json"} {
						if _, err := os.Stat(filepath.Join(out, name)); err != nil {
							t.Fatalf("APT projection missing %s: %v", name, err)
						}
					}
					if _, err := os.Stat(filepath.Join(out, ".nvkr")); !os.IsNotExist(err) {
						t.Fatalf("transport path leaked into destination: %v", err)
					}
					if scenario == "read-only directories" {
						info, err := os.Stat(filepath.Join(out, "dists/stable"))
						if err != nil || info.Mode().Perm() != 0o555 {
							t.Fatalf("APT directory permissions changed: %v %v", info, err)
						}
						// Test cleanup owns the published output after its mode assertion.
						if err := os.Chmod(filepath.Join(out, "dists/stable"), 0o755); err != nil {
							t.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func TestPullHelmRejectsAmbiguousConfigBeforePayload(t *testing.T) {
	for _, scenario := range []string{"case alias", "duplicate version", "unknown field"} {
		t.Run(scenario, func(t *testing.T) {
			layout, archive := helmMetadataLayoutFixture(t)
			_, manifest := fixtureManifest(t, layout)
			config, err := readBlob(t.Context(), layout, manifest.Config, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "case alias":
				config = bytes.Replace(config, []byte(`"version"`), []byte(`"Version"`), 1)
			case "duplicate version":
				config = append([]byte(`{"version":"1.2.3",`), config[1:]...)
			case "unknown field":
				config = append([]byte(`{"unexpected":true,`), config[1:]...)
			}
			writer := layoutWriter{root: layout}
			manifest.Config, err = writer.addBlobBytes(config, MediaHelmConfig)
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				http.Error(w, "payload must not be requested", http.StatusForbidden)
			}))
			defer server.Close()
			object := PublishedObject{Name: "package", ArtifactType: manifest.ArtifactType,
				DescriptorDigest: descriptor.Digest, Size: descriptor.Size, BlobDigest: digestBytes(archive),
				Output: &OutputBinding{Format: outputFormatHelm}}
			parent, workspace := t.TempDir(), t.TempDir()
			reference := strings.TrimPrefix(server.URL, "http://") + "/package@" + descriptor.Digest
			_, err = pullHelmOutput(t.Context(), reference, filepath.Join(parent, "out"), layout, workspace, object, RegistryOptions{Anonymous: true, PlainHTTP: true})
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if requests.Load() != 0 {
				t.Fatal("invalid chart config reached payload retrieval")
			}
			assertEmptyPullDirectory(t, parent)
			assertEmptyPullDirectory(t, workspace)
		})
	}
}

type packagePullData struct {
	root, payloadDigest, otherPayload, semantic string
	payload                                     []byte
}

func packagePullFixture(t *testing.T, format, scenario string) packagePullData {
	t.Helper()
	invalid := scenario == "invalid contents"
	request := releasePublicationRequestFixture(t, "linux", "amd64")
	build := request.Objects[0].Output.BuildDigest
	fixture := packagePullData{}
	var object ObjectInput
	if format == outputFormatHelm {
		fixture.payload = helmChartArchiveFixture(t, map[string]string{
			"demo/Chart.yaml":  "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
			"demo/values.yaml": "replicas: 1\n",
		})
		if invalid {
			fixture.payload[len(fixture.payload)-8] ^= 1 // Legacy production does not inspect this trailer.
		}
		layout := filepath.Join(t.TempDir(), "chart")
		built, err := BuildHelmChartArtifact(t.Context(), layout, "application/vnd.nvkr.helm-bundle.v1", fixture.payload)
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := PayloadDescriptor(t.Context(), layout)
		if err != nil {
			t.Fatal(err)
		}
		object = ObjectInput{Name: "package", Kind: outputFormatHelm, ArtifactType: "application/vnd.nvkr.helm-bundle.v1", Layout: true, Path: layout,
			BlobDigest: built.RootDigest, SemanticDigest: built.SemanticDigest,
			Output: &OutputBinding{Format: format, Repository: "package", BuildDigest: build, Components: []string{}}}
		bom, err := sbom.NewSubject(build, "package", descriptor.Digest)
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.ForSubject(bom, descriptor.Digest, "packaging", digestBytes(fixture.payload))
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "application", Name: "demo", Version: "1.2.3"}}, "helm-manifests/v1", "")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		request.Objects = append(request.Objects, ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json",
			LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: raw,
			Evidence: &EvidenceBinding{Outputs: []string{"package"}, Component: "package", Stage: "packaging", SubjectKind: "helm-bundle", SubjectDigest: descriptor.Digest, InspectionInputDigest: digestBytes(fixture.payload)}})
	} else {
		workspace := t.TempDir()
		binary := filepath.Join(workspace, "cli")
		if err := os.WriteFile(binary, []byte("exact executable"), 0o755); err != nil {
			t.Fatal(err)
		}
		name := "cli"
		if invalid {
			name = "other-cli"
		}
		deb := filepath.Join(workspace, "input.deb")
		if _, err := assemble.Deb(binary, name, "1.0.0", 1_748_736_000, "amd64", deb); err != nil { //nolint:staticcheck // explicitly unqualified package fixture
			t.Fatal(err)
		}
		transportPath := ".nvkr/factory/packages/package"
		apt, err := BuildAPTRepository(t.Context(), filepath.Join(workspace, transportPath), APTRequest{Distribution: "stable", Component: "main", Epoch: 1_748_736_000,
			Packages: []DebianPackageInput{{Path: deb}}})
		if err != nil {
			t.Fatal(err)
		}
		if scenario == "read-only directories" {
			directory := filepath.Join(workspace, transportPath, "dists/stable")
			if err := os.Chmod(directory, 0o555); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chmod(directory, 0o755) }()
		}
		archive := filepath.Join(workspace, "repository.tar")
		if err := transportarchive.Pack(t.Context(), archive, workspace, []string{transportPath}); err != nil {
			t.Fatal(err)
		}
		fixture.payload, err = os.ReadFile(archive)
		if err != nil {
			t.Fatal(err)
		}
		object = ObjectInput{Name: "package", Kind: "apt-repository", ArtifactType: "application/vnd.nvkr.apt-repository.v1", Path: archive,
			LayerMediaType: "application/vnd.nvkr.apt-repository.v1.tar", SemanticDigest: apt.Digest,
			Output: &OutputBinding{Format: format, Repository: "package", BuildDigest: build, Components: []string{"cli"}, TransportPath: transportPath,
				BuildSubjects: append([]BuildSubject(nil), request.Objects[0].Output.BuildSubjects...)}}
		// The same observed source file supplies both the loose and packaged output.
		request.Objects[1].Evidence.Outputs = []string{"package", "tools"}
	}
	fixture.semantic = object.SemanticDigest
	request.Objects = append(request.Objects, object)
	var outputs []Output
	for _, object := range request.Objects {
		if object.Output == nil {
			continue
		}
		digest := object.SemanticDigest
		if object.Output.Format != "apt-repository" {
			descriptor, err := PayloadDescriptor(t.Context(), object.Path)
			if err != nil {
				t.Fatal(err)
			}
			digest = descriptor.Digest
		}
		outputs = append(outputs, Output{Name: object.Name, Kind: object.Output.Format, Digest: digest, BuildDigest: object.Output.BuildDigest, Components: object.Output.Components})
	}
	var err error
	request.Release.ContentSubject, err = ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	fixture.root = filepath.Join(t.TempDir(), "release")
	if _, err := BuildPublicationRoot(t.Context(), fixture.root, request); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Output == nil {
			continue
		}
		manifest, err := flatManifest(t.Context(), fixture.root, object)
		if err != nil {
			t.Fatal(err)
		}
		if object.Name == "package" {
			fixture.payloadDigest = manifest.Layers[0].Digest
		} else {
			fixture.otherPayload = manifest.Layers[0].Digest
		}
	}
	return fixture
}

func TestPullAPTArchiveRejectsUnsafeMembers(t *testing.T) {
	const root = ".nvkr/factory/packages/package"
	for _, test := range []struct {
		name, member, link string
		kind               byte
		mode               int64
	}{
		{"outside selected root", ".nvkr/factory/packages/other/file", "", tar.TypeReg, 0o644},
		{"traversal", root + "/../escape", "", tar.TypeReg, 0o644},
		{"symlink", root + "/link", "file", tar.TypeSymlink, 0o644},
		{"hard link", root + "/link", root + "/file", tar.TypeLink, 0o644},
		{"special file", root + "/fifo", "", tar.TypeFifo, 0o644},
		{"inaccessible directory", root + "/dists", "", tar.TypeDir, 0},
		{"unsearchable directory", root + "/dists", "", tar.TypeDir, 0o600},
		{"unreadable directory", root + "/dists", "", tar.TypeDir, 0o300},
		{"unreadable file", root + "/Packages", "", tar.TypeReg, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			if err := writer.WriteHeader(&tar.Header{Name: test.member, Typeflag: test.kind, Linkname: test.link, Mode: test.mode}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			file, err := os.CreateTemp(t.TempDir(), "carrier")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.Write(archive.Bytes()); err != nil {
				t.Fatal(err)
			}
			_, err = inspectPullAPTArchive(t.Context(), file, Descriptor{Digest: digestBytes(archive.Bytes()), Size: int64(archive.Len())}, root)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestPullAPTDirectoryRejectsCopyTimeChanges(t *testing.T) {
	for _, scenario := range []string{"changed bytes", "changed mode", "symlink", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			sourcePath := t.TempDir()
			payload := []byte("original")
			filePath := filepath.Join(sourcePath, "Packages")
			if err := os.WriteFile(filePath, payload, 0o644); err != nil {
				t.Fatal(err)
			}
			members := map[string]pullAPTMember{"": {directory: true, mode: 0o755}, "Packages": {mode: 0o644, size: int64(len(payload)), digest: digestBytes(payload)}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "changed bytes":
				if err := os.WriteFile(filePath, []byte("modified"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "changed mode":
				if err := os.Chmod(filePath, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(filePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("outside", filePath); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			source, _, err := securepath.OpenDirectory(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			parent := t.TempDir()
			err = publishPullDirectory(ctx, filepath.Join(parent, "out"), func(stage *os.File) error {
				return copyPullAPTDirectory(ctx, source, stage, "", members, map[string]bool{})
			})
			code := e.CodeVerifyFailed
			if scenario == "canceled" {
				code = e.CodeTimeout
			}
			assertRegistryCode(t, err, code)
			assertEmptyPullDirectory(t, parent)
		})
	}
}

func TestPullAPTRepositoryJoinsExactManifestAndPackageMembers(t *testing.T) {
	payload := []byte("exact package bytes")
	repository := APTRepository{SchemaVersion: APTRepositorySchema, Distribution: "stable", Component: "main",
		Packages: []DebianPackage{{Name: "cli", Version: "1.0.0", Architecture: "amd64", Filename: "pool/cli.deb", SHA256: digestBytes(payload), Size: int64(len(payload))}}}
	raw, err := canonical.Marshal(repository)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := repository.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"exact", "missing manifest", "malformed manifest", "missing package", "directory package", "different bytes", "different size"} {
		t.Run(scenario, func(t *testing.T) {
			members := map[string]pullAPTMember{
				"nvkr-apt-repository.json": {manifest: bytes.Clone(raw)},
				"pool/cli.deb":             {digest: digestBytes(payload), size: int64(len(payload))},
			}
			member := members["pool/cli.deb"]
			switch scenario {
			case "missing manifest":
				delete(members, "nvkr-apt-repository.json")
			case "malformed manifest":
				members["nvkr-apt-repository.json"] = pullAPTMember{manifest: []byte("{}")}
			case "missing package":
				delete(members, "pool/cli.deb")
			case "directory package":
				member.directory = true
				members["pool/cli.deb"] = member
			case "different bytes":
				member.digest = digestBytes([]byte("other package bytes"))
				members["pool/cli.deb"] = member
			case "different size":
				member.size++
				members["pool/cli.deb"] = member
			}
			result, err := inspectPullAPTRepository(members)
			if scenario == "exact" {
				if err != nil || result.Digest != expected {
					t.Fatalf("exact carrier inventory rejected: %+v %v", result, err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestPullAPTArchiveRejectsSparseAndOversizedManifest(t *testing.T) {
	const root = ".nvkr/factory/packages/package"
	for _, test := range []struct {
		name, message string
		size          int64
		pax           map[string]string
	}{
		{"sparse", "unsupported sparse member", 0, map[string]string{"GNU.xparse.major": "0", "GNU.xparse.minor": "1", "GNU.xparse.numblocks": "1", "GNU.xparse.map": "67108864,0", "GNU.xparse.realsize": "67108864"}},
		{"oversized manifest", "manifest exceeds its byte bound", (8 << 20) + 1, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			if err := writer.WriteHeader(&tar.Header{Name: root + "/nvkr-apt-repository.json", Typeflag: tar.TypeReg, Mode: 0o644,
				Size: test.size, PAXRecords: test.pax, Format: tar.FormatPAX}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(make([]byte, test.size)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			// Go's writer omits real sparse keys. Substituting equal-length PAX
			// data preserves headers and creates the real reader expansion case.
			raw := bytes.ReplaceAll(archive.Bytes(), []byte("GNU.xparse."), []byte("GNU.sparse."))
			file, err := os.CreateTemp(t.TempDir(), "carrier")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.Write(raw); err != nil {
				t.Fatal(err)
			}
			_, err = inspectPullAPTArchive(t.Context(), file, Descriptor{Digest: digestBytes(raw), Size: int64(len(raw))}, root)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if !strings.Contains(err.Error(), test.message) {
				t.Fatalf("archive rejected for unexpected reason: %v", err)
			}
		})
	}
}
