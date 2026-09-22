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
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
		{"valid", "test -z \"${NKD_CREDENTIAL:-}\" || exit 19\nprintf '%s' '" + inventory + "'", false, ""},
		{"wrong scanner pin", "exit 0", true, e.CodeVerifyFailed},
		{"scanner failure", "exit 23", false, e.CodeProvenanceMissing},
		{"malformed report", "printf broken", false, e.CodeProvenanceMissing},
		{"empty inventory", `printf '%s' '{"bomFormat":"CycloneDX","specVersion":"1.5","components":[]}'`, false, e.CodeProvenanceMissing},
		{"subject mutation", "file=${2#file:}; chmod u+w \"$file\"; printf changed > \"$file\"; printf '%s' '" + inventory + "'", false, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NKD_CREDENTIAL", "must-not-reach-scanner")
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

func assertDependencyCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
