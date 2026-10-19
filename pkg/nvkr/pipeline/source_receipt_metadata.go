// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"encoding/hex"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ValidateSourceReceiptMetadata admits only the receipt's closed descriptive
// schema for public CI transport. It does not authenticate a source checkout;
// consumers must still verify the complete restored closure against their node.
func ValidateSourceReceiptMetadata(raw []byte) error {
	if len(raw) > 1<<20 {
		return e.New(e.CodeVerifyFailed, "source receipt metadata exceeds its bound")
	}
	var receipt sourceReceipt
	if err := canonical.UnmarshalExact(raw, &receipt); err != nil {
		return err
	}
	commit, err := hex.DecodeString(receipt.Commit)
	if err != nil || len(commit) != 20 || strings.ToLower(receipt.Commit) != receipt.Commit || receipt.Schema != sourceReceiptSchema ||
		receipt.TreeHash == "" || len(receipt.TreeHash) > 128 || !canonical.IsDigest(receipt.VendorSpecDigest) || !canonical.IsDigest(receipt.ClosureDigest) {
		return e.New(e.CodeVerifyFailed, "source receipt metadata has invalid identities")
	}
	return nil
}
