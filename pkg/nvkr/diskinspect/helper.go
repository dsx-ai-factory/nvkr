// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskinspect

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// helperOperationTimeout bounds work inside the appliance. The pipeline owns
// its separate complete-operation budget, including transport and cleanup.
const helperOperationTimeout = 10 * time.Minute

type diskInspectionReport struct {
	Subject   string                 `json:"subject"`
	Size      int64                  `json:"size"`
	Inspector string                 `json:"inspector"`
	Codec     string                 `json:"codec"`
	Packages  []InstalledDiskPackage `json:"packages"`
}

// RunDiskInspectionHelper is the thin container entrypoint's owning operation.
// It accepts exact stdin bytes, pins both Linux ELF tools and uses the existing
// held-envelope gate before libguestfs. No guest executable is ever run.
func RunDiskInspectionHelper(ctx context.Context, inspectorDigest, codecDigest, subjectDigest string, size int64, input io.Reader, output io.Writer) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if size <= 0 || size > MaxDiagnosticDiskBytes || !canonical.IsDigest(subjectDigest) || !canonical.IsDigest(inspectorDigest) || !canonical.IsDigest(codecDigest) {
		return e.New(e.CodeUnpinnedInput, "inspection helper requires a bounded exact subject")
	}
	if input == nil || output == nil {
		return e.New(e.CodeUsage, "inspection helper requires input and output streams")
	}
	ctx, cancel := context.WithTimeout(ctx, helperOperationTimeout)
	defer cancel()
	codec, err := assemble.OpenQemuImgCodec(ctx, "/usr/bin/qemu-img", codecDigest)
	if err != nil {
		return err
	}
	defer func() { _ = codec.Close() }()
	inspector, err := OpenDiskInspector(ctx, "/usr/bin/virt-inspector", inspectorDigest, codec)
	if err != nil {
		return err
	}
	defer func() { _ = inspector.Close() }()
	subject, err := holdDiskInspectionSubject(ctx, input, subjectDigest, size)
	if err != nil {
		return err
	}
	defer func() { _ = subject.Close() }()
	inventory, err := inspector.InspectHeldInstalledPackages(ctx, subject, "qcow2", "none")
	if err != nil {
		return err
	}
	report := diskInspectionReport{Subject: subject.Digest(), Size: subject.Size(), Inspector: inspectorDigest, Codec: codecDigest, Packages: inventory.Packages()}
	raw, err := canonical.Marshal(report)
	if err != nil {
		return err
	}
	if len(raw) > MaxPackageReportBytes {
		return e.New(e.CodeEnvelopeDecode, "inspection helper report exceeds its budget")
	}
	// Do not emit an accepted report before owned capabilities have closed. The
	// enclosing adapter still waits for process exit and container cleanup.
	if err := errors.Join(inspector.Close(), codec.Close(), subject.Close()); err != nil {
		return e.Wrap(e.CodeInternal, err, "closing inspection helper capabilities")
	}
	if _, err := output.Write(raw); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing inspection report")
	}
	return nil
}

func holdDiskInspectionSubject(ctx context.Context, input io.Reader, digest string, size int64) (*heldfile.File, error) {
	subject, err := heldfile.FromReaderContext(ctx, input, heldfile.Options{Label: "inspection-subject", MaxBytes: size, ExpectedDigest: digest})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, errors.Join(err, ctx.Err()), "holding container inspection subject")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding container inspection subject")
	}
	if subject.Size() != size {
		_ = subject.Close()
		return nil, e.New(e.CodeVerifyFailed, "inspection subject size differs")
	}
	return subject, nil
}
