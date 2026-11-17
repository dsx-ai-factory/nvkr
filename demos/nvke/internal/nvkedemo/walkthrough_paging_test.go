// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type walkthroughPageBuffer struct {
	bytes.Buffer
	pages  []OperatorStatus
	cancel context.CancelFunc
}

func (*walkthroughPageBuffer) OperatorStatusPageDuration() time.Duration { return time.Millisecond }
func (out *walkthroughPageBuffer) WriteOperatorStatus(status OperatorStatus) error {
	out.pages = append(out.pages, status)
	_, _ = out.WriteString(status.Text())
	if out.cancel != nil {
		out.cancel()
	}
	return nil
}

func TestWalkthroughPagingBoundsAndCancellation(t *testing.T) {
	for _, scenario := range []string{"complete", "cancel", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			out := &walkthroughPageBuffer{}
			if scenario == "cancel" {
				out.cancel = cancel
			}
			text := "Site / Kind management\n$ kubectl --kubeconfig <site> get pods -A -o json\nNAMESPACE POD PHASE READY RESTARTS\n"
			for i := 0; i < 52; i++ {
				text += fmt.Sprintf("default pod-%03d Running 1/1 0\n", i)
			}
			if scenario == "oversize" {
				text = strings.Repeat("x", (64<<10)+1)
			}
			err := writeWalkthroughPages(ctx, out, text)
			if scenario == "cancel" {
				if !errors.Is(err, context.Canceled) || len(out.pages) != 1 {
					t.Fatal("canceled paging continued", err, len(out.pages))
				}
				return
			}
			if scenario == "oversize" {
				if err == nil || len(out.pages) != 0 {
					t.Fatal("unbounded paging emitted", err)
				}
				return
			}
			if err != nil || len(out.pages) < 4 {
				t.Fatal("missing pages", err, len(out.pages))
			}
			for _, page := range out.pages {
				if !page.Walkthrough() || strings.Count(page.Text(), "\n") > 15 || !strings.Contains(page.Text(), "get pods -A") {
					t.Fatal("page lost owner kind, query or row bound", page.Text())
				}
			}
			for i := 0; i < 52; i++ {
				if !strings.Contains(out.String(), fmt.Sprintf("pod-%03d", i)) {
					t.Fatal("pod observation lost", i)
				}
			}
		})
	}
}
