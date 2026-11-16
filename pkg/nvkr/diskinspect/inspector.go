// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskinspect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const (
	// MaxDiagnosticDiskBytes bounds the held input and private snapshot.
	MaxDiagnosticDiskBytes = 8 << 30
	// MaxDiagnosticBytes bounds tool diagnostics in user-visible errors.
	MaxDiagnosticBytes = 4096
)

// DiskInspector holds one pinned virt-inspector executable. This diagnostic
// capability authenticates the executable, not its root-owned loader, libraries,
// QEMU, or libguestfs appliance. Those are the enclosing runtime's TCB. It cannot
// issue full-disk SBOM, publication, or reproduction authority.
type DiskInspector struct {
	mu         sync.Mutex
	executable *heldfile.File
	codec      *assemble.QemuImgCodec
}

// InstalledInventory is an immutable, process-local observation. It binds
// installed package metadata to exact QCOW2 and inspector bytes. It deliberately
// has no complete-material-closure or full-disk evidence verdict.
type InstalledInventory struct {
	subjectDigest   string
	subjectSize     int64
	inspectorDigest string
	codecDigest     string
	packages        []InstalledDiskPackage
}

func (inventory *InstalledInventory) Packages() []InstalledDiskPackage {
	if inventory == nil {
		return nil
	}
	return slices.Clone(inventory.packages)
}

// Binding returns descriptive exact-byte identities, not a serialized receipt.
func (inventory *InstalledInventory) Binding() (subject string, size int64, inspector, codec string) {
	if inventory == nil {
		return "", 0, "", ""
	}
	return inventory.subjectDigest, inventory.subjectSize, inventory.inspectorDigest, inventory.codecDigest
}

// OpenDiskInspector snapshots an exact, absolute, no-follow virt-inspector path.
// Linux executable capability admission precedes filesystem access. No tool is
// discovered or downloaded; callers must realize the reviewed runtime first.
// The caller retains codec ownership and must keep it open for inspection.
func OpenDiskInspector(ctx context.Context, executablePath, digest string, codec *assemble.QemuImgCodec) (*DiskInspector, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if !canonical.IsDigest(digest) {
		return nil, e.New(e.CodeUnpinnedInput, "disk inspector requires an exact executable pin")
	}
	if !filepath.IsAbs(executablePath) || filepath.Clean(executablePath) != executablePath {
		return nil, e.New(e.CodeUsage, "disk inspector requires an exact absolute path")
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding disk inspector executable")
	}
	if _, err := codec.DigestContext(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspector admission canceled")
		}
		return nil, e.Wrap(e.CodeEngineCapability, err, "disk inspection requires a held qemu-img codec")
	}
	executable, err := heldfile.OpenContext(ctx, executablePath, heldfile.Options{
		Label: "disk-inspector", Executable: true, MaxBytes: 512 << 20, ExpectedDigest: digest,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspector capture canceled")
		}
		return nil, e.Wrap(e.CodeUnverifiedRef, err, "capturing pinned disk inspector")
	}
	// debug/elf is the standard-library format owner; no approximate ELF grammar.
	image, err := elf.NewFile(executable)
	if err != nil || !NativeELF(image) {
		_ = executable.Close()
		return nil, e.New(e.CodeEngineCapability, "disk inspector is not a Linux ELF executable")
	}
	return &DiskInspector{executable: executable, codec: codec}, nil
}

// NativeELF checks the native executable header; it does not authenticate tool identity.
func NativeELF(image *elf.File) bool {
	if image == nil || image.Entry == 0 || (image.Type != elf.ET_EXEC && image.Type != elf.ET_DYN) {
		return false
	}
	// This is a native ELF sanity check, not proof that an ELF is virt-inspector
	// or excludes every shared library. ET_DYN admits PIE; the exact digest pin
	// identifies the selected tool, and its enclosing runtime still needs review.
	switch runtime.GOARCH {
	case "amd64":
		return image.Machine == elf.EM_X86_64
	case "arm64":
		return image.Machine == elf.EM_AARCH64
	default:
		return false
	}
}

// Close waits for an active inspection to finish. Cancel that inspection's
// context first when immediate shutdown is required; its deadline is ten minutes.
func (inspector *DiskInspector) Close() error {
	if inspector == nil {
		return nil
	}
	inspector.mu.Lock()
	defer inspector.mu.Unlock()
	if inspector.executable == nil {
		return nil
	}
	err := inspector.executable.Close()
	inspector.executable = nil
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "closing disk inspector")
	}
	return nil
}

// InspectHeldInstalledPackages observes one immutable unencrypted QCOW2. The
// private read-only copy accommodates libguestfs's appliance/QEMU subprocesses,
// which reopen disk paths rather than share NVKR's inherited descriptor. Both the
// copy and held source are rechecked before any inventory is returned. No host
// mount, guest executable, decryption key, or network acquisition is requested.
// Callers MUST provide a non-root, network-less, credential-free container or
// equivalent sandbox before calling this API. This capability does not admit or
// authenticate containment; direct libguestfs execution is not a host sandbox.
// The sandbox must supply writable /tmp for the short private workspace, up to
// 8 GiB for the held input and another 8 GiB copy plus appliance/cache storage.
// TMPDIR does not redirect that workspace; memory-backed /tmp consumes RAM.
func (inspector *DiskInspector) InspectHeldInstalledPackages(ctx context.Context,
	subject *heldfile.File, format, encryption string,
) (*InstalledInventory, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if encryption != "none" {
		return nil, e.New(e.CodeEncryptedArtifact, "disk package inspection requires unencrypted content")
	}
	if format != "qcow2" {
		return nil, e.New(e.CodeEngineCapability, "disk package inspection supports only QCOW2")
	}
	if os.Geteuid() == 0 {
		return nil, e.New(e.CodeEngineCapability, "disk package inspection requires a non-root sandbox caller")
	}
	if inspector == nil || subject == nil {
		return nil, e.New(e.CodeEngineCapability, "held disk and pinned inspector are required")
	}
	if subject.Size() <= 0 || subject.Size() > MaxDiagnosticDiskBytes {
		return nil, e.New(e.CodeEngineCapability, "disk inspection subject must be nonempty and within its diagnostic byte budget")
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "executing held disk inspector")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// Match the existing held codec's cancellation-aware lifetime serialization.
	if err := lockDiskInspector(ctx, &inspector.mu); err != nil {
		return nil, err
	}
	defer inspector.mu.Unlock()
	if inspector.executable == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspector is closed")
	}
	if err := inspector.executable.AssertUnchangedContext(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspector validation canceled")
		}
		return nil, e.Wrap(e.CodeUnverifiedRef, err, "rechecking disk inspector executable")
	}
	if err := inspector.codec.ValidateHeldEnvelope(ctx, "qcow2", subject); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk envelope inspection canceled")
		}
		return nil, err
	}
	codecDigest, err := inspector.codec.DigestContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspection canceled")
		}
		return nil, err
	}
	inventory, err := inspectHeldDiskPackages(ctx, subject, inspector.executable.Digest(),
		func(ctx context.Context, path, workspace string, output, diagnostic io.Writer) error {
			return heldfile.RunContext(ctx, inspector.executable, diskInspectorArguments(path),
				heldfile.RunOptions{Stdin: bytes.NewReader(nil), Stdout: output, Stderr: diagnostic, Dir: workspace,
					TerminationGrace: time.Second,
					Env:              diskInspectorEnvironment(workspace)})
		})
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspection canceled")
	}
	if err != nil {
		return nil, err
	}
	inventory.codecDigest = codecDigest
	return inventory, nil
}

func diskInspectorArguments(path string) []string {
	// The selected release has no "all:error" key selector. Force key reads
	// from empty stdin instead of a controlling terminal. No key is supplied.
	return []string{"--format=qcow2", "--keys-from-stdin", "--no-icon", "-a", path}
}

func diskInspectorEnvironment(workspace string) []string {
	// No KVM capability is admitted; use the supported TCG setting. Nothing
	// from the caller's ambient environment configures the appliance.
	return []string{"PATH=/usr/bin:/bin", "HOME=" + workspace, "TMPDIR=" + workspace,
		"XDG_CONFIG_HOME=" + workspace, "XDG_RUNTIME_DIR=" + workspace,
		"LIBGUESTFS_CACHEDIR=" + workspace, "LC_ALL=C", "TZ=UTC", "LIBGUESTFS_BACKEND=direct",
		"LIBGUESTFS_BACKEND_SETTINGS=force_tcg"}
}

func lockDiskInspector(ctx context.Context, mu *sync.Mutex) error {
	for !mu.TryLock() {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return e.Wrap(e.CodeTimeout, ctx.Err(), "waiting for disk inspector")
		case <-timer.C:
		}
	}
	return nil
}

type diskInspectCommand func(context.Context, string, string, io.Writer, io.Writer) error

func inspectHeldDiskPackages(ctx context.Context, subject *heldfile.File, inspectorDigest string,
	run diskInspectCommand,
) (*InstalledInventory, error) {
	if subject.Size() <= 0 || !canonical.IsDigest(subject.Digest()) || !canonical.IsDigest(inspectorDigest) {
		return nil, e.New(e.CodeUnverifiedRef, "disk inspection requires exact nonempty subject and tool bytes")
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspection subject validation canceled")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rechecking held disk subject")
	}
	// libguestfs v1.52 puts non-root sockets beneath XDG_RUNTIME_DIR and adds
	// /libguestfsXXXXXX/guestfsd.sock. A short /tmp parent keeps that pathname
	// within Linux sockaddr_un's limit, regardless of the caller's TMPDIR.
	// Root ignores XDG_RUNTIME_DIR upstream and is rejected at API admission.
	// Cache, appliance temporary files and sockets share ownership.
	workspace, err := os.MkdirTemp("/tmp", "nvkr-di-*")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating private disk inspection workspace")
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	path := filepath.Join(workspace, "subject.qcow2")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating disk inspection snapshot")
	}
	_, copyErr := subject.WriteToContext(ctx, file)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspection snapshot canceled")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "staging held disk inspection subject")
	}
	var output, diagnostic boundedScanBuffer
	if err := run(ctx, path, workspace, &output, &diagnostic); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspection canceled")
		}
		raw := diagnostic.Bytes()
		truncated := len(raw) > MaxDiagnosticBytes
		if truncated {
			raw = raw[:MaxDiagnosticBytes]
		}
		return nil, e.Wrap(e.CodeEnvelopeDecode, err, "disk inspector failed: %s (truncated=%t)",
			strconv.Quote(strings.TrimSpace(string(raw))), truncated)
	}
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspection canceled before snapshot verification")
	}
	// A successful command with diagnostics is not accepted as a complete scan.
	if diagnostic.Len() != 0 {
		return nil, e.New(e.CodeEnvelopeDecode, "disk inspector returned diagnostics")
	}
	// Rehash in bounded chunks instead of making another multi-GiB memfd copy.
	checked, info, err := securepath.OpenRegular(path)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "disk inspection snapshot changed")
	}
	defer func() { _ = checked.Close() }()
	if info.Size() != subject.Size() || info.Mode().Perm() != 0o400 {
		return nil, e.New(e.CodeVerifyFailed, "disk inspection snapshot size or permissions changed")
	}
	hash := sha256.New()
	if _, err := transportarchive.CopyExact(ctx, hash, checked, subject.Size()); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspection snapshot verification canceled")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rehashing disk inspection snapshot")
	}
	if fmt.Sprintf("sha256:%x", hash.Sum(nil)) != subject.Digest() {
		return nil, e.New(e.CodeVerifyFailed, "disk inspection snapshot bytes changed")
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "disk inspection subject revalidation canceled")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "disk subject changed during inspection")
	}
	packages, err := ParseInstalledPackagesXML(output.Bytes())
	if err != nil {
		return nil, err
	}
	return &InstalledInventory{subjectDigest: subject.Digest(), subjectSize: subject.Size(),
		inspectorDigest: inspectorDigest, packages: packages}, nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "disk inspection requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "disk inspection canceled")
	}
	return nil
}

type boundedScanBuffer struct{ data bytes.Buffer }

func (buffer *boundedScanBuffer) Write(raw []byte) (int, error) {
	const maxScannerOutput = 32 << 20
	if buffer.Len()+len(raw) > maxScannerOutput {
		return 0, fmt.Errorf("scanner output exceeds %d bytes", maxScannerOutput)
	}
	return buffer.data.Write(raw)
}
func (buffer *boundedScanBuffer) Len() int      { return buffer.data.Len() }
func (buffer *boundedScanBuffer) Bytes() []byte { return buffer.data.Bytes() }

func (buffer *boundedScanBuffer) String() string { return buffer.data.String() }
