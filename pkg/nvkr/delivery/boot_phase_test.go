// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func bootPhaseRequestFixture(t *testing.T, selection definition.BootSelection, limits BootTransferLimits, origin string) BootPhaseRequest {
	t.Helper()
	destination := definition.ObjectStoreDestination{Name: "boot", Mode: definition.ObjectStoreBootMode, Provider: "s3",
		S3:       &definition.S3ObjectStoreDestination{Region: "us-east-1", Bucket: "synthetic-boot", Prefix: "selected", ExpectedBucketOwner: "123456789012", RoleRef: "github:BOOT_ROLE"},
		Serving:  &definition.BootServing{Profile: definition.BootServingProfile, Origin: origin, Prefix: "/boot", ReaderMode: "anonymous"},
		Transfer: definition.ObjectStoreTransfer{MaxObjectBytes: uint64(limits.Storage.MaxObjectBytes), MaxTotalBytes: uint64(limits.Storage.MaxTotalBytes), MaxObjects: uint64(limits.Storage.MaxObjects), OperationTimeoutSeconds: 60}}
	minimum, err := BootPhaseMinimumReadBytes(selection, destination)
	if err != nil {
		t.Fatal(err)
	}
	return BootPhaseRequest{Selection: selection, Destination: destination, Limits: limits, ReadBytes: minimum}
}

func TestBootPhaseRejectsForeignContextsAndJoinsUnfinishedWork(t *testing.T) {
	selection, _, _, limits := bootTransferFixture(t)
	request := bootPhaseRequestFixture(t, selection, limits, "https://boot.example.test")
	store := &bootClosingStore{mirrorMemoryStore: &mirrorMemoryStore{}}
	joined := make(chan struct{})
	err := withBootPhase(t.Context(), request, BootReaderCredential{}, func(context.Context) (ObjectStore, error) { return store, nil }, func(ctx context.Context, phase *BootPhase) error {
		foreign, _, err := readbudget.WithLimit(t.Context(), request.ReadBytes)
		if err != nil {
			return err
		}
		foreign, cancel := context.WithTimeout(foreign, time.Second)
		defer cancel()
		_, _, err = phase.begin(foreign)
		assertRegistryCode(t, err, e.CodeGateUnmet)
		_, _, err = phase.begin(context.WithoutCancel(ctx))
		assertRegistryCode(t, err, e.CodeGateUnmet)
		_, finish, err := phase.begin(ctx)
		if err != nil {
			return err
		}
		go func() {
			<-ctx.Done()
			close(joined)
			finish(e.New(e.CodeTimeout, "synthetic unfinished operation canceled"))
		}()
		return nil
	})
	assertRegistryCode(t, err, e.CodeGateUnmet)
	if !store.closed {
		t.Fatal("unfinished phase did not close its store")
	}
	select {
	case <-joined:
	default:
		t.Fatal("phase returned without joining canceled operation")
	}
}

func TestBootPhaseHandledFailureStillFailsCompletion(t *testing.T) {
	selection, _, _, limits := bootTransferFixture(t)
	request := bootPhaseRequestFixture(t, selection, limits, "https://boot.example.test")
	store := &bootClosingStore{mirrorMemoryStore: &mirrorMemoryStore{}}
	err := withBootPhase(t.Context(), request, BootReaderCredential{}, func(context.Context) (ObjectStore, error) { return store, nil }, func(ctx context.Context, phase *BootPhase) error {
		_, err := phase.Copy(ctx, nil)
		assertRegistryCode(t, err, e.CodeEngineCapability)
		_, err = phase.Copy(ctx, nil)
		assertRegistryCode(t, err, e.CodeEngineCapability)
		return nil
	})
	assertRegistryCode(t, err, e.CodeEngineCapability)
	if !store.closed || len(store.writes) != 0 {
		t.Fatal("failed phase leaked store or wrote payloads")
	}
}

type bootClosingStore struct {
	*mirrorMemoryStore
	closed    bool
	failClose bool
}

func (store *bootClosingStore) Close() error {
	store.closed = true
	if store.failClose {
		return e.New(e.CodeUnavailable, "synthetic cleanup failure")
	}
	return nil
}

func TestBootPhaseFreshCounterRevocationAndCleanup(t *testing.T) {
	selection, assets, _, limits := bootTransferFixture(t)
	for _, failClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cleanup failure"}[failClose], func(t *testing.T) {
			store := &bootClosingStore{mirrorMemoryStore: &mirrorMemoryStore{}, failClose: failClose}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, found := store.data[strings.TrimPrefix(r.URL.Path, "/boot/")]
				if !found {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			original := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = original }()
			request := bootPhaseRequestFixture(t, selection, limits, server.URL)
			var previous *readbudget.Budget
			for range 2 {
				store.closed = false
				var receipt *BootAvailability
				var escaped *BootPhase
				var escapedContext context.Context
				err := withBootPhase(t.Context(), request, BootReaderCredential{}, func(ctx context.Context) (ObjectStore, error) {
					budget, err := readbudget.Require(ctx)
					if err != nil {
						return nil, err
					}
					if budget == previous || budget.Used() != 0 {
						t.Error("phase reused a previous response counter")
					}
					previous = budget
					return store, nil
				}, func(ctx context.Context, phase *BootPhase) error {
					escaped, escapedContext = phase, ctx
					var err error
					receipt, err = phase.Copy(ctx, assets)
					if err != nil {
						return err
					}
					records, err := receipt.Descriptions()
					if err != nil || len(records) != len(assets) {
						t.Fatalf("no live byte observation: %v", err)
					}
					records[0].URL = "foreign"
					again, err := receipt.Descriptions()
					if err != nil || again[0].URL == "foreign" {
						t.Fatal("receipt exposed mutable state")
					}
					return nil
				})
				if failClose {
					assertRegistryCode(t, err, e.CodeUnavailable)
				} else if err != nil {
					t.Fatal(err)
				}
				if !store.closed {
					t.Fatal("phase did not close its store")
				}
				_, err = receipt.Descriptions()
				assertRegistryCode(t, err, e.CodeGateUnmet)
				_, err = escaped.Copy(escapedContext, assets)
				assertRegistryCode(t, err, e.CodeTimeout)
			}
		})
	}
}

func TestBootPhaseRejectsInsufficientLimitsBeforeStore(t *testing.T) {
	selection, _, _, limits := bootTransferFixture(t)
	for _, tc := range []struct {
		name string
		edit func(*BootPhaseRequest)
		code e.Code
	}{
		{"read allowance", func(r *BootPhaseRequest) { r.ReadBytes-- }, e.CodeEngineCapability},
		{"negative read allowance", func(r *BootPhaseRequest) { r.ReadBytes = -1 }, e.CodeEngineCapability},
		{"overflow", func(r *BootPhaseRequest) { r.Destination.Transfer.MaxTotalBytes = math.MaxInt64 }, e.CodeEngineCapability},
		{"control count", func(r *BootPhaseRequest) { r.Limits.ControlObjects = 1 }, e.CodeEngineCapability},
		{"foreign limits", func(r *BootPhaseRequest) { r.Limits.Storage.MaxTotalBytes++ }, e.CodeVerifyFailed},
		{"full mirror", func(r *BootPhaseRequest) {
			r.Destination.Mode = definition.ObjectStoreFullMirrorMode
			r.Destination.Serving = nil
		}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := bootPhaseRequestFixture(t, selection, limits, "https://boot.example.test")
			tc.edit(&request)
			calls := 0
			err := withBootPhase(t.Context(), request, BootReaderCredential{}, func(context.Context) (ObjectStore, error) { calls++; return &mirrorMemoryStore{}, nil }, func(context.Context, *BootPhase) error { calls++; return nil })
			assertRegistryCode(t, err, tc.code)
			if calls != 0 {
				t.Fatal("invalid phase performed work")
			}
		})
	}
}
