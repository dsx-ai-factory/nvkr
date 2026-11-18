// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"sort"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// Collection begins only after the private PID namespaces have joined. Reads
// stay beneath held directories, reject links/special files, and apply the same
// count/byte limits as the existing container diagnostic transport.
func collectProducedPackerBuildLogs(ctx context.Context, work *os.File, target *packerDiagnosticTarget) (returnedErr error) {
	logs, _, err := securepath.OpenChild(work, "build-logs", true)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "holding produced Packer logs")
	}
	defer func() { returnedErr = errors.Join(returnedErr, logs.Close()) }()
	archive, err := heldfile.NewOutput("produced-packer-logs", maxPackerDiagnosticArchiveBytes)
	if err != nil {
		return err
	}
	defer func() { returnedErr = errors.Join(returnedErr, archive.Close()) }()
	writer := tar.NewWriter(&packerContainerLimitWriter{writer: archive.OSFile(), remaining: maxPackerDiagnosticArchiveBytes})
	budget := producedPackerLogBudget{}
	packErr := budget.pack(ctx, writer, logs, "")
	if err := errors.Join(packErr, writer.Close()); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "collecting bounded produced Packer logs")
	}
	held, err := archive.SealContext(ctx)
	if err != nil {
		return err
	}
	defer func() { returnedErr = errors.Join(returnedErr, held.Close()) }()
	return target.retainBuildLogs(ctx, held)
}

type producedPackerLogBudget struct {
	entries, files int
	bytes          int64
}

func (budget *producedPackerLogBudget) pack(ctx context.Context, writer *tar.Writer, directory *os.File, prefix string) error {
	names, err := directory.Readdirnames(maxPackerDiagnosticEntries + 1)
	if err != nil && err != io.EOF {
		return err
	}
	if len(names) > maxPackerDiagnosticEntries {
		return e.Wrap(e.CodeVerifyFailed, errPackerDiagnosticsIncomplete, "produced Packer logs exceed entry bound")
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		budget.entries++
		if budget.entries > maxPackerDiagnosticEntries {
			return e.Wrap(e.CodeVerifyFailed, errPackerDiagnosticsIncomplete, "produced Packer logs exceed entry bound")
		}
		relative := path.Join(prefix, name)
		entry, err := securepath.InspectChild(directory, name, relative)
		if err != nil {
			return err
		}
		if entry.File == nil {
			return e.New(e.CodeVerifyFailed, "produced Packer logs contain a link")
		}
		err = budget.packEntry(ctx, writer, entry, relative)
		if err := errors.Join(err, entry.File.Close()); err != nil {
			return err
		}
	}
	return nil
}

func (budget *producedPackerLogBudget) packEntry(ctx context.Context, writer *tar.Writer, entry securepath.Entry, relative string) error {
	if entry.Info.IsDir() {
		if err := writer.WriteHeader(&tar.Header{Name: relative, Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
			return err
		}
		return budget.pack(ctx, writer, entry.File, relative)
	}
	budget.files++
	size := entry.Info.Size()
	if budget.files > maxPackerDiagnosticFiles || size < 0 || size > maxPackerDiagnosticFileBytes || size > maxPackerDiagnosticPayloadBytes-budget.bytes {
		return e.Wrap(e.CodeVerifyFailed, errPackerDiagnosticsIncomplete, "produced Packer logs exceed file or byte bounds")
	}
	budget.bytes += size
	if err := writer.WriteHeader(&tar.Header{Name: relative, Typeflag: tar.TypeReg, Mode: 0o600, Size: size}); err != nil {
		return err
	}
	n, err := copyPipelineContext(ctx, writer, io.NewSectionReader(entry.File, 0, size))
	if err != nil {
		return err
	}
	if n != size {
		return e.New(e.CodeVerifyFailed, "produced Packer log changed during collection")
	}
	return nil
}
