package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

const maxInspectionBytes int64 = 2 << 30
const maxScannerOutput = 32 << 20

type Scanner struct{ Path, Digest string }

type DependencyInventory struct {
	Components    []Component
	Method        string
	ScannerDigest string
}

// DependencyScanReceipt is issued only after the owning scanner has read the
// exact held input. It authenticates that scan, not complete disk coverage or a
// relationship to another artifact. Factory must bind it to the original Run.
type DependencyScanReceipt struct {
	subject   string
	size      int64
	kind      string
	inventory DependencyInventory
}

// Binding identifies the bytes actually supplied to the scanner.
func (receipt *DependencyScanReceipt) Binding() (subject string, size int64, kind string) {
	if receipt == nil {
		return "", 0, ""
	}
	return receipt.subject, receipt.size, receipt.kind
}

// Inventory returns diagnostic data; modifying it cannot alter the receipt.
func (receipt *DependencyScanReceipt) Inventory() DependencyInventory {
	if receipt == nil {
		return DependencyInventory{}
	}
	result := receipt.inventory
	result.Components = slices.Clone(result.Components)
	for index := range result.Components {
		result.Components[index] = cloneComponent(result.Components[index])
	}
	return result
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
	return inspectHeldDependencies(ctx, subject, kind, scanner)
}

// InspectHeldDependencies consumes the existing held capability without opening
// a caller-selected path. It does not transfer ownership. Unsupported disk/ISO
// and generic archive formats fail before executing a scanner.
func InspectHeldDependencies(ctx context.Context, subject *heldfile.File, kind string, scanner Scanner) (*DependencyScanReceipt, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	if subject == nil || subject.Size() <= 0 || subject.Size() > maxInspectionBytes {
		return nil, e.New(e.CodeVerifyFailed, "dependency scan requires a bounded held subject")
	}
	if kind != "host-package" && kind != "oci-image" {
		return nil, e.New(e.CodeEngineCapability, "held composition scan does not support artifact kind %q", kind)
	}
	if !canonical.IsDigest(scanner.Digest) || scanner.Path == "" {
		return nil, e.New(e.CodeProvenanceMissing, "dependency inspection requires a pinned Syft executable")
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "dependency scanner requires held executable support")
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "checking held dependency scan input")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	inventory, err := inspectHeldDependencies(ctx, subject, kind, scanner)
	if err != nil {
		return nil, err
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rechecking held dependency scan input")
	}
	return &DependencyScanReceipt{subject: subject.Digest(), size: subject.Size(), kind: kind, inventory: inventory}, nil
}

func inspectHeldDependencies(ctx context.Context, subject *heldfile.File, kind string, scanner Scanner) (DependencyInventory, error) {
	artifactDigest := subject.Digest()
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
