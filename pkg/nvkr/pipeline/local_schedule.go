// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"fmt"
	"io"
	"sync"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// progressSink keeps short lifecycle messages live even while each node's detailed
// log remains privately buffered for non-interleaved output. The scheduler and
// concurrently running build nodes share it, so every write is serialized.
type progressSink struct {
	mu sync.Mutex
	w  io.Writer
}

func (p *progressSink) printf(format string, args ...any) {
	if p == nil || p.w == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.w, format, args...)
}

// selectNodes computes the set of node IDs to execute. With no Only filter every
// node runs; otherwise only the named build components, the fetch nodes they need,
// and the terminal verify node.
func selectNodes(nodes []ir.Node, only []string) (map[string]bool, error) {
	included := map[string]bool{}
	if len(only) == 0 {
		for _, n := range nodes {
			included[n.ID] = true
		}
		return included, nil
	}
	want := map[string]bool{}
	for _, c := range only {
		want[c] = true
	}
	found := map[string]bool{}
	for _, n := range nodes {
		if (n.Kind == ir.KindBuild || n.Kind == ir.KindPull) && want[n.Component] {
			found[n.Component] = true
			included[n.ID] = true
			for _, dep := range n.Needs {
				included[dep] = true
			}
		}
		if n.Kind == ir.KindVerify {
			included[n.ID] = true
		}
	}
	for c := range want {
		if !found[c] {
			return nil, e.New(e.CodeUnsupported, "--only names component %q not present in the build graph", c)
		}
	}
	return included, nil
}
