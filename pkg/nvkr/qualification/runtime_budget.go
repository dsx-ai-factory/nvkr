// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"time"
)

const hostedRuntimeTimeout = 19 * time.Minute

// WithHostedRuntimeBudget bounds the complete hosted qualifier or smoke worker,
// leaving one minute before the generated step deadline for cancellation,
// cleanup, and diagnostics. A caller's earlier deadline still takes precedence.
// Local artifact checking does not use this hosted execution budget.
func WithHostedRuntimeBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, hostedRuntimeTimeout)
}
