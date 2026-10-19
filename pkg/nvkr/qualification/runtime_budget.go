// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"time"
)

// These enclosing allowances cover complete candidate work; individual operation
// limits remain independent. They do not establish that every candidate fits.
const (
	hostedRuntimeTimeout     = 59 * time.Minute
	hostedStepTimeoutMinutes = int((hostedRuntimeTimeout + time.Minute) / time.Minute)
	hostedJobTimeoutMinutes  = hostedStepTimeoutMinutes + 30
)

// WithHostedRuntimeBudget bounds the complete hosted qualifier or smoke worker,
// leaving one minute before the generated step deadline for cancellation,
// cleanup, and diagnostics. A caller's earlier deadline still takes precedence.
// Local artifact checking does not use this hosted execution budget.
func WithHostedRuntimeBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, hostedRuntimeTimeout)
}
