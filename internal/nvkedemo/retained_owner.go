// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const retainedRequestFile = "request.json"
const retainedDescriptionFile = "control.json"

// This is private launch intent, not an executable NVKR receipt, sudo credential
// or imported runtime owner. The new process independently admits the real Run.
type retainedLaunch struct {
	Version    int    `json:"version"`
	Config     Config `json:"config"`
	CachedSudo bool   `json:"cachedSudo"`
	// Private scheduling data, not ownership or a persisted execution receipt.
	WorkDeadline time.Time `json:"workDeadline,omitempty"`
}

func readRetainedJSON[T any](ctx context.Context, path string) (T, error) {
	var result T
	if err := requireContext(ctx); err != nil {
		return result, nvkrerr.Wrap(nvkrerr.CodeUsage, err, "retained session read requires a context")
	}
	file, info, err := securepath.OpenRegular(path)
	if err != nil {
		return result, nvkrerr.New(nvkrerr.CodePreflightFailed, "private retained session record unavailable")
	}
	defer file.Close()
	if !currentOwner(info) || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > retainedControlLimit {
		return result, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained session record must be private, owned and bounded")
	}
	raw, err := io.ReadAll(io.LimitReader(file, retainedControlLimit+1))
	if err != nil || len(raw) > retainedControlLimit {
		return result, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained session record could not be read completely")
	}
	after, err := file.Stat()
	if err != nil || !securepath.SameMetadata(info, after) || int64(len(raw)) != info.Size() {
		return result, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained session record changed during inspection")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, nvkrerr.New(nvkrerr.CodePreflightFailed, "invalid retained session record schema")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return result, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained session record must contain one document")
	}
	if err := ctx.Err(); err != nil {
		return result, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained record inspection canceled")
	}
	return result, nil
}

// ServeRetained is the CLI's fixed hidden child entry, not a command accepting
// arbitrary argv or PID adoption. It starts no service until the parent has
// authenticated this new endpoint and explicitly requested start. Its one Run
// stack owns every service through explicit cleanup. Finish requires joined
// cleanup, or returned failed teardown with known-joined producers; the latter
// never claims successful cleanup or admits host-network restoration.
func ServeRetained(ctx context.Context, session string) error {
	if err := checkRetainedPeerCapability(ctx); err != nil {
		return err
	}
	directory, root, err := privateNetworkDirectory(session)
	if err != nil {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "retained owner requires its original private launch directory")
	}
	defer directory.Close()
	launch, err := readRetainedJSON[retainedLaunch](ctx, filepath.Join(root, retainedRequestFile))
	if err != nil {
		return err
	}
	if launch.Version != 1 || !filepath.IsAbs(launch.Config.Out) || !operatorArgument(launch.Config.Out) || filepath.Clean(launch.Config.Out) != launch.Config.Out {
		return nvkrerr.New(nvkrerr.CodeUsage, "retained launch requires exact version and absolute workspace")
	}
	executable, err := os.Executable()
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "select retained owner's own executable")
	}
	launch.Config.DemoExecutable = executable
	return serveRetained(ctx, root, launch, DeployArtifacts)
}

func serveRetained(ctx context.Context, root string, launch retainedLaunch, work func(context.Context, Config) error) error {
	lifetime := context.WithoutCancel(ctx)
	deadline := launch.WorkDeadline
	if deadline.IsZero() {
		var bounded bool
		deadline, bounded = ctx.Deadline()
		if !bounded {
			deadline = time.Now().Add(8 * time.Hour) // Legacy launch records only.
		}
	}
	operation, stopWork := context.WithDeadline(lifetime, deadline)
	defer stopWork()
	owner, err := newRetainedRuntime(lifetime, stopWork)
	if err != nil {
		return err
	}
	owner.failureRecorder = func(primary error) error {
		raw := []byte(primary.Error())
		raw = append(raw[:min(len(raw), 1<<20)], '\n')
		if err := privateWrite(filepath.Join(root, "owner-error.log"), raw); err != nil {
			return nvkrerr.New(nvkrerr.CodePreflightFailed, "private operation failure diagnostics could not be retained")
		}
		return nil
	}
	owner.panicRecorder = func(stack []byte) error { return privateWrite(filepath.Join(root, "owner-panic.log"), stack) }
	operation = context.WithValue(operation, retainedRuntimeKey{}, owner)
	stopAttached := context.AfterFunc(ctx, stopWork)
	defer stopAttached()
	progress := &retainedProgress{cached: launch.CachedSudo}
	control, err := newRetainedControlWithProgress(ctx, owner, filepath.Join(launch.Config.Out, "runtime"), root, func() error {
		if err := detachRetainedProcess(); err != nil {
			return err
		}
		stopAttached()
		return nil
	}, progress)
	if err != nil {
		return err
	}
	if err := privateJSON(filepath.Join(root, retainedDescriptionFile), control.description); err != nil {
		closeContext, cancel := context.WithTimeout(lifetime, 10*time.Second)
		defer cancel()
		return errors.Join(nvkrerr.New(nvkrerr.CodePreflightFailed, "retained endpoint could not be published; no service work started"), control.close(closeContext))
	}
	// A missing foreground never starts an orphan deployment. Cleanup before
	// start closes only this control, not any imported processes/resources.
	startup, cancelStartup := context.WithTimeout(ctx, 30*time.Second)
	select {
	case <-control.start:
	case <-owner.cleanup:
	case <-startup.Done():
		cancelStartup()
		closeContext, cancel := context.WithTimeout(lifetime, 10*time.Second)
		defer cancel()
		return errors.Join(nvkrerr.New(nvkrerr.CodeTimeout, "retained startup was not acknowledged; no service work started"), control.close(closeContext))
	}
	cancelStartup()
	launch.Config.Progress = progress
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		var primary error
		owner.mu.Lock()
		requested := owner.snapshot.state == retainedCleanupRequested
		owner.mu.Unlock()
		if !requested {
			primary = retainedWork(operation, root, launch.Config, work)
		}
		// Covers failures before the first downstream finalizer as well as the
		// normal Run's nested holds. There is no viewer-loss cleanup fallback.
		failureErr := owner.retainFailure(primary)
		primary = errors.Join(primary, failureErr, owner.hold())
		owner.recordTeardownReturn(primary)
		if primary != nil {
			_ = privateWrite(filepath.Join(root, "owner-return.log"), []byte(primary.Error()+"\n"))
		}
		_ = control.recordJoined(lifetime, owner.joinedCleanup())
	}()
	<-control.finish // Joined cleanup, or explicit exit after safely returned failed teardown.
	<-workerDone     // Sole owner joins its Run goroutine, including finalizers.
	for {
		closeContext, cancel := context.WithTimeout(lifetime, 10*time.Second)
		err := control.close(closeContext)
		cancel()
		if !errors.Is(err, ErrUnjoined) {
			return err
		}
		// An admitted handler remains owned even after a join budget expires.
		// Never exit or restore controls merely because shutdown was quiet.
	}
}

func retainedWork(ctx context.Context, root string, cfg Config, work func(context.Context, Config) error) (result error) {
	defer func() {
		if value := recover(); value != nil {
			// Do not call a panic value's formatter or print provider payloads.
			stack := debug.Stack()
			stack = stack[:min(len(stack), 1<<20)]
			storeErr := privateWrite(filepath.Join(root, "owner-panic.log"), fmt.Appendf(nil, "Panic: %s\n\n%s", PrivatePanicValueDescription(value), stack))
			result = errors.Join(ErrRetainedControls, nvkrerr.Wrap(nvkrerr.CodeInternal, storeErr, "retained service owner panic; ownership is unknown, inspect private diagnostics"))
		}
	}()
	return work(ctx, cfg)
}
