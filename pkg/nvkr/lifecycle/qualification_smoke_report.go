// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/google/go-containerregistry/pkg/name"
)

// Validate checks a successful report's complete structural bindings. It does
// not authenticate the worker, policy, artifact checks or execution. Aggregation
// must verify the signed exact bytes and recompute the complete expected scope.
func (report NativeToolSmokeReport) Validate() error {
	invalid := func() error { return e.New(e.CodeVerifyFailed, "native smoke report is incomplete or inconsistent") }
	if report.SchemaVersion != NativeToolSmokeSchema || report.Status != "pass" || report.Code != "" || len(report.Results) != 0 || !qualificationNativePlatform(report.Platform) || len(report.Members) == 0 || len(report.Members) > 4096 || !canonical.IsDigest(report.PolicyDigest) || !canonical.IsDigest(report.RuntimeImage) || !canonical.IsDigest(report.RuntimeIdentity) {
		return invalid()
	}
	image, err := name.NewDigest(report.Image, name.StrictValidation)
	if err != nil || strings.Contains(report.Image, "://") || image.Name() != report.Image || !canonical.IsDigest(image.DigestStr()) {
		return invalid()
	}
	checks := report.ArtifactChecks
	if checks.GateVersion != ArtifactCheckGateVersion || len(checks.Outputs) == 0 || len(checks.Outputs) > 1024 || !canonical.IsDigest(checks.CatalogDigest) {
		return invalid()
	}
	if err := checks.Inputs.Validate(); err != nil {
		return err
	}
	if err := checks.Origin.Validate(); err != nil {
		return err
	}
	if err := report.Invocation.Validate(); err != nil {
		return err
	}
	if report.Invocation.Repository != checks.Origin.Repository || report.Invocation.DefaultBranch != checks.Origin.DefaultBranch || report.Invocation.Workflow == checks.Origin.Workflow || report.Invocation.RunID == checks.Origin.RunID {
		return invalid()
	}
	outputs := map[string]ArtifactCheckOutput{}
	previous := ""
	for _, output := range checks.Outputs {
		if output.Name <= previous || !qualificationSmokeName.MatchString(output.Name) || output.Overall != "pass" || !canonical.IsDigest(output.Subject) || !canonical.IsDigest(output.BuildDigest) || len(output.Checks) == 0 {
			return invalid()
		}
		ref, err := name.NewDigest(output.Reference, name.StrictValidation)
		if err != nil || ref.Name() != output.Reference || ref.DigestStr() != output.Subject || delivery.ValidatePublicationReference(output.Reference) != nil {
			return invalid()
		}
		switch output.Kind {
		case OutputExecutable, OutputOCI, OutputAPT, OutputHelm, OutputDiskImage:
		default:
			return invalid()
		}
		if output.ImageIndex != nil {
			if output.Kind != OutputOCI || output.Platform != "" || len(output.BuildSubjects) != 0 {
				return invalid()
			}
			if err := output.ImageIndex.Validate(); err != nil {
				return err
			}
		} else if (output.Kind != OutputHelm && len(output.BuildSubjects) == 0) || (output.Platform != "" && !qualificationNativePlatform(output.Platform)) {
			return invalid()
		}
		previous = output.Name
		seenChecks := map[string]bool{}
		for _, check := range output.Checks {
			if check.Name == "" || seenChecks[check.Name] || (check.Status != "pass" && !((check.Name == "embedded-declarations" || check.Name == "payload-pins") && check.Status == "not-applicable")) || check.Code != "" {
				return invalid()
			}
			seenChecks[check.Name] = true
		}
		previousSubject := ""
		for _, subject := range output.BuildSubjects {
			if !qualificationSmokeName.MatchString(subject.Component) || subject.Component <= previousSubject || !canonical.IsDigest(subject.Digest) {
				return invalid()
			}
			previousSubject = subject.Component
		}
		outputs[output.Name] = output
	}
	previous = ""
	for _, member := range report.Members {
		key := member.Output + "\x00" + member.Member
		output, exists := outputs[member.Output]
		if !exists || key <= previous || !qualificationSmokeName.MatchString(member.Member) || output.Kind != OutputExecutable || output.Platform != report.Platform || member.Platform != report.Platform || member.Subject != output.Subject || member.BuildDigest != output.BuildDigest || !canonical.IsDigest(member.PayloadDigest) || member.Size <= 0 || member.Size > pipeline.MaxToolSmokeExecutableBytes || member.ExpectedVersion == "" || len(member.ExpectedVersion) > 256 || member.ExpectedVersion != member.ObservedVersion || member.ExpectedCommit != member.ObservedCommit {
			return invalid()
		}
		if _, err := nativeSmokeProfile(member.Profile); err != nil {
			return err
		}
		if member.Profile == "nvkectl-version-json" || member.Profile == "clusterctl-version-json" || member.Profile == "oras-version" || member.Profile == "nicosim-version-json" || member.Profile == "nicosim-patched-version-json" {
			if !smokeHexCommit(member.ExpectedCommit) {
				return invalid()
			}
			if member.Profile != "nvkectl-version-json" && len(member.ExpectedCommit) != 40 {
				return invalid()
			}
		} else if member.ExpectedCommit != "" {
			return invalid()
		}
		previous = key
	}
	return nil
}

// Canonical is available only for structurally complete successful reports.
// Failed reports remain diagnostics and cannot enter signed worker transport.
func (report NativeToolSmokeReport) Canonical() ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(report)
}
func (report NativeToolSmokeReport) Digest() (string, error) {
	if err := report.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindToolSmokeWorker, report)
}
func smokeHexCommit(commit string) bool {
	if len(commit) != 8 && len(commit) != 40 {
		return false
	}
	for _, ch := range commit {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}
