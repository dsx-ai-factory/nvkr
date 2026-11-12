// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"debug/elf"
	"runtime"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/diskinspect"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// AuthenticateResultContext checks recorded metadata against the original
// concrete producer result. Matching executable bytes alone cannot authorize a
// substituted result record or a different evidence claim.
func (tool *ProducedExecutable) AuthenticateResultContext(ctx context.Context, result ArtifactResult) error {
	if _, err := tool.BindingContext(ctx); err != nil {
		return err
	}
	s := tool.state
	if err := lockDiskInspection(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if s.file == nil || s.report == nil || len(s.report.Artifacts) != 1 {
		return e.New(e.CodeVerifyFailed, "produced executable result receipt is closed or absent")
	}
	want, err := canonical.Marshal(s.report.Artifacts[0])
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "encoding original executable result")
	}
	got, err := canonical.Marshal(result)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "encoding observed executable result")
	}
	if !bytes.Equal(got, want) {
		return e.New(e.CodeVerifyFailed, "executable result differs from original production")
	}
	return nil
}

// ProducedExecutable holds authenticated static tool bytes from an exact graph
// producer. It grants consumption of those bytes, never qualification, Run
// completeness or publication. The factory owns the Plan/consumer/evidence join.
// Copies share state, including revocation; no path or digest constructor exists.
type ProducedExecutable struct{ state *producedExecutableState }

type producedExecutableState struct {
	mu          sync.Mutex
	file        *heldfile.File
	buildDigest string
	platform    string
	component   string
	report      *Report
	terminal    terminalVerificationReceipt
}

// ProducedExecutableBinding is descriptive. It cannot recreate a capability.
type ProducedExecutableBinding struct {
	BuildDigest    string `json:"buildDigest"`
	Platform       string `json:"platform"`
	Component      string `json:"component"`
	Digest         string `json:"digest"`
	Size           int64  `json:"size"`
	TerminalDigest string `json:"terminalDigest"`
}

// ExecuteProducedExecutable executes one producer from the complete admitted
// graph and retains its exact executable bytes. Source preparation remains an
// explicit graph dependency authenticated by ExecNode. Only this concrete
// execution boundary can issue the capability; recorded result files cannot.
func (l Local) ExecuteProducedExecutable(graph *ir.Graph, buildDigest, component string, opts RunOptions) (*ArtifactResult, *ProducedExecutable, error) {
	producer, err := admitProducedExecutable(opts.Ctx, graph, buildDigest, component)
	if err != nil {
		return nil, nil, err
	}
	if !opts.Launch || opts.DryRun || opts.Qualification != nil || opts.Package {
		return nil, nil, e.New(e.CodeEngineCapability, "produced executable requires launched unqualified native production")
	}
	result, err := l.ExecNode(*producer, opts)
	if err != nil {
		return nil, nil, err
	}
	if result == nil {
		return nil, nil, e.New(e.CodeVerifyFailed, "tool producer returned no artifact")
	}
	tool, err := captureExecutedTool(opts.Ctx, graph.Platform, buildDigest, *producer, *result, withDefaults(opts))
	if err != nil {
		return nil, nil, err
	}
	return result, tool, nil
}

func admitProducedExecutable(ctx context.Context, graph *ir.Graph, buildDigest, component string) (*ir.Node, error) {
	if err := requireRunContext(ctx, "produced executable capture"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "produced executable capture canceled")
	}
	if err := validateGraphIdentity(graph, buildDigest); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" || graph.Platform != "linux/"+runtime.GOARCH {
		return nil, e.New(e.CodeEngineCapability, "produced executable requires its native Linux host")
	}
	return selectProducedExecutableProducer(graph, component)
}

// ValidateProducedExecutableProducer admits the result form without launching
// work or requiring the producer's native host. Compilation and restored Plan
// validation share the concrete executor's regular-file boundary.
func ValidateProducedExecutableProducer(graph *ir.Graph, buildDigest, component string) error {
	if err := validateGraphIdentity(graph, buildDigest); err != nil {
		return err
	}
	_, err := selectProducedExecutableProducer(graph, component)
	return err
}

func selectProducedExecutableProducer(graph *ir.Graph, component string) (*ir.Node, error) {
	var producer *ir.Node
	for i := range graph.Nodes {
		node := &graph.Nodes[i]
		if node.Component == component && (node.Kind == ir.KindBuild || node.Kind == ir.KindPull) {
			if producer != nil {
				return nil, e.New(e.CodeVerifyFailed, "tool component %q has ambiguous producers", component)
			}
			producer = node
		}
	}
	if producer == nil || (producer.OutputType != "host-package" && producer.OutputType != ir.OutputFile) {
		return nil, e.New(e.CodeEngineCapability, "tool %q requires a selected regular-file producer", component)
	}
	return producer, nil
}

// The result is from the concrete executor above, never a caller description.
func captureExecutedTool(ctx context.Context, platform, buildDigest string, producer ir.Node, observed ArtifactResult, opts RunOptions) (*ProducedExecutable, error) {
	result, err := authenticateConsumedResultMetadata(producer, observed)
	if err != nil {
		return nil, err
	}
	if result.Component != producer.Component || result.OutputType != producer.OutputType || result.AbsPath == "" || result.Size <= 0 || result.Size > maxPinnedToolBytes {
		return nil, e.New(e.CodeVerifyFailed, "tool producer returned an invalid or unbounded artifact")
	}
	file, err := heldfile.OpenContext(ctx, result.AbsPath, heldfile.Options{Label: "produced-executable", Executable: true, MaxBytes: maxPinnedToolBytes, ExpectedDigest: result.SHA256})
	if err != nil {
		return nil, mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing produced executable"), "produced executable capture")
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = file.Close()
		}
	}()
	if file.Size() != result.Size {
		return nil, e.New(e.CodeVerifyFailed, "produced executable size changed during capture")
	}
	image, err := elf.NewFile(file)
	if err != nil || !diskinspect.NativeELF(image) || image.Class != elf.ELFCLASS64 || image.Data != elf.ELFDATA2LSB ||
		(image.OSABI != elf.ELFOSABI_NONE && image.OSABI != elf.ELFOSABI_LINUX) {
		return nil, e.New(e.CodeEngineCapability, "produced tool is not a native Linux ELF executable")
	}
	entryMapped := false
	for _, segment := range image.Progs {
		if segment.Type == elf.PT_INTERP {
			return nil, e.New(e.CodeEngineCapability, "produced tool requires a dynamic interpreter")
		}
		if segment.Type == elf.PT_LOAD && segment.Flags&elf.PF_X != 0 && image.Entry >= segment.Vaddr && image.Entry-segment.Vaddr < segment.Memsz {
			entryMapped = true
		}
	}
	if !entryMapped {
		return nil, e.New(e.CodeEngineCapability, "produced tool lacks a mapped executable entry point")
	}
	libraries, err := image.ImportedLibraries()
	if err != nil || len(libraries) != 0 {
		return nil, e.New(e.CodeEngineCapability, "produced tool requires dynamic libraries or has malformed linkage")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "produced executable capture canceled")
	}
	// Detach nested descriptions before binding them into the existing terminal
	// receipt family. This is one selected producer, not full Run completion.
	raw, err := canonical.Marshal(result)
	if err != nil {
		return nil, err
	}
	var retained ArtifactResult
	if err := canonical.UnmarshalExact(raw, &retained); err != nil {
		return nil, err
	}
	retained.AbsPath = result.AbsPath
	report := &Report{BuildDigest: buildDigest, Artifacts: []ArtifactResult{retained},
		Nodes: []NodeResult{{ID: producer.ID, Kind: producer.Kind, Status: "ok"}}}
	terminal, err := issueTerminalReceipt(ctx, report, map[string]ir.Node{producer.ID: producer}, map[string]bool{producer.ID: true}, opts)
	if err != nil {
		return nil, err
	}
	accepted = true
	return &ProducedExecutable{state: &producedExecutableState{file: file, buildDigest: buildDigest, platform: platform, component: producer.Component, report: report, terminal: terminal}}, nil
}

// BindingContext revalidates the live held bytes before returning descriptions.
func (tool *ProducedExecutable) BindingContext(ctx context.Context) (ProducedExecutableBinding, error) {
	if err := requireRunContext(ctx, "produced executable binding"); err != nil {
		return ProducedExecutableBinding{}, err
	}
	if tool == nil || tool.state == nil {
		return ProducedExecutableBinding{}, e.New(e.CodeVerifyFailed, "produced executable receipt is absent")
	}
	s := tool.state
	if err := lockDiskInspection(ctx, &s.mu); err != nil {
		return ProducedExecutableBinding{}, err
	}
	defer s.mu.Unlock()
	if s.file == nil {
		return ProducedExecutableBinding{}, e.New(e.CodeVerifyFailed, "produced executable receipt is closed")
	}
	if err := s.terminal.require(ctx, s.report); err != nil {
		return ProducedExecutableBinding{}, err
	}
	if err := s.file.AssertUnchangedContext(ctx); err != nil {
		return ProducedExecutableBinding{}, mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "revalidating produced executable"), "produced executable binding")
	}
	return ProducedExecutableBinding{BuildDigest: s.buildDigest, Platform: s.platform, Component: s.component, Digest: s.file.Digest(), Size: s.file.Size(), TerminalDigest: s.terminal.binding}, nil
}

// Close revokes the held tool and every shallow copy. It is idempotent.
func (tool *ProducedExecutable) Close() error {
	if tool == nil || tool.state == nil {
		return nil
	}
	s := tool.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}
