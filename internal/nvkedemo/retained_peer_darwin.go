// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package nvkedemo

/*
#cgo LDFLAGS: -lproc -lbsm
#include <errno.h>
#include <bsm/libbsm.h>
#include <libproc.h>
#include <mach/mach.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/ucred.h>
#include <sys/un.h>

static int nvkr_self_audit_token(audit_token_t *token) {
	mach_msg_type_number_t count = TASK_AUDIT_TOKEN_COUNT;
	if (task_info(mach_task_self(), TASK_AUDIT_TOKEN, (task_info_t)token, &count) != KERN_SUCCESS ||
	    count != TASK_AUDIT_TOKEN_COUNT) {
		errno = ENOTSUP;
		return -1;
	}
	return 0;
}

static int nvkr_socket_audit_token(int fd, audit_token_t *token) {
	socklen_t size = sizeof(*token);
	if (getsockopt(fd, SOL_LOCAL, LOCAL_PEERTOKEN, token, &size) != 0) return -1;
	if (size != sizeof(*token)) { errno = EIO; return -1; }
	return 0;
}

static int nvkr_original_token_live(audit_token_t *token) {
	if (__builtin_available(macOS 11.0, *)) {
		char path[PROC_PIDPATHINFO_MAXSIZE];
		errno = 0;
		if (proc_pidpath_audittoken(token, path, sizeof(path)) > 0) return 0;
		if (errno == 0) errno = ESRCH;
		return -1;
	}
	errno = ENOTSUP;
	return -1;
}

static pid_t nvkr_audit_pid(audit_token_t token) { return audit_token_to_pid(token); }
static uid_t nvkr_audit_uid(audit_token_t token) { return audit_token_to_euid(token); }
*/
import "C"

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"syscall"
	"unsafe"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// Generation is an opaque selector captured by the owner itself. Its initial
// consumer must still own the actual unreaped child before releasing that child.
// It cannot reconstruct ownership, readiness or a joined-cleanup receipt.
func retainedPeerGeneration(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", nvkrerr.New(nvkrerr.CodeUsage, "retained generation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained generation canceled")
	}
	var token C.audit_token_t
	if result, err := C.nvkr_self_audit_token(&token); result != 0 {
		return "", nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "Darwin self audit-token API unavailable")
	}
	if result, err := C.nvkr_original_token_live(&token); result != 0 {
		return "", nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "Darwin original-generation validation unavailable")
	}
	if err := ctx.Err(); err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained generation canceled")
	}
	// The system header owns token layout; Go only encodes its opaque bytes.
	return hex.EncodeToString(C.GoBytes(unsafe.Pointer(&token), C.int(C.sizeof_audit_token_t))), nil
}

func observeRetainedPeer(ctx context.Context, connection *net.UnixConn, expectedPID int, generation string) (*SupervisorPeer, error) {
	if ctx == nil || connection == nil || expectedPID <= 0 {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "retained peer requires its context, connection and original child selection")
	}
	if err := ctx.Err(); err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained peer observation canceled")
	}
	encoded, err := hex.DecodeString(generation)
	if err != nil || len(encoded) != C.sizeof_audit_token_t {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "retained peer requires its original opaque generation")
	}
	var original C.audit_token_t
	C.memcpy(unsafe.Pointer(&original), unsafe.Pointer(&encoded[0]), C.size_t(len(encoded)))
	if int(C.nvkr_audit_pid(original)) != expectedPID || uint32(C.nvkr_audit_uid(original)) != uint32(os.Getuid()) {
		return nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained generation does not select the original current-user child")
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "inspect retained socket")
	}
	var peer *SupervisorPeer
	var observedErr error
	err = raw.Control(func(socket uintptr) {
		if observedErr = ctx.Err(); observedErr != nil {
			return
		}
		credentials, readErr := unix.GetsockoptXucred(int(socket), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if readErr != nil {
			observedErr = readErr
			return
		}
		pid, readErr := unix.GetsockoptInt(int(socket), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if readErr != nil {
			observedErr = readErr
			return
		}
		if credentials.Version != C.XUCRED_VERSION || credentials.Uid != uint32(os.Getuid()) || pid != expectedPID {
			observedErr = errors.New("retained socket does not match the selected user and child")
			return
		}
		var connected C.audit_token_t
		if result, readErr := C.nvkr_socket_audit_token(C.int(socket), &connected); result != 0 {
			observedErr = readErr
			return
		}
		if !bytes.Equal(encoded, C.GoBytes(unsafe.Pointer(&connected), C.int(C.sizeof_audit_token_t))) {
			observedErr = errors.New("retained socket no longer matches the original owner generation")
			return
		}
		watch, readErr := newDarwinExitQueue(ctx, expectedPID)
		if readErr != nil {
			observedErr = nvkrerr.Wrap(nvkrerr.CodeEngineCapability, readErr, "Darwin process exit observation unavailable")
			return
		}
		// LOCAL_PEERTOKEN itself re-resolves a numeric PID. Validate the ORIGINAL
		// self-captured token after watch attachment, never a replacement token.
		// Success proves that generation was still alive after registration.
		if result, readErr := C.nvkr_original_token_live(&original); result != 0 {
			_ = watch.Close()
			observedErr = readErr
			return
		}
		peer = &SupervisorPeer{pid: expectedPID, uid: credentials.Uid, file: watch}
	})
	if err == nil && observedErr == nil {
		observedErr = ctx.Err()
	}
	if err != nil || observedErr != nil {
		if peer != nil {
			_ = peer.Close()
		}
		combined := errors.Join(err, observedErr)
		if ctx.Err() != nil {
			return nil, nvkrerr.Wrap(nvkrerr.CodeTimeout, combined, "retained peer observation canceled")
		}
		var coded *nvkrerr.Error
		if errors.As(combined, &coded) {
			return nil, combined
		}
		return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, combined, "retained original-generation binding failed")
	}
	return peer, nil
}

func checkRetainedPeerCapability(ctx context.Context) error {
	generation, err := retainedPeerGeneration(ctx)
	if err != nil {
		return err
	}
	syscall.ForkLock.RLock()
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(sockets[0])
		unix.CloseOnExec(sockets[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "Darwin retained socket capability unavailable")
	}
	input := os.NewFile(uintptr(sockets[0]), "retained-capability")
	output := os.NewFile(uintptr(sockets[1]), "retained-capability-peer")
	defer input.Close()
	defer output.Close()
	connection, err := net.FileConn(input)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "adopt retained capability socket")
	}
	defer func() { _ = connection.Close() }()
	peer, err := observeRetainedPeer(ctx, connection.(*net.UnixConn), os.Getpid(), generation)
	if err != nil {
		return err
	}
	return peer.Close()
}
