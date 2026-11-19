// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The summary contract for the two artifact sources. A run that built everything from the subject
// commit must not read as a run of NVKR outputs, and the reverse, whichever field is edited.

const (
	forgeE2EMessageSource       = "artifactSource must be nvkr-outputs or build-subject"
	forgeE2EMessageProducer     = "producer must name a run id for NVKR outputs and be null for a build from the subject commit"
	forgeE2EMessagePlan         = "a build from the subject commit must keep the plan unmodified, and a run of NVKR outputs must have changed it"
	forgeE2EMessageImages       = "nodeImages must be empty for a build from the subject commit, and nonempty https locations with a sha256 for NVKR outputs"
	forgeE2EMessageLegSource    = "every leg must have staged from the artifact source the summary names"
	forgeE2EMessageCommit       = "a build from the subject commit must name that commit as the harness commit and the plan's, and NVKR outputs must name none"
	forgeE2EMessageUnbuilt      = "every leg must say that the deploy path, the imager EFI and the site profiles are not NVKR-built"
	forgeE2EMessageSubjectBuilt = "a build from the subject commit must say that the controller, capi-provisioned, the CLI and the node image are not NVKR-built"
	forgeE2EMessageSkew         = "nodeImageVersionSkew must say whether any node image version differs from the subject version"
	forgeE2EMessageStaged       = "every leg must record what it staged and what is not NVKR-built"
)

func TestForgeE2ESummaryBuildSubjectRecordsAFullRunAndSaysNothingIsNvkrBuilt(t *testing.T) {
	root := forgeE2ERoot(t)
	fixture, err := os.ReadFile(filepath.Join(root, forgeE2EPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	in := forgeE2EBuildSubjectSummaryInputs(t, "full", forgeE2EArmStart)
	out := in.path("summary.json")

	result := forgeE2ERun(t, forgeE2ESummaryTool, append(in.writeArgs(out), "--node-user", "dgxc-admin")...)
	if result.exit != 0 {
		t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
	}
	if !strings.Contains(result.output, "qualificationAuthority none") {
		t.Errorf("the script should say it recorded no authority:\n%s", result.output)
	}

	summary := forgeE2EReadSummary(t, out)
	raw := forgeE2EReadMap(t, out)
	for _, check := range []struct {
		name      string
		got, want any
	}{
		{"schema", summary.Schema, "nvkr.dev/forge-e2e-summary/v1"},
		{"authority", summary.QualificationAuthority, "none"},
		{"artifact source", summary.ArtifactSource, "build-subject"},
		{"complete", summary.Complete, true},
		{"harness commit", summary.Harness.Commit, forgeE2ECommit},
		{"subject commit", summary.Subject.Commit, forgeE2ECommit},
		{"subject version", summary.Subject.Version, forgeE2EVersion},
		{"substituted plan digest", summary.Plan.SHA256, forgeE2ESHA256(fixture)},
		{"fixture digest", summary.Plan.FixtureSHA256, forgeE2ESHA256(fixture)},
		{"node image version skew", summary.NodeImageVersionSkew, false},
		{"node images", len(summary.NodeImages), 0},
		{"legs", len(summary.Legs), 2},
		{"planned streams", summary.PlannedStreams, 2},
	} {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
	if producer, present := raw["producer"]; !present || producer != nil {
		t.Errorf("producer = %v (present %v), want null: no producer run made these artifacts", producer, present)
	}
	if got := slices.Sorted(maps.Keys(raw)); !slices.Equal(got, forgeE2ESummaryFields) {
		t.Errorf("summary fields = %v, want %v", got, forgeE2ESummaryFields)
	}
	retained := map[string]bool{}
	for _, name := range raw["retainedFromNvke"].([]any) {
		retained[name.(string)] = true
	}
	for _, want := range []string{"controller", "capi-provisioned", "cli-amd64", "cli-arm64", "os-image-amd64", "os-image-arm64"} {
		if !retained[want] {
			t.Errorf("retainedFromNvke lacks %s: nothing was substituted, so the plan's own entries were all used", want)
		}
	}
	for _, leg := range raw["legs"].([]any) {
		staged := leg.(map[string]any)["staged"].(map[string]any)
		if staged["source"] != "build-subject" || staged["builtFromCommit"] != forgeE2ECommit {
			t.Errorf("leg staged = %v, want build-subject at %s", staged, forgeE2ECommit)
		}
	}
	if asserted := forgeE2ERun(t, forgeE2ESummaryTool, "assert", out); asserted.exit != 0 {
		t.Errorf("assert of a written summary: exit %d\n%s", asserted.exit, asserted.output)
	}
}

func TestForgeE2ESummaryBuildSubjectWriteRefusesWhatItCannotSupport(t *testing.T) {
	const verify = "NVKR-E-VERIFY-FAILED"
	otherCommit := strings.Repeat("0", 40)
	stagedEdit := func(arch string, edit func(map[string]any)) forgeE2EMutation {
		return func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
			t.Helper()
			forgeE2EEditJSON(t, filepath.Join(in.leg(arch), "staged.json"), edit)
			return a
		}
	}
	cases := []struct {
		name    string
		source  string // the inputs' artifact source: build-subject unless it says otherwise
		mutate  forgeE2EMutation
		exit    int
		code    string
		message string
	}{
		// Admission of the source itself.
		{name: "no artifact source", exit: 2, code: "NVKR-E-USAGE",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return forgeE2EDropFlag(a, "--artifact-source", "")
			})},
		{name: "an artifact source the lane does not have", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "unsupported artifact source: nvkr-nightly",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return forgeE2ESetFlag(a, "--artifact-source", "nvkr-nightly")
			})},
		{name: "an artifact source in the wrong case", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "unsupported artifact source",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return forgeE2ESetFlag(a, "--artifact-source", "Build-Subject")
			})},
		{name: "a node image check taken for a build from the subject commit", exit: 2, code: "NVKR-E-USAGE", message: "node image checks are taken only for NVKR outputs",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				check := forgeE2EWriteJSON(t, in.scratch, "check-os-image-amd64.json", forgeE2ENodeImageCheck(forgeE2EOutputs(), "os-image-amd64"))
				return append(slices.Clone(a), "--node-image-check", check)
			}},

		// The flag and the substitution record must agree on the source.
		{name: "build-subject named for a record of NVKR outputs", source: "nvkr-outputs", exit: 22, code: verify,
			message: "the substitution record names artifact source nvkr-outputs, not build-subject",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				a = forgeE2ESetFlag(a, "--artifact-source", "build-subject")
				return forgeE2EDropFlag(a, "--node-image-check", "")
			})},
		{name: "nvkr-outputs named for a record of a build from the subject commit", exit: 22, code: verify,
			message: "the substitution record names artifact source build-subject, not nvkr-outputs",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				a = forgeE2ESetFlag(a, "--artifact-source", "nvkr-outputs")
				for _, slot := range forgeE2ENodeImageSlots {
					check := forgeE2EWriteJSON(t, in.scratch, "check-"+slot+".json", forgeE2ENodeImageCheck(forgeE2EOutputs(), slot))
					a = append(a, "--node-image-check", check)
				}
				return a
			}},
		{name: "a record that names no artifact source", exit: 22, code: verify, message: "the substitution record names artifact source none, not build-subject",
			mutate: forgeE2EEdit(func(m map[string]any) { delete(m, "artifactSource") }, "sub", "substitutions.json")},

		// What the plan, the legs and the commits must say.
		{name: "a plan that was modified", exit: 22, code: verify, message: forgeE2EMessagePlan,
			mutate: forgeE2EEdit(func(m map[string]any) { m["note"] = "edited" }, "sub", "plan.json")},
		{name: "a run of NVKR outputs whose plan is the fixture", source: "nvkr-outputs", exit: 22, code: verify, message: forgeE2EMessagePlan,
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				fixture, err := os.ReadFile(filepath.Join(forgeE2ERoot(t), forgeE2EPlanFile))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(in.path("sub", "plan.json"), fixture, 0o600); err != nil {
					t.Fatal(err)
				}
				return a
			}},
		{name: "a leg that staged from the other source", exit: 22, code: verify, message: forgeE2EMessageLegSource,
			mutate: stagedEdit("amd64", func(m map[string]any) { m["source"] = "nvkr-outputs"; m["builtFromCommit"] = nil })},
		{name: "a leg built from the subject commit in a run of NVKR outputs", source: "nvkr-outputs", exit: 22, code: verify, message: forgeE2EMessageLegSource,
			mutate: stagedEdit("arm64", func(m map[string]any) { m["source"] = "build-subject"; m["builtFromCommit"] = forgeE2ECommit })},
		{name: "a leg built from another commit", exit: 22, code: verify, message: forgeE2EMessageCommit,
			mutate: stagedEdit("arm64", func(m map[string]any) { m["builtFromCommit"] = otherCommit })},
		{name: "a leg that names no commit", exit: 22, code: verify, message: forgeE2EMessageCommit,
			mutate: stagedEdit("amd64", func(m map[string]any) { m["builtFromCommit"] = nil })},
		{name: "a leg of NVKR outputs that names a commit", source: "nvkr-outputs", exit: 22, code: verify, message: forgeE2EMessageCommit,
			mutate: stagedEdit("amd64", func(m map[string]any) { m["builtFromCommit"] = forgeE2ECommit })},
		{name: "a harness at another commit than the subject", exit: 22, code: verify, message: forgeE2EMessageCommit,
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return forgeE2ESetFlag(a, "--harness-commit", otherCommit)
			})},
		// A record without these fields reaches the summary as null, which no run may claim.
		{name: "a leg record with no source field", exit: 22, code: verify, message: forgeE2EMessageLegSource,
			mutate: stagedEdit("amd64", func(m map[string]any) { delete(m, "source") })},
		{name: "a leg record with no commit field", exit: 22, code: verify, message: forgeE2EMessageCommit,
			mutate: stagedEdit("amd64", func(m map[string]any) { delete(m, "builtFromCommit") })},

		// What is not NVKR-built.
		{name: "a leg that does not say the deploy path is not NVKR-built", exit: 22, code: verify, message: forgeE2EMessageUnbuilt,
			mutate: stagedEdit("amd64", func(m map[string]any) {
				m["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "controller", "imager-efi", "node-image", "site-profiles"}
			})},
		{name: "a leg that does not say the site profiles are not NVKR-built", source: "nvkr-outputs", exit: 22, code: verify, message: forgeE2EMessageUnbuilt,
			mutate: stagedEdit("arm64", func(m map[string]any) { m["notNvkrBuilt"] = []any{"deploy-path", "imager-efi"} })},
		{name: "a leg that does not say the controller is not NVKR-built", exit: 22, code: verify, message: forgeE2EMessageSubjectBuilt,
			mutate: stagedEdit("amd64", func(m map[string]any) {
				m["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "deploy-path", "imager-efi", "node-image", "site-profiles"}
			})},
		{name: "a leg that does not say the node image is not NVKR-built", exit: 22, code: verify, message: forgeE2EMessageSubjectBuilt,
			mutate: stagedEdit("arm64", func(m map[string]any) {
				m["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "controller", "deploy-path", "imager-efi", "site-profiles"}
			})},
		{name: "a leg that says only what NVKR runs say", exit: 22, code: verify, message: forgeE2EMessageSubjectBuilt,
			mutate: stagedEdit("amd64", func(m map[string]any) { m["notNvkrBuilt"] = []any{"deploy-path", "imager-efi", "site-profiles"} })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var in forgeE2ESummaryInputs
			if tc.source == "nvkr-outputs" {
				in = forgeE2EBuildSummaryInputs(t, "full", forgeE2EArmStart)
			} else {
				in = forgeE2EBuildSubjectSummaryInputs(t, "full", forgeE2EArmStart)
			}
			out := in.path("summary.json")
			args := tc.mutate(t, in, in.writeArgs(out))

			forgeE2EExpectFailure(t, forgeE2ERun(t, forgeE2ESummaryTool, args...), tc.exit, tc.code, tc.message)
			if content, err := os.ReadFile(out); err == nil {
				t.Errorf("a refused write left a summary:\n%s", content)
			}
		})
	}
}

func TestForgeE2ESummaryAssertRefusesAnArtifactSourceThatDisagreesWithTheRest(t *testing.T) {
	const verify = "NVKR-E-VERIFY-FAILED"
	subject := forgeE2EBuildSubjectSummaryInputs(t, "full", forgeE2EArmStart)
	subjectBytes, err := os.ReadFile(forgeE2ERequireSummaryWritten(t, subject, subject.writeArgs(subject.path("summary.json"))))
	if err != nil {
		t.Fatal(err)
	}
	outputs := forgeE2EBuildSummaryInputs(t, "full", forgeE2EArmStart)
	outputsBytes, err := os.ReadFile(forgeE2ERequireSummaryWritten(t, outputs, outputs.writeArgs(outputs.path("summary.json"))))
	if err != nil {
		t.Fatal(err)
	}
	staged := func(s map[string]any, i int) map[string]any {
		return s["legs"].([]any)[i].(map[string]any)["staged"].(map[string]any)
	}
	someImage := func() map[string]any {
		var s map[string]any
		if err := json.Unmarshal(outputsBytes, &s); err != nil {
			t.Fatal(err)
		}
		return s["nodeImages"].([]any)[0].(map[string]any)
	}

	cases := []struct {
		name    string
		base    []byte
		edit    func(map[string]any)
		message string
	}{
		// A summary of a build from the subject commit.
		{"source removed", subjectBytes, func(s map[string]any) { delete(s, "artifactSource") }, "unexpected or missing top-level fields"},
		{"source of another name", subjectBytes, func(s map[string]any) { s["artifactSource"] = "nvkr-nightly" }, forgeE2EMessageSource},
		{"source emptied", subjectBytes, func(s map[string]any) { s["artifactSource"] = "" }, forgeE2EMessageSource},
		{"source that is not text", subjectBytes, func(s map[string]any) { s["artifactSource"] = true }, forgeE2EMessageSource},
		{"source relabelled as NVKR outputs", subjectBytes, func(s map[string]any) { s["artifactSource"] = "nvkr-outputs" }, forgeE2EMessageProducer},
		{"a producer run named for a build from the subject commit", subjectBytes, func(s map[string]any) {
			s["producer"] = map[string]any{"runId": "1234567890"}
		}, forgeE2EMessageProducer},
		{"the producer removed", subjectBytes, func(s map[string]any) { delete(s, "producer") }, "unexpected or missing top-level fields"},
		{"a node image added", subjectBytes, func(s map[string]any) { s["nodeImages"] = []any{someImage()} }, forgeE2EMessageImages},
		{"node images that are not a list", subjectBytes, func(s map[string]any) { s["nodeImages"] = nil }, forgeE2EMessageImages},
		{"a skew declared with no node image to differ", subjectBytes, func(s map[string]any) { s["nodeImageVersionSkew"] = true }, forgeE2EMessageSkew},
		{"a plan digest that is not the fixture's", subjectBytes, func(s map[string]any) { s["plan"].(map[string]any)["sha256"] = forgeE2EDigest("a") }, forgeE2EMessagePlan},
		{"a fixture digest that is not the plan's", subjectBytes, func(s map[string]any) { s["plan"].(map[string]any)["fixtureSha256"] = forgeE2EDigest("a") }, forgeE2EMessagePlan},
		{"a leg built from no commit", subjectBytes, func(s map[string]any) { staged(s, 0)["builtFromCommit"] = nil }, forgeE2EMessageCommit},
		{"a leg built from another commit", subjectBytes, func(s map[string]any) { staged(s, 1)["builtFromCommit"] = strings.Repeat("0", 40) }, forgeE2EMessageCommit},
		{"a harness at another commit than the legs were built from", subjectBytes, func(s map[string]any) {
			s["harness"].(map[string]any)["commit"] = strings.Repeat("0", 40)
		}, forgeE2EMessageCommit},
		{"a subject at another commit than the legs were built from", subjectBytes, func(s map[string]any) {
			s["subject"].(map[string]any)["commit"] = strings.Repeat("0", 40)
		}, forgeE2EMessageCommit},
		{"a leg that staged from NVKR outputs", subjectBytes, func(s map[string]any) { staged(s, 0)["source"] = "nvkr-outputs" }, forgeE2EMessageLegSource},
		{"a leg with no source", subjectBytes, func(s map[string]any) { delete(staged(s, 0), "source") }, forgeE2EMessageStaged},
		{"a leg with no commit field", subjectBytes, func(s map[string]any) { delete(staged(s, 1), "builtFromCommit") }, forgeE2EMessageStaged},
		{"a leg that no longer lists the controller as not NVKR-built", subjectBytes, func(s map[string]any) {
			staged(s, 0)["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "deploy-path", "imager-efi", "node-image", "site-profiles"}
		}, forgeE2EMessageSubjectBuilt},
		{"a leg that no longer lists the node image as not NVKR-built", subjectBytes, func(s map[string]any) {
			staged(s, 1)["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "controller", "deploy-path", "imager-efi", "site-profiles"}
		}, forgeE2EMessageSubjectBuilt},
		{"a leg that no longer lists the deploy path as not NVKR-built", subjectBytes, func(s map[string]any) {
			staged(s, 0)["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "controller", "imager-efi", "node-image", "site-profiles"}
		}, forgeE2EMessageUnbuilt},
		{"a leg that lists nothing as not NVKR-built", subjectBytes, func(s map[string]any) { staged(s, 0)["notNvkrBuilt"] = []any{} }, forgeE2EMessageUnbuilt},

		// A summary of NVKR outputs.
		{"NVKR outputs relabelled as a build from the subject commit", outputsBytes, func(s map[string]any) { s["artifactSource"] = "build-subject" }, forgeE2EMessageProducer},
		{"NVKR outputs with the producer nulled", outputsBytes, func(s map[string]any) { s["producer"] = nil }, forgeE2EMessageProducer},
		{"NVKR outputs with a plan digest equal to the fixture's", outputsBytes, func(s map[string]any) {
			plan := s["plan"].(map[string]any)
			plan["sha256"] = plan["fixtureSha256"]
		}, forgeE2EMessagePlan},
		{"NVKR outputs with no node images", outputsBytes, func(s map[string]any) { s["nodeImages"] = []any{} }, forgeE2EMessageImages},
		{"NVKR outputs whose leg names a commit", outputsBytes, func(s map[string]any) { staged(s, 0)["builtFromCommit"] = forgeE2ECommit }, forgeE2EMessageCommit},
		{"NVKR outputs whose leg says it was built from the subject commit", outputsBytes, func(s map[string]any) {
			staged(s, 1)["source"] = "build-subject"
		}, forgeE2EMessageLegSource},
		{"NVKR outputs whose leg no longer lists the site profiles as not NVKR-built", outputsBytes, func(s map[string]any) {
			staged(s, 0)["notNvkrBuilt"] = []any{"deploy-path", "imager-efi"}
		}, forgeE2EMessageUnbuilt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var summary map[string]any
			if err := json.Unmarshal(tc.base, &summary); err != nil {
				t.Fatal(err)
			}
			tc.edit(summary)
			tampered := filepath.Join(t.TempDir(), "summary.json")
			forgeE2EWriteMap(t, tampered, summary)
			forgeE2EExpectFailure(t, forgeE2ERun(t, forgeE2ESummaryTool, "assert", tampered), 22, verify, tc.message)
		})
	}

	t.Run("the untampered summaries pass", func(t *testing.T) {
		for name, content := range map[string][]byte{"build-subject": subjectBytes, "nvkr-outputs": outputsBytes} {
			filename := filepath.Join(t.TempDir(), "summary.json")
			if err := os.WriteFile(filename, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if result := forgeE2ERun(t, forgeE2ESummaryTool, "assert", filename); result.exit != 0 {
				t.Errorf("assert %s: exit %d\n%s", name, result.exit, result.output)
			}
		}
	})
}
