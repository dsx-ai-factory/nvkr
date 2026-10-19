// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublicationCloseJoinsEvidenceVisitor(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
	}{
		{name: "callback returns"},
		{name: "callback observes cancellation", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, _ := releasePublicationFixture(t)
			destination := filepath.Join(t.TempDir(), "attested")
			result, err := AttestPreparedEvidence(t.Context(), root, destination, func(_ context.Context, subject string, document []byte) ([]byte, error) {
				return imageIndexFixtureAttestation(subject, document)
			})
			if err != nil {
				t.Fatal(err)
			}
			publication, err := OpenPublication(t.Context(), destination, result.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			copyOf := *publication
			held := slices.Clone(publication.files)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			entered, release := make(chan struct{}), make(chan struct{})
			visitReturned, closeReturned := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			var visitErr, closeErr error
			closeStarted := false
			wait := func(done <-chan struct{}, label string) bool {
				select {
				case <-done:
					return true
				case <-time.After(5 * time.Second):
					t.Errorf("timed out waiting for %s", label)
					return false
				}
			}
			t.Cleanup(func() {
				cancel()
				releaseOnce.Do(func() { close(release) })
				if !wait(visitReturned, "evidence visit cleanup") {
					return
				}
				if closeStarted && !wait(closeReturned, "publication close cleanup") {
					return
				}
				if err := publication.Close(); err != nil {
					t.Error(err)
				}
			})
			go func() {
				defer close(visitReturned)
				visitErr = publication.VisitEvidence(ctx, func(ctx context.Context, _ EvidenceBinding, _ []byte) error {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return deliveryContext(ctx)
					}
				})
			}()
			if !wait(entered, "evidence callback") {
				t.FailNow()
			}
			closing := make(chan struct{})
			closeStarted = true
			go func() {
				defer close(closeReturned)
				close(closing)
				closeErr = copyOf.Close()
			}()
			if !wait(closing, "concurrent Close") {
				t.FailNow()
			}
			select {
			case <-closeReturned:
				t.Fatalf("Close returned before the active callback: %v", closeErr)
			case <-time.After(20 * time.Millisecond):
			}
			if test.cancel {
				cancel()
			} else {
				releaseOnce.Do(func() { close(release) })
			}
			if !wait(visitReturned, "evidence visit") || !wait(closeReturned, "publication Close") {
				t.FailNow()
			}
			if test.cancel {
				assertRegistryCode(t, visitErr, e.CodeTimeout)
				if !errors.Is(visitErr, context.Canceled) {
					t.Fatalf("visitor lost cancellation cause: %v", visitErr)
				}
			} else if visitErr != nil {
				t.Fatal(visitErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			for _, file := range held {
				if file.OSFile() != nil {
					t.Fatal("Close leaked held publication bytes")
				}
			}
			for _, handle := range []*Publication{publication, &copyOf} {
				assertRegistryCode(t, handle.VisitEvidence(t.Context(), func(context.Context, EvidenceBinding, []byte) error {
					t.Error("closed publication admitted an evidence callback")
					return nil
				}), e.CodeGateUnmet)
				if err := handle.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
