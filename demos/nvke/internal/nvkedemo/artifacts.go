// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"golang.org/x/sys/unix"
)

// openRunFile never follows a substituted child directory or final symlink.
func openRunFile(root, relative string) (*os.File, os.FileInfo, error) {
	if !securepath.WorkspaceRelative(relative) || filepath.Clean(relative) != relative {
		return nil, nil, errors.New("unsafe relative input path")
	}
	dir, _, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, nil, err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		next, _, openErr := securepath.OpenChild(dir, part, true)
		_ = dir.Close()
		if openErr != nil {
			return nil, nil, openErr
		}
		dir = next
	}
	defer dir.Close()
	return securepath.OpenChild(dir, parts[len(parts)-1], false)
}

func readRunFile(ctx context.Context, root, relative string, limit int64) ([]byte, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	file, info, err := openRunFile(root, relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("input exceeds byte bound")
	}
	var data bytes.Buffer
	if _, err := transportarchive.CopyExact(ctx, &data, file, info.Size()); err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !securepath.SameMetadata(info, after) {
		return nil, errors.New("input changed while reading")
	}
	return data.Bytes(), nil
}

// selectedRunOutput selects a consumer role from lifecycle-inspected metadata.
// It grants no authority; payload acceptance remains with the delivery owner.
func selectedRunOutput(run lifecycle.Run, name, kind string) (lifecycle.Artifact, error) {
	for _, output := range run.Observed {
		if output.Name == name && output.Kind == kind {
			return output, nil
		}
	}
	return lifecycle.Artifact{}, nvkrerr.New(nvkrerr.CodeVerifyFailed, "completed Run omits %s output %q", kind, name)
}

// DigestFile identifies exact blob bytes only. It grants no structured identity
// or Release authority and rejects final symlink/device/FIFO substitutions.
func DigestFile(ctx context.Context, filename string) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	file, info, err := securepath.OpenRegular(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := transportarchive.CopyExact(ctx, hash, file, info.Size()); err != nil {
		return "", err
	}
	after, err := file.Stat()
	if err != nil || !securepath.SameMetadata(info, after) {
		return "", errors.New("file changed during hashing")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// SafeExtractAssets uses Go's standard gzip/tar decoders and descriptor-relative
// writes. It admits at most 256 regular/directory entries and 16 MiB of data;
// no links, devices, duplicate names, traversal or overwrites are accepted.
// out is an existing private directory (usually a fresh attempt workspace).
func SafeExtractAssets(ctx context.Context, archive, out string) error {
	return safeExtractAssets(ctx, archive, out, []string{"internal/seed/default-seed.yaml", "ui/index.html"})
}

func safeExtractAssets(ctx context.Context, archive, out string, requiredMembers []string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	source, info, err := securepath.OpenRegular(archive)
	if err != nil {
		return err
	}
	defer source.Close()
	if info.Size() > 16<<20 {
		return errors.New("compressed assets exceed byte bound")
	}
	root, rootInfo, err := securepath.OpenDirectory(out)
	if err != nil {
		return err
	}
	defer root.Close()
	var rootStat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &rootStat); err != nil {
		return err
	}
	if rootInfo.Mode().Perm() != 0o700 || rootStat.Uid != uint32(os.Getuid()) {
		return errors.New("assets destination must be a private owned directory")
	}
	stage, err := os.MkdirTemp(out, ".nvkr-assets-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	stageRoot, _, err := securepath.OpenDirectory(stage)
	if err != nil {
		return err
	}
	defer stageRoot.Close()
	zipped, err := gzip.NewReader(source)
	if err != nil {
		return err
	}
	defer func() { _ = zipped.Close() }()
	limited := &io.LimitedReader{R: zipped, N: (16 << 20) + (256 << 10) + 1}
	reader := tar.NewReader(limited)
	seen := make(map[string]bool)
	var total int64
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		// Standard-library locality admission precedes every archive-derived
		// filesystem operation. Held descriptor-relative writes additionally
		// reject substituted symlink parents; lexical checks alone cannot do so.
		if !filepath.IsLocal(name) {
			return errors.New("asset archive member is not a local relative path")
		}
		if count >= 256 || name == "" || name == "." || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00") || seen[name] || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir) || header.Size < 0 || header.Size > (16<<20)-total || header.Mode&0o7000 != 0 {
			return errors.New("unsafe or oversized asset archive member")
		}
		if header.Typeflag == tar.TypeDir && header.Size != 0 {
			return errors.New("asset directory has data")
		}
		seen[name] = true
		total += header.Size
		parent, base, err := securepath.CreateParent(stageRoot, filepath.FromSlash(name))
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeDir {
			err = unix.Mkdirat(int(parent.Fd()), base, 0o700)
			if errors.Is(err, unix.EEXIST) {
				held, _, openErr := securepath.OpenChild(parent, base, true)
				err = openErr
				if held != nil {
					_ = held.Close()
				}
			}
			_ = parent.Close()
			if err != nil {
				return err
			}
			continue
		}
		fd, err := unix.Openat(int(parent.Fd()), base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		_ = parent.Close()
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), name)
		_, copyErr := transportarchive.CopyExact(ctx, file, reader, header.Size)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
	}
	// Drain through the gzip CRC, bounding even padding after tar termination.
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return err
	}
	if limited.N <= 0 {
		return errors.New("expanded assets exceed byte bound")
	}
	after, err := source.Stat()
	if err != nil || !securepath.SameMetadata(info, after) {
		return errors.New("asset archive changed")
	}
	for _, required := range requiredMembers {
		file, _, err := openRunFile(stage, filepath.FromSlash(required))
		if err != nil {
			return errors.New("assets missing required member: " + required)
		}
		_ = file.Close()
	}
	entries, err := stageRoot.ReadDir(-1)
	if err != nil {
		return err
	}
	// Validate the complete destination scope before publishing any member.
	for _, entry := range entries {
		var st unix.Stat_t
		if err := unix.Fstatat(int(root.Fd()), entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
			return errors.New("asset destination already exists")
		} else if !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	moved := []string{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, rollbackAssets(root, stageRoot, moved))
		}
		if err := transportarchive.PublishAtNoReplace(int(stageRoot.Fd()), entry.Name(), int(root.Fd()), entry.Name()); err != nil {
			return errors.Join(err, rollbackAssets(root, stageRoot, moved))
		}
		moved = append(moved, entry.Name())
	}
	return nil
}

func rollbackAssets(root, stage *os.File, names []string) error {
	var result error
	for _, name := range names {
		result = errors.Join(result, transportarchive.PublishAtNoReplace(int(root.Fd()), name, int(stage.Fd()), name))
	}
	return result
}

// DomainBinding describes the live libvirt owner of one private instance disk.
// It cannot be restored from JSON to authorize mutation.
type DomainBinding struct {
	UUID string
	XML  string
}

func DomainInventory(ctx context.Context, runner Runner) ([]string, error) {
	runner.Work = "" // Cleanup inventory must not depend on writable diagnostics.
	raw, err := runner.Capture(ctx, []string{"virsh", "-c", "qemu:///session", "list", "--all", "--name"}, "domain-inventory", 30*time.Second)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(raw)), nil
}

func OwnedDomain(ctx context.Context, runner Runner, name, work string) (*DomainBinding, error) {
	runner.Work = ""
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	id := strings.TrimPrefix(name, "nico-vm-")
	if id == name || id == "" || filepath.Base(id) != id || id == "." || id == ".." || strings.ContainsAny(id, "\\\x00\r\n") {
		return nil, nil
	}
	if !filepath.IsAbs(work) {
		return nil, errors.New("private domain workspace must be absolute")
	}
	raw, err := runner.Capture(ctx, []string{"virsh", "-c", "qemu:///session", "dumpxml", name}, "domain-inspection", 30*time.Second)
	if err != nil {
		return nil, err
	}
	var domain struct {
		XMLName xml.Name `xml:"domain"`
		Names   []string `xml:"name"`
		UUIDs   []string `xml:"uuid"`
		Disks   []struct {
			Sources []struct {
				File string `xml:"file,attr"`
			} `xml:"source"`
		} `xml:"devices>disk"`
	}
	if err := xml.Unmarshal(raw, &domain); err != nil {
		return nil, err
	}
	if len(domain.Names) != 1 || domain.Names[0] != name || len(domain.UUIDs) != 1 {
		return nil, nil
	}
	expected := filepath.Join(work, "var", "instances", id, "disk.qcow2")
	count := 0
	for _, disk := range domain.Disks {
		for _, source := range disk.Sources {
			if source.File == expected {
				count++
			}
		}
	}
	if count != 1 {
		return nil, nil
	}
	if err := admitPrivateDomainPath(work, id); err != nil {
		return nil, err
	}
	// Delegate UUID interpretation to libvirt, rather than adding a UUID grammar.
	identity, err := runner.Capture(ctx, []string{"virsh", "-c", "qemu:///session", "domuuid", name}, "domain-uuid", 30*time.Second)
	if err != nil {
		return nil, err
	}
	uuid := strings.TrimSpace(string(identity))
	if uuid == "" || strings.HasPrefix(uuid, "-") || strings.ContainsAny(uuid, " /\\\t\r\n") || uuid != domain.UUIDs[0] {
		return nil, errors.New("domain UUID observation differs from XML")
	}
	return &DomainBinding{UUID: uuid, XML: string(raw)}, nil
}

func admitPrivateDomainPath(work, id string) error {
	dir, info, err := securepath.OpenDirectory(work)
	if err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &stat); err != nil {
		_ = dir.Close()
		return err
	}
	if info.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Getuid()) {
		_ = dir.Close()
		return errors.New("domain workspace must remain private and owned")
	}
	// Missing paths are possible after an ambiguous Create/Delete. Present
	// components must never redirect the exact XML binding through a symlink.
	for _, part := range []string{"var", "instances", id} {
		next, _, openErr := securepath.OpenChild(dir, part, true)
		_ = dir.Close()
		if os.IsNotExist(openErr) {
			return nil
		}
		if openErr != nil {
			return openErr
		}
		dir = next
	}
	defer dir.Close()
	file, _, err := securepath.OpenChild(dir, "disk.qcow2", false)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

func RemoveOwnedDomain(ctx context.Context, runner Runner, name, work string) error {
	runner.Work = ""
	binding, err := OwnedDomain(ctx, runner, name, work)
	if err != nil {
		return err
	}
	if binding == nil {
		return errors.New("refusing foreign domain deletion")
	}
	state, err := runner.Capture(ctx, []string{"virsh", "-c", "qemu:///session", "domstate", binding.UUID}, "domain-state", 30*time.Second)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(state)) != "shut off" {
		if err := runner.Run(ctx, []string{"virsh", "-c", "qemu:///session", "destroy", binding.UUID}, "domain-stop", 30*time.Second); err != nil {
			return err
		}
	}
	return runner.Run(ctx, []string{"virsh", "-c", "qemu:///session", "undefine", binding.UUID, "--nvram", "--tpm"}, "domain-remove", 30*time.Second)
}

// SweepOwnedDomains must be called only after the simulator producer is joined.
// It recovers an ambiguous Create without inspecting private databases or PIDs.
func SweepOwnedDomains(ctx context.Context, runner Runner, work string) error {
	names, err := DomainInventory(ctx, runner)
	if err != nil {
		return err
	}
	for _, name := range names {
		binding, err := OwnedDomain(ctx, runner, name, work)
		if err != nil {
			return err
		}
		if binding != nil {
			if err := RemoveOwnedDomain(ctx, runner, name, work); err != nil {
				return err
			}
		}
	}
	remaining, err := DomainInventory(ctx, runner)
	if err != nil {
		return err
	}
	for _, name := range remaining {
		binding, err := OwnedDomain(ctx, runner, name, work)
		if err != nil {
			return err
		}
		if binding != nil {
			return errors.New("owned domain remains after sweep")
		}
	}
	return nil
}
