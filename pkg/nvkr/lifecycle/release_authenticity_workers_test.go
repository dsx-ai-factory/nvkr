// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestAuthenticateOutputsBoundOrderAndJoin(t *testing.T) {
	for _, kind := range []string{"success", "failure", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				outputs := make([]delivery.PublicationOutput, 40)
				for i := range outputs {
					outputs[i].Reference = strconv.Itoa(i)
					outputs[i].Name = fmt.Sprint(i)
				}
				var active, peak atomic.Int32
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if kind == "canceled" {
					cancel()
				}
				artifacts, err := authenticateReleaseOutputs(ctx, outputs, func(ctx context.Context, ref string) error {
					n := active.Add(1)
					defer active.Add(-1)
					for old := peak.Load(); n > old; old = peak.Load() {
						if peak.CompareAndSwap(old, n) {
							break
						}
					}
					i, _ := strconv.Atoi(ref)
					if kind == "failure" && i == 3 {
						return e.New(e.CodeSignatureInvalid, "later subject")
					}
					if kind == "failure" && i > 3 {
						<-ctx.Done()
						return authenticityContext(ctx)
					}
					select {
					case <-ctx.Done():
						return authenticityContext(ctx)
					case <-time.After(time.Duration(20-i%16) * time.Millisecond):
					}
					if kind == "failure" && i == 1 {
						return e.New(e.CodeUntrustedSigner, "earlier subject")
					}
					return nil
				})
				if active.Load() != 0 || peak.Load() > authenticityConcurrency {
					t.Fatalf("active=%d peak=%d", active.Load(), peak.Load())
				}
				switch kind {
				case "success":
					if err != nil || len(artifacts) != len(outputs) || peak.Load() != authenticityConcurrency {
						t.Fatalf("artifacts=%d peak=%d error=%v", len(artifacts), peak.Load(), err)
					}
					for i := range artifacts {
						if artifacts[i].Name != fmt.Sprint(i) {
							t.Fatal("result order changed")
						}
					}
				case "failure":
					assertCode(t, err, e.CodeUntrustedSigner)
				case "canceled":
					assertCode(t, err, e.CodeTimeout)
				}
				if err != nil && artifacts != nil {
					t.Fatal("failure returned partial authority")
				}
			})
		})
	}
}
