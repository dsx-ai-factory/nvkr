// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package resultspublish

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
)

// Column-header metadata keys, in the order of TestGrid's nvkr layout. The run URL
// is at index 4, where the board reads it for every group.
const (
	keySHA          = "sha"
	keySourceRun    = "source_run"
	keyQualifierRun = "qualifier_run"
	keyLane         = "lane"
	keyRunURL       = "run_url"
	keyStatus       = "status"
)

type startedDocument struct {
	Timestamp int64             `json:"timestamp"`
	Metadata  map[string]string `json:"metadata"`
}

type finishedDocument struct {
	Timestamp int64             `json:"timestamp"`
	Passed    bool              `json:"passed"`
	Result    string            `json:"result"`
	Metadata  map[string]string `json:"metadata"`
}

// metadata describes one outcome in TestGrid's terms. TestGrid reads column headers
// only from finished.json metadata, and only string values; started.json carries
// the same map. The run URL names the qualifier attempt, or the producer attempt
// for a producer that did not succeed.
func metadata(outcome qualificationoutcome.Outcome, at layout, group, definition string) map[string]string {
	run := outcome.Qualifier
	if outcome.Stage == qualificationoutcome.StageProducer {
		run = outcome.Source.Run
	}
	values := map[string]string{
		keySHA:          outcome.Source.Commit,
		keySourceRun:    outcome.Source.RunID + "-" + outcome.Source.Attempt,
		keyQualifierRun: outcome.Qualifier.RunID + "-" + outcome.Qualifier.Attempt,
		keyLane:         outcome.Lane,
		keyRunURL:       "https://github.com/" + run.Repository + "/actions/runs/" + run.RunID + "/attempts/" + run.Attempt,
		keyStatus:       string(outcome.Status),
		"repository":    outcome.Source.Repository,
		"group":         group,
		"definition":    definition,
		"outcome":       at.record,
	}
	if outcome.EvidenceDigest != "" {
		values["evidence_digest"] = outcome.EvidenceDigest
	}
	if outcome.ManifestDigest != "" {
		values["manifest_digest"] = outcome.ManifestDigest
	}
	return values
}

// startedJSON times the build from the outcome's start, which TestGrid uses as the
// column's start, its results-window clock and its staleness clock.
func startedJSON(outcome qualificationoutcome.Outcome, at layout, group, definition string) ([]byte, error) {
	return canonical.Marshal(startedDocument{Timestamp: at.started.Unix(), Metadata: metadata(outcome, at, group, definition)})
}

// finishedJSON closes the build: only a passed outcome passes.
func finishedJSON(outcome qualificationoutcome.Outcome, at layout, group, definition string) ([]byte, error) {
	passed := outcome.Status == qualificationoutcome.StatusPassed
	result := "FAILURE"
	if passed {
		result = "SUCCESS"
	}
	return canonical.Marshal(finishedDocument{Timestamp: at.finished.Unix(), Passed: passed, Result: result, Metadata: metadata(outcome, at, group, definition)})
}
