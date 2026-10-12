// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func dependencySubject(t *testing.T) *BOM {
	t.Helper()
	resolved, digest := resolveCP(t)
	return fromComponentForArch(resolved.Components[0], digest, "sha256:"+strings.Repeat("a", 64), "")
}

func TestDependencyInventoryCanonicalVersionsAndEdges(t *testing.T) {
	base := dependencySubject(t)
	old := Component{Type: "library", Name: "example", Version: "1.0.0", PURL: "pkg:generic/example@1.0.0"}
	newer := old
	newer.Version = "2.0.0"
	newer.PURL = "pkg:generic/example@2.0.0"
	first, err := WithDependencies(base, []Component{newer, old, old}, "syft-cyclonedx/v1", "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	second, err := WithDependencies(base, []Component{old, newer}, "syft-cyclonedx/v1", "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	a, err := first.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || len(first.Components) != 2 || len(first.Dependencies[0].DependsOn) != 2 {
		t.Fatalf("unstable dependency identity: %s / %s", a, b)
	}
	if len(base.Components) != 0 || base.Metadata.Component.BOMRef != "" {
		t.Fatal("mutated source BOM")
	}
	clone := cloneBOM(first)
	clone.Dependencies[0].DependsOn[0] = "changed"
	if first.Dependencies[0].DependsOn[0] == "changed" {
		t.Fatal("cloned graph aliases original")
	}
	first.Dependencies[0].DependsOn[0] = "missing"
	assertDependencyCode(t, first.Validate(), e.CodeVerifyFailed)
}

func TestDependencyInventoryRejectsFalseClaims(t *testing.T) {
	for _, tc := range []struct {
		name       string
		components []Component
		pin        string
		code       e.Code
	}{
		{"empty", nil, "sha256:" + strings.Repeat("b", 64), e.CodeProvenanceMissing},
		{"missing scanner pin", []Component{{Type: "library", Name: "lib"}}, "", e.CodeUnverifiedRef},
		{"conflicting version", []Component{{Type: "library", Name: "lib", Version: "1", PURL: "pkg:generic/lib@1"}, {Type: "library", Name: "lib", Version: "2", PURL: "pkg:generic/lib@1"}}, "sha256:" + strings.Repeat("b", 64), e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := WithDependencies(dependencySubject(t), tc.components, "syft-cyclonedx/v1", tc.pin)
			assertDependencyCode(t, err, tc.code)
		})
	}
}

func TestDependencyMethodsKeepDiskInventorySemanticsScoped(t *testing.T) {
	for _, subjectType := range []string{"application", "container", "operating-system"} {
		for _, method := range []string{"helm-manifests/v1", "syft-cyclonedx/v1", "disk-content/v1", "disk-reconciled/v1"} {
			for _, componentType := range []string{"library", "file"} {
				t.Run(subjectType+"/"+method+"/"+componentType, func(t *testing.T) {
					base := dependencySubject(t)
					base.Metadata.Component.Type = subjectType
					component := Component{Type: componentType, Name: "observed", Version: "1"}
					if componentType == "file" {
						component.Hashes = []Hash{{Alg: "SHA-256", Content: strings.Repeat("c", 64)}}
					}
					want := e.Code("")
					if method == "disk-reconciled/v1" && subjectType != "operating-system" || componentType == "file" && method != "disk-reconciled/v1" && method != "disk-content/v1" {
						want = e.CodeVerifyFailed
					}
					bom, err := WithDependencies(base, []Component{component}, method, "sha256:"+strings.Repeat("b", 64))
					if want != "" {
						assertDependencyCode(t, err, want)
						if bom != nil {
							t.Fatal("rejected inventory returned a document")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if err := bom.Validate(); err != nil {
						t.Fatal(err)
					}
					// Serialized/caller-modified descriptions must encounter the
					// same gate, not just the document-construction helper.
					if method == "disk-reconciled/v1" {
						bom.Metadata.Component.Type = "application"
						assertDependencyCode(t, bom.Validate(), e.CodeVerifyFailed)
					} else if componentType == "file" {
						for index := range bom.Metadata.Props {
							if bom.Metadata.Props[index].Name == "nvkr:dependencyInspection" {
								bom.Metadata.Props[index].Value = "syft-cyclonedx/v1"
							}
						}
						assertDependencyCode(t, bom.Validate(), e.CodeVerifyFailed)
					}
				})
			}
		}
	}
}

func TestDiskMethodsRequirePerArtifactSubjectOnSerializedAggregate(t *testing.T) {
	for _, method := range []string{"disk-content/v1", "disk-reconciled/v1"} {
		t.Run(method, func(t *testing.T) {
			resolved, digest := resolveCP(t)
			bom := fromBuild(resolved, digest, nil, false)
			if err := bom.Validate(); err != nil {
				t.Fatalf("aggregate fixture is invalid before assigning a method: %v", err)
			}
			bom.Metadata.Props = append(bom.Metadata.Props, Property{Name: "nvkr:dependencyInspection", Value: method},
				Property{Name: "nvkr:dependencyScannerDigest", Value: "sha256:" + strings.Repeat("b", 64)})
			slices.SortFunc(bom.Metadata.Props, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
			err := bom.Validate()
			assertDependencyCode(t, err, e.CodeVerifyFailed)
			if !strings.Contains(err.Error(), "disk inventory requires an exact per-artifact subject") {
				t.Fatalf("wrong semantic guard: %v", err)
			}
		})
	}
}

func helmTestArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for name, raw := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(raw))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestHelmDependencyMetadataUsesDeclaredVersions(t *testing.T) {
	raw := helmTestArchive(t, map[string]string{
		"helm/server/Chart.yaml": "apiVersion: v2\nname: server\nversion: 1.2.3\ndependencies:\n  - name: database\n    version: 4.5.6\n    repository: https://charts.example.test\n",
		"helm/server/Chart.lock": "dependencies:\n  - name: database\n    version: 4.5.6\n    repository: https://charts.example.test\n",
	})
	components, err := inspectHelmArchive(t.Context(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	bom, err := WithDependencies(dependencySubject(t), components, "helm-manifests/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(bom.Components) != 2 {
		t.Fatalf("got %d components", len(bom.Components))
	}
	versions := map[string]string{}
	for _, component := range bom.Components {
		versions[component.Name] = component.Version
	}
	if versions["server"] != "1.2.3" || versions["database"] != "4.5.6" {
		t.Fatalf("changed declared versions: %v", versions)
	}
}

func TestHelmDependencyInspectionRejectsUnsafeOrMissingMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		code  e.Code
	}{
		{"traversal", map[string]string{"../Chart.yaml": "name: bad\nversion: 1"}, e.CodeVerifyFailed},
		{"missing version", map[string]string{"helm/Chart.yaml": "name: chart"}, e.CodeProvenanceMissing},
		{"duplicate key", map[string]string{"helm/Chart.yaml": "name: one\nname: two\nversion: 1"}, e.CodeVerifyFailed},
		{"no chart", map[string]string{"helm/values.yaml": "image: example"}, e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inspectHelmArchive(t.Context(), bytes.NewReader(helmTestArchive(t, tc.files)))
			assertDependencyCode(t, err, tc.code)
		})
	}
}

func TestDependencyInspectionBindsArtifactBytes(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "chart.tar")
	raw := helmTestArchive(t, map[string]string{"helm/Chart.yaml": "name: chart\nversion: 1.2.3"})
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := InspectDependencies(t.Context(), file, "sha256:"+strings.Repeat("0", 64), "tarball", Scanner{})
	assertDependencyCode(t, err, e.CodeVerifyFailed)
	got, err := InspectDependencies(t.Context(), file, fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), "tarball", Scanner{})
	if err != nil || len(got.Components) != 1 {
		t.Fatalf("exact subject rejected: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = InspectDependencies(ctx, file, fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), "tarball", Scanner{})
	assertDependencyCode(t, err, e.CodeTimeout)
}

func TestPinnedScannerProtocol(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held scanner execution is Linux-only")
	}
	const inventory = `{"bomFormat":"CycloneDX","specVersion":"1.5","components":[{"type":"library","name":"dependency","version":"1.0.0","purl":"pkg:generic/dependency@1.0.0"}]}`
	for _, tc := range []struct {
		name, body string
		wrongPin   bool
		code       e.Code
	}{
		{"valid", "test -z \"${NVKR_CREDENTIAL:-}\" || exit 19\nprintf '%s' '" + inventory + "'", false, ""},
		{"wrong scanner pin", "exit 0", true, e.CodeVerifyFailed},
		{"scanner failure", "exit 23", false, e.CodeProvenanceMissing},
		{"malformed report", "printf broken", false, e.CodeProvenanceMissing},
		{"empty inventory", `printf '%s' '{"bomFormat":"CycloneDX","specVersion":"1.5","components":[]}'`, false, e.CodeProvenanceMissing},
		{"subject mutation", "file=${2#file:}; chmod u+w \"$file\"; printf changed > \"$file\"; printf '%s' '" + inventory + "'", false, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NVKR_CREDENTIAL", "must-not-reach-scanner")
			dir := t.TempDir()
			file := filepath.Join(dir, "subject")
			scannerPath := filepath.Join(dir, "scanner")
			if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			script := []byte("#!/bin/sh\n" + tc.body + "\n")
			if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
				t.Fatal(err)
			}
			pin := fmt.Sprintf("sha256:%x", sha256.Sum256(script))
			if tc.wrongPin {
				pin = "sha256:" + strings.Repeat("0", 64)
			}
			got, err := InspectDependencies(t.Context(), file, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("fixture"))), "host-package", Scanner{Path: scannerPath, Digest: pin})
			if tc.code != "" {
				assertDependencyCode(t, err, tc.code)
				if len(got.Components) != 0 {
					t.Fatal("failed scan returned components")
				}
				return
			}
			if err != nil || len(got.Components) != 1 {
				t.Fatalf("scanner result: %+v %v", got, err)
			}
		})
	}
}

func TestPinnedScannerDiscoveryHint(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held scanner execution is Linux-only")
	}
	const inventory = `{"bomFormat":"CycloneDX","specVersion":"1.5","components":[{"type":"library","name":"dependency","version":"1.0.0","purl":"pkg:generic/dependency@1.0.0"}]}`
	for _, tc := range []struct {
		name, filename, snapshot string
		held, mutate             bool
	}{
		{"generic binary", "tool", "subject", false, false},
		{"Debian archive", "source-package.deb", "subject.deb", false, false},
		{"uppercase is not a hint", "package.DEB", "subject", false, false},
		{"nonterminal extension", "package.deb.tmp", "subject", false, false},
		{"other extension", "package.rpm", "subject", false, false},
		{"held input has no filename hint", "package.deb", "subject", true, false},
		{"Debian snapshot mutation", "package.deb", "subject.deb", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, tc.filename)
			raw := []byte("fixture")
			if err := os.WriteFile(file, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			body := "file=${2#file:}\n[ \"${file##*/}\" = " + tc.snapshot + " ] || exit 29\n"
			if tc.mutate {
				body += "chmod u+w \"$file\"\nprintf changed > \"$file\"\n"
			}
			script := []byte("#!/bin/sh\n" + body + "printf '%s' '" + inventory + "'\n")
			scannerPath := filepath.Join(dir, "scanner")
			if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
				t.Fatal(err)
			}
			scanner := Scanner{Path: scannerPath, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(script))}
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
			var err error
			if tc.held {
				subject, openErr := heldfile.OpenContext(t.Context(), file, heldfile.Options{Label: "hint-test-subject", MaxBytes: 1024, ExpectedDigest: digest})
				if openErr != nil {
					t.Fatal(openErr)
				}
				defer subject.Close()
				var receipt *DependencyScanReceipt
				receipt, err = InspectHeldDependencies(t.Context(), subject, "host-package", scanner)
				if err == nil {
					observed, size, kind := receipt.Binding()
					if observed != digest || size != int64(len(raw)) || kind != "host-package" {
						t.Fatalf("held scan binding = %s/%d/%s", observed, size, kind)
					}
				}
			} else {
				_, err = InspectDependencies(t.Context(), file, digest, "host-package", scanner)
			}
			if tc.mutate {
				assertDependencyCode(t, err, e.CodeVerifyFailed)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertDependencyCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestHelmDependencyDuplicateRecordContract(t *testing.T) {
	for _, tc := range []struct {
		name, path, first, second string
		mode                      int64
		good                      bool
	}{
		{"identical subchart metadata", "demo/charts/sub/Chart.yaml", "name: sub\nversion: 1.2.3\n", "name: sub\nversion: 1.2.3\n", 0o644, true},
		{"conflicting body", "demo/charts/sub/templates/item.yaml", "one", "two", 0o644, false},
		{"conflicting mode", "demo/charts/sub/templates/item.yaml", "one", "one", 0o600, false},
		{"root metadata", "demo/Chart.yaml", "name: demo\nversion: 1.2.3\n", "name: demo\nversion: 1.2.3\n", 0o644, false},
		{"root template", "demo/templates/item.yaml", "one", "one", 0o644, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			writer := tar.NewWriter(&output)
			write := func(name, body string, mode int64) {
				t.Helper()
				if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(body))}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write([]byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.path != "demo/Chart.yaml" {
				write("demo/Chart.yaml", "name: demo\nversion: 1.2.3\n", 0o644)
			}
			write(tc.path, tc.first, 0o644)
			write(tc.path, tc.second, tc.mode)
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			inventory, err := inspectHelmArchive(t.Context(), bytes.NewReader(output.Bytes()))
			if tc.good {
				if err != nil || len(inventory) != 2 {
					t.Fatalf("duplicate inventory was not coalesced: %+v, %v", inventory, err)
				}
			} else {
				assertDependencyCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}
