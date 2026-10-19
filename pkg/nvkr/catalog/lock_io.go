// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

// LoadLockContext reads the registry and optional lock through one bounded,
// no-follow context provider. The returned value carries a private CAS token consumed
// by WriteLock.
func LoadLockContext(ctx context.Context, dir string) (*PinLock, error) {
	if err := requireCatalogContext(ctx, "catalog lock snapshot"); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "resolving catalog directory %q", dir)
	}
	bound, err := BindContext(ctx, Dir(abs))
	if err != nil {
		return nil, err
	}
	if err := rejectLegacyComposition(bound); err != nil {
		return nil, err
	}
	registryRaw, registryExists, err := readOptionalProviderFile(bound, RegistryFile)
	if err != nil {
		return nil, catalogLockReadError(ctx, err, RegistryFile)
	}
	if !registryExists {
		return nil, e.New(e.CodeInvalidCatalog, "required %s is missing from %q", RegistryFile, abs)
	}
	if _, err := decodeRegistry(registryRaw, filepath.Join(abs, RegistryFile)); err != nil {
		return nil, err
	}
	raw, lockExists, err := readOptionalProviderFile(bound, LockFile)
	if err != nil {
		return nil, catalogLockReadError(ctx, err, LockFile)
	}
	if !lockExists {
		return &PinLock{
			APIVersion: lockAPIVersion,
			Kind:       lockKind,
			Pins:       map[string]Pin{},
			revision:   revisionOf(nil, false, abs).withRegistry(registryRaw),
		}, nil
	}
	lock, err := decodePinLock(raw, filepath.Join(abs, LockFile))
	if err != nil {
		return nil, err
	}
	lock.revision = revisionOf(raw, true, abs).withRegistry(registryRaw)
	return lock, nil
}

func catalogLockReadError(ctx context.Context, err error, name string) error {
	if ctx == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return normalizeCatalogContextError(ctx, err, "catalog lock snapshot")
	}
	return e.Wrap(e.CodeInvalidCatalog, err, "reading %s", name)
}

func readOptionalProviderFile(provider DataProvider, name string) ([]byte, bool, error) {
	raw, err := provider.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return raw, true, nil
}

// serializePinLock is shared by dry-run admission and the writer so both enforce
// the size of the exact same machine-managed bytes, including their header.
func serializePinLock(lock *PinLock) ([]byte, error) {
	if err := validatePinLockShape(lock); err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(clonePinLock(lock))
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "marshaling lock")
	}
	const header = "# Machine-managed by `nvkr catalog pin` / `nvkr catalog bump`. Do not hand-edit.\n"
	out := append([]byte(header), body...)
	if int64(len(out)) > MaxCatalogFileBytes {
		return nil, e.New(e.CodeInvalidCatalog, "serialized %s exceeds the %d-byte bound", LockFile, MaxCatalogFileBytes)
	}
	return out, nil
}

// WriteLockContext atomically replaces the lock iff the exact bytes observed by
// LoadLock are still current. It stages in the target directory, fsyncs file and
// directory, and serializes same-checkout writers with an advisory lock. Cancellation
// before rename aborts and removes the stage; after rename, commit wins and durability
// work completes. A stale writer receives CodeWriteConflict.
func WriteLockContext(ctx context.Context, dir string, lock *PinLock) error {
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		return err
	}
	if lock == nil {
		return e.New(e.CodeInvalidCatalog, "cannot write a nil lock")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "resolving catalog directory %q", dir)
	}
	if !lock.revision.known {
		return e.New(e.CodeWriteConflict, "lock has no authenticated update snapshot; call LoadLock before WriteLock")
	}
	if lock.revision.dir != abs {
		return e.New(e.CodeWriteConflict, "lock snapshot belongs to %q, not %q", lock.revision.dir, abs)
	}

	if err := validatePinLockShape(lock); err != nil {
		return err
	}
	candidate := clonePinLock(lock)
	out, err := serializePinLock(candidate)
	if err != nil {
		return err
	}
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		return err
	}

	bound, err := BindContext(ctx, Dir(abs))
	if err != nil {
		return err
	}
	if err := rejectLegacyComposition(bound); err != nil {
		return err
	}

	// Potentially slow upstream reads precede the writer guard and final CAS.
	registryBefore, err := bound.ReadFile(RegistryFile)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "reading registry before pin verification")
	}
	if sha256.Sum256(registryBefore) != lock.revision.registryDigest {
		return e.New(e.CodeWriteConflict, "registry changed before pin verification")
	}
	checkedRegistry, err := decodeRegistry(registryBefore, RegistryFile)
	if err != nil {
		return err
	}
	if err := validateLockAgainstRegistry(checkedRegistry, candidate, true); err != nil {
		return err
	}
	if err := validateDerivedPins(ctx, checkedRegistry, candidate); err != nil {
		return err
	}

	guard, err := os.OpenFile(filepath.Join(abs, "."+LockFile+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "opening lock writer guard")
	}
	defer guard.Close()
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return e.New(e.CodeWriteConflict, "another catalog lock writer is active")
		}
		return e.Wrap(e.CodeInvalidCatalog, err, "locking lock writer guard")
	}
	defer func() { _ = syscall.Flock(int(guard.Fd()), syscall.LOCK_UN) }() // best effort during return
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		return err
	}

	target := filepath.Join(abs, LockFile)
	current, exists, err := readLockBytesContext(ctx, target)
	if err != nil {
		return err
	}
	currentRevision := revisionOf(current, exists, abs)
	if currentRevision.exists != lock.revision.exists ||
		(currentRevision.exists && currentRevision.digest != lock.revision.digest) {
		return e.New(e.CodeWriteConflict,
			"%s changed since LoadLock (expected %s, found %s)", LockFile,
			revisionString(lock.revision), revisionString(currentRevision))
	}
	registryRaw, registryExists, err := readLockBytesContext(ctx, filepath.Join(abs, RegistryFile))
	if err != nil {
		return err
	}
	if !lock.revision.registryKnown || !registryExists ||
		sha256.Sum256(registryRaw) != lock.revision.registryDigest {
		return e.New(e.CodeWriteConflict,
			"%s changed since LoadLock; reload catalog sources and lock before resolving pins", RegistryFile)
	}
	reg, err := decodeRegistry(registryRaw, filepath.Join(abs, RegistryFile))
	if err != nil {
		return err
	}
	if err := validateLockAgainstRegistry(reg, candidate, true); err != nil {
		return err
	}
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(abs, "."+LockFile+".stage-*")
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "staging %s", LockFile)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return e.Wrap(e.CodeInvalidCatalog, err, "setting staged lock permissions")
	}
	written, err := io.Copy(tmp, catalogContextReader{ctx: ctx, reader: bytes.NewReader(out)})
	if err != nil || written != int64(len(out)) {
		_ = tmp.Close()
		if ctx.Err() != nil {
			return normalizeCatalogContextError(ctx, err, "catalog lock write")
		}
		if err == nil {
			err = io.ErrShortWrite
		}
		return e.Wrap(e.CodeInvalidCatalog, err, "writing staged %s", LockFile)
	}
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return e.Wrap(e.CodeInvalidCatalog, err, "syncing staged %s", LockFile)
	}
	if err := tmp.Close(); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "closing staged %s", LockFile)
	}
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "atomically replacing %s", LockFile)
	}
	dirHandle, err := os.Open(abs)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "opening catalog directory for sync")
	}
	if err := dirHandle.Sync(); err != nil {
		_ = dirHandle.Close()
		return e.Wrap(e.CodeInvalidCatalog, err, "syncing catalog directory")
	}
	if err := dirHandle.Close(); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "closing catalog directory")
	}
	committed = true
	lock.APIVersion = candidate.APIVersion
	lock.Kind = candidate.Kind
	lock.revision = revisionOf(out, true, abs).withRegistry(registryRaw)
	return nil
}

func readLockBytesContext(ctx context.Context, path string) ([]byte, bool, error) {
	if err := requireCatalogContext(ctx, "catalog lock read"); err != nil {
		return nil, false, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, false, e.Wrap(e.CodeInvalidCatalog, err, "resolving %s", path)
	}
	provider := Dir(filepath.Dir(abs)).(ContextDataProvider)
	raw, err := provider.ReadFileContext(ctx, filepath.Base(abs))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, normalizeCatalogContextError(ctx, err, "catalog lock read")
		}
		return nil, false, e.Wrap(e.CodeInvalidCatalog, err, "reading %s", abs)
	}
	return raw, true, nil
}
