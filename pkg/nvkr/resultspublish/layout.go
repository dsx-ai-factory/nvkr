// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package resultspublish

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
)

// segmentPattern admits a group or Definition name as one bucket path segment and
// one part of a TestGrid dashboard or tab name: what a Definition name allows
// (lowercase letters, digits, '.', '_' and '-', alphanumeric at both ends), in at
// most 128 characters.
var segmentPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,126}[a-z0-9])?$`)

// latestFinish is the last finish a build name can carry: its prefix counts
// nanoseconds since the epoch.
var latestFinish = time.Unix(0, math.MaxInt64)

// layout is where one outcome is published.
type layout struct {
	// record is results/<g>/<d>/<source run>-<attempt>/<qualifier run>-<attempt>/outcome.json.
	record string
	// build is groups/nvkr/<g>/<d>/<build>/. The build name starts with the
	// finish in zero-padded nanoseconds, because TestGrid reads builds in lexical
	// order and skips names below one it has already read.
	build    string
	started  time.Time
	finished time.Time
}

// CheckNames refuses a group or Definition that cannot be one segment of the
// index's paths, so a caller can refuse it before any work; Publish checks them too.
func CheckNames(group, definition string) error {
	if !segmentPattern.MatchString(group) || !segmentPattern.MatchString(definition) {
		return e.New(e.CodeUsage, "the results group and Definition must be lowercase path segments")
	}
	return nil
}

func layoutFor(outcome qualificationoutcome.Outcome, group, definition string) (layout, error) {
	if err := CheckNames(group, definition); err != nil {
		return layout{}, err
	}
	if err := outcome.Validate(); err != nil {
		return layout{}, err
	}
	started, finished, err := outcome.Times()
	if err != nil {
		return layout{}, err
	}
	// TestGrid treats a zero timestamp as absent, and a negative or overflowing one
	// breaks the build name's order.
	if started.Unix() <= 0 || finished.After(latestFinish) || finished.UnixNano() <= 0 {
		return layout{}, e.New(e.CodeVerifyFailed, "an outcome time is outside what a build name can carry")
	}
	source := outcome.Source.RunID + "-" + outcome.Source.Attempt
	qualifier := outcome.Qualifier.RunID + "-" + outcome.Qualifier.Attempt
	return layout{
		record:   path.Join("results", group, definition, source, qualifier, "outcome.json"),
		build:    path.Join("groups", "nvkr", group, definition, fmt.Sprintf("%019d-%s-%s", finished.UnixNano(), source, qualifier)) + "/",
		started:  started,
		finished: finished,
	}, nil
}
