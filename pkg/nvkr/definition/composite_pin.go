// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// MemberPin reports one member whose loaded digest differs from its pin.
type MemberPin struct {
	Name    string `json:"name"`
	Pinned  string `json:"pinned"`
	Current string `json:"current"`
}

// PinReport describes one composite pin check or rewrite.
type PinReport struct {
	Path    string      `json:"path"`
	Drift   []MemberPin `json:"drift"`
	Written bool        `json:"written"`
}

// PinComposite recomputes every member's nvkr.definition digest. With check it
// reports drift as unverified-ref without writing. Otherwise it replaces only
// the drifted digest strings, admits the result, and commits it with a byte
// CAS against the bytes it read, so a concurrent writer wins with write-conflict.
func PinComposite(ctx context.Context, root, compositePath string, check bool) (PinReport, error) {
	report := PinReport{Path: compositePath, Drift: []MemberPin{}}
	if ctx == nil {
		return report, e.New(e.CodeUsage, "composite pin requires a context")
	}
	if err := ctx.Err(); err != nil {
		return report, e.Wrap(e.CodeTimeout, err, "composite pin canceled")
	}
	read := RootReader(root)
	raw, err := read(ctx, compositePath)
	if err != nil {
		return report, err
	}
	composite, err := DecodeComposite(raw)
	if err != nil {
		return report, err
	}
	paths, err := resolveMembers(compositePath, composite)
	if err != nil {
		return report, err
	}
	total := int64(len(raw))
	var replacements []string
	for i, member := range composite.Members {
		if err := ctx.Err(); err != nil {
			return report, e.Wrap(e.CodeTimeout, err, "composite pin canceled")
		}
		definition, size, err := loadMember(ctx, read, member, paths[i], maxCompositeInputBytes-total)
		if err != nil {
			return report, err
		}
		total += size
		digest, err := definition.Digest()
		if err != nil {
			return report, err
		}
		if digest != member.Digest {
			report.Drift = append(report.Drift, MemberPin{Name: member.Name, Pinned: member.Digest, Current: digest})
			replacements = append(replacements, member.Digest, digest)
		}
	}
	if len(report.Drift) == 0 {
		return report, nil
	}
	if check {
		return report, e.New(e.CodeUnverifiedRef, "composite %s has %d member pin(s) that differ from their Definitions; run definition pin", compositePath, len(report.Drift))
	}
	for _, drift := range report.Drift {
		if count := bytes.Count(raw, []byte(drift.Pinned)); count != 1 {
			return report, e.New(e.CodeInvalidCatalog, "composite %s spells member %q digest %d times; it cannot be rewritten exactly", compositePath, drift.Name, count)
		}
	}
	// One pass replaces every old pin, so a swapped digest cannot be rewritten twice.
	updated := []byte(strings.NewReplacer(replacements...).Replace(string(raw)))
	if _, err := AdmitComposite(ctx, compositePath, updated, read); err != nil {
		return report, err
	}
	if err := replaceFileCAS(ctx, root, compositePath, raw, updated); err != nil {
		return report, err
	}
	report.Written = true
	return report, nil
}

// replaceFileCAS atomically replaces root/name iff its bytes still equal
// expected. Same-file writers serialize on an advisory guard; the stage is
// fsynced, renamed and the directory fsynced. Cancellation before rename
// aborts and removes the stage.
func replaceFileCAS(ctx context.Context, root, name string, expected, replacement []byte) error {
	target := filepath.Join(root, filepath.FromSlash(name))
	directory, base := filepath.Dir(target), filepath.Base(target)
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() {
		return e.New(e.CodeInvalidCatalog, "composite %s is not a regular file", name)
	}
	guard, err := os.OpenFile(filepath.Join(directory, "."+base+".pin.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "opening composite pin guard")
	}
	defer guard.Close()
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return e.New(e.CodeWriteConflict, "another composite pin writer is active for %s", name)
		}
		return e.Wrap(e.CodeInvalidCatalog, err, "locking composite pin guard")
	}
	defer func() { _ = syscall.Flock(int(guard.Fd()), syscall.LOCK_UN) }()
	current, err := RootReader(root)(ctx, name)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, expected) {
		return e.New(e.CodeWriteConflict, "composite %s changed while its pins were recomputed", name)
	}
	stage, err := os.CreateTemp(directory, "."+base+".stage-*")
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "staging composite %s", name)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(stage.Name())
		}
	}()
	if err := stage.Chmod(info.Mode().Perm()); err != nil {
		_ = stage.Close()
		return e.Wrap(e.CodeInvalidCatalog, err, "setting staged composite permissions")
	}
	if _, err := stage.Write(replacement); err != nil {
		_ = stage.Close()
		return e.Wrap(e.CodeInvalidCatalog, err, "writing staged composite %s", name)
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		return e.Wrap(e.CodeInvalidCatalog, err, "syncing staged composite %s", name)
	}
	if err := stage.Close(); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "closing staged composite %s", name)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "composite pin canceled before commit")
	}
	if err := os.Rename(stage.Name(), target); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "replacing composite %s", name)
	}
	committed = true
	handle, err := os.Open(directory)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "opening composite directory for sync")
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "syncing composite directory")
	}
	return nil
}
