// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"path/filepath"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Private integration seam: the session owns prepared source and retained bytes.
// Only Local.build issues the archive, after successful execution and collection.
// This does not admit public Run execution or authenticate runtime/evidence.
func (handle *NodeSession) execDebianProducer(node ir.Node, options RunOptions) (result *ArtifactResult, returnErr error) {
	if err := requireRunContext(options.Ctx, "session Debian producer"); err != nil {
		return nil, err
	}
	if err := validateDebianArchiveProducer(node); err != nil {
		return nil, err
	}
	if handle == nil || handle.state == nil || !options.Launch || options.DryRun || options.Package ||
		options.Qualification != nil || options.execution != nil || options.SourceWorkspace != nil ||
		options.SourceGitEnvironment != nil || options.reap != nil || options.sources != nil || options.debianArchives != nil {
		return nil, e.New(e.CodeEngineCapability, "private Debian producer requires an open launched session without foreign owners or wrapping")
	}
	if options.debianExecutor != nil {
		if options.debianExecutor.state == nil || options.debianExecutor.state.session == nil ||
			options.debianExecutor.state.session != handle.state {
			return nil, e.New(e.CodeVerifyFailed, "Debian selection belongs to another original session")
		}
		options.debianExecutor.state.mu.Lock()
		admitted := options.debianExecutor.state.runtime
		options.debianExecutor.state.mu.Unlock()
		if options.debianRuntime != admitted {
			return nil, e.New(e.CodeVerifyFailed, "Debian runtime differs from original selected admission")
		}
		if err := options.debianExecutor.matchesInstruction(options.debianSubject, node); err != nil {
			return nil, err
		}
	} else if options.debianSubject != "" || options.debianRuntime != nil {
		return nil, e.New(e.CodeProvenanceMissing, "Debian subject has no original executor")
	}
	options = withDefaults(options)
	root, err := filepath.Abs(options.SourceRoot)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolving Debian source owner")
	}
	session := handle.state
	session.mu.Lock()
	if session.closed || session.ctx.Err() != nil {
		session.mu.Unlock()
		return nil, e.New(e.CodeEngineCapability, "Debian producer session is closed or canceled")
	}
	if session.debianArchives == nil {
		session.debianArchives, err = newDebianArchiveOwner(session.ctx, MaxDebianPackageBytes)
		if err != nil {
			session.mu.Unlock()
			return nil, err
		}
	}
	cache := session.sources[root]
	if cache == nil {
		cache = newPreparedSourceCache()
		session.sources[root] = cache
	}
	options.sources, options.debianArchives = cache, session.debianArchives
	ctx, cancel := context.WithCancel(options.Ctx)
	session.calls[ctx] = cancel
	session.active.Add(1)
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		delete(session.calls, ctx)
		session.mu.Unlock()
		session.active.Done()
	}()
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()
	options.Ctx = ctx
	options.progress = &progressSink{w: options.Stdout}
	options.reap = newReaper(ctx, options.Runtime)
	if options.debianRuntime != nil {
		options.reap.setRuntime(options.Runtime, options.debianRuntime)
	}
	// This runs before unregistering the producer. A failed cleanup or concurrent
	// session close returns no successful description and revokes collected bytes.
	defer func() {
		returnErr = finishDebianSessionProducer(ctx, &result, returnErr, options.reap.closeChecked())
	}()
	result, returnErr = (Local{}).build(node, options)
	if returnErr == nil && (result == nil || result.debian == nil) {
		returnErr = e.New(e.CodeProvenanceMissing, "session Debian producer returned no retained archive")
	}
	return result, returnErr
}

func finishDebianSessionProducer(ctx context.Context, result **ArtifactResult, cause, cleanup error) error {
	cause = errors.Join(cause, cleanup)
	if ctx.Err() != nil {
		cause = e.Wrap(e.CodeTimeout, errors.Join(cause, ctx.Err()), "completing session Debian producer")
	}
	if cause != nil {
		if *result != nil {
			cause = errors.Join(cause, (*result).debian.Close())
		}
		*result = nil
	}
	return cause
}
