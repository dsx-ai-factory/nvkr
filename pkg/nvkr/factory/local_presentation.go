// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// localNodeLabel is a display projection only. The Plan and recorded node IDs
// remain unchanged, and verbose output retains the exact operation identifier.
func localNodeLabel(plan *Plan, node Node, verbose bool) string {
	if verbose {
		return node.ID + " (" + node.Kind + ")"
	}
	label := node.Kind
	if node.IRNode != "" {
		build, instruction, err := plan.producerInstruction(node)
		if err == nil {
			switch instruction.Kind {
			case ir.KindFetch:
				source := strings.TrimSuffix(path.Base(instruction.Repo), ".git")
				tag := ""
				for _, component := range build.ResolvedComponents {
					if component.Repo == instruction.Repo && component.Commit == instruction.Commit && component.TreeHash == instruction.TreeHash {
						if tag != "" && tag != component.Tag {
							tag = ""
							break
						}
						tag = component.Tag
					}
				}
				if tag != "" {
					source += "@" + tag
				}
				label = "fetch " + source
				if len(instruction.VendorCmd) > 0 {
					label += " (including vendor dependencies)"
				}
			case ir.KindBuild:
				label = "build " + instruction.Component
				if instruction.SourceDest != "" {
					label += " (including source snapshot)"
				}
			case ir.KindPull:
				label = "acquire " + instruction.Component
			}
		}
	}
	if node.BuildTarget != "" {
		return node.BuildTarget + ": " + label
	}
	// Utility IDs name the tool without source digests.
	return node.ID + ": " + label
}

// startLocalHeartbeat owns only a timer, never execution. Its caller supplies
// the same serialized writer as the operation and must stop/join before return.
func startLocalHeartbeat(ctx context.Context, out io.Writer, label string, interval time.Duration) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				fmt.Fprintf(out, "[working] %s…\n", label)
			}
		}
	}()
	return sync.OnceFunc(func() { close(stop); <-done })
}
