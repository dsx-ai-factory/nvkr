// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testreporting

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

func markdown(value string) string {

	var out strings.Builder
	for _, c := range value {
		if unicode.IsControl(c) || unicode.In(c, unicode.Cf) {
			fmt.Fprintf(&out, "\\u%04x", c)
			continue
		}
		if strings.ContainsRune("\\`*_{}[]()#+-.!|><", c) {
			out.WriteByte('\\')
		}
		out.WriteRune(c)
	}
	return out.String()
}

// Markdown projects exact observed coverage and historical outcomes. Artifact
// digests without independently supplied locators remain subjects, not guessed URLs.
func (r *Report) Markdown(ctx context.Context) (string, error) {
	if r == nil {
		return "", failure("report unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index, err := r.indexLocked(ctx)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("Authenticated historical test observations. These records do not establish current cluster health, release qualification or TestGrid publication.\n")
	for _, entry := range index.Entries {
		o := entry.Observation
		suite := o.Suite
		fmt.Fprintf(&out, "\n### %s / %s\n\n", markdown(entry.Namespace), markdown(entry.SourcePath))
		fmt.Fprintf(&out, "Observation: %s\n\nOCI bundle: %s\n\n", markdown(entry.ObservationDigest), markdown(entry.Reference))
		fmt.Fprintf(&out, "Signer: %s; issuer: %s; admitted tier: %s.\n\n", markdown(entry.Identity), markdown(entry.Issuer), markdown(entry.Tier))
		if entry.SignerProjectionToken != "" {
			fmt.Fprintf(&out, "Signer projection token (deterministic grouping label; raw signer identity is disclosed): %s.\n\n", markdown(entry.SignerProjectionToken))
		}
		fmt.Fprintf(&out, "Suite: %s; invocation: %s; harness revision: %s; image: %s; policy: %s.\n\n", markdown(suite.Name), markdown(suite.Invocation), markdown(suite.HarnessRevision), markdown(suite.ImageDigest), markdown(suite.PolicyDigest))
		fmt.Fprintf(&out, "Selection: %s. Required cases: %d; observed cases: %d.\n\n", markdown(strings.Join(suite.Selection, ", ")), entry.Coverage.Required, entry.Coverage.Total)
		fmt.Fprintf(&out, "Execution: %s (exit %d); cleanup: %s (exit %d); passing observation: %t.\n\n", markdown(o.Execution.Status), o.Execution.ExitCode, markdown(o.Cleanup.Status), o.Cleanup.ExitCode, entry.Passing)
		fmt.Fprintf(&out, "Counts: %d passed, %d failed, %d errors, %d skipped. Origin evidence: %s; Run: %s; attempt: %d; role: %s.\n\n", o.Summary.Passed, o.Summary.Failed, o.Summary.Errors, o.Summary.Skipped, markdown(o.Origin.Status), markdown(o.Origin.Run), o.Origin.Attempt, markdown(o.Environment.Role))
		out.WriteString("| Output | Platform | Build | Artifact | Payload |\n| --- | --- | --- | --- | --- |\n")
		for _, subject := range o.Subjects {
			fmt.Fprintf(&out, "| %s | %s | %s | %s | %s |\n", markdown(subject.Output), markdown(subject.Platform), markdown(subject.BuildDigest), markdown(subject.ArtifactDigest), markdown(subject.PayloadDigest))
		}
		out.WriteString("\n| Environment fact | Phase | Value | Evidence |\n| --- | --- | --- | --- |\n")
		for _, fact := range o.Environment.Facts {
			fmt.Fprintf(&out, "| %s | %s | %s | %s |\n", markdown(fact.Name), markdown(fact.Phase), markdown(fact.Value), markdown(fact.Evidence))
		}
		out.WriteString("\n| Covered file | Role | Raw digest | Bytes |\n| --- | --- | --- | --- |\n")
		for _, file := range o.Files {
			fmt.Fprintf(&out, "| %s | %s | %s | %d |\n", markdown(file.Name), markdown(file.Role), markdown(file.Digest), file.Size)
		}
		fmt.Fprintf(&out, "\nReproduction requires the exact harness revision, pinned suite image, selection, policy and admitted artifact inputs above. This report performs no rerun.\n")
	}
	if out.Len() > MaxIndexBytes {
		return "", failure("Markdown report exceeds byte budget")
	}
	if err := r.check(ctx); err != nil {
		return "", err
	}
	return out.String(), nil
}
