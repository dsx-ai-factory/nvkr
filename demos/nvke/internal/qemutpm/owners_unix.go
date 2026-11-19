// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package qemutpm

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"golang.org/x/sys/unix"
)

// An instance keeps its TPM across source-owned Delete/CreateAndStart recovery.
// This guard serializes those invocations; every launch gets a separate audit.
type launchOwner struct {
	parent    *os.File
	id        string
	directory *os.File
	guard     int
}

func openLaunchOwner(ctx context.Context, parent *os.File, id string, create bool) (*launchOwner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if create {
		if err := unix.Mkdirat(int(parent.Fd()), id, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	directory, _, err := securepath.OpenChild(parent, id, true)
	if err != nil {
		return nil, err
	}
	owner := &launchOwner{parent: parent, id: id, directory: directory, guard: -1}
	fail := func(err error) (*launchOwner, error) { _ = owner.close(false); return nil, err }
	if err := privateDirectory(directory); err != nil {
		return fail(err)
	}
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		owner.guard, err = unix.Openat(int(directory.Fd()), "guard", flags|unix.O_CREAT|unix.O_EXCL, 0600)
	}
	if !create || errors.Is(err, unix.EEXIST) {
		owner.guard, err = unix.Openat(int(directory.Fd()), "guard", flags, 0)
	}
	if err != nil {
		return fail(err)
	}
	if _, err := owner.identity(ctx); err != nil {
		return fail(err)
	}
	if err := unix.Flock(owner.guard, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail(errors.Join(ErrUnjoined, err))
	}
	// A no-follow descriptor must still name the same guard after locking.
	if _, err := owner.identity(ctx); err != nil {
		return fail(err)
	}
	if create {
		if err := errors.Join(unix.Fsync(owner.guard), directory.Sync(), parent.Sync()); err != nil {
			return fail(err)
		}
	}
	return owner, nil
}

func (o *launchOwner) identity(ctx context.Context) (fileIdentity, error) {
	if err := ctx.Err(); err != nil {
		return fileIdentity{}, err
	}
	namedDirectory, info, err := securepath.OpenChild(o.parent, o.id, true)
	if err != nil {
		return fileIdentity{}, err
	}
	heldInfo, statErr := o.directory.Stat()
	if err := errors.Join(statErr, namedDirectory.Close(), privateDirectory(o.directory)); err != nil || !os.SameFile(info, heldInfo) {
		return fileIdentity{}, errors.Join(err, errors.New("native QEMU instance owner directory changed"))
	}
	var held, named unix.Stat_t
	if err := unix.Fstat(o.guard, &held); err != nil {
		return fileIdentity{}, err
	}
	if err := unix.Fstatat(int(o.directory.Fd()), "guard", &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fileIdentity{}, err
	}
	if held.Dev != named.Dev || held.Ino != named.Ino || held.Mode&unix.S_IFMT != unix.S_IFREG || held.Mode&07777 != 0600 || held.Uid != uint32(os.Getuid()) || held.Nlink != 1 || held.Size != 0 {
		return fileIdentity{}, errors.New("native QEMU guard must remain an empty private owned regular file")
	}
	return fileIdentity{uint64(held.Dev), held.Ino}, nil //nolint:unconvert // Stat_t.Dev is int32 on Darwin and uint64 on Linux.
}

func (o *launchOwner) state(ctx context.Context, instances, guest *os.File, id string, expected instanceState) (instanceState, error) {
	if err := ctx.Err(); err != nil {
		return instanceState{}, err
	}
	named, info, err := securepath.OpenChild(instances, id, true)
	if err != nil {
		return instanceState{}, err
	}
	heldInfo, statErr := guest.Stat()
	if err := errors.Join(statErr, named.Close(), protectedGuestDirectory(guest)); err != nil || !os.SameFile(info, heldInfo) {
		return instanceState{}, errors.Join(err, errors.New("native QEMU guest directory changed"))
	}
	if expected == (instanceState{}) {
		// A preexisting unrecorded TPM cannot be adopted or manufactured again.
		if err := unix.Mkdirat(int(guest.Fd()), "nvkr-tpm", 0700); err != nil {
			return instanceState{}, err
		}
		if err := guest.Sync(); err != nil {
			return instanceState{}, err
		}
	}
	tpm, _, err := securepath.OpenChild(guest, "nvkr-tpm", true)
	if err != nil {
		return instanceState{}, err
	}
	defer tpm.Close()
	if err := privateDirectory(tpm); err != nil {
		return instanceState{}, err
	}
	var guestStat, tpmStat unix.Stat_t
	if err := errors.Join(unix.Fstat(int(guest.Fd()), &guestStat), unix.Fstat(int(tpm.Fd()), &tpmStat)); err != nil {
		return instanceState{}, err
	}
	guard, err := o.identity(ctx)
	state := instanceState{guard, fileIdentity{uint64(guestStat.Dev), guestStat.Ino}, fileIdentity{uint64(tpmStat.Dev), tpmStat.Ino}} //nolint:unconvert // Stat_t.Dev is int32 on Darwin and uint64 on Linux.
	if err != nil || expected != (instanceState{}) && state != expected {
		return instanceState{}, errors.Join(err, errors.New("native QEMU TPM or instance identity changed"))
	}
	return state, nil
}

// An unjoined hidden supervisor retains the raw guard descriptor until process
// exit; no os.File finalizer may unlock it while a child remains unjoined.
func (o *launchOwner) close(unjoined bool) error {
	err := o.directory.Close()
	if !unjoined && o.guard >= 0 {
		err = errors.Join(err, unix.Close(o.guard)) // close releases the kernel lock
		o.guard = -1
	}
	return err
}

func (o *launchOwner) previous(ctx context.Context) (instanceState, error) {
	if err := ctx.Err(); err != nil {
		return instanceState{}, err
	}
	rows, err := o.directory.ReadDir(-1)
	if err != nil {
		return instanceState{}, err
	}
	var state instanceState
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return instanceState{}, err
		}
		if row.Name() == "guard" {
			continue
		}
		if !strings.HasPrefix(row.Name(), "launch-") {
			return instanceState{}, errors.New("unexpected native QEMU launch record")
		}
		dir, _, err := securepath.OpenChild(o.directory, row.Name(), true)
		if err != nil {
			return instanceState{}, err
		}
		receipt, readErr := readJoined(ctx, dir)
		err = errors.Join(privateDirectory(dir), readErr, dir.Close())
		if err != nil || !receipt.Joined || receipt.State.Guard.Inode == 0 || receipt.State.Guest.Inode == 0 || receipt.State.TPM.Inode == 0 || state != (instanceState{}) && state != receipt.State {
			return instanceState{}, errors.Join(ErrUnjoined, err, errors.New("native QEMU predecessor lacks a matching joined state receipt"))
		}
		state = receipt.State
	}
	guard, err := o.identity(ctx)
	if err != nil || state != (instanceState{}) && state.Guard != guard {
		return instanceState{}, errors.Join(err, errors.New("native QEMU guard identity changed"))
	}
	return state, nil
}

func readJoined(ctx context.Context, dir *os.File) (termination, error) {
	if err := ctx.Err(); err != nil {
		return termination{}, err
	}
	file, info, err := securepath.OpenChild(dir, "joined", false)
	if err != nil {
		return termination{}, err
	}
	defer file.Close()
	if err := protectedOwner(file); err != nil || info.Mode().Perm() != 0600 || info.Size() > 1024 {
		return termination{}, errors.Join(err, errors.New("require bounded private native QEMU termination receipt"))
	}
	var receipt termination
	decoder := json.NewDecoder(io.LimitReader(file, 1024))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&receipt)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = errors.New("trailing native QEMU termination data")
	}
	return receipt, err
}

func (o *launchOwner) record(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := "launch-" + rand.Text()
	if err := unix.Mkdirat(int(o.directory.Fd()), name, 0700); err != nil {
		return nil, err
	}
	if err := o.directory.Sync(); err != nil {
		return nil, err
	}
	file, _, err := securepath.OpenChild(o.directory, name, true)
	return file, err
}

func writeJoined(ctx context.Context, dir *os.File, state instanceState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat(int(dir.Fd()), "joined", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "joined")
	err = errors.Join(json.NewEncoder(file).Encode(termination{Joined: true, State: state}), file.Sync(), file.Close())
	return errors.Join(err, dir.Sync())
}

func closeOwners(ctx context.Context, cfg configuration) error {
	root, _, err := securepath.OpenDirectory(cfg.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := privateDirectory(root); err != nil {
		return err
	}
	owners, _, err := securepath.OpenChild(root, "launches", true)
	if err != nil {
		return err
	}
	defer owners.Close()
	if err := privateDirectory(owners); err != nil {
		return err
	}
	rows, err := owners.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, row := range rows {
		owner, err := openLaunchOwner(ctx, owners, row.Name(), false)
		if err != nil {
			return err
		}
		state, priorErr := owner.previous(ctx)
		err = priorErr
		if err == nil && state != (instanceState{}) {
			instances, _, openErr := securepath.OpenDirectory(cfg.Instances)
			if openErr == nil {
				guest, _, guestErr := securepath.OpenChild(instances, row.Name(), true)
				if guestErr == nil {
					_, err = owner.state(ctx, instances, guest, row.Name(), state)
					err = errors.Join(err, guest.Close())
				} else if !errors.Is(guestErr, os.ErrNotExist) {
					err = guestErr
				}
				// Source-owned deletion removes the instance directory after all
				// children join. Its absence cannot excuse a missing audit above.
				err = errors.Join(err, instances.Close())
			} else {
				err = openErr
			}
		}
		if err = errors.Join(err, owner.close(false)); err != nil {
			return err
		}
	}
	return ctx.Err()
}
