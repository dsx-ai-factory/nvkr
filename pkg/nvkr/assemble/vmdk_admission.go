// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package assemble

import (
	"context"
	"encoding/binary"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// admitVMDKSparseInput admits only the single-file VMDK4 sparse branch before
// QEMU opens the input. QEMU v8.2.2 block/vmdk.c's VMDK4Header places capacity
// at bytes 12..20, after magic, version and flags. Text descriptors and a binary
// header with zero capacity can invoke vmdk_parse_extents during info, despite
// BDRV_O_NO_IO, opening external files before JSON is available. Reject those
// branches rather than implementing QEMU's descriptor grammar here. A complete
// first sector, KDMV magic and nonzero capacity select the held-file-only branch;
// QEMU remains responsible for full header, grain table and footer validation.
func admitVMDKSparseInput(ctx context.Context, input *heldfile.File) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "admitting held VMDK sparse layout")
	}
	var header [512]byte
	if _, err := input.ReadAt(header[:], 0); err != nil {
		return e.Wrap(e.CodeEnvelopeDecode, err, "reading complete held VMDK sparse header")
	}
	if string(header[:4]) != "KDMV" || binary.LittleEndian.Uint64(header[12:20]) == 0 {
		return e.New(e.CodeEnvelopeDecode, "held VMDK requires a single-file VMDK4 sparse header with nonzero capacity")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "admitting held VMDK sparse layout")
	}
	return nil
}
