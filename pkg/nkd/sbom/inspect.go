package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/internal/heldfile"
)

const maxInspectionBytes int64 = 2 << 30
const maxScannerOutput = 32 << 20

type Scanner struct{ Path, Digest string }

type DependencyInventory struct {
	Components    []Component
	Method        string
	ScannerDigest string
}

// InspectDependencies reads exactly the observed bytes. Syft is an external,
// digest-pinned executable; it receives a held artifact descriptor and a minimal
// environment. The workflow runs this node with networking disabled.
func InspectDependencies(ctx context.Context, artifactPath, artifactDigest, kind string, scanner Scanner) (DependencyInventory, error) {
	if ctx == nil {
		return DependencyInventory{}, e.New(e.CodeUsage, "dependency inspection requires context")
	}
	if err := ctx.Err(); err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeTimeout, err, "dependency inspection canceled")
	}
	if !canonical.IsDigest(artifactDigest) {
		return DependencyInventory{}, e.New(e.CodeUnverifiedRef, "dependency inspection requires an exact artifact digest")
	}
	if kind != "host-package" && kind != "oci-image" && kind != "tarball" {
		return DependencyInventory{}, e.New(e.CodeEngineCapability, "dependency inspection does not support artifact kind %q", kind)
	}
	if kind != "tarball" {
		if !canonical.IsDigest(scanner.Digest) || scanner.Path == "" {
			return DependencyInventory{}, e.New(e.CodeProvenanceMissing, "dependency inspection requires a pinned Syft executable")
		}
		if err := heldfile.RequireExecutableSupport(); err != nil {
			return DependencyInventory{}, e.Wrap(e.CodeEngineCapability, err, "dependency scanner requires held executable support")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	subject, err := heldfile.OpenContext(ctx, artifactPath, heldfile.Options{Label: "sbom-subject", MaxBytes: maxInspectionBytes})
	if err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeVerifyFailed, err, "opening SBOM subject")
	}
	defer func() { _ = subject.Close() }()
	if subject.Digest() != artifactDigest {
		return DependencyInventory{}, e.New(e.CodeVerifyFailed, "SBOM subject bytes differ from producer digest")
	}
	if kind == "tarball" {
		components, err := inspectHelmArchive(ctx, io.NewSectionReader(subject, 0, subject.Size()))
		return DependencyInventory{Components: components, Method: "helm-manifests/v1"}, err
	}
	executable, err := heldfile.OpenContext(ctx, scanner.Path, heldfile.Options{Label: "sbom-scanner", MaxBytes: 512 << 20, Executable: true})
	if err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeVerifyFailed, err, "opening pinned dependency scanner")
	}
	defer func() { _ = executable.Close() }()
	if executable.Digest() != scanner.Digest {
		return DependencyInventory{}, e.New(e.CodeVerifyFailed, "dependency scanner differs from executable pin")
	}
	childPath, err := heldfile.ChildPath(1)
	if err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeEngineCapability, err, "addressing held SBOM subject")
	}
	source := "file:" + childPath
	if kind == "oci-image" {
		source = "oci-archive:" + childPath
	}
	var output, diagnostic boundedScanBuffer
	workdir, err := os.MkdirTemp("", "nkd-sbom-scan-*")
	if err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeInternal, err, "creating scanner workspace")
	}
	defer func() { _ = os.RemoveAll(workdir) }()
	// Syft's file resolver canonicalizes symlinks and cannot resolve a deleted
	// memfd name. Give only file scans a private read-only copy of the held bytes,
	// then recheck it before accepting the inventory. OCI scans use the held FD.
	var snapshot string
	if kind == "host-package" {
		snapshot = filepath.Join(workdir, "subject")
		file, err := os.OpenFile(snapshot, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
		if err != nil {
			return DependencyInventory{}, e.Wrap(e.CodeInternal, err, "staging scanner subject")
		}
		_, copyErr := subject.WriteToContext(ctx, file)
		closeErr := file.Close()
		if copyErr != nil {
			return DependencyInventory{}, e.Wrap(e.CodeVerifyFailed, copyErr, "copying held scanner subject")
		}
		if closeErr != nil {
			return DependencyInventory{}, e.Wrap(e.CodeInternal, closeErr, "closing scanner subject")
		}
		source = "file:" + snapshot
	}
	config := filepath.Join(workdir, "syft.yaml")
	if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeInternal, err, "writing isolated scanner configuration")
	}
	args := []string{"scan", source, "--output", "cyclonedx-json@1.5", "--config", config}
	err = heldfile.RunContext(ctx, executable, args, heldfile.RunOptions{
		Stdout: &output, Stderr: &diagnostic, TerminationGrace: time.Second,
		Dir: workdir,
		Env: []string{"PATH=/usr/bin:/bin", "HOME=" + workdir, "TMPDIR=" + workdir, "LC_ALL=C", "TZ=UTC", "SYFT_CHECK_FOR_APP_UPDATE=false", "SYFT_GOLANG_SEARCH_REMOTE_LICENSES=false", "SYFT_JAVA_USE_NETWORK=false"},
	}, subject)
	if err != nil {
		if ctx.Err() != nil {
			return DependencyInventory{}, e.Wrap(e.CodeTimeout, ctx.Err(), "dependency scan canceled")
		}
		return DependencyInventory{}, e.Wrap(e.CodeProvenanceMissing, err, "dependency scanner failed: %s", strings.TrimSpace(diagnostic.String()))
	}
	if snapshot != "" {
		checked, err := heldfile.OpenContext(ctx, snapshot, heldfile.Options{Label: "scanner-subject-check", MaxBytes: maxInspectionBytes, ExpectedDigest: artifactDigest})
		if err != nil {
			return DependencyInventory{}, e.Wrap(e.CodeVerifyFailed, err, "scanner subject changed during inspection")
		}
		_ = checked.Close()
	}
	var scanned struct {
		BOMFormat   string `json:"bomFormat"`
		SpecVersion string `json:"specVersion"`
		Components  []struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Version string `json:"version"`
			PURL    string `json:"purl"`
		} `json:"components"`
	}
	if err := json.Unmarshal(output.Bytes(), &scanned); err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeProvenanceMissing, err, "decoding scanner inventory")
	}
	if scanned.BOMFormat != "CycloneDX" || scanned.SpecVersion != "1.5" {
		return DependencyInventory{}, e.New(e.CodeProvenanceMissing, "scanner returned unsupported inventory schema")
	}
	result := DependencyInventory{Method: "syft-cyclonedx/v1", ScannerDigest: scanner.Digest}
	for _, component := range scanned.Components {
		// CycloneDX file entries are scanner evidence locations, not package
		// dependencies. Their temporary paths must not enter artifact identity.
		if component.Type == "file" {
			continue
		}
		result.Components = append(result.Components, Component{Type: component.Type, Name: component.Name, Version: component.Version, PURL: component.PURL})
	}
	if len(result.Components) == 0 {
		return DependencyInventory{}, e.New(e.CodeProvenanceMissing, "scanner found no dependency components")
	}
	return result, nil
}

type boundedScanBuffer struct{ data bytes.Buffer }

func (buffer *boundedScanBuffer) Write(raw []byte) (int, error) {
	if buffer.Len()+len(raw) > maxScannerOutput {
		return 0, fmt.Errorf("scanner output exceeds %d bytes", maxScannerOutput)
	}
	return buffer.data.Write(raw)
}

func (buffer *boundedScanBuffer) Len() int       { return buffer.data.Len() }
func (buffer *boundedScanBuffer) Bytes() []byte  { return buffer.data.Bytes() }
func (buffer *boundedScanBuffer) String() string { return buffer.data.String() }
