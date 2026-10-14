// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

func projectionFailure(cause error) error {
	if cause != nil {
		return e.Wrap(e.CodeEngineCapability, cause, "conformance metadata projection exceeds the supported output contract")
	}
	return e.New(e.CodeEngineCapability, "conformance metadata projection exceeds the supported output contract")
}

// A rejected phase leaves the already captured, encodable Partial unchanged;
// its error explicitly records omitted diagnostics rather than granting success.
func appendProjection(output *testresults.Output, facts []testresults.EnvironmentFact) error {
	candidate := *output
	candidate.Environment = append(append([]testresults.EnvironmentFact(nil), output.Environment...), facts...)
	if _, err := testresults.Encode(candidate); err != nil {
		return projectionFailure(err)
	}
	*output = candidate
	return nil
}

func admitProjectionCount(beforeFacts int, targets []DeploymentTarget) error {
	replicas := 0
	for _, target := range targets {
		replicas += target.RequiredReplicas
	}
	// Source Run contributes four facts and the disposable acknowledgment one.
	if 2*(beforeFacts+replicas)+5 > testresults.MaxEnvironmentFacts {
		return projectionFailure(nil)
	}
	return nil
}

// Reserve the actual baseline projection twice plus the largest captured-report
// descriptor before suite resources. Terminal growth is independently checked;
// it fails with retained before facts/reports, never a reduced complete claim.
func admitTerminalProjection(output testresults.Output, before []testresults.EnvironmentFact, baseline []deploymentObservation, reportPath string) error {
	after := append([]testresults.EnvironmentFact(nil), before...)
	for i := range after {
		after[i].Phase = "after"
	}
	output.Environment = append(append(append([]testresults.EnvironmentFact(nil), output.Environment...), after...), deploymentFacts(baseline, "after")...)
	// This private capacity reservation is never returned as a captured report.
	output.Reports = []testresults.ReportReference{{Format: testresults.FormatJUnit, Profile: JUnitProfile, Reference: reportPath, Digest: blobDigest(nil), ByteLength: maxReportBytes}}
	if _, err := testresults.Encode(output); err != nil {
		return projectionFailure(err)
	}
	return nil
}
