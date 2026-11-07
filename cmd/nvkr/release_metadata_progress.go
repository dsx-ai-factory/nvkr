// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/urfave/cli/v2"
)

// Progress stays on stderr, leaving serialized stdout byte-for-byte unchanged.
// The terminal path supplies its own snapshot reporter instead.
func releaseMetadataReporter(c *cli.Context, view runPresentation) func(delivery.MetadataProgress) {
	if view.tui {
		return nil
	}
	writer := c.App.ErrWriter
	if writer == nil {
		writer = os.Stderr
	}
	var last time.Time
	return func(update delivery.MetadataProgress) {
		if update.Done != 0 && update.Done != update.Total && time.Since(last) < time.Second {
			return
		}
		last = time.Now()
		_, _ = fmt.Fprintf(writer, "Loading release metadata: %d/%d objects (closure checks pending)\n", update.Done, update.Total)
	}
}
