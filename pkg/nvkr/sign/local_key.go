// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import "github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/signkey"

// NormalizeLocalPublicKey admits and normalizes the explicit P-256 public-key
// format. Parsing a key authenticates no signature, origin or qualification.
func NormalizeLocalPublicKey(raw []byte) ([]byte, error) {
	return signkey.NormalizePublicKey(raw)
}
