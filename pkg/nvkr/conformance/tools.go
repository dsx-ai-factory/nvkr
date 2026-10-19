// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"debug/elf"
	"errors"
	"io"
	"runtime"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// ExecutableSelector chooses an exact member of an authenticated tool publication.
// It supplies no path, caller digest or executable authority.
type ExecutableSelector struct {
	Output   string `json:"output"`
	Member   string `json:"member"`
	Platform string `json:"platform"`
}
type executableCapture func(context.Context, ExecutableSelector) (*heldfile.File, error)
type executableStream func(context.Context, string, string, string, int64, io.Writer) (lifecycle.ExecutableMemberDescription, error)

func validateExecutableSelector(selector ExecutableSelector) error {
	for _, value := range []string{selector.Output, selector.Member} {
		if !safetext.Valid(value, 4096, false) || strings.TrimSpace(value) != value {
			return e.New(e.CodeUsage, "conformance tool requires an exact output and member selector")
		}
	}
	if err := target.ValidatePlatform(selector.Platform); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "conformance tool platform")
	}
	if selector.Platform != "linux/"+runtime.GOARCH {
		return e.New(e.CodeEngineCapability, "conformance tool platform must match the native execution host")
	}
	return nil
}
func validateToolSelections(description lifecycle.ArtifactCheckDescription, options Options) error {
	if options.Hydrophone == options.Kubectl {
		return e.New(e.CodeUsage, "conformance harness and kubectl require distinct executable selectors")
	}
	for _, selector := range []ExecutableSelector{options.Hydrophone, options.Kubectl} {
		matches := 0
		for _, output := range description.Outputs {
			if output.Name != selector.Output {
				continue
			}
			matches++
			if output.Kind != lifecycle.OutputExecutable || output.Platform != selector.Platform {
				return e.New(e.CodeEngineCapability, "conformance tool must select a matching native executable output")
			}
			members := 0
			for _, subject := range output.BuildSubjects {
				if subject.Component == selector.Member {
					members++
				}
			}
			if members != 1 {
				return failure("conformance tool requires one exact authenticated member")
			}
		}
		if matches != 1 {
			return failure("conformance tool requires one exact authenticated output")
		}
	}
	return nil
}

// This private streaming seam shares the production seal/recheck path with tests.
func captureExecutable(ctx context.Context, selector ExecutableSelector, stream executableStream) (*heldfile.File, error) {
	output, err := heldfile.NewOutput("authenticated conformance tool", maxToolBytes)
	if err != nil {
		return nil, captureFailure(ctx, err, e.CodeEngineCapability, "holding conformance tool output")
	}
	defer func() { _ = output.Close() }()
	writer := &executableWriter{ctx: ctx, destination: output.OSFile()}
	descriptor, err := stream(ctx, selector.Output, selector.Member, selector.Platform, maxToolBytes, writer)
	if err != nil {
		var coded *e.Error
		if errors.As(err, &coded) {
			return nil, err
		}
		return nil, captureFailure(ctx, err, e.CodeVerifyFailed, "capturing authenticated executable member")
	}
	sealed, err := output.SealContext(ctx)
	if err != nil {
		return nil, captureFailure(ctx, err, e.CodeVerifyFailed, "sealing authenticated conformance tool")
	}
	defer func() { _ = sealed.Close() }()
	if descriptor.Output != selector.Output || descriptor.Member != selector.Member || descriptor.Platform != selector.Platform || descriptor.PayloadDigest != sealed.Digest() || descriptor.Size != sealed.Size() {
		return nil, failure("conformance tool bytes differ from authenticated member")
	}
	held, err := heldfile.FromReaderContext(ctx, io.NewSectionReader(sealed.OSFile(), 0, sealed.Size()), heldfile.Options{Label: "authenticated conformance executable", Executable: true, MaxBytes: maxToolBytes, ExpectedDigest: descriptor.PayloadDigest})
	if err != nil {
		return nil, captureFailure(ctx, err, e.CodeEngineCapability, "holding native conformance executable")
	}
	return validateHeldExecutable(held)
}

type executableWriter struct {
	ctx         context.Context
	destination io.Writer
	written     int64
}

func (w *executableWriter) Write(raw []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(raw)) > maxToolBytes-w.written {
		return 0, io.ErrShortWrite
	}
	n, err := w.destination.Write(raw)
	w.written += int64(n)
	return n, err
}

func validateHeldExecutable(held *heldfile.File) (*heldfile.File, error) {
	parsed, err := elf.NewFile(held)
	if err != nil {
		_ = held.Close()
		return nil, e.New(e.CodeEngineCapability, "conformance tool must be static Linux ELF")
	}
	defer func() { _ = parsed.Close() }()
	native := (runtime.GOARCH == "amd64" && parsed.Machine == elf.EM_X86_64) || (runtime.GOARCH == "arm64" && parsed.Machine == elf.EM_AARCH64)
	if !native || parsed.Type != elf.ET_EXEC || parsed.Class != elf.ELFCLASS64 || parsed.Data != elf.ELFDATA2LSB {
		_ = held.Close()
		return nil, e.New(e.CodeEngineCapability, "conformance tool requires native 64-bit static Linux ELF")
	}

	for _, p := range parsed.Progs {
		if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
			_ = held.Close()
			return nil, e.New(e.CodeEngineCapability, "conformance dynamic executable unsupported")
		}
	}
	return held, nil
}

// ImageSelector names a native OCI image in the authenticated tools publication.
type ImageSelector struct {
	Output   string `json:"output"`
	Platform string `json:"platform"`
}

func validateImageSelector(selector ImageSelector) error {
	if !safetext.Valid(selector.Output, 256, false) || strings.TrimSpace(selector.Output) != selector.Output {
		return e.New(e.CodeUsage, "conformance image requires an exact named output")
	}
	if err := target.ValidatePlatform(selector.Platform); err != nil {
		return e.Wrap(e.CodeUsage, err, "conformance image platform")
	}
	if selector.Platform != "linux/amd64" && selector.Platform != "linux/arm64" {
		return e.New(e.CodeEngineCapability, "conformance image requires native Linux platform")
	}
	return nil
}
func resolveImages(description lifecycle.ArtifactCheckDescription, options Options) (string, string, error) {
	selectors := []ImageSelector{options.ConformanceImage, options.Busybox}
	refs := make([]string, 0, 2)
	for _, selector := range selectors {
		selected, err := selectOutputs(description, []DeploymentTarget{{Output: selector.Output}})
		if err != nil {
			return "", "", err
		}
		ref, err := nativeReference(selected[selector.Output], selector.Platform)
		if err != nil {
			return "", "", err
		}
		refs = append(refs, ref)
	}
	return refs[0], refs[1], nil
}
