// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package planview

import e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"

// ComparisonOptions selects a comparison display; Details applies only to text.
type ComparisonOptions struct {
	Format  string
	Details bool
}

// Validate rejects unsupported or ineffective display options before input I/O.
func (o ComparisonOptions) Validate() error {
	if err := ValidateComparisonFormat(o.Format); err != nil {
		return err
	}
	if o.Details && o.Format != "text" {
		return e.New(e.CodeUsage, "--details requires text output")
	}
	return nil
}

// RenderView emits a text summary, detailed text, or complete descriptive JSON.
func (c *Comparison) RenderView(o ComparisonOptions) ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if o.Format == "text" && !o.Details {
		return c.Summary()
	}
	return c.Render(o.Format)
}
