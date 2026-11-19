// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"github.com/dsx-ai-factory/nvkr/internal/httptrust"
	"net/http"
)

// NewTransport shares direct HTTPS trust admission between the provider API,
// carrier retrieval and isolated registry publication. Ambient proxy and
// certificate variables do not select provider authority.
func NewTransport(ctx context.Context, certificateAuthority string) (*http.Transport, error) {
	return httptrust.NewTransport(ctx, certificateAuthority)
}
