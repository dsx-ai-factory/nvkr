// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"golang.org/x/sync/semaphore"
)

// This private owner budgets retained snapshots, separately from inspection
// scratch/capacity. It is not yet an admitted public Run execution capability.
type debianArchiveOwner struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	capacity *semaphore.Weighted
	closed   bool
	done     chan struct{}
	closeErr error
	leases   map[*debianArchiveLease]struct{}
}

type debianArchiveLease struct {
	owner       *debianArchiveOwner
	limit       int64
	claimed     bool
	producing   bool
	closed      bool
	done        chan struct{}
	closeErr    error
	active      sync.WaitGroup
	calls       map[context.Context]context.CancelFunc
	file        *heldfile.File
	instruction []byte
	input       string
	executor    *debianProducerExecutorState
	subject     string
	digest      string
	size        int64
}

type debianArchiveOperation int

const (
	debianProduce debianArchiveOperation = iota
	debianCapture
	debianRead
)

// DebianProducerArchive retains sealed source-produced bytes awaiting format inspection. Only
// successful private producer completion can issue this handle; no decoder or
// public path/result constructor exists. Shallow copies share revocation.
type DebianProducerArchive struct{ lease *debianArchiveLease }

func newDebianArchiveOwner(ctx context.Context, budget int64) (*debianArchiveOwner, error) {
	if err := requireRunContext(ctx, "Debian archive owner"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening Debian archive owner")
	}
	if budget <= 0 || budget > MaxDebianPackageBytes {
		return nil, e.New(e.CodeEngineCapability, "private Debian retention requires a positive byte budget within the package bound")
	}
	owned, cancel := context.WithCancel(ctx)
	return &debianArchiveOwner{ctx: owned, cancel: cancel, capacity: semaphore.NewWeighted(budget),
		done: make(chan struct{}), leases: make(map[*debianArchiveLease]struct{})}, nil
}

// Reserve the complete capture allowance before producer/source/output I/O.
// Do not wait behind retained payloads: they may live until the Run closes.
func (owner *debianArchiveOwner) reserve(ctx context.Context, limit int64) (*debianArchiveLease, error) {
	if err := requireRunContext(ctx, "Debian archive reservation"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "reserving Debian archive")
	}
	if owner == nil || owner.ctx == nil || owner.capacity == nil || limit <= 0 || limit > MaxDebianPackageBytes {
		return nil, e.New(e.CodeEngineCapability, "Debian archive reservation requires an owner and bounded allowance")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || owner.ctx.Err() != nil || !owner.capacity.TryAcquire(limit) {
		return nil, e.New(e.CodeEngineCapability, "Debian archive owner is closed, canceled or lacks retained-byte capacity")
	}
	lease := &debianArchiveLease{owner: owner, limit: limit, done: make(chan struct{}), calls: make(map[context.Context]context.CancelFunc)}
	owner.leases[lease] = struct{}{}
	return lease, nil
}

func (lease *debianArchiveLease) begin(ctx context.Context, operation debianArchiveOperation) (context.Context, func(), error) {
	if err := requireRunContext(ctx, "retained Debian archive"); err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "using retained Debian archive")
	}
	if lease == nil || lease.owner == nil {
		return nil, nil, e.New(e.CodeEngineCapability, "retained Debian archive has no issuing owner")
	}
	owner := lease.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || lease.closed || owner.ctx.Err() != nil {
		return nil, nil, e.New(e.CodeEngineCapability, "retained Debian archive owner is closed or canceled")
	}
	if len(lease.instruction) == 0 {
		return nil, nil, e.New(e.CodeProvenanceMissing, "Debian producer instruction was not frozen before work")
	}
	if operation != debianRead && lease.claimed {
		return nil, nil, e.New(e.CodeVerifyFailed, "Debian archive reservation was already consumed")
	}
	if operation == debianProduce {
		if lease.producing {
			return nil, nil, e.New(e.CodeVerifyFailed, "Debian archive reservation already has a producer")
		}
		lease.producing = true
	}
	if operation == debianRead && lease.file == nil {
		return nil, nil, e.New(e.CodeProvenanceMissing, "Debian archive has no completed producer capture")
	}
	if operation == debianCapture {
		lease.claimed = true
	}
	owned, cancel := context.WithCancel(ctx)
	lease.calls[owned] = cancel
	lease.active.Add(1)
	stop := context.AfterFunc(owner.ctx, cancel)
	return owned, func() {
		stop()
		cancel()
		owner.mu.Lock()
		delete(lease.calls, owned)
		owner.mu.Unlock()
		lease.active.Done()
	}, nil
}

func (lease *debianArchiveLease) Close() error {
	if lease == nil || lease.owner == nil {
		return nil
	}
	owner := lease.owner
	owner.mu.Lock()
	if lease.closed {
		owner.mu.Unlock()
		<-lease.done
		return lease.closeErr
	}
	lease.closed = true
	for _, cancel := range lease.calls {
		cancel()
	}
	owner.mu.Unlock()
	lease.active.Wait()
	err := lease.file.Close()
	owner.mu.Lock()
	if err != nil {
		lease.closeErr = e.Wrap(e.CodeInternal, err, "closing retained Debian archive")
	}
	lease.file = nil
	delete(owner.leases, lease)
	owner.capacity.Release(lease.limit)
	close(lease.done)
	owner.mu.Unlock()
	return lease.closeErr
}

func (owner *debianArchiveOwner) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		<-owner.done
		return owner.closeErr
	}
	owner.closed = true
	leases := make([]*debianArchiveLease, 0, len(owner.leases))
	for lease := range owner.leases {
		for _, cancel := range lease.calls {
			cancel()
		}
		leases = append(leases, lease)
	}
	owner.cancel()
	owner.mu.Unlock()
	var err error
	for _, lease := range leases {
		err = errors.Join(err, lease.Close())
	}
	owner.mu.Lock()
	owner.closeErr = err
	close(owner.done)
	owner.mu.Unlock()
	return err
}

// Session close cancels every active archive call before publishing its own
// cancellation signal. The later Close joins and releases the same leases.
func (owner *debianArchiveOwner) cancelOperations() {
	if owner == nil {
		return
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	for lease := range owner.leases {
		for _, cancel := range lease.calls {
			cancel()
		}
	}
	owner.cancel()
}

func validateDebianArchiveProducer(node ir.Node) error {
	if node.Kind != ir.KindBuild || node.OutputType != "debian-package" || node.BuilderType != ir.BuilderOCIHermetic ||
		node.Debian == nil || !node.SourceReadOnly || node.WriteDir == "" || node.Requirements != nil || node.ExecutionBinding != nil {
		return e.New(e.CodeEngineCapability, "retained Debian archive requires an isolated explicit source producer")
	}
	return node.Validate()
}

// Freeze before source/runtime I/O, so caller-owned argv, environment, patches
// and target pointers cannot redefine the instruction after execution starts.
func (lease *debianArchiveLease) prepare(ctx context.Context, node ir.Node) (ir.Node, error) {
	if err := requireRunContext(ctx, "preparing retained Debian producer"); err != nil {
		return ir.Node{}, err
	}
	if err := ctx.Err(); err != nil {
		return ir.Node{}, e.Wrap(e.CodeTimeout, err, "preparing retained Debian producer")
	}
	if lease == nil || lease.owner == nil {
		return ir.Node{}, e.New(e.CodeEngineCapability, "Debian producer requires retained-byte ownership")
	}
	if err := validateDebianArchiveProducer(node); err != nil {
		return ir.Node{}, err
	}
	raw, err := canonical.Marshal(node)
	if err != nil {
		return ir.Node{}, e.Wrap(e.CodeInvalidCatalog, err, "freezing Debian producer")
	}
	var frozen ir.Node
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return ir.Node{}, e.Wrap(e.CodeInvalidCatalog, err, "copying frozen Debian producer")
	}
	owner := lease.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || lease.closed || owner.ctx.Err() != nil || lease.producing || lease.claimed || len(lease.instruction) != 0 {
		return ir.Node{}, e.New(e.CodeEngineCapability, "Debian reservation cannot admit another producer instruction")
	}
	lease.instruction = raw
	return frozen, nil
}

// Called inside the actual successful producer owner, never from an exported
// ArtifactResult description. Reserve and validate before opening the output.
func collectDebianProducerArchive(ctx context.Context, node ir.Node, source *materializedSource,
	root, relative string, lease *debianArchiveLease,
) (archive *DebianProducerArchive, returnErr error) {
	if err := requireRunContext(ctx, "collecting produced Debian archive"); err != nil {
		return nil, err
	}
	if err := validateDebianArchiveProducer(node); err != nil {
		return nil, err
	}
	if source == nil || source.Dir == "" || !canonical.IsDigest(source.InputDigest) {
		return nil, e.New(e.CodeProvenanceMissing, "Debian archive requires original prepared source ownership")
	}
	if relative != artifactOutputRelative(node) {
		return nil, e.New(e.CodeVerifyFailed, "Debian archive path differs from the compiled producer output")
	}
	instruction, err := canonical.Marshal(node)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "freezing Debian producer instruction")
	}
	owned, finish, err := lease.begin(ctx, debianCapture)
	if err != nil {
		return nil, err
	}
	defer finish()
	if !bytes.Equal(instruction, lease.instruction) {
		return nil, e.New(e.CodeVerifyFailed, "Debian completion differs from the instruction frozen before work")
	}
	file, before, err := openRegularBeneath(root, relative)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening source-created Debian archive")
	}
	held, captureErr := heldfile.FromReaderContext(owned, file, heldfile.Options{Label: "produced-debian-archive", MaxBytes: lease.limit})
	after, statErr := file.Stat()
	closeErr := file.Close()
	if err = errors.Join(captureErr, statErr, closeErr); err == nil &&
		(!os.SameFile(before, after) || held.Size() <= 0 || held.Size() != before.Size() || after.Size() != before.Size() ||
			after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime())) {
		err = e.New(e.CodeVerifyFailed, "Debian archive changed during producer capture or is empty")
	}
	if err == nil {
		err = owned.Err()
	}
	if err != nil {
		err = errors.Join(err, held.Close())
		if owned.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "capturing produced Debian archive")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "capturing produced Debian archive")
	}
	lease.owner.mu.Lock()
	if lease.owner.closed || lease.closed || lease.owner.ctx.Err() != nil || owned.Err() != nil {
		lease.owner.mu.Unlock()
		return nil, e.Wrap(e.CodeEngineCapability, held.Close(), "Debian archive owner closed during capture")
	}
	lease.file, lease.input = held, source.InputDigest
	lease.digest, lease.size = held.Digest(), held.Size()
	lease.owner.mu.Unlock()
	return &DebianProducerArchive{lease: lease}, nil
}

func (archive *DebianProducerArchive) matches(ctx context.Context, node ir.Node, input string) error {
	if archive == nil {
		return e.New(e.CodeProvenanceMissing, "produced Debian archive is required")
	}
	owned, finish, err := archive.lease.begin(ctx, debianRead)
	if err != nil {
		return err
	}
	defer finish()
	encoded, err := canonical.Marshal(node)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "matching Debian producer instruction")
	}
	if !bytes.Equal(encoded, archive.lease.instruction) || input != archive.lease.input {
		return e.New(e.CodeVerifyFailed, "retained Debian archive differs from producer instruction or prepared source")
	}
	if err := owned.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "matching retained Debian archive")
	}
	return nil
}

func (archive *DebianProducerArchive) WriteToContext(ctx context.Context, destination io.Writer) (int64, error) {
	if archive == nil || destination == nil {
		return 0, e.New(e.CodeUsage, "produced Debian archive and destination are required")
	}
	owned, finish, err := archive.lease.begin(ctx, debianRead)
	if err != nil {
		return 0, err
	}
	defer finish()
	size, err := archive.lease.file.WriteToContext(owned, destination)
	if err == nil {
		err = archive.lease.file.AssertUnchangedContext(owned)
	}
	if err != nil {
		if owned.Err() != nil {
			return size, e.Wrap(e.CodeTimeout, err, "exporting retained Debian archive")
		}
		return size, e.Wrap(e.CodeVerifyFailed, err, "exporting retained Debian archive")
	}
	return size, nil
}

func (archive *DebianProducerArchive) Close() error {
	if archive == nil {
		return nil
	}
	return archive.lease.Close()
}

func (archive *DebianProducerArchive) identity(ctx context.Context) (string, int64, error) {
	if archive == nil {
		return "", 0, e.New(e.CodeProvenanceMissing, "produced Debian archive is required")
	}
	_, finish, err := archive.lease.begin(ctx, debianRead)
	if err != nil {
		return "", 0, err
	}
	defer finish()
	return archive.lease.digest, archive.lease.size, nil
}

// MatchesProducerContext compares a description with the original retained IR
// and prepared source binding. It cannot issue or restore a producer handle.
func (archive *DebianProducerArchive) MatchesProducerContext(ctx context.Context, node ir.Node, inputDigest string) error {
	return archive.matches(ctx, node, inputDigest)
}
