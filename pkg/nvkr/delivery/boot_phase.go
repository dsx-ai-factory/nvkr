// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// BootPhaseRequest is a revalidated projection of one compiled destination and
// one phase's limits. Lifecycle owns signer, input and complete graph admission
// before opening the phase. This type does not authorize a destination or policy.
type BootPhaseRequest struct {
	Selection   definition.BootSelection          `json:"selection"`
	Destination definition.ObjectStoreDestination `json:"destination"`
	Limits      BootTransferLimits                `json:"limits"`
	ReadBytes   int64                             `json:"readBytes"`
}

// BootPhaseMinimumReadBytes reserves conservative mandatory network passes:
// two complete source acquisitions at the per-object ceiling, three passes of
// each control/proof pool, both destination reads, inventory readback, and 32 MiB of trust-metadata response headroom.
// Retry/error responses debit the same counter; callers may declare a larger
// finite allowance. This calculation is deterministic and performs no I/O.
func BootPhaseMinimumReadBytes(selection definition.BootSelection, destination definition.ObjectStoreDestination) (int64, error) {
	if err := selection.Validate(); err != nil {
		return 0, err
	}
	if err := destination.Validate(); err != nil {
		return 0, err
	}
	if destination.Mode != definition.ObjectStoreBootMode {
		return 0, e.New(e.CodeEngineCapability, "boot phase requires an explicit selective destination")
	}
	count := uint64(len(selection.Disks) + len(selection.Imagers))
	limits := destination.EffectiveTransfer()
	control := (count+1)*uint64(maxMetadataBytes) + uint64(maxMirrorProofBytes)
	// Source objects are individually bounded at 64 GiB and selection at 1026.
	fixed := 2*count*limits.MaxObjectBytes + 3*control + uint64(maxMirrorInventoryBytes) + 32<<20
	if fixed > math.MaxInt64 || limits.MaxTotalBytes > (math.MaxInt64-fixed)/2 {
		return 0, e.New(e.CodeEngineCapability, "boot phase read reservation exceeds finite accounting range")
	}
	return int64(fixed + 2*limits.MaxTotalBytes), nil
}

func (request BootPhaseRequest) Validate() error {
	minimum, err := BootPhaseMinimumReadBytes(request.Selection, request.Destination)
	if err != nil {
		return err
	}
	if request.ReadBytes < minimum {
		return e.New(e.CodeEngineCapability, "boot phase response allowance cannot cover mandatory passes")
	}
	transfer := request.Destination.EffectiveTransfer()
	want := MirrorLimits{MaxObjectBytes: int64(transfer.MaxObjectBytes), MaxTotalBytes: int64(transfer.MaxTotalBytes), MaxObjects: int(transfer.MaxObjects)}
	if request.Limits.Storage != want {
		return e.New(e.CodeVerifyFailed, "boot phase storage limits differ from its exact destination")
	}
	count := len(request.Selection.Disks) + len(request.Selection.Imagers)
	reserved := int64(count+1)*maxMetadataBytes + maxMirrorProofBytes + maxMirrorInventoryBytes
	// Each original root needs its index, manifest, config and two proof blobs;
	// every selected output additionally needs manifest/config in both roots.
	if request.Limits.ControlObjects < 5*(count+1)+4*count ||
		request.Limits.ControlObjects > want.MaxObjects-count-1 || reserved >= want.MaxTotalBytes || want.MaxObjectBytes < maxMirrorInventoryBytes {
		return e.New(e.CodeEngineCapability, "boot phase cannot reserve its complete control/proof/inventory closure")
	}
	return nil
}

// BootPhase owns one fresh operation/session, response counter and independent
// serving reader. It exists only during WithBootPhase's synchronous callback.
// Shallow copies share revocation, and cleanup cancels and joins active work.
// Operation failure is sticky even when the callback handles the returned error;
// retry requires a fresh phase and cannot revive an earlier availability receipt.
type BootPhase struct{ state *bootPhaseState }

type bootPhaseState struct {
	mu      sync.Mutex
	closed  bool
	pending int
	failure error
	active  sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
	budget  *readbudget.Budget
	request BootPhaseRequest
	store   ObjectStore
	reader  *bootServingClient
}

// BootAvailability is an in-process observation of exact raw bytes at one
// destination and serving mapping. It grants no signer, aggregate membership,
// freshness or boot authority. Transported descriptions cannot recreate it.
type BootAvailability struct {
	owner   *bootPhaseState
	records []BootStoredAsset
}

// Descriptions returns detached transport records while the issuing phase is
// live. Lifecycle binds its own stronger preliminary receipt to the same owner.
func (receipt *BootAvailability) Descriptions() ([]BootStoredAsset, error) {
	if receipt == nil || receipt.owner == nil {
		return nil, e.New(e.CodeGateUnmet, "boot availability was not issued")
	}
	receipt.owner.mu.Lock()
	defer receipt.owner.mu.Unlock()
	if receipt.owner.closed || receipt.owner.ctx.Err() != nil {
		return nil, e.New(e.CodeGateUnmet, "boot availability phase ended")
	}
	if receipt.owner.failure != nil {
		return nil, receipt.owner.failure
	}
	if err := receipt.owner.budget.Check(); err != nil {
		return nil, err
	}
	return slices.Clone(receipt.records), nil
}

// WithBootPhase admits all numeric/configuration bounds before opening a fresh
// S3 session. Runtime options must match the destination exactly; only runtime
// credentials and provider tuning are supplied separately. The callback must
// authenticate sources using its supplied metered context. Success includes
// cleanup; callers must not publish completion from inside the callback.
func WithBootPhase(ctx context.Context, request BootPhaseRequest, options S3ObjectStoreOptions, credential BootReaderCredential,
	work func(context.Context, *BootPhase) error,
) error {
	if err := request.Validate(); err != nil {
		return err
	}
	s3 := request.Destination.S3
	timeout := time.Duration(request.Destination.EffectiveTransfer().OperationTimeoutSeconds) * time.Second
	if options.Region != s3.Region || options.Endpoint != s3.Endpoint || options.Bucket != s3.Bucket || options.Prefix != s3.Prefix ||
		options.ExpectedBucketOwner != s3.ExpectedBucketOwner || options.Addressing != s3.Addressing || options.OperationTimeout != timeout {
		return e.New(e.CodeVerifyFailed, "boot phase runtime destination differs from its admitted configuration")
	}
	if options.CleanupTimeout != 0 && options.CleanupTimeout != time.Minute {
		return e.New(e.CodeEngineCapability, "boot phase requires the admitted one-minute cleanup reservation")
	}
	if _, err := admitObjectOptions(options); err != nil {
		return err
	}
	return withBootPhase(ctx, request, credential, func(ctx context.Context) (ObjectStore, error) { return newS3ObjectStore(ctx, options) }, work)
}

func withBootPhase(ctx context.Context, request BootPhaseRequest, credential BootReaderCredential,
	open func(context.Context) (ObjectStore, error), work func(context.Context, *BootPhase) error,
) (resultErr error) {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if open == nil || work == nil {
		return e.New(e.CodeUsage, "boot phase requires its store and operation")
	}
	request.Selection = request.Selection.Clone()
	s3, serving := *request.Destination.S3, *request.Destination.Serving
	request.Destination.S3, request.Destination.Serving = &s3, &serving
	timeout := time.Duration(request.Destination.EffectiveTransfer().OperationTimeoutSeconds) * time.Second
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	bounded, budget, err := readbudget.WithLimit(bounded, request.ReadBytes)
	if err != nil {
		return err
	}
	reader, err := newBootServingClient(bounded, serving, credential, timeout)
	if err != nil {
		return err
	}
	state := &bootPhaseState{ctx: bounded, cancel: cancel, budget: budget, request: request, reader: reader}
	defer func() {
		state.mu.Lock()
		if state.pending != 0 {
			resultErr = errors.Join(resultErr, e.New(e.CodeGateUnmet, "boot phase callback returned with unfinished operations"))
		}
		state.closed = true
		state.cancel()
		state.mu.Unlock()
		reader.close()
		if state.store != nil {
			resultErr = errors.Join(resultErr, state.store.Close())
		}
		state.active.Wait()
		state.mu.Lock()
		if state.failure != nil && !errors.Is(resultErr, state.failure) {
			resultErr = errors.Join(resultErr, state.failure)
		}
		state.mu.Unlock()
		resultErr = errors.Join(resultErr, budget.Check())
	}()
	state.store, err = open(bounded)
	if err != nil {
		return err
	}
	if state.store == nil {
		return e.New(e.CodeEngineCapability, "boot phase has no owned store")
	}
	if err := work(bounded, &BootPhase{state: state}); err != nil {
		return err
	}
	return deliveryContext(bounded)
}

func (phase *BootPhase) begin(ctx context.Context) (*bootPhaseState, func(error), error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, nil, err
	}
	if phase == nil || phase.state == nil {
		return nil, nil, e.New(e.CodeGateUnmet, "boot phase was not issued")
	}
	state := phase.state
	state.mu.Lock()
	defer state.mu.Unlock()
	budget, err := readbudget.Require(ctx)
	if err != nil {
		return nil, nil, err
	}
	if state.closed || state.ctx.Err() != nil || budget != state.budget {
		return nil, nil, e.New(e.CodeGateUnmet, "boot operation is outside its issuing phase")
	}
	if state.failure != nil {
		return nil, nil, state.failure
	}
	deadline, bounded := ctx.Deadline()
	limit, _ := state.ctx.Deadline()
	if !bounded || deadline.After(limit) {
		return nil, nil, e.New(e.CodeGateUnmet, "boot operation lost its admitted phase deadline")
	}
	state.pending++
	state.active.Add(1)
	return state, func(err error) {
		state.mu.Lock()
		if state.failure == nil {
			state.failure = err
		} else if err != nil && !errors.Is(state.failure, err) {
			state.failure = errors.Join(state.failure, err)
		}
		state.pending--
		state.mu.Unlock()
		state.active.Done()
	}, nil
}

// Copy borrows authenticated raw owners; lifecycle must authenticate them before
// this byte-only operation. No metadata, archive or unselected payload is copied.
func (phase *BootPhase) Copy(ctx context.Context, assets []*BootAsset) (_ *BootAvailability, resultErr error) {
	state, finish, err := phase.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { finish(resultErr) }()
	records, err := copyBootAssets(ctx, state.store, state.reader, state.request.Selection, assets, state.request.Limits)
	if err != nil {
		return nil, err
	}
	return &BootAvailability{owner: state, records: records}, nil
}

// Recheck repeats all raw-byte reads in this fresh phase. Expected bindings must
// come from independently authenticated original metadata, never the inventory.
func (phase *BootPhase) Recheck(ctx context.Context, expected []BootAssetBinding, records []BootStoredAsset) (_ *BootAvailability, resultErr error) {
	state, finish, err := phase.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { finish(resultErr) }()
	if err := recheckBootAssets(ctx, state.store, state.reader, state.request.Selection, expected, records, state.request.Limits); err != nil {
		return nil, err
	}
	return &BootAvailability{owner: state, records: slices.Clone(records)}, nil
}
