// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/diskdiagnostic"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// inspectionArchive is private scanner input, not an execution descriptor or
// receipt. Original inputs remain sealed heldfile capabilities; generated
// exports use private disk scratch and exact pre/post-observation hash checks.
type inspectionArchive interface {
	io.ReaderAt
	Size() int64
	Digest() string
	WriteToContext(context.Context, io.Writer) (int64, error)
	AssertUnchangedContext(context.Context) error
}

type diskScanDepthKey struct{}

type generatedInspectionFile struct {
	file      *os.File
	workspace string
	digest    string
	size      int64
}

// createGeneratedInspectionFile creates its own directory and regular file. No
// caller pathname can become a scanner subject through this owner. Its bound is
// enforced while writing; disk capacity itself is observed, not quota-reserved.
func createGeneratedInspectionFile(ctx context.Context, maxBytes int64, produce func(io.Writer) error) (*generatedInspectionFile, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	if maxBytes <= 0 || maxBytes > 16<<30 || produce == nil {
		return nil, e.New(e.CodeEngineCapability, "generated inspection transport requires a bounded producer")
	}
	workspace, err := os.MkdirTemp("/tmp", "nvkr-export-*")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating private generated transport")
	}
	file, err := os.OpenFile(filepath.Join(workspace, "subject"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o400)
	if err != nil {
		_ = os.RemoveAll(workspace)
		return nil, e.Wrap(e.CodeInternal, err, "creating generated transport file")
	}
	result := &generatedInspectionFile{file: file, workspace: workspace}
	if err := writeBoundedArchive(ctx, file, maxBytes, produce); err != nil {
		_ = result.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > maxBytes {
		_ = result.Close()
		return nil, e.New(e.CodeVerifyFailed, "generated transport is empty or exceeds its bound")
	}
	result.size = info.Size()
	result.digest, err = filesystemArchiveDigest(ctx, file)
	if err != nil {
		_ = result.Close()
		return nil, err
	}
	return result, nil
}

func (file *generatedInspectionFile) Size() int64    { return file.size }
func (file *generatedInspectionFile) Digest() string { return file.digest }
func (file *generatedInspectionFile) ReadAt(raw []byte, offset int64) (int, error) {
	return file.file.ReadAt(raw, offset)
}
func (file *generatedInspectionFile) Close() error {
	if file == nil || file.file == nil {
		return nil
	}
	err := file.file.Close()
	file.file = nil
	return errors.Join(err, os.RemoveAll(file.workspace))
}
func (file *generatedInspectionFile) WriteToContext(ctx context.Context, output io.Writer) (int64, error) {
	if err := file.AssertUnchangedContext(ctx); err != nil {
		return 0, err
	}
	return io.Copy(output, filesystemContextReader{ctx: ctx, ReadCloser: io.NopCloser(io.NewSectionReader(file.file, 0, file.size))})
}
func (file *generatedInspectionFile) AssertUnchangedContext(ctx context.Context) error {
	if err := validateSBOMContext(ctx); err != nil {
		return err
	}
	if file == nil || file.file == nil {
		return e.New(e.CodeEngineCapability, "generated inspection transport is closed")
	}
	checked, info, err := securepath.OpenRegular(filepath.Join(file.workspace, "subject"))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reopening generated inspection transport")
	}
	defer func() { _ = checked.Close() }()
	original, err := file.file.Stat()
	if err != nil || !os.SameFile(original, info) || info.Size() != file.size || info.Mode().Perm() != 0o400 {
		return e.New(e.CodeVerifyFailed, "generated inspection transport inode, size or permissions changed")
	}
	digest, err := filesystemArchiveDigest(ctx, checked)
	if err != nil {
		return err
	}
	if digest != file.digest {
		return e.New(e.CodeVerifyFailed, "generated inspection transport bytes changed")
	}
	return nil
}

// inspectGeneratedDependencies follows the existing Syft private-file pattern.
// The pinned scanner gets only this owner's path; it cannot mint a source/disk
// receipt. Inventory is accepted only after exact file revalidation.
func inspectGeneratedDependencies(ctx context.Context, subject *generatedInspectionFile, scanner Scanner) (DependencyInventory, error) {
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return DependencyInventory{}, err
	}
	executable, err := heldfile.OpenContext(ctx, scanner.Path, heldfile.Options{Label: "disk-content-scanner", Executable: true, MaxBytes: 512 << 20, ExpectedDigest: scanner.Digest})
	if err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeUnverifiedRef, err, "authenticating generated transport scanner")
	}
	defer func() { _ = executable.Close() }()
	configuration := filepath.Join(subject.workspace, "syft.yaml")
	if err := os.WriteFile(configuration, []byte("{}\n"), 0o600); err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeInternal, err, "creating isolated generated transport scanner configuration")
	}
	var output, diagnostic boundedScanBuffer
	depth, _ := ctx.Value(diskScanDepthKey{}).(int)
	phase := "root-scan"
	if depth > 0 {
		phase = "nested-scan"
	}
	diskdiagnostic.Emit(ctx, phase, "start", depth, 0)
	err = heldfile.RunContext(ctx, executable, []string{"scan", "docker-archive:" + filepath.Join(subject.workspace, "subject"), "--output", "syft-json", "--config", configuration, "--select-catalogers", "+deb-archive-cataloger"}, heldfile.RunOptions{
		Stdout: &output, Stderr: &diagnostic, Dir: subject.workspace, TerminationGrace: time.Second,
		Env: []string{"PATH=/usr/bin:/bin", "HOME=" + subject.workspace, "TMPDIR=" + subject.workspace, "LC_ALL=C", "TZ=UTC", "SYFT_CHECK_FOR_APP_UPDATE=false", "SYFT_GOLANG_SEARCH_REMOTE_LICENSES=false", "SYFT_JAVA_USE_NETWORK=false"},
	})
	if ctx.Err() != nil {
		return DependencyInventory{}, e.Wrap(e.CodeTimeout, ctx.Err(), "generated transport scan canceled")
	}
	rawDiagnostic := diagnostic.Bytes()
	truncated := len(rawDiagnostic) > maxDiskDiagnosticBytes
	if truncated {
		rawDiagnostic = rawDiagnostic[:maxDiskDiagnosticBytes]
	}
	message := strconv.Quote(strings.TrimSpace(string(rawDiagnostic)))
	if err != nil {
		return DependencyInventory{}, e.Wrap(e.CodeProvenanceMissing, err, "generated transport scanner failed: %s (truncated=%t)", message, truncated)
	}
	if diagnostic.Len() != 0 {
		return DependencyInventory{}, e.New(e.CodeProvenanceMissing, "generated transport scanner returned diagnostics: %s (truncated=%t)", message, truncated)
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return DependencyInventory{}, err
	}
	inventory, err := filesystemDependenciesFromJSON(output.Bytes(), scanner.Digest)
	if err == nil {
		diskdiagnostic.Emit(ctx, phase, "complete", depth, len(inventory.Components))
	}
	return inventory, err
}
