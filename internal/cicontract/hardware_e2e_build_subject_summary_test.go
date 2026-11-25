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
	hardwareE2EMessageSource       = "artifactSource must be nvkr-outputs or build-subject"
	hardwareE2EMessageProducer     = "producer must name a run id for NVKR outputs and be null for a build from the subject commit"
	hardwareE2EMessagePlan         = "a build from the subject commit must run the plan as generated, and a run of NVKR outputs must have changed it"
	hardwareE2EMessageImages       = "nodeImages must be empty for a build from the subject commit, and nonempty https locations with a sha256 for NVKR outputs"
	hardwareE2EMessageLegSource    = "every leg must have staged from the artifact source the summary names"
	hardwareE2EMessageCommit       = "a build from the subject commit must name that commit as the harness commit and the plan's, and NVKR outputs must name none"
	hardwareE2EMessageUnbuilt      = "every leg must say that the deploy path, the imager EFI and the site profiles are not NVKR-built"
	hardwareE2EMessageSubjectBuilt = "a build from the subject commit must say that the controller, capi-provisioned, the CLI and the node image are not NVKR-built"
	hardwareE2EMessageSkew         = "nodeImageVersionSkew must say whether any node image version differs from the subject version"
	hardwareE2EMessageStaged       = "every leg must record what it staged and what is not NVKR-built"
)

func TestHardwareE2ESummaryBuildSubjectRecordsAFullRunAndSaysNothingIsNvkrBuilt(t *testing.T) {
	root := hardwareE2ERoot(t)
	generated, err := os.ReadFile(filepath.Join(root, hardwareE2EPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	in := hardwareE2EBuildSubjectSummaryInputs(t, "full", hardwareE2EArmStart)
	out := in.path("summary.json")

	result := hardwareE2ERun(t, hardwareE2ESummaryTool, append(in.writeArgs(out), "--node-user", "node-admin")...)
	if result.exit != 0 {
		t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
	}
	if !strings.Contains(result.output, "qualificationAuthority none") {
		t.Errorf("the script should say it recorded no authority:\n%s", result.output)
	}

	summary := hardwareE2EReadSummary(t, out)
	raw := hardwareE2EReadMap(t, out)
	for _, check := range []struct {
		name      string
		got, want any
	}{
		{"schema", summary.Schema, "nvkr.dev/hardware-e2e-summary/v1"},
		{"authority", summary.QualificationAuthority, "none"},
		{"artifact source", summary.ArtifactSource, "build-subject"},
		{"complete", summary.Complete, true},
		{"harness commit", summary.Harness.Commit, hardwareE2ECommit},
		{"subject commit", summary.Subject.Commit, hardwareE2ECommit},
		{"subject version", summary.Subject.Version, hardwareE2EVersion},
		{"plan digest", summary.Plan.SHA256, hardwareE2ESHA256(generated)},
		{"generated plan digest", summary.Plan.GeneratedSHA256, hardwareE2ESHA256(generated)},
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
	if got := slices.Sorted(maps.Keys(raw)); !slices.Equal(got, hardwareE2ESummaryFields) {
		t.Errorf("summary fields = %v, want %v", got, hardwareE2ESummaryFields)
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
		if staged["source"] != "build-subject" || staged["builtFromCommit"] != hardwareE2ECommit {
			t.Errorf("leg staged = %v, want build-subject at %s", staged, hardwareE2ECommit)
		}
	}
	if asserted := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", out); asserted.exit != 0 {
		t.Errorf("assert of a written summary: exit %d\n%s", asserted.exit, asserted.output)
	}
}

// A build from the subject commit dispatches the amd64 stream only, so its summary names one leg of the
// two the plan has streams for. It is a record of that run, and it must say the run is not complete.
func TestHardwareE2ESummaryBuildSubjectWithOnlyTheAmd64LegIsRecordedAsIncomplete(t *testing.T) {
	in := hardwareE2EBuildSubjectSummaryInputs(t, "full", hardwareE2EArmStart)
	hardwareE2EEditJSON(t, in.path("matrix.json"), func(m map[string]any) { m["include"] = m["include"].([]any)[:1] })
	args := hardwareE2EDropFlag(in.writeArgs(in.path("summary.json")), "--leg", in.leg("arm64"))
	out := hardwareE2ERequireSummaryWritten(t, in, args)

	summary := hardwareE2EReadSummary(t, out)
	if summary.ArtifactSource != "build-subject" || summary.QualificationAuthority != "none" {
		t.Errorf("source %q, authority %q, want build-subject and none", summary.ArtifactSource, summary.QualificationAuthority)
	}
	if summary.Complete {
		t.Error("a build from the subject commit that ran one of the plan's two streams is complete")
	}
	if len(summary.Legs) != 1 || summary.Legs[0].Arch != "amd64" || summary.PlannedStreams != 2 {
		t.Errorf("legs = %v, planned streams = %d, want the amd64 leg alone and 2 planned streams", summary.Legs, summary.PlannedStreams)
	}
	if asserted := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", out); asserted.exit != 0 {
		t.Errorf("assert: exit %d\n%s", asserted.exit, asserted.output)
	}
	// Edited to claim the whole plan ran, the same summary is refused.
	hardwareE2EEditJSON(t, out, func(s map[string]any) { s["complete"] = true })
	hardwareE2EExpectFailure(t, hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", out), 22, "NVKR-E-VERIFY-FAILED",
		"complete must be true exactly for a full-stage run")
}

func TestHardwareE2ESummaryBuildSubjectWriteRefusesWhatItCannotSupport(t *testing.T) {
	const verify = "NVKR-E-VERIFY-FAILED"
	otherCommit := strings.Repeat("0", 40)
	stagedEdit := func(arch string, edit func(map[string]any)) hardwareE2EMutation {
		return func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
			t.Helper()
			hardwareE2EEditJSON(t, filepath.Join(in.leg(arch), "staged.json"), edit)
			return a
		}
	}
	cases := []struct {
		name    string
		source  string // the inputs' artifact source: build-subject unless it says otherwise
		mutate  hardwareE2EMutation
		exit    int
		code    string
		message string
	}{
		// Admission of the source itself.
		{name: "no artifact source", exit: 2, code: "NVKR-E-USAGE",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2EDropFlag(a, "--artifact-source", "")
			})},
		{name: "an artifact source the lane does not have", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "unsupported artifact source: nvkr-nightly",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(a, "--artifact-source", "nvkr-nightly")
			})},
		{name: "an artifact source in the wrong case", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "unsupported artifact source",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(a, "--artifact-source", "Build-Subject")
			})},
		{name: "a node image check taken for a build from the subject commit", exit: 2, code: "NVKR-E-USAGE", message: "node image checks are taken only for NVKR outputs",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				check := hardwareE2EWriteJSON(t, in.scratch, "check-os-image-amd64.json", hardwareE2ENodeImageCheck(hardwareE2EOutputs(), "os-image-amd64"))
				return append(slices.Clone(a), "--node-image-check", check)
			}},

		// The flag and the substitution record must agree on the source.
		{name: "build-subject named for a record of NVKR outputs", source: "nvkr-outputs", exit: 22, code: verify,
			message: "the substitution record names artifact source nvkr-outputs, not build-subject",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				a = hardwareE2ESetFlag(a, "--artifact-source", "build-subject")
				return hardwareE2EDropFlag(a, "--node-image-check", "")
			})},
		{name: "nvkr-outputs named for a record of a build from the subject commit", exit: 22, code: verify,
			message: "the substitution record names artifact source build-subject, not nvkr-outputs",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				a = hardwareE2ESetFlag(a, "--artifact-source", "nvkr-outputs")
				for _, slot := range hardwareE2ENodeImageSlots {
					check := hardwareE2EWriteJSON(t, in.scratch, "check-"+slot+".json", hardwareE2ENodeImageCheck(hardwareE2EOutputs(), slot))
					a = append(a, "--node-image-check", check)
				}
				return a
			}},
		{name: "a record that names no artifact source", exit: 22, code: verify, message: "the substitution record names artifact source none, not build-subject",
			mutate: hardwareE2EEdit(func(m map[string]any) { delete(m, "artifactSource") }, "sub", "substitutions.json")},

		// What the plan, the legs and the commits must say.
		{name: "a plan that was modified", exit: 22, code: verify, message: hardwareE2EMessagePlan,
			mutate: hardwareE2EEdit(func(m map[string]any) { m["note"] = "edited" }, "sub", "plan.json")},
		{name: "a run of NVKR outputs whose plan is the one generated", source: "nvkr-outputs", exit: 22, code: verify, message: hardwareE2EMessagePlan,
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				generated, err := os.ReadFile(filepath.Join(hardwareE2ERoot(t), hardwareE2EPlanFile))
				if err != nil {
					t.Fatal(err)
				}
				// Make the record and the checks name the generated plan's own node image locations, so that
				// only the rule about an unchanged plan can refuse this run.
				var plan map[string]any
				if err := json.Unmarshal(generated, &plan); err != nil {
					t.Fatal(err)
				}
				for _, slot := range []string{"os-image-amd64", "os-image-arm64"} {
					url := hardwareE2EPlanNodeURLOf(t, plan, slot)
					hardwareE2ENodeImageBoth(slot, "to", "url", url)(t, in, a)
				}
				// The generated plan's exact bytes go last: the edits above re-encode the plan they touch.
				if err := os.WriteFile(in.path("sub", "plan.json"), generated, 0o600); err != nil {
					t.Fatal(err)
				}
				return a
			}},
		{name: "a leg that staged from the other source", exit: 22, code: verify, message: hardwareE2EMessageLegSource,
			mutate: stagedEdit("amd64", func(m map[string]any) { m["source"] = "nvkr-outputs"; m["builtFromCommit"] = nil })},
		{name: "a leg built from the subject commit in a run of NVKR outputs", source: "nvkr-outputs", exit: 22, code: verify, message: hardwareE2EMessageLegSource,
			mutate: stagedEdit("arm64", func(m map[string]any) { m["source"] = "build-subject"; m["builtFromCommit"] = hardwareE2ECommit })},
		{name: "a leg built from another commit", exit: 22, code: verify, message: hardwareE2EMessageCommit,
			mutate: stagedEdit("arm64", func(m map[string]any) { m["builtFromCommit"] = otherCommit })},
		{name: "a leg that names no commit", exit: 22, code: verify, message: hardwareE2EMessageCommit,
			mutate: stagedEdit("amd64", func(m map[string]any) { m["builtFromCommit"] = nil })},
		{name: "a leg of NVKR outputs that names a commit", source: "nvkr-outputs", exit: 22, code: verify, message: hardwareE2EMessageCommit,
			mutate: stagedEdit("amd64", func(m map[string]any) { m["builtFromCommit"] = hardwareE2ECommit })},
		{name: "a harness at another commit than the subject", exit: 22, code: verify, message: hardwareE2EMessageCommit,
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(a, "--harness-commit", otherCommit)
			})},
		// A record without these fields reaches the summary as null, which no run may claim.
		{name: "a leg record with no source field", exit: 22, code: verify, message: hardwareE2EMessageLegSource,
			mutate: stagedEdit("amd64", func(m map[string]any) { delete(m, "source") })},
		{name: "a leg record with no commit field", exit: 22, code: verify, message: hardwareE2EMessageCommit,
			mutate: stagedEdit("amd64", func(m map[string]any) { delete(m, "builtFromCommit") })},

		// What is not NVKR-built.
		{name: "a leg that does not say the deploy path is not NVKR-built", exit: 22, code: verify, message: hardwareE2EMessageUnbuilt,
			mutate: stagedEdit("amd64", func(m map[string]any) {
				m["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "controller", "imager-efi", "node-image", "site-profiles"}
			})},
		{name: "a leg that does not say the site profiles are not NVKR-built", source: "nvkr-outputs", exit: 22, code: verify, message: hardwareE2EMessageUnbuilt,
			mutate: stagedEdit("arm64", func(m map[string]any) { m["notNvkrBuilt"] = []any{"deploy-path", "imager-efi"} })},
		{name: "a leg that does not say the controller is not NVKR-built", exit: 22, code: verify, message: hardwareE2EMessageSubjectBuilt,
			mutate: stagedEdit("amd64", func(m map[string]any) {
				m["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "deploy-path", "imager-efi", "node-image", "site-profiles"}
			})},
		{name: "a leg that does not say the node image is not NVKR-built", exit: 22, code: verify, message: hardwareE2EMessageSubjectBuilt,
			mutate: stagedEdit("arm64", func(m map[string]any) {
				m["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "controller", "deploy-path", "imager-efi", "site-profiles"}
			})},
		{name: "a leg that says only what NVKR runs say", exit: 22, code: verify, message: hardwareE2EMessageSubjectBuilt,
			mutate: stagedEdit("amd64", func(m map[string]any) { m["notNvkrBuilt"] = []any{"deploy-path", "imager-efi", "site-profiles"} })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var in hardwareE2ESummaryInputs
			if tc.source == "nvkr-outputs" {
				in = hardwareE2EBuildSummaryInputs(t, "full", hardwareE2EArmStart)
			} else {
				in = hardwareE2EBuildSubjectSummaryInputs(t, "full", hardwareE2EArmStart)
			}
			out := in.path("summary.json")
			args := tc.mutate(t, in, in.writeArgs(out))

			hardwareE2EExpectFailure(t, hardwareE2ERun(t, hardwareE2ESummaryTool, args...), tc.exit, tc.code, tc.message)
			if content, err := os.ReadFile(out); err == nil {
				t.Errorf("a refused write left a summary:\n%s", content)
			}
		})
	}
}

func TestHardwareE2ESummaryAssertRefusesAnArtifactSourceThatDisagreesWithTheRest(t *testing.T) {
	const verify = "NVKR-E-VERIFY-FAILED"
	subject := hardwareE2EBuildSubjectSummaryInputs(t, "full", hardwareE2EArmStart)
	subjectBytes, err := os.ReadFile(hardwareE2ERequireSummaryWritten(t, subject, subject.writeArgs(subject.path("summary.json"))))
	if err != nil {
		t.Fatal(err)
	}
	outputs := hardwareE2EBuildSummaryInputs(t, "full", hardwareE2EArmStart)
	outputsBytes, err := os.ReadFile(hardwareE2ERequireSummaryWritten(t, outputs, outputs.writeArgs(outputs.path("summary.json"))))
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
		{"source of another name", subjectBytes, func(s map[string]any) { s["artifactSource"] = "nvkr-nightly" }, hardwareE2EMessageSource},
		{"source emptied", subjectBytes, func(s map[string]any) { s["artifactSource"] = "" }, hardwareE2EMessageSource},
		{"source that is not text", subjectBytes, func(s map[string]any) { s["artifactSource"] = true }, hardwareE2EMessageSource},
		{"source relabelled as NVKR outputs", subjectBytes, func(s map[string]any) { s["artifactSource"] = "nvkr-outputs" }, hardwareE2EMessageProducer},
		{"a producer run named for a build from the subject commit", subjectBytes, func(s map[string]any) {
			s["producer"] = map[string]any{"runId": "1234567890"}
		}, hardwareE2EMessageProducer},
		{"the producer removed", subjectBytes, func(s map[string]any) { delete(s, "producer") }, "unexpected or missing top-level fields"},
		{"a node image added", subjectBytes, func(s map[string]any) { s["nodeImages"] = []any{someImage()} }, hardwareE2EMessageImages},
		{"node images that are not a list", subjectBytes, func(s map[string]any) { s["nodeImages"] = nil }, hardwareE2EMessageImages},
		{"a skew declared with no node image to differ", subjectBytes, func(s map[string]any) { s["nodeImageVersionSkew"] = true }, hardwareE2EMessageSkew},
		{"a plan digest that is not the generated plan's", subjectBytes, func(s map[string]any) { s["plan"].(map[string]any)["sha256"] = hardwareE2EDigest("a") }, hardwareE2EMessagePlan},
		{"a generated plan digest that is not the plan's", subjectBytes, func(s map[string]any) { s["plan"].(map[string]any)["generatedSha256"] = hardwareE2EDigest("a") }, hardwareE2EMessagePlan},
		{"a plan with the old fixture digest field", subjectBytes, func(s map[string]any) {
			plan := s["plan"].(map[string]any)
			plan["fixtureSha256"] = plan["generatedSha256"]
			delete(plan, "generatedSha256")
		}, "plan must carry two sha256 digests"},
		{"a leg built from no commit", subjectBytes, func(s map[string]any) { staged(s, 0)["builtFromCommit"] = nil }, hardwareE2EMessageCommit},
		{"a leg built from another commit", subjectBytes, func(s map[string]any) { staged(s, 1)["builtFromCommit"] = strings.Repeat("0", 40) }, hardwareE2EMessageCommit},
		{"a harness at another commit than the legs were built from", subjectBytes, func(s map[string]any) {
			s["harness"].(map[string]any)["commit"] = strings.Repeat("0", 40)
		}, hardwareE2EMessageCommit},
		{"a subject at another commit than the legs were built from", subjectBytes, func(s map[string]any) {
			s["subject"].(map[string]any)["commit"] = strings.Repeat("0", 40)
		}, hardwareE2EMessageCommit},
		{"a leg that staged from NVKR outputs", subjectBytes, func(s map[string]any) { staged(s, 0)["source"] = "nvkr-outputs" }, hardwareE2EMessageLegSource},
		{"a leg with no source", subjectBytes, func(s map[string]any) { delete(staged(s, 0), "source") }, hardwareE2EMessageStaged},
		{"a leg with no commit field", subjectBytes, func(s map[string]any) { delete(staged(s, 1), "builtFromCommit") }, hardwareE2EMessageStaged},
		{"a leg that no longer lists the controller as not NVKR-built", subjectBytes, func(s map[string]any) {
			staged(s, 0)["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "deploy-path", "imager-efi", "node-image", "site-profiles"}
		}, hardwareE2EMessageSubjectBuilt},
		{"a leg that no longer lists the node image as not NVKR-built", subjectBytes, func(s map[string]any) {
			staged(s, 1)["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "controller", "deploy-path", "imager-efi", "site-profiles"}
		}, hardwareE2EMessageSubjectBuilt},
		{"a leg that no longer lists the deploy path as not NVKR-built", subjectBytes, func(s map[string]any) {
			staged(s, 0)["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "controller", "imager-efi", "node-image", "site-profiles"}
		}, hardwareE2EMessageUnbuilt},
		{"a leg that lists nothing as not NVKR-built", subjectBytes, func(s map[string]any) { staged(s, 0)["notNvkrBuilt"] = []any{} }, hardwareE2EMessageUnbuilt},

		// A summary of NVKR outputs.
		{"NVKR outputs relabelled as a build from the subject commit", outputsBytes, func(s map[string]any) { s["artifactSource"] = "build-subject" }, hardwareE2EMessageProducer},
		{"NVKR outputs with the producer nulled", outputsBytes, func(s map[string]any) { s["producer"] = nil }, hardwareE2EMessageProducer},
		{"NVKR outputs with a plan digest equal to the generated plan's", outputsBytes, func(s map[string]any) {
			plan := s["plan"].(map[string]any)
			plan["sha256"] = plan["generatedSha256"]
		}, hardwareE2EMessagePlan},
		{"NVKR outputs with no node images", outputsBytes, func(s map[string]any) { s["nodeImages"] = []any{} }, hardwareE2EMessageImages},
		{"NVKR outputs whose leg names a commit", outputsBytes, func(s map[string]any) { staged(s, 0)["builtFromCommit"] = hardwareE2ECommit }, hardwareE2EMessageCommit},
		{"NVKR outputs whose leg says it was built from the subject commit", outputsBytes, func(s map[string]any) {
			staged(s, 1)["source"] = "build-subject"
		}, hardwareE2EMessageLegSource},
		{"NVKR outputs whose leg no longer lists the site profiles as not NVKR-built", outputsBytes, func(s map[string]any) {
			staged(s, 0)["notNvkrBuilt"] = []any{"deploy-path", "imager-efi"}
		}, hardwareE2EMessageUnbuilt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var summary map[string]any
			if err := json.Unmarshal(tc.base, &summary); err != nil {
				t.Fatal(err)
			}
			tc.edit(summary)
			tampered := filepath.Join(t.TempDir(), "summary.json")
			hardwareE2EWriteMap(t, tampered, summary)
			hardwareE2EExpectFailure(t, hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", tampered), 22, verify, tc.message)
		})
	}

	t.Run("the untampered summaries pass", func(t *testing.T) {
		for name, content := range map[string][]byte{"build-subject": subjectBytes, "nvkr-outputs": outputsBytes} {
			filename := filepath.Join(t.TempDir(), "summary.json")
			if err := os.WriteFile(filename, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if result := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", filename); result.exit != 0 {
				t.Errorf("assert %s: exit %d\n%s", name, result.exit, result.output)
			}
		}
	})
}
