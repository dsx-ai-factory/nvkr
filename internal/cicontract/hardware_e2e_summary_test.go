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
	"time"
)

// These contracts cover the script that records what a hardware E2E run covered and
// refuses a record that claims more. Every input is synthetic or the committed plan fixture;
// nothing here is evidence about a run on hardware.

const (
	hardwareE2ESummaryTool        = "scripts/hardware-e2e-summary.sh"
	hardwareE2EOrchestratorCommit = "81c8f1e2a9b3d4c5e6f708192a3b4c5d6e7f8091"
	hardwareE2EOrchestratorRun    = "9876543210"
	hardwareE2EAmdStart           = "2026-11-18T10:00:00Z"
	hardwareE2EArmStart           = "2026-11-18T12:00:00Z"
)

var hardwareE2ESummaryFields = []string{
	"armStartedAfterAmd", "artifactSource", "complete", "harness", "legs", "nodeImageVersionSkew", "nodeImages", "nodeUser",
	"orchestrator", "plan", "plannedStreams", "producer", "qualificationAuthority", "retainedFromNvke",
	"schema", "stage", "subject",
}

var hardwareE2ENodeImageSlots = []string{"os-image-amd64", "os-image-arm64"}

type hardwareE2ESummary struct {
	Schema                 string `json:"schema"`
	QualificationAuthority string `json:"qualificationAuthority"`
	ArtifactSource         string `json:"artifactSource"`
	Stage                  string `json:"stage"`
	Complete               bool   `json:"complete"`
	NodeUser               string `json:"nodeUser"`
	ArmStartedAfterAmd     bool   `json:"armStartedAfterAmd"`
	NodeImageVersionSkew   bool   `json:"nodeImageVersionSkew"`
	PlannedStreams         int    `json:"plannedStreams"`
	Harness                struct {
		Commit string `json:"commit"`
	} `json:"harness"`
	Subject struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"subject"`
	Orchestrator struct {
		Commit string `json:"commit"`
		RunID  string `json:"runId"`
	} `json:"orchestrator"`
	Producer struct {
		RunID string `json:"runId"`
	} `json:"producer"`
	Plan struct {
		SHA256        string `json:"sha256"`
		FixtureSHA256 string `json:"fixtureSha256"`
	} `json:"plan"`
	NodeImages []struct {
		Slot   string `json:"slot"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
		Source struct {
			Kind      string `json:"kind"`
			Reference string `json:"reference"`
			Version   string `json:"version"`
		} `json:"source"`
		Checked struct {
			Method            string `json:"method"`
			ObservedByThisRun bool   `json:"observedByThisRun"`
			Run               string `json:"run"`
			Bytes             int64  `json:"bytes"`
			QCOW2Version      int    `json:"qcow2Version"`
			RangeSupported    bool   `json:"rangeSupported"`
			VirtualSizeBytes  int64  `json:"virtualSizeBytes"`
		} `json:"checked"`
	} `json:"nodeImages"`
	Legs []struct {
		Arch      string `json:"arch"`
		Stream    string `json:"stream"`
		StartedAt string `json:"startedAt"`
		Coverage  struct {
			Complete bool `json:"complete"`
		} `json:"coverage"`
		Staged struct {
			NotNvkrBuilt []string `json:"notNvkrBuilt"`
		} `json:"staged"`
	} `json:"legs"`
}

func hardwareE2EReadMap(t *testing.T, filename string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode %s: %v", filename, err)
	}
	return value
}

func hardwareE2EWriteMap(t *testing.T, filename string, value map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hardwareE2EEditJSON(t *testing.T, filename string, edit func(map[string]any)) {
	t.Helper()
	value := hardwareE2EReadMap(t, filename)
	edit(value)
	hardwareE2EWriteMap(t, filename, value)
}

func hardwareE2EReadSummary(t *testing.T, filename string) hardwareE2ESummary {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var summary hardwareE2ESummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("decode %s: %v", filename, err)
	}
	return summary
}

func hardwareE2EExpectFailure(t *testing.T, result hardwareE2EResult, exit int, code, message string) {
	t.Helper()
	if result.exit != exit {
		t.Fatalf("exit %d, want %d:\n%s", result.exit, exit, result.output)
	}
	if !strings.Contains(result.output, "["+code+"]") {
		t.Errorf("output does not carry code %s:\n%s", code, result.output)
	}
	if message != "" && !strings.Contains(result.output, message) {
		t.Errorf("output does not say %q:\n%s", message, result.output)
	}
}

func hardwareE2ECoverage(stage string) map[string]any {
	passed := 80
	// Below the full stage the later specs skip by design: only the provisioning group passes here.
	categories := map[string]any{
		"provisioning": map[string]any{"selected": 7, "passed": 7},
		"site-checks":  map[string]any{"selected": 31, "passed": 31},
		"tenant":       map[string]any{"selected": 42, "passed": 42},
	}
	if stage != "full" {
		passed = 7
		categories["site-checks"] = map[string]any{"selected": 31, "passed": 0}
		categories["tenant"] = map[string]any{"selected": 42, "passed": 0}
	}
	return map[string]any{
		"schema":       "nvkr.dev/hardware-e2e-coverage/v1",
		"stage":        stage,
		"complete":     stage == "full",
		"selected":     80,
		"passed":       passed,
		"specsInSuite": 530,
		"labelFilter":  "site-controller && !nico-dsx",
		"tenantLabel":  hardwareE2ETenantLabel,
		"categories":   categories,
		"dryRunSha256": hardwareE2EDigest("1"),
		"reportSha256": hardwareE2EDigest("2"),
	}
}

// hardwareE2EStaged is what hardware-e2e-stage-inputs.sh records for a leg of NVKR outputs whose node
// image comes from another CI run, as the default synthetic manifest's does: the node image is named
// as not NVKR-built. hardwareE2EFinishSummaryInputs drops it for a leg whose image is an NVKR release.
func hardwareE2EStaged(arch string) map[string]any {
	return map[string]any{
		"schema": "nvkr.dev/hardware-e2e-staged/v1",
		"arch":   arch,
		"files": map[string]any{
			"controller.tar":        hardwareE2EDigest("3"),
			"capi-provisioned.tar":  hardwareE2EDigest("4"),
			"nvkectl-linux-" + arch: hardwareE2EDigest("5"),
		},
		"source":                 "nvkr-outputs",
		"builtFromCommit":        nil,
		"controllerVersionCheck": "matched",
		"notNvkrBuilt":           []any{"deploy-path", "imager-efi", "node-image", "site-profiles"},
	}
}

// hardwareE2ENodeSourceKindOf is the kind of source the substitution record gives an architecture's
// node image, as the staging script reads it.
func hardwareE2ENodeSourceKindOf(t *testing.T, substitutions, arch string) string {
	t.Helper()
	for _, entry := range hardwareE2EReadMap(t, substitutions)["replaced"].([]any) {
		replacement := entry.(map[string]any)
		if replacement["slot"] == "os-image-"+arch {
			return replacement["source"].(map[string]any)["kind"].(string)
		}
	}
	t.Fatalf("the substitution record has no node image for %s", arch)
	return ""
}

// hardwareE2EReplacedTo is the reference or location the substitution record gives a slot.
func hardwareE2EReplacedTo(t *testing.T, substitutions, slot string) string {
	t.Helper()
	for _, entry := range hardwareE2EReadMap(t, substitutions)["replaced"].([]any) {
		if replacement := entry.(map[string]any); replacement["slot"] == slot {
			return replacement["to"].(string)
		}
	}
	t.Fatalf("the substitution record has no replacement for %s", slot)
	return ""
}

// hardwareE2EStagedBuilt is what hardware-e2e-build-subject.sh records for a leg: everything built from
// the subject commit, and nothing NVKR-built.
func hardwareE2EStagedBuilt(arch string) map[string]any {
	staged := hardwareE2EStaged(arch)
	staged["source"] = "build-subject"
	staged["builtFromCommit"] = hardwareE2ECommit
	staged["controllerVersionCheck"] = "not-determined"
	staged["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "controller", "deploy-path", "imager-efi", "node-image", "site-profiles"}
	return staged
}

// hardwareE2ENodeImageBytes is the size every synthetic node image check reports.
const hardwareE2ENodeImageBytes = 538 * 1000 * 1000

// hardwareE2ENodeImageRule is the summary rule that holds each node image's check record.
const hardwareE2ENodeImageRule = "each node image must record how it was checked, whether this run observed that, and what the check saw"

// hardwareE2ECoverageGroupsRule is the summary rule that holds each leg's per-group coverage counts.
const hardwareE2ECoverageGroupsRule = "every leg coverage must count its groups, the groups must add up to the totals"

// hardwareE2ENodeBuiltRule is the summary rule that ties a leg's list of what NVKR did not build to the
// source of its node image.
const hardwareE2ENodeBuiltRule = "a node image whose source is not an NVKR release must be named as not NVKR-built"

// hardwareE2ENodeImageCheck is what the check-node-image subcommand of internal/hwe2e writes for a node image, built
// from the same synthetic outputs manifest the substitution script was given.
func hardwareE2ENodeImageCheck(manifest map[string]any, slot string) map[string]any {
	image := hardwareE2ESlots(manifest)[slot].(map[string]any)
	if verification := image["verification"].(map[string]any); verification["method"] == "s3-readback" {
		// The check does not fetch such an image: it records what the publication run reported.
		return map[string]any{
			"schema":            "nvkr.dev/hardware-e2e-node-image-check/v1",
			"slot":              slot,
			"method":            "s3-readback",
			"observedByThisRun": false,
			"run":               verification["run"],
			"url":               image["url"],
			"sha256":            image["sha256"],
			"bytes":             verification["bytes"],
			"qcow2":             map[string]any{"version": verification["qcow2Version"], "virtualSizeBytes": verification["virtualSizeBytes"]},
		}
	}
	return map[string]any{
		"schema":            "nvkr.dev/hardware-e2e-node-image-check/v1",
		"slot":              slot,
		"method":            "http-get",
		"observedByThisRun": true,
		"url":               image["url"],
		"sha256":            image["sha256"],
		"bytes":             hardwareE2ENodeImageBytes,
		"rangeSupported":    false,
		"acceptRanges":      "",
		"qcow2":             map[string]any{"version": 3, "virtualSizeBytes": 12 << 30, "clusterSizeBytes": 65536},
	}
}

type hardwareE2ESummaryStream struct{ name, arch string }

// hardwareE2ESummaryInputs is a scratch directory holding everything `write` consumes: the
// substituted plan and its record (from the real substitution script), the matrix, and one
// stamped, covered and staged directory per stream.
type hardwareE2ESummaryInputs struct {
	scratch       string
	stage         string
	source        string
	harnessCommit string
	combination   string
	streams       []hardwareE2ESummaryStream
}

func (in hardwareE2ESummaryInputs) path(parts ...string) string {
	return filepath.Join(append([]string{in.scratch}, parts...)...)
}

func (in hardwareE2ESummaryInputs) leg(arch string) string { return in.path("leg-" + arch) }

func (in hardwareE2ESummaryInputs) nodeCheck(slot string) string {
	return in.path("check-" + slot + ".json")
}

func (in hardwareE2ESummaryInputs) writeArgs(out string) []string {
	args := []string{
		"write", out,
		"--artifact-source", in.source,
		"--stage", in.stage,
		"--harness-commit", in.harnessCommit,
		"--orchestrator-commit", hardwareE2EOrchestratorCommit,
		"--orchestrator-run", hardwareE2EOrchestratorRun,
		"--plan", in.path("sub", "plan.json"),
		"--substitutions", in.path("sub", "substitutions.json"),
		"--matrix", in.path("matrix.json"),
	}
	if in.source == "nvkr-outputs" {
		for _, slot := range hardwareE2ENodeImageSlots {
			args = append(args, "--node-image-check", in.nodeCheck(slot))
		}
	}
	for _, stream := range in.streams {
		args = append(args, "--leg", in.leg(stream.arch))
	}
	return args
}

func hardwareE2EBuildSummaryInputs(t *testing.T, stage, armStart string) hardwareE2ESummaryInputs {
	t.Helper()
	return hardwareE2EBuildSummaryInputsFrom(t, stage, armStart, hardwareE2EOutputs())
}

// hardwareE2EBuildSummaryInputsFrom builds the inputs from the given outputs manifest, running the
// real substitution script with any extra leading flags.
func hardwareE2EBuildSummaryInputsFrom(t *testing.T, stage, armStart string, manifest map[string]any, substituteFlags ...string) hardwareE2ESummaryInputs {
	t.Helper()
	root := hardwareE2ERoot(t)
	in := hardwareE2ESummaryInputs{scratch: t.TempDir(), stage: stage, source: "nvkr-outputs", harnessCommit: hardwareE2ECommit}
	outputs := hardwareE2EWriteJSON(t, in.scratch, "outputs.json", manifest)
	for _, slot := range hardwareE2ENodeImageSlots {
		hardwareE2EWriteJSON(t, in.scratch, "check-"+slot+".json", hardwareE2ENodeImageCheck(manifest, slot))
	}
	substituted := hardwareE2ERun(t, hardwareE2ESubstituteTool, append(slices.Clone(substituteFlags),
		filepath.Join(root, hardwareE2EPlanFile), filepath.Join(root, hardwareE2EProvenanceFile), outputs, in.path("sub"))...)
	if substituted.exit != 0 {
		t.Fatalf("substitute plan: exit %d\n%s", substituted.exit, substituted.output)
	}
	return hardwareE2EFinishSummaryInputs(t, in, armStart)
}

// hardwareE2EBuildSubjectSummaryInputs builds the inputs for a run whose artifacts were built from the
// subject commit: the plan is kept as the fixture, there are no node image checks, and each leg
// staged what hardware-e2e-build-subject.sh records.
func hardwareE2EBuildSubjectSummaryInputs(t *testing.T, stage, armStart string) hardwareE2ESummaryInputs {
	t.Helper()
	root := hardwareE2ERoot(t)
	return hardwareE2EBuildSubjectSummaryInputsFrom(t, stage, armStart,
		filepath.Join(root, hardwareE2EPlanFile), filepath.Join(root, hardwareE2EProvenanceFile), hardwareE2ECommit)
}

// hardwareE2EBuildSubjectSummaryInputsFrom takes the plan, its provenance and the harness commit the
// legs were built at, for a test that needs a subject commit it can check out.
func hardwareE2EBuildSubjectSummaryInputsFrom(t *testing.T, stage, armStart, planFile, provenanceFile, harnessCommit string) hardwareE2ESummaryInputs {
	t.Helper()
	in := hardwareE2ESummaryInputs{scratch: t.TempDir(), stage: stage, source: "build-subject", harnessCommit: harnessCommit}
	substituted := hardwareE2ERun(t, hardwareE2ESubstituteTool, "--build-subject", planFile, provenanceFile, in.path("sub"))
	if substituted.exit != 0 {
		t.Fatalf("keep plan: exit %d\n%s", substituted.exit, substituted.output)
	}
	return hardwareE2EFinishSummaryInputs(t, in, armStart)
}

// hardwareE2EFinishSummaryInputs reads the plan in in.path("sub") and writes the matrix and one
// stamped, covered and staged directory per stream, staged as the artifact source says.
func hardwareE2EFinishSummaryInputs(t *testing.T, in hardwareE2ESummaryInputs, armStart string) hardwareE2ESummaryInputs {
	t.Helper()
	stage := in.stage
	var plan struct {
		Combinations []struct {
			ID      string `json:"id"`
			Streams []struct {
				Name string `json:"name"`
			} `json:"streams"`
		} `json:"combinations"`
	}
	data, err := os.ReadFile(in.path("sub", "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Combinations) != 1 {
		t.Fatalf("the fixture has %d combinations, want 1", len(plan.Combinations))
	}
	in.combination = plan.Combinations[0].ID
	var amd, arm []hardwareE2ESummaryStream
	for _, stream := range plan.Combinations[0].Streams {
		switch {
		case strings.Contains(stream.Name, "-amd64-"):
			amd = append(amd, hardwareE2ESummaryStream{stream.Name, "amd64"})
		case strings.Contains(stream.Name, "-arm64-"):
			arm = append(arm, hardwareE2ESummaryStream{stream.Name, "arm64"})
		default:
			t.Fatalf("stream %q names no architecture", stream.Name)
		}
	}
	in.streams = append(amd, arm...)

	var matrix []map[string]any
	for _, stream := range in.streams {
		matrix = append(matrix, hardwareE2EStream(in.combination, stream.name, stream.arch, hardwareE2EHarnessProvider))
		directory := in.leg(stream.arch)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		start := hardwareE2EAmdStart
		if stream.arch == "arm64" {
			start = armStart
		}
		stamped := hardwareE2ERunEnv(t, []string{"HARDWARE_E2E_NOW=" + start}, hardwareE2ESummaryTool,
			"leg", directory, stream.name, in.combination, stream.arch)
		if stamped.exit != 0 {
			t.Fatalf("stamp %s: exit %d\n%s", stream.name, stamped.exit, stamped.output)
		}
		hardwareE2EWriteJSON(t, directory, "coverage.json", hardwareE2ECoverage(stage))
		staged := hardwareE2EStaged(stream.arch)
		if in.source == "build-subject" {
			staged = hardwareE2EStagedBuilt(stream.arch)
		} else if hardwareE2ENodeSourceKindOf(t, in.path("sub", "substitutions.json"), stream.arch) == "nvkr-release" {
			// Only an NVKR release is claimed as NVKR-built.
			staged["notNvkrBuilt"] = []any{"deploy-path", "imager-efi", "site-profiles"}
		}
		if in.source == "nvkr-outputs" {
			substitutions := in.path("sub", "substitutions.json")
			staged["pulledByDigest"] = map[string]any{
				"controller":         hardwareE2EReplacedTo(t, substitutions, "controller"),
				"capi-provisioned":   hardwareE2EReplacedTo(t, substitutions, "capi-provisioned"),
				"cli-" + stream.arch: hardwareE2EReplacedTo(t, substitutions, "cli-"+stream.arch),
			}
		}
		hardwareE2EWriteJSON(t, directory, "staged.json", staged)
	}
	hardwareE2EMatrixFile(t, in.scratch, matrix...)
	return in
}

func hardwareE2ESetFlag(args []string, flag, value string) []string {
	args = slices.Clone(args)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			args[i+1] = value
			return args
		}
	}
	return append(args, flag, value)
}

// hardwareE2EDropFlag removes a flag and its value; an empty value removes every occurrence.
func hardwareE2EDropFlag(args []string, flag, value string) []string {
	var kept []string
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) && (value == "" || args[i+1] == value) {
			i++
			continue
		}
		kept = append(kept, args[i])
	}
	return kept
}

func hardwareE2ERequireSummaryWritten(t *testing.T, in hardwareE2ESummaryInputs, args []string) string {
	t.Helper()
	result := hardwareE2ERun(t, hardwareE2ESummaryTool, args...)
	if result.exit != 0 {
		t.Fatalf("write: exit %d, want 0:\n%s", result.exit, result.output)
	}
	return args[1]
}

func TestHardwareE2ESummaryRecordsAFullRunAndDescribesItself(t *testing.T) {
	root := hardwareE2ERoot(t)
	in := hardwareE2EBuildSummaryInputs(t, "full", hardwareE2EArmStart)
	out := in.path("summary.json")

	result := hardwareE2ERun(t, hardwareE2ESummaryTool, append(in.writeArgs(out), "--node-user", "node-admin")...)
	if result.exit != 0 {
		t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
	}
	if !strings.Contains(result.output, "qualificationAuthority none") {
		t.Errorf("the script should say it recorded no authority:\n%s", result.output)
	}

	summary := hardwareE2EReadSummary(t, out)
	plan := hardwareE2EReadPlan(t, filepath.Join(root, hardwareE2EPlanFile))
	substituted, err := os.ReadFile(in.path("sub", "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join(root, hardwareE2EPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	slots := hardwareE2ESlots(hardwareE2EOutputs())
	image := func(slot string) map[string]any { return slots[slot].(map[string]any) }

	checks := []struct {
		name      string
		got, want any
	}{
		{"schema", summary.Schema, "nvkr.dev/hardware-e2e-summary/v1"},
		{"authority", summary.QualificationAuthority, "none"},
		{"stage", summary.Stage, "full"},
		{"complete", summary.Complete, true},
		{"nodeUser", summary.NodeUser, "node-admin"},
		{"harness commit", summary.Harness.Commit, hardwareE2ECommit},
		{"subject commit", summary.Subject.Commit, plan.Commit},
		{"subject version", summary.Subject.Version, plan.Version},
		{"orchestrator commit", summary.Orchestrator.Commit, hardwareE2EOrchestratorCommit},
		{"orchestrator run", summary.Orchestrator.RunID, hardwareE2EOrchestratorRun},
		{"producer run", summary.Producer.RunID, "1234567890"},
		{"substituted plan digest", summary.Plan.SHA256, hardwareE2ESHA256(substituted)},
		{"fixture digest", summary.Plan.FixtureSHA256, hardwareE2ESHA256(fixture)},
		{"planned streams", summary.PlannedStreams, 2},
		{"arm started after amd", summary.ArmStartedAfterAmd, true},
		{"node images", len(summary.NodeImages), 2},
		{"first node image slot", summary.NodeImages[0].Slot, "os-image-amd64"},
		{"first node image url", summary.NodeImages[0].URL, image("os-image-amd64")["url"]},
		{"first node image digest", summary.NodeImages[0].SHA256, image("os-image-amd64")["sha256"]},
		{"second node image url", summary.NodeImages[1].URL, image("os-image-arm64")["url"]},
		{"node image version skew", summary.NodeImageVersionSkew, false},
		{"first node image source kind", summary.NodeImages[0].Source.Kind, "ci-run"},
		{"first node image source reference", summary.NodeImages[0].Source.Reference, "https://ci.example.test/runs/9001"},
		{"first node image source version", summary.NodeImages[0].Source.Version, hardwareE2EVersion},
		{"second node image source version", summary.NodeImages[1].Source.Version, hardwareE2EVersion},
		{"first node image bytes seen", summary.NodeImages[0].Checked.Bytes, int64(hardwareE2ENodeImageBytes)},
		{"first node image qcow2 version", summary.NodeImages[0].Checked.QCOW2Version, 3},
		{"first node image virtual size", summary.NodeImages[0].Checked.VirtualSizeBytes, int64(12 << 30)},
		{"first node image range support", summary.NodeImages[0].Checked.RangeSupported, false},
		{"legs", len(summary.Legs), 2},
		{"first leg arch", summary.Legs[0].Arch, "amd64"},
		{"first leg start", summary.Legs[0].StartedAt, hardwareE2EAmdStart},
		{"second leg arch", summary.Legs[1].Arch, "arm64"},
		{"second leg start", summary.Legs[1].StartedAt, hardwareE2EArmStart},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}

	if got := slices.Sorted(maps.Keys(hardwareE2EReadMap(t, out))); !slices.Equal(got, hardwareE2ESummaryFields) {
		t.Errorf("summary fields = %v, want %v", got, hardwareE2ESummaryFields)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("summary mode = %v (%v), want 0600", info, err)
	}

	if asserted := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", out); asserted.exit != 0 {
		t.Errorf("assert of a written summary: exit %d\n%s", asserted.exit, asserted.output)
	}

	defaulted := hardwareE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("default-user.json")))
	if got := hardwareE2EReadSummary(t, defaulted).NodeUser; got != "" {
		t.Errorf("nodeUser without --node-user = %q, want the harness default (empty)", got)
	}
}

func TestHardwareE2ESummaryLegStampsItsStartAndRefusesToRestamp(t *testing.T) {
	directory := t.TempDir()
	run := func(env []string, args ...string) hardwareE2EResult {
		t.Helper()
		return hardwareE2ERunEnv(t, env, hardwareE2ESummaryTool, append([]string{"leg"}, args...)...)
	}
	fixed := []string{"HARDWARE_E2E_NOW=" + hardwareE2EAmdStart}

	if result := run(fixed, directory, hardwareE2EPlanStream, "combination-1", "amd64"); result.exit != 0 {
		t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
	}
	got := hardwareE2EReadMap(t, filepath.Join(directory, "leg.json"))
	want := map[string]any{
		"schema":      "nvkr.dev/hardware-e2e-leg/v1",
		"stream":      hardwareE2EPlanStream,
		"combination": "combination-1",
		"arch":        "amd64",
		"startedAt":   hardwareE2EAmdStart,
	}
	if !maps.Equal(got, want) {
		t.Errorf("leg.json = %v, want %v", got, want)
	}
	if info, err := os.Stat(filepath.Join(directory, "leg.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("leg.json mode = %v (%v), want 0600", info, err)
	}

	again := run([]string{"HARDWARE_E2E_NOW=2026-11-18T13:00:00Z"}, directory, hardwareE2EPlanStream, "combination-1", "amd64")
	hardwareE2EExpectFailure(t, again, 47, "NVKR-E-IMMUTABLE-VIOLATION", "")
	if after := hardwareE2EReadMap(t, filepath.Join(directory, "leg.json")); after["startedAt"] != hardwareE2EAmdStart {
		t.Errorf("a refused restamp changed the start to %v", after["startedAt"])
	}

	t.Run("stamps the real clock when none is injected", func(t *testing.T) {
		directory := t.TempDir()
		before := time.Now().UTC().Truncate(time.Second)
		result := run([]string{"HARDWARE_E2E_NOW="}, directory, "s", "c", "arm64")
		after := time.Now().UTC()
		if result.exit != 0 {
			t.Fatalf("exit %d:\n%s", result.exit, result.output)
		}
		stamped, err := time.Parse(time.RFC3339, hardwareE2EReadMap(t, filepath.Join(directory, "leg.json"))["startedAt"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if stamped.Before(before) || stamped.After(after) {
			t.Errorf("stamped %s outside [%s, %s]", stamped, before, after)
		}
	})

	cases := []struct {
		name  string
		env   []string
		args  func(directory string) []string
		exit  int
		code  string
		clean bool
	}{
		{"missing directory", fixed, func(string) []string { return []string{"/nonexistent/leg", "s", "c", "amd64"} }, 2, "NVKR-E-USAGE", false},
		{"too few arguments", fixed, func(d string) []string { return []string{d, "s", "c"} }, 2, "NVKR-E-USAGE", true},
		{"too many arguments", fixed, func(d string) []string { return []string{d, "s", "c", "amd64", "extra"} }, 2, "NVKR-E-USAGE", true},
		{"unsupported architecture", fixed, func(d string) []string { return []string{d, "s", "c", "ppc64le"} }, 13, "NVKR-E-UNSUPPORTED", true},
		{"stream with a path separator", fixed, func(d string) []string { return []string{d, "../s", "c", "amd64"} }, 13, "NVKR-E-UNSUPPORTED", true},
		{"stream with a space", fixed, func(d string) []string { return []string{d, "a b", "c", "amd64"} }, 13, "NVKR-E-UNSUPPORTED", true},
		{"combination with a space", fixed, func(d string) []string { return []string{d, "s", "a b", "amd64"} }, 13, "NVKR-E-UNSUPPORTED", true},
		{"clock that is not a time", []string{"HARDWARE_E2E_NOW=yesterday"}, func(d string) []string { return []string{d, "s", "c", "amd64"} }, 2, "NVKR-E-USAGE", true},
		{"clock that is not UTC", []string{"HARDWARE_E2E_NOW=2026-11-18T10:00:00+01:00"}, func(d string) []string { return []string{d, "s", "c", "amd64"} }, 2, "NVKR-E-USAGE", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			hardwareE2EExpectFailure(t, run(tc.env, tc.args(directory)...), tc.exit, tc.code, "")
			if _, err := os.Lstat(filepath.Join(directory, "leg.json")); tc.clean && err == nil {
				t.Error("a refused stamp left a leg.json")
			}
		})
	}

	t.Run("directory that is a symlink", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		hardwareE2EExpectFailure(t, run(fixed, link, "s", "c", "amd64"), 2, "NVKR-E-USAGE", "")
		if _, err := os.Lstat(filepath.Join(target, "leg.json")); err == nil {
			t.Error("a stamp followed a symlink out of the leg directory")
		}
	})
}

func TestHardwareE2ESummaryUsage(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"verify"},
		{"assert"},
		{"assert", "a", "b"},
		{"write"},
	} {
		hardwareE2EExpectFailure(t, hardwareE2ERun(t, hardwareE2ESummaryTool, args...), 2, "NVKR-E-USAGE", "")
	}
}

type hardwareE2EMutation func(t *testing.T, in hardwareE2ESummaryInputs, args []string) []string

// hardwareE2EEdit edits one scratch file and leaves the arguments alone.
func hardwareE2EEdit(edit func(map[string]any), parts ...string) hardwareE2EMutation {
	return func(t *testing.T, in hardwareE2ESummaryInputs, args []string) []string {
		t.Helper()
		hardwareE2EEditJSON(t, in.path(parts...), edit)
		return args
	}
}

// hardwareE2EArgs edits only the arguments.
func hardwareE2EArgs(edit func(in hardwareE2ESummaryInputs, args []string) []string) hardwareE2EMutation {
	return func(t *testing.T, in hardwareE2ESummaryInputs, args []string) []string {
		t.Helper()
		return edit(in, args)
	}
}

func hardwareE2EReplaced(slot, field string, value any) func(map[string]any) {
	return func(record map[string]any) {
		for _, entry := range record["replaced"].([]any) {
			replacement := entry.(map[string]any)
			if replacement["slot"] != slot {
				continue
			}
			if value == nil {
				delete(replacement, field)
			} else {
				replacement[field] = value
			}
		}
	}
}

// hardwareE2EReplacedSource edits one field of a node image's source in the substitution record.
func hardwareE2EReplacedSource(slot, field string, value any) func(map[string]any) {
	return func(record map[string]any) {
		for _, entry := range record["replaced"].([]any) {
			if replacement := entry.(map[string]any); replacement["slot"] == slot {
				replacement["source"].(map[string]any)[field] = value
			}
		}
	}
}

// hardwareE2EPlanNodeURL sets the location a plan names for a node image slot, wherever the plan lists it.
func hardwareE2EPlanNodeURL(slot string, url any) func(map[string]any) {
	return func(plan map[string]any) {
		visit := func(list any) {
			for _, entry := range list.([]any) {
				if e := entry.(map[string]any); e["name"] == slot {
					e["url"] = url
				}
			}
		}
		for _, combination := range plan["combinations"].([]any) {
			c := combination.(map[string]any)
			visit(c["components"])
			visit(c["dependencies"])
			for _, stream := range c["streams"].([]any) {
				visit(stream.(map[string]any)["dependencies"])
			}
		}
	}
}

// hardwareE2ECoverageEdit edits one leg's coverage record and leaves the arguments alone.
func hardwareE2ECoverageEdit(arch string, edit func(map[string]any)) hardwareE2EMutation {
	return func(t *testing.T, in hardwareE2ESummaryInputs, args []string) []string {
		t.Helper()
		hardwareE2EEditJSON(t, filepath.Join(in.leg(arch), "coverage.json"), edit)
		return args
	}
}

// hardwareE2EStagedEdit edits one leg's staged record and leaves the arguments alone.
func hardwareE2EStagedEdit(arch string, edit func(map[string]any)) hardwareE2EMutation {
	return func(t *testing.T, in hardwareE2ESummaryInputs, args []string) []string {
		t.Helper()
		hardwareE2EEditJSON(t, filepath.Join(in.leg(arch), "staged.json"), edit)
		return args
	}
}

// hardwareE2EPlanNodeURLOf is the location a plan names for a node image slot.
func hardwareE2EPlanNodeURLOf(t *testing.T, plan map[string]any, slot string) string {
	t.Helper()
	for _, combination := range plan["combinations"].([]any) {
		for _, stream := range combination.(map[string]any)["streams"].([]any) {
			for _, entry := range stream.(map[string]any)["dependencies"].([]any) {
				if e := entry.(map[string]any); e["name"] == slot {
					return e["url"].(string)
				}
			}
		}
	}
	t.Fatalf("the plan names no location for %s", slot)
	return ""
}

// hardwareE2ENodeCheckEdit edits one node image's check record and leaves the arguments alone.
func hardwareE2ENodeCheckEdit(slot string, edit func(map[string]any)) hardwareE2EMutation {
	return func(t *testing.T, in hardwareE2ESummaryInputs, args []string) []string {
		t.Helper()
		hardwareE2EEditJSON(t, in.nodeCheck(slot), edit)
		return args
	}
}

// hardwareE2ENodeImageBoth changes a node image in the substitution record and in its check alike.
// A nil value removes the field.
func hardwareE2ENodeImageBoth(slot, replacedField, checkField string, value any) hardwareE2EMutation {
	return func(t *testing.T, in hardwareE2ESummaryInputs, args []string) []string {
		t.Helper()
		hardwareE2EEditJSON(t, in.path("sub", "substitutions.json"), hardwareE2EReplaced(slot, replacedField, value))
		if replacedField == "to" {
			// The plan names the same location, so only the summary contract can refuse it.
			hardwareE2EEditJSON(t, in.path("sub", "plan.json"), hardwareE2EPlanNodeURL(slot, value))
		}
		hardwareE2EEditJSON(t, in.nodeCheck(slot), func(check map[string]any) {
			if value == nil {
				delete(check, checkField)
			} else {
				check[checkField] = value
			}
		})
		return args
	}
}

func TestHardwareE2ESummaryWriteRefusesWhatItCannotSupport(t *testing.T) {
	const verify = "NVKR-E-VERIFY-FAILED"
	cases := []struct {
		name     string
		stage    string
		armStart string
		mutate   hardwareE2EMutation
		exit     int
		code     string
		message  string
	}{
		// Usage.
		{name: "no legs", exit: 2, code: "NVKR-E-USAGE",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string { return hardwareE2EDropFlag(a, "--leg", "") })},
		{name: "no stage", exit: 2, code: "NVKR-E-USAGE",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string { return hardwareE2EDropFlag(a, "--stage", "") })},
		{name: "no matrix", exit: 2, code: "NVKR-E-USAGE",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string { return hardwareE2EDropFlag(a, "--matrix", "") })},
		{name: "flag without a value", exit: 2, code: "NVKR-E-USAGE", message: "needs a value",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string { return append(a[:2:2], "--stage") })},
		{name: "unknown flag", exit: 2, code: "NVKR-E-USAGE", message: "unknown argument",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return append(slices.Clone(a), "--verdict", "pass")
			})},
		{name: "no output path", exit: 2, code: "NVKR-E-USAGE",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, _ []string) []string { return []string{"write"} })},
		{name: "plan is a directory", exit: 2, code: "NVKR-E-USAGE", message: "not a regular file",
			mutate: hardwareE2EArgs(func(in hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(a, "--plan", in.scratch)
			})},
		{name: "leg directory does not exist", exit: 2, code: "NVKR-E-USAGE", message: "leg directory does not exist",
			mutate: hardwareE2EArgs(func(in hardwareE2ESummaryInputs, a []string) []string {
				return append(slices.Clone(a), "--leg", in.path("nowhere"))
			})},
		{name: "leg without coverage", exit: 2, code: "NVKR-E-USAGE", message: "not a regular file",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				if err := os.Remove(filepath.Join(in.leg("amd64"), "coverage.json")); err != nil {
					t.Fatal(err)
				}
				return a
			}},

		// Existing output.
		{name: "output already exists", exit: 47, code: "NVKR-E-IMMUTABLE-VIOLATION",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				if err := os.WriteFile(a[1], []byte("keep\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return a
			}},

		// Inputs the script does not understand.
		{name: "substitution record with another schema", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "unsupported schema",
			mutate: hardwareE2EEdit(func(m map[string]any) { m["schema"] = "nvkr.dev/hardware-e2e-substitutions/v2" }, "sub", "substitutions.json")},
		{name: "matrix naming no streams", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "names no streams",
			mutate: hardwareE2EEdit(func(m map[string]any) { m["include"] = []any{} }, "matrix.json")},
		{name: "plan that is not JSON", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "the plan could not be read for its streams",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				if err := os.WriteFile(in.path("sub", "plan.json"), []byte("not json\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return a
			}},
		{name: "coverage with another schema", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "unsupported schema",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				hardwareE2EEditJSON(t, filepath.Join(in.leg("amd64"), "coverage.json"), func(m map[string]any) { m["schema"] = "nvkr.dev/hardware-e2e-coverage/v2" })
				return a
			}},
		{name: "staged files for another architecture", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "mixes architectures",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				hardwareE2EEditJSON(t, filepath.Join(in.leg("amd64"), "staged.json"), func(m map[string]any) { m["arch"] = "arm64" })
				return a
			}},
		{name: "evidence for a stream that was not dispatched", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "were not dispatched",
			mutate: hardwareE2EEdit(func(m map[string]any) { m["include"] = m["include"].([]any)[:1] }, "matrix.json")},

		// A dispatched leg with no evidence.
		{name: "dispatched leg without evidence", exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "no evidence for dispatched legs",
			mutate: hardwareE2EArgs(func(in hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2EDropFlag(a, "--leg", in.leg("arm64"))
			})},
		{name: "the same leg given twice", exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "no evidence for dispatched legs",
			mutate: hardwareE2EArgs(func(in hardwareE2ESummaryInputs, a []string) []string {
				return append(slices.Clone(a), "--leg", in.leg("amd64"))
			})},
		{name: "a dispatched stream on another architecture than the plan gives it", exit: 22, code: "NVKR-E-VERIFY-FAILED", message: "the matrix names streams the plan does not",
			mutate: hardwareE2EEdit(func(m map[string]any) { m["include"].([]any)[0].(map[string]any)["arch"] = "arm64" }, "matrix.json")},
		{name: "a dispatched stream the plan does not name", exit: 22, code: "NVKR-E-VERIFY-FAILED", message: "/gb200-not-in-the-plan",
			mutate: hardwareE2EEdit(func(m map[string]any) {
				m["include"].([]any)[1].(map[string]any)["stream"] = "gb200-not-in-the-plan"
			}, "matrix.json")},
		{name: "a dispatched combination the plan does not name", exit: 22, code: "NVKR-E-VERIFY-FAILED", message: "the matrix names streams the plan does not",
			mutate: hardwareE2EEdit(func(m map[string]any) {
				m["include"].([]any)[0].(map[string]any)["combination"] = "some-other-combination"
			}, "matrix.json")},

		// The contract the summary must meet before it is written.
		{name: "a stage the harness does not have", exit: 22, code: verify, message: "stage is not one of the harness stages",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(a, "--stage", "nightly")
			})},
		{name: "coverage of another stage", exit: 22, code: verify, message: "every leg coverage must match the summary stage",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(a, "--stage", "provision")
			})},
		{name: "coverage claiming completeness before the full stage", stage: "provision", exit: 22, code: verify, message: "every leg coverage must match the summary stage",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				hardwareE2EEditJSON(t, filepath.Join(in.leg("amd64"), "coverage.json"), func(m map[string]any) { m["complete"] = true })
				return a
			}},
		{name: "full stage with incomplete coverage", exit: 22, code: verify, message: "every leg coverage must match the summary stage",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				hardwareE2EEditJSON(t, filepath.Join(in.leg("amd64"), "coverage.json"), func(m map[string]any) { m["complete"] = false })
				return a
			}},
		{name: "full stage where a selected spec did not pass", exit: 22, code: verify, message: "every leg coverage must match the summary stage",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				hardwareE2EEditJSON(t, filepath.Join(in.leg("arm64"), "coverage.json"), func(m map[string]any) { m["passed"] = 79 })
				return a
			}},
		{name: "coverage that selected nothing", exit: 22, code: verify, message: "every leg coverage must match the summary stage",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				hardwareE2EEditJSON(t, filepath.Join(in.leg("amd64"), "coverage.json"), func(m map[string]any) { m["selected"] = 0 })
				return a
			}},
		// The groups a leg's coverage is made of: counted, adding up, and present where the stage registers them.
		{name: "coverage whose groups do not add up to its totals", exit: 22, code: verify, message: hardwareE2ECoverageGroupsRule,
			mutate: hardwareE2ECoverageEdit("amd64", func(m map[string]any) {
				m["categories"].(map[string]any)["tenant"] = map[string]any{"selected": 41, "passed": 41}
			})},
		{name: "coverage that counts its groups with bare numbers", exit: 22, code: verify, message: hardwareE2ECoverageGroupsRule,
			mutate: hardwareE2ECoverageEdit("arm64", func(m map[string]any) {
				m["categories"] = map[string]any{"provisioning": 7, "site-checks": 31, "tenant": 42}
			})},
		{name: "full stage where every group reports zero passes", exit: 22, code: verify, message: hardwareE2ECoverageGroupsRule,
			mutate: hardwareE2ECoverageEdit("amd64", func(m map[string]any) {
				for _, group := range m["categories"].(map[string]any) {
					group.(map[string]any)["passed"] = 0
				}
			})},
		{name: "a group that passed more than it selected", exit: 22, code: verify, message: hardwareE2ECoverageGroupsRule,
			mutate: hardwareE2ECoverageEdit("amd64", func(m map[string]any) {
				categories := m["categories"].(map[string]any)
				categories["provisioning"] = map[string]any{"selected": 7, "passed": 8}
				categories["tenant"] = map[string]any{"selected": 42, "passed": 41}
			})},
		{name: "below the full stage a group that passed more than it selected", stage: "deployment", exit: 22, code: verify, message: hardwareE2ECoverageGroupsRule,
			mutate: hardwareE2ECoverageEdit("amd64", func(m map[string]any) {
				m["categories"].(map[string]any)["site-checks"] = map[string]any{"selected": 31, "passed": 32}
				m["passed"] = 39
			})},
		{name: "a group the coverage does not count", exit: 22, code: verify, message: hardwareE2ECoverageGroupsRule,
			mutate: hardwareE2ECoverageEdit("amd64", func(m map[string]any) { delete(m["categories"].(map[string]any), "tenant") })},
		{name: "a group with a count it should not have", exit: 22, code: verify, message: hardwareE2ECoverageGroupsRule,
			mutate: hardwareE2ECoverageEdit("amd64", func(m map[string]any) {
				m["categories"].(map[string]any)["tenant"].(map[string]any)["skipped"] = 0
			})},
		{name: "full stage with no tenant specs selected", exit: 22, code: verify, message: hardwareE2ECoverageGroupsRule,
			mutate: hardwareE2ECoverageEdit("amd64", func(m map[string]any) {
				m["categories"].(map[string]any)["tenant"] = map[string]any{"selected": 0, "passed": 0}
				m["selected"], m["passed"] = 38, 38
			})},
		{name: "deployment stage with no site checks selected", stage: "deployment", exit: 22, code: verify, message: hardwareE2ECoverageGroupsRule,
			mutate: hardwareE2ECoverageEdit("amd64", func(m map[string]any) {
				m["categories"].(map[string]any)["site-checks"] = map[string]any{"selected": 0, "passed": 0}
				m["selected"] = 49
			})},

		// What a leg staged must be what this run's substitution record names.
		{name: "staged evidence that pulled another controller", exit: 22, code: verify, message: "names other artifacts than the substitution record for: controller",
			mutate: hardwareE2EStagedEdit("amd64", func(m map[string]any) {
				m["pulledByDigest"].(map[string]any)["controller"] = "oci://ghcr.io/example/other-run/controller@sha256:" + strings.Repeat("f", 64)
			})},
		{name: "staged evidence that pulled another CLI for its architecture", exit: 22, code: verify, message: "names other artifacts than the substitution record for: cli-arm64",
			mutate: hardwareE2EStagedEdit("arm64", func(m map[string]any) {
				m["pulledByDigest"].(map[string]any)["cli-arm64"] = "oci://ghcr.io/example/other-run/cli@sha256:" + strings.Repeat("e", 64)
			})},
		{name: "staged evidence that pulled the other architecture's CLI", exit: 22, code: verify, message: "names other artifacts than the substitution record for: cli-amd64",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, args []string) []string {
				t.Helper()
				arm := hardwareE2EReplacedTo(t, in.path("sub", "substitutions.json"), "cli-arm64")
				return hardwareE2EStagedEdit("amd64", func(m map[string]any) { m["pulledByDigest"].(map[string]any)["cli-amd64"] = arm })(t, in, args)
			}},
		{name: "staged evidence that names nothing it pulled", exit: 22, code: verify, message: "names other artifacts than the substitution record for: controller, capi-provisioned, cli-amd64",
			mutate: hardwareE2EStagedEdit("amd64", func(m map[string]any) { delete(m, "pulledByDigest") })},
		{name: "staged evidence that pulled something more", exit: 22, code: verify, message: "the set of pulled references",
			mutate: hardwareE2EStagedEdit("amd64", func(m map[string]any) {
				m["pulledByDigest"].(map[string]any)["cli-arm64"] = "oci://ghcr.io/example/cli@sha256:" + strings.Repeat("d", 64)
			})},
		{name: "staged evidence whose pulled references are not text", exit: 22, code: verify, message: "names other artifacts than the substitution record for: controller",
			mutate: hardwareE2EStagedEdit("amd64", func(m map[string]any) { m["pulledByDigest"].(map[string]any)["controller"] = 5 })},
		{name: "staged evidence whose pulled references are not an object", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "has no readable pulledByDigest",
			mutate: hardwareE2EStagedEdit("amd64", func(m map[string]any) { m["pulledByDigest"] = "controller" })},

		// The plan the summary digests must name the node image locations the checks saw.
		{name: "a plan that names another node image location than the checks and the record", exit: 22, code: verify,
			message: "the plan names other node image locations than the checks and the substitution record for: os-image-amd64",
			mutate:  hardwareE2EEdit(hardwareE2EPlanNodeURL("os-image-amd64", "https://cdn.example.test/another-image.qcow2"), "sub", "plan.json")},
		{name: "a plan that names no location for a node image", exit: 22, code: verify,
			message: "the plan names other node image locations than the checks and the substitution record for: os-image-arm64",
			mutate:  hardwareE2EEdit(hardwareE2EPlanNodeURL("os-image-arm64", nil), "sub", "plan.json")},

		// An observation flag must fit its method: a fetch was observed by this run, a read-back was not.
		{name: "an amd64 node image check whose observation flag does not fit its method", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "observation flag that does not fit its method",
			mutate: hardwareE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["observedByThisRun"] = !m["observedByThisRun"].(bool) })},
		{name: "an arm64 node image check whose observation flag does not fit its method", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "observation flag that does not fit its method",
			mutate: hardwareE2ENodeCheckEdit("os-image-arm64", func(m map[string]any) { m["observedByThisRun"] = !m["observedByThisRun"].(bool) })},
		{name: "a node image check with no observation flag", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "observation flag that does not fit its method",
			mutate: hardwareE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { delete(m, "observedByThisRun") })},

		{name: "harness commit that is not a full hash", exit: 22, code: verify, message: "harness must name exactly a 40-hex commit",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(a, "--harness-commit", "c044392")
			})},
		{name: "harness named by a ref", exit: 22, code: verify, message: "harness must name exactly a 40-hex commit",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(a, "--harness-commit", "main")
			})},
		{name: "orchestrator commit in upper case", exit: 22, code: verify, message: "orchestrator must name a 40-hex commit and a run id",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(a, "--orchestrator-commit", strings.ToUpper(hardwareE2EOrchestratorCommit))
			})},
		{name: "orchestrator run that is not a number", exit: 22, code: verify, message: "orchestrator must name a 40-hex commit and a run id",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(a, "--orchestrator-run", "latest")
			})},
		{name: "node user with a space", exit: 22, code: verify, message: "nodeUser must be empty or a plain account name",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return append(slices.Clone(a), "--node-user", "bad user")
			})},
		{name: "node user that looks like an option", exit: 22, code: verify, message: "nodeUser must be empty or a plain account name",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return append(slices.Clone(a), "--node-user", "-rf")
			})},
		{name: "controller version check with an unknown value", exit: 22, code: verify, message: "every leg must record what it staged",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				hardwareE2EEditJSON(t, filepath.Join(in.leg("amd64"), "staged.json"), func(m map[string]any) { m["controllerVersionCheck"] = "skipped" })
				return a
			}},
		{name: "a leg that staged nothing", exit: 22, code: verify, message: "every leg must record what it staged",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				hardwareE2EEditJSON(t, filepath.Join(in.leg("arm64"), "staged.json"), func(m map[string]any) { m["files"] = map[string]any{} })
				return a
			}},
		{name: "a staged file without a sha256", exit: 22, code: verify, message: "every leg must record what it staged",
			mutate: func(t *testing.T, in hardwareE2ESummaryInputs, a []string) []string {
				t.Helper()
				hardwareE2EEditJSON(t, filepath.Join(in.leg("amd64"), "staged.json"), func(m map[string]any) { m["files"].(map[string]any)["controller.tar"] = "not-a-digest" })
				return a
			}},
		{name: "arm64 leg started before the amd64 leg", armStart: "2026-11-18T09:00:00Z", exit: 22, code: verify, message: "armStartedAfterAmd must be true"},
		{name: "arm64 leg started at the same instant as the amd64 leg", armStart: hardwareE2EAmdStart, exit: 22, code: verify, message: "armStartedAfterAmd must be true"},
		// The node image the plan carries, and the check that saw it. These change the record and its
		// check together, so the two agree and only the summary contract can refuse the image.
		{name: "node image over plain http", exit: 22, code: verify, message: "nonempty https locations with a sha256 for NVKR outputs",
			mutate: hardwareE2ENodeImageBoth("os-image-amd64", "to", "url", "http://cdn.example.test/node.qcow2")},
		{name: "node image URL with credentials", exit: 22, code: verify, message: "nonempty https locations with a sha256 for NVKR outputs",
			mutate: hardwareE2ENodeImageBoth("os-image-amd64", "to", "url", "https://user@cdn.example.test/node.qcow2")},
		{name: "node image URL with a query", exit: 22, code: verify, message: "nonempty https locations with a sha256 for NVKR outputs",
			mutate: hardwareE2ENodeImageBoth("os-image-arm64", "to", "url", "https://cdn.example.test/node.qcow2?sig=synthetic")},
		{name: "node image without a digest", exit: 22, code: verify, message: "nonempty https locations with a sha256 for NVKR outputs",
			mutate: hardwareE2ENodeImageBoth("os-image-amd64", "sha256", "sha256", nil)},

		// The node image checks: required, one per slot, and naming exactly the bytes the plan carries.
		{name: "no node image check", exit: 2, code: "NVKR-E-USAGE",
			mutate: hardwareE2EArgs(func(_ hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2EDropFlag(a, "--node-image-check", "")
			})},
		{name: "node image check that does not exist", exit: 2, code: "NVKR-E-USAGE", message: "not a regular file",
			mutate: hardwareE2EArgs(func(in hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2ESetFlag(hardwareE2EDropFlag(a, "--node-image-check", in.nodeCheck("os-image-arm64")), "--node-image-check", in.path("nowhere.json"))
			})},
		{name: "node image check with another schema", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "node image check",
			mutate: hardwareE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["schema"] = "nvkr.dev/hardware-e2e-node-image-check/v2" })},
		{name: "node image check for a slot the plan did not substitute", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "names a slot the plan did not substitute",
			mutate: hardwareE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["slot"] = "os-image-ppc64le" })},
		{name: "node image check for a slot that is not a node image", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "names a slot the plan did not substitute",
			mutate: hardwareE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["slot"] = "controller" })},
		{name: "node image check of another URL than the plan carries", exit: 22, code: verify, message: "names other bytes than the plan for: os-image-amd64",
			mutate: hardwareE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["url"] = "https://cdn.example.test/other.qcow2" })},
		{name: "node image check of other bytes than the plan carries", exit: 22, code: verify, message: "names other bytes than the plan for: os-image-arm64",
			mutate: hardwareE2ENodeCheckEdit("os-image-arm64", func(m map[string]any) { m["sha256"] = hardwareE2EDigest("9") })},
		{name: "only one node image checked", exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "without exactly one check: os-image-arm64",
			mutate: hardwareE2EArgs(func(in hardwareE2ESummaryInputs, a []string) []string {
				return hardwareE2EDropFlag(a, "--node-image-check", in.nodeCheck("os-image-arm64"))
			})},
		{name: "one node image checked twice", exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "without exactly one check",
			mutate: hardwareE2EArgs(func(in hardwareE2ESummaryInputs, a []string) []string {
				return append(hardwareE2EDropFlag(a, "--node-image-check", in.nodeCheck("os-image-arm64")), "--node-image-check", in.nodeCheck("os-image-amd64"))
			})},
		{name: "node image check that read nothing", exit: 22, code: verify, message: hardwareE2ENodeImageRule,
			mutate: hardwareE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["bytes"] = 0 })},
		{name: "node image check of a qcow2 version the lane does not support", exit: 22, code: verify, message: hardwareE2ENodeImageRule,
			mutate: hardwareE2ENodeCheckEdit("os-image-arm64", func(m map[string]any) { m["qcow2"].(map[string]any)["version"] = 4 })},
		{name: "node image check with a range answer that is not a boolean", exit: 22, code: verify, message: hardwareE2ENodeImageRule,
			mutate: hardwareE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["rangeSupported"] = "yes" })},

		// Where the node image came from, and whether its NVKE version differs from the plan's.
		{name: "node image source of an unknown kind", exit: 22, code: verify, message: "each node image must name its source",
			mutate: hardwareE2EEdit(hardwareE2EReplacedSource("os-image-amd64", "kind", "docker-hub"), "sub", "substitutions.json")},
		{name: "node image source version that is not a version", exit: 22, code: verify, message: "each node image must name its source",
			mutate: hardwareE2EEdit(hardwareE2EReplacedSource("os-image-arm64", "version", "latest"), "sub", "substitutions.json")},
		{name: "node image source with a field the contract does not know", exit: 22, code: verify, message: "each node image must name its source",
			mutate: hardwareE2EEdit(hardwareE2EReplacedSource("os-image-amd64", "trusted", true), "sub", "substitutions.json")},
		{name: "node image with no source", exit: 22, code: verify, message: "each node image must name its source",
			mutate: hardwareE2EEdit(hardwareE2EReplaced("os-image-amd64", "source", nil), "sub", "substitutions.json")},
		{name: "substitution record that says nothing about version skew", exit: 22, code: verify, message: "nodeImageVersionSkew must say",
			mutate: hardwareE2EEdit(func(m map[string]any) { delete(m, "nodeImageVersionSkew") }, "sub", "substitutions.json")},
		{name: "substitution record that claims a skew nobody has", exit: 22, code: verify, message: "nodeImageVersionSkew must say",
			mutate: hardwareE2EEdit(func(m map[string]any) { m["nodeImageVersionSkew"] = true }, "sub", "substitutions.json")},
		{name: "substitution record that hides a skew", exit: 22, code: verify, message: "nodeImageVersionSkew must say",
			mutate: hardwareE2EEdit(hardwareE2EReplacedSource("os-image-arm64", "version", "v0.23.1"), "sub", "substitutions.json")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stage, armStart := tc.stage, tc.armStart
			if stage == "" {
				stage = "full"
			}
			if armStart == "" {
				armStart = hardwareE2EArmStart
			}
			in := hardwareE2EBuildSummaryInputs(t, stage, armStart)
			out := in.path("summary.json")
			args := in.writeArgs(out)
			if tc.mutate != nil {
				args = tc.mutate(t, in, args)
			}

			hardwareE2EExpectFailure(t, hardwareE2ERun(t, hardwareE2ESummaryTool, args...), tc.exit, tc.code, tc.message)

			content, err := os.ReadFile(out)
			switch {
			case tc.exit == 47:
				if err != nil || string(content) != "keep\n" {
					t.Errorf("a refused write changed the existing output: %q (%v)", content, err)
				}
			case err == nil:
				t.Errorf("a refused write left a summary:\n%s", content)
			}
		})
	}
}

func TestHardwareE2ESummaryOfAnUnfinishedRunIsNeverComplete(t *testing.T) {
	for _, stage := range []string{"provision", "bootstrap", "deployment"} {
		t.Run(stage, func(t *testing.T) {
			in := hardwareE2EBuildSummaryInputs(t, stage, hardwareE2EArmStart)
			out := hardwareE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("summary.json")))

			summary := hardwareE2EReadSummary(t, out)
			if summary.Complete || summary.Stage != stage {
				t.Errorf("stage %s: complete = %v, stage = %q", stage, summary.Complete, summary.Stage)
			}
			for _, leg := range summary.Legs {
				if leg.Coverage.Complete {
					t.Errorf("stage %s: leg %s claims complete coverage", stage, leg.Stream)
				}
			}
			if asserted := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", out); asserted.exit != 0 {
				t.Errorf("assert: exit %d\n%s", asserted.exit, asserted.output)
			}
		})
	}

	t.Run("full stage with one of two streams dispatched", func(t *testing.T) {
		in := hardwareE2EBuildSummaryInputs(t, "full", hardwareE2EArmStart)
		hardwareE2EEditJSON(t, in.path("matrix.json"), func(m map[string]any) { m["include"] = m["include"].([]any)[:1] })
		args := hardwareE2EDropFlag(in.writeArgs(in.path("summary.json")), "--leg", in.leg("arm64"))
		out := hardwareE2ERequireSummaryWritten(t, in, args)

		summary := hardwareE2EReadSummary(t, out)
		if summary.Complete {
			t.Error("a run that covered one of two planned streams is complete")
		}
		if len(summary.Legs) != 1 || summary.PlannedStreams != 2 {
			t.Errorf("legs = %d, planned streams = %d, want 1 and 2", len(summary.Legs), summary.PlannedStreams)
		}
		if asserted := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", out); asserted.exit != 0 {
			t.Errorf("assert: exit %d\n%s", asserted.exit, asserted.output)
		}
	})
}

func TestHardwareE2ESummaryStatesWhereNodeImagesCameFromAndWhetherTheyAreSkewed(t *testing.T) {
	t.Run("a skewed run is recorded as skewed and still carries no authority", func(t *testing.T) {
		manifest := hardwareE2EOutputs()
		hardwareE2ENodeSource(manifest, "os-image-amd64")["version"] = "v0.23.1"
		hardwareE2ENodeSource(manifest, "os-image-arm64")["version"] = "v0.23.1"
		in := hardwareE2EBuildSummaryInputsFrom(t, "full", hardwareE2EArmStart, manifest, "--allow-node-image-version-skew")
		out := hardwareE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("summary.json")))

		summary := hardwareE2EReadSummary(t, out)
		if !summary.NodeImageVersionSkew || summary.Subject.Version != hardwareE2EVersion {
			t.Errorf("skew = %v, subject version = %q, want true and %q", summary.NodeImageVersionSkew, summary.Subject.Version, hardwareE2EVersion)
		}
		for _, image := range summary.NodeImages {
			if image.Source.Version != "v0.23.1" {
				t.Errorf("%s source version = %q, want the version the image was built at", image.Slot, image.Source.Version)
			}
		}
		if summary.QualificationAuthority != "none" {
			t.Errorf("authority = %q, want none", summary.QualificationAuthority)
		}
		if asserted := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", out); asserted.exit != 0 {
			t.Errorf("assert: exit %d\n%s", asserted.exit, asserted.output)
		}
	})

	t.Run("one skewed image is enough to mark the run", func(t *testing.T) {
		manifest := hardwareE2EOutputs()
		hardwareE2ENodeSource(manifest, "os-image-arm64")["version"] = "v0.23.1"
		in := hardwareE2EBuildSummaryInputsFrom(t, "full", hardwareE2EArmStart, manifest, "--allow-node-image-version-skew")
		out := hardwareE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("summary.json")))

		summary := hardwareE2EReadSummary(t, out)
		if !summary.NodeImageVersionSkew || summary.NodeImages[0].Source.Version != hardwareE2EVersion {
			t.Errorf("skew = %v, amd64 version = %q", summary.NodeImageVersionSkew, summary.NodeImages[0].Source.Version)
		}
	})

	t.Run("an image from a release names its digest", func(t *testing.T) {
		manifest := hardwareE2EOutputs()
		reference := "oci://registry.example.test/team/node-image@sha256:" + hardwareE2EDigest("1")
		source := hardwareE2ENodeSource(manifest, "os-image-amd64")
		source["kind"], source["reference"] = "nvkr-release", reference
		in := hardwareE2EBuildSummaryInputsFrom(t, "full", hardwareE2EArmStart, manifest)
		out := hardwareE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("summary.json")))

		summary := hardwareE2EReadSummary(t, out)
		if got := summary.NodeImages[0].Source; got.Kind != "nvkr-release" || got.Reference != reference {
			t.Errorf("amd64 source = %+v, want the release digest reference", got)
		}
		if got := summary.NodeImages[1].Source.Kind; got != "ci-run" {
			t.Errorf("arm64 source kind = %q, want ci-run: sources are recorded per image", got)
		}
		if summary.NodeImageVersionSkew {
			t.Error("no image differs from the plan, so there is no skew to declare")
		}
		// Only the NVKR release is claimed as NVKR-built: the amd64 leg does not name its node image as
		// built elsewhere, and the arm64 leg, whose image is another CI run's, does.
		for _, leg := range summary.Legs {
			listed := slices.Contains(leg.Staged.NotNvkrBuilt, "node-image")
			if want := leg.Arch != "amd64"; listed != want {
				t.Errorf("%s leg lists node-image as not NVKR-built = %v, want %v (not NVKR-built: %v)", leg.Arch, listed, want, leg.Staged.NotNvkrBuilt)
			}
		}
	})
}

func TestHardwareE2ESummaryStatesHowEachNodeImageWasCheckedAndWhetherThisRunSawIt(t *testing.T) {
	const verify, unsupported = "NVKR-E-VERIFY-FAILED", "NVKR-E-UNSUPPORTED"
	writeSummary := func(t *testing.T, manifest map[string]any) hardwareE2ESummary {
		t.Helper()
		in := hardwareE2EBuildSummaryInputsFrom(t, "full", hardwareE2EArmStart, manifest)
		out := hardwareE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("summary.json")))
		if asserted := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", out); asserted.exit != 0 {
			t.Fatalf("assert: exit %d\n%s", asserted.exit, asserted.output)
		}
		return hardwareE2EReadSummary(t, out)
	}

	t.Run("a fetched image is recorded as observed by this run", func(t *testing.T) {
		summary := writeSummary(t, hardwareE2EOutputs())
		for _, image := range summary.NodeImages {
			got := image.Checked
			if got.Method != "http-get" || !got.ObservedByThisRun || got.Run != "" || got.Bytes != hardwareE2ENodeImageBytes || got.QCOW2Version != 3 {
				t.Errorf("%s checked = %+v, want a fetch this run observed", image.Slot, got)
			}
		}
	})

	t.Run("a published image is recorded as read back by another run and not observed here", func(t *testing.T) {
		manifest := hardwareE2EOutputs()
		hardwareE2EUseReadback(manifest)
		summary := writeSummary(t, manifest)
		if len(summary.NodeImages) != 2 {
			t.Fatalf("%d node images, want 2", len(summary.NodeImages))
		}
		for _, image := range summary.NodeImages {
			got := image.Checked
			if got.Method != "s3-readback" || got.ObservedByThisRun || got.Run != hardwareE2EPublicationRun ||
				got.Bytes != hardwareE2ENodeImageBytes || got.QCOW2Version != 3 || got.VirtualSizeBytes != 12<<30 || got.RangeSupported {
				t.Errorf("%s checked = %+v, want what the publication run reported, not observed here", image.Slot, got)
			}
		}
		if summary.QualificationAuthority != "none" {
			t.Errorf("authority = %q, want none", summary.QualificationAuthority)
		}
	})

	t.Run("each image keeps the method its own output named", func(t *testing.T) {
		manifest := hardwareE2EOutputs()
		hardwareE2ESlots(manifest)["os-image-arm64"].(map[string]any)["verification"] = hardwareE2EReadback()
		summary := writeSummary(t, manifest)
		methods := map[string]string{}
		for _, image := range summary.NodeImages {
			methods[image.Slot] = image.Checked.Method
		}
		if methods["os-image-amd64"] != "http-get" || methods["os-image-arm64"] != "s3-readback" {
			t.Errorf("methods = %v, want amd64 fetched and arm64 read back", methods)
		}
	})

	refusals := []struct {
		name     string
		readback bool
		edit     func(t *testing.T, in hardwareE2ESummaryInputs)
		exit     int
		code     string
		message  string
	}{
		{"the plan names a read-back and the check fetched", true, func(t *testing.T, in hardwareE2ESummaryInputs) {
			hardwareE2EWriteMap(t, in.nodeCheck("os-image-amd64"), hardwareE2ENodeImageCheck(hardwareE2EOutputs(), "os-image-amd64"))
		}, 22, verify, "disagrees with the verification the plan names for: os-image-amd64"},
		{"the plan names a fetch and the check read back", false, func(t *testing.T, in hardwareE2ESummaryInputs) {
			readback := hardwareE2EOutputs()
			hardwareE2EUseReadback(readback)
			hardwareE2EWriteMap(t, in.nodeCheck("os-image-arm64"), hardwareE2ENodeImageCheck(readback, "os-image-arm64"))
		}, 22, verify, "disagrees with the verification the plan names for: os-image-arm64"},
		{"the check cites another run than the plan", true, func(t *testing.T, in hardwareE2ESummaryInputs) {
			hardwareE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) {
				m["run"] = "https://github.com/dsx-ai-factory/nvkr/actions/runs/9000000002"
			})
		}, 22, verify, "disagrees with the verification the plan names for: os-image-amd64"},
		{"the check reports other bytes than the plan's verification", true, func(t *testing.T, in hardwareE2ESummaryInputs) {
			hardwareE2EEditJSON(t, in.nodeCheck("os-image-arm64"), func(m map[string]any) { m["bytes"] = hardwareE2ENodeImageBytes + 1 })
		}, 22, verify, "disagrees with the verification the plan names for: os-image-arm64"},
		{"the check reports another qcow2 version than the plan's verification", true, func(t *testing.T, in hardwareE2ESummaryInputs) {
			hardwareE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) { m["qcow2"].(map[string]any)["version"] = 2 })
		}, 22, verify, "disagrees with the verification the plan names for: os-image-amd64"},
		{"the check reports another virtual size than the plan's verification", true, func(t *testing.T, in hardwareE2ESummaryInputs) {
			hardwareE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) { m["qcow2"].(map[string]any)["virtualSizeBytes"] = 1 })
		}, 22, verify, "disagrees with the verification the plan names for: os-image-amd64"},
		{"the plan's record names no verification", true, func(t *testing.T, in hardwareE2ESummaryInputs) {
			hardwareE2EEditJSON(t, in.path("sub", "substitutions.json"), func(m map[string]any) {
				for _, entry := range m["replaced"].([]any) {
					if entry := entry.(map[string]any); entry["slot"] == "os-image-amd64" {
						delete(entry, "verification")
					}
				}
			})
		}, 22, verify, "disagrees with the verification the plan names for: os-image-amd64"},
		{"a read-back image whose check names other bytes than the plan", true, func(t *testing.T, in hardwareE2ESummaryInputs) {
			hardwareE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) { m["sha256"] = hardwareE2EDigest("9") })
		}, 22, verify, "names other bytes than the plan for: os-image-amd64"},
		{"a check of a method the lane does not know", true, func(t *testing.T, in hardwareE2ESummaryInputs) {
			hardwareE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) { m["method"] = "head-object" })
		}, 13, unsupported, "unsupported schema or method"},
		{"a check with no method", false, func(t *testing.T, in hardwareE2ESummaryInputs) {
			hardwareE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) { delete(m, "method") })
		}, 13, unsupported, "unsupported schema or method"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			manifest := hardwareE2EOutputs()
			if tc.readback {
				hardwareE2EUseReadback(manifest)
			}
			in := hardwareE2EBuildSummaryInputsFrom(t, "full", hardwareE2EArmStart, manifest)
			tc.edit(t, in)
			out := in.path("summary.json")
			hardwareE2EExpectFailure(t, hardwareE2ERun(t, hardwareE2ESummaryTool, in.writeArgs(out)...), tc.exit, tc.code, tc.message)
			if _, err := os.Lstat(out); err == nil {
				t.Errorf("a refused summary left %s behind", out)
			}
		})
	}
}

func TestHardwareE2ESummaryAssertRefusesAnySummaryThatClaimsMore(t *testing.T) {
	const verify = "NVKR-E-VERIFY-FAILED"

	full := hardwareE2EBuildSummaryInputs(t, "full", hardwareE2EArmStart)
	fullPath := hardwareE2ERequireSummaryWritten(t, full, full.writeArgs(full.path("summary.json")))
	fullBytes, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	provision := hardwareE2EBuildSummaryInputs(t, "provision", hardwareE2EArmStart)
	provisionPath := hardwareE2ERequireSummaryWritten(t, provision, provision.writeArgs(provision.path("summary.json")))
	provisionBytes, err := os.ReadFile(provisionPath)
	if err != nil {
		t.Fatal(err)
	}
	readbackManifest := hardwareE2EOutputs()
	hardwareE2EUseReadback(readbackManifest)
	readbackInputs := hardwareE2EBuildSummaryInputsFrom(t, "full", hardwareE2EArmStart, readbackManifest)
	readbackPath := hardwareE2ERequireSummaryWritten(t, readbackInputs, readbackInputs.writeArgs(readbackInputs.path("summary.json")))
	readbackBytes, err := os.ReadFile(readbackPath)
	if err != nil {
		t.Fatal(err)
	}

	leg := func(s map[string]any, i int) map[string]any { return s["legs"].([]any)[i].(map[string]any) }
	field := func(s map[string]any, key string) map[string]any { return s[key].(map[string]any) }
	image := func(s map[string]any) map[string]any { return s["nodeImages"].([]any)[0].(map[string]any) }
	imageField := func(s map[string]any, key string) map[string]any { return image(s)[key].(map[string]any) }
	coverage := func(s map[string]any, i int) map[string]any { return leg(s, i)["coverage"].(map[string]any) }
	staged := func(s map[string]any, i int) map[string]any { return leg(s, i)["staged"].(map[string]any) }
	swap := func(s map[string]any) {
		legs := s["legs"].([]any)
		legs[0], legs[1] = legs[1], legs[0]
	}

	cases := []struct {
		name    string
		base    []byte
		edit    func(map[string]any)
		message string
	}{
		{"authority claimed", fullBytes, func(s map[string]any) { s["qualificationAuthority"] = "qualified" }, "qualificationAuthority is not none"},
		{"authority emptied", fullBytes, func(s map[string]any) { s["qualificationAuthority"] = "" }, "qualificationAuthority is not none"},
		{"authority removed", fullBytes, func(s map[string]any) { delete(s, "qualificationAuthority") }, "unexpected or missing top-level fields"},
		{"a verdict added", fullBytes, func(s map[string]any) { s["verdict"] = "pass" }, "unexpected or missing top-level fields"},
		{"a field removed", fullBytes, func(s map[string]any) { delete(s, "armStartedAfterAmd") }, "unexpected or missing top-level fields"},
		{"another schema", fullBytes, func(s map[string]any) { s["schema"] = "nvkr.dev/hardware-e2e-summary/v2" }, "schema is not nvkr.dev/hardware-e2e-summary/v1"},
		{"a stage the harness does not have", fullBytes, func(s map[string]any) { s["stage"] = "nightly" }, "stage is not one of the harness stages"},
		{"another stage than the coverage", fullBytes, func(s map[string]any) { s["stage"] = "provision" }, "every leg coverage must match the summary stage"},
		{"node user with a space", fullBytes, func(s map[string]any) { s["nodeUser"] = "bad user" }, "nodeUser must be empty or a plain account name"},
		{"node user that is a number", fullBytes, func(s map[string]any) { s["nodeUser"] = 5 }, "nodeUser must be empty or a plain account name"},
		{"harness named by a ref", fullBytes, func(s map[string]any) { field(s, "harness")["commit"] = "main" }, "harness must name exactly a 40-hex commit"},
		{"harness with an extra field", fullBytes, func(s map[string]any) { field(s, "harness")["branch"] = "main" }, "harness must name exactly a 40-hex commit"},
		{"subject without a version", fullBytes, func(s map[string]any) { field(s, "subject")["version"] = "" }, "subject must name a 40-hex commit and a version"},
		{"orchestrator run that is not a number", fullBytes, func(s map[string]any) { field(s, "orchestrator")["runId"] = "run-1" }, "orchestrator must name a 40-hex commit and a run id"},
		{"producer run of zero", fullBytes, func(s map[string]any) { field(s, "producer")["runId"] = "0" }, "producer must name a run id"},
		{"producer run too long to be a run id", fullBytes, func(s map[string]any) { field(s, "producer")["runId"] = strings.Repeat("9", 21) }, "producer must name a run id"},
		{"plan digest that is not hex", fullBytes, func(s map[string]any) { field(s, "plan")["sha256"] = "xyz" }, "plan must carry two sha256 digests"},
		{"no node images", fullBytes, func(s map[string]any) { s["nodeImages"] = []any{} }, "nonempty https locations with a sha256 for NVKR outputs"},
		{"node image over http", fullBytes, func(s map[string]any) { image(s)["url"] = "http://cdn.example.test/node.qcow2" }, "nonempty https locations with a sha256 for NVKR outputs"},
		{"node image URL with credentials", fullBytes, func(s map[string]any) { image(s)["url"] = "https://user@cdn.example.test/node.qcow2" }, "nonempty https locations with a sha256 for NVKR outputs"},
		{"node image URL with a query", fullBytes, func(s map[string]any) { image(s)["url"] = "https://cdn.example.test/node.qcow2?sig=synthetic" }, "nonempty https locations with a sha256 for NVKR outputs"},
		{"node image digest in upper case", fullBytes, func(s map[string]any) { image(s)["sha256"] = strings.Repeat("E", 64) }, "nonempty https locations with a sha256 for NVKR outputs"},
		{"node image in another slot", fullBytes, func(s map[string]any) { image(s)["slot"] = "controller" }, "nonempty https locations with a sha256 for NVKR outputs"},
		{"node image with an extra field", fullBytes, func(s map[string]any) { image(s)["trusted"] = true }, "nonempty https locations with a sha256 for NVKR outputs"},
		{"node image source of an unknown kind", fullBytes, func(s map[string]any) { imageField(s, "source")["kind"] = "docker-hub" }, "each node image must name its source"},
		{"node image source version without the v", fullBytes, func(s map[string]any) { imageField(s, "source")["version"] = "0.24.0" }, "each node image must name its source"},
		{"node image source version of latest", fullBytes, func(s map[string]any) { imageField(s, "source")["version"] = "latest" }, "each node image must name its source"},
		{"node image CI run source that is not https", fullBytes, func(s map[string]any) { imageField(s, "source")["reference"] = "http://ci.example.test/runs/1" }, "each node image must name its source"},
		{"node image CI run source with credentials", fullBytes, func(s map[string]any) { imageField(s, "source")["reference"] = "https://user@ci.example.test/runs/1" }, "each node image must name its source"},
		{"node image release source pinned by tag", fullBytes, func(s map[string]any) {
			imageField(s, "source")["kind"], imageField(s, "source")["reference"] = "nvkr-release", "oci://registry.example.test/team/node:v0.24.0-beta.12"
		}, "each node image must name its source"},
		{"node image source with an extra field", fullBytes, func(s map[string]any) { imageField(s, "source")["trusted"] = true }, "each node image must name its source"},
		{"node image source removed", fullBytes, func(s map[string]any) { delete(image(s), "source") }, "each node image must name its source"},
		{"a CI-run node image not named as not NVKR-built", fullBytes, func(s map[string]any) {
			staged(s, 0)["notNvkrBuilt"] = []any{"deploy-path", "imager-efi", "site-profiles"}
		}, hardwareE2ENodeBuiltRule},
		{"an NVKR release node image named as not NVKR-built", fullBytes, func(s map[string]any) {
			imageField(s, "source")["kind"], imageField(s, "source")["reference"] = "nvkr-release", "oci://registry.example.test/team/node@sha256:"+hardwareE2EDigest("1")
		}, hardwareE2ENodeBuiltRule},
		{"node image check removed", fullBytes, func(s map[string]any) { delete(image(s), "checked") }, hardwareE2ENodeImageRule},
		{"node image check that is null", fullBytes, func(s map[string]any) { image(s)["checked"] = nil }, hardwareE2ENodeImageRule},
		{"node image check that is text", fullBytes, func(s map[string]any) { image(s)["checked"] = "fetched" }, hardwareE2ENodeImageRule},
		{"node image check that is a number", fullBytes, func(s map[string]any) { image(s)["checked"] = 3 }, hardwareE2ENodeImageRule},
		{"node image check that is a list", fullBytes, func(s map[string]any) { image(s)["checked"] = []any{"http-get"} }, hardwareE2ENodeImageRule},
		{"node image check of zero bytes", fullBytes, func(s map[string]any) { imageField(s, "checked")["bytes"] = 0 }, hardwareE2ENodeImageRule},
		{"node image check of a fractional size", fullBytes, func(s map[string]any) { imageField(s, "checked")["bytes"] = 1.5 }, hardwareE2ENodeImageRule},
		{"node image check of a qcow2 version the lane does not support", fullBytes, func(s map[string]any) { imageField(s, "checked")["qcow2Version"] = 4 }, hardwareE2ENodeImageRule},
		{"node image check without a virtual size", fullBytes, func(s map[string]any) { imageField(s, "checked")["virtualSizeBytes"] = 0 }, hardwareE2ENodeImageRule},
		{"node image check with a range answer that is not a boolean", fullBytes, func(s map[string]any) { imageField(s, "checked")["rangeSupported"] = "yes" }, hardwareE2ENodeImageRule},
		{"node image check with an extra field", fullBytes, func(s map[string]any) { imageField(s, "checked")["acceptRanges"] = "bytes" }, hardwareE2ENodeImageRule},
		{"node image check with no method", fullBytes, func(s map[string]any) { delete(imageField(s, "checked"), "method") }, hardwareE2ENodeImageRule},
		{"node image check of an unknown method", fullBytes, func(s map[string]any) { imageField(s, "checked")["method"] = "head-object" }, hardwareE2ENodeImageRule},
		{"node image check of a method in another case", fullBytes, func(s map[string]any) { imageField(s, "checked")["method"] = "HTTP-GET" }, hardwareE2ENodeImageRule},
		{"node image check with no statement of who observed it", fullBytes, func(s map[string]any) { delete(imageField(s, "checked"), "observedByThisRun") }, hardwareE2ENodeImageRule},
		{"a fetched image said not to be observed by this run", fullBytes, func(s map[string]any) { imageField(s, "checked")["observedByThisRun"] = false }, hardwareE2ENodeImageRule},
		{"a fetched image whose observation is not a boolean", fullBytes, func(s map[string]any) { imageField(s, "checked")["observedByThisRun"] = "true" }, hardwareE2ENodeImageRule},
		{"a fetched image citing a run", fullBytes, func(s map[string]any) { imageField(s, "checked")["run"] = hardwareE2EPublicationRun }, hardwareE2ENodeImageRule},
		{"a read-back image said to be observed by this run", readbackBytes, func(s map[string]any) { imageField(s, "checked")["observedByThisRun"] = true }, hardwareE2ENodeImageRule},
		{"a read-back image with no statement of who observed it", readbackBytes, func(s map[string]any) { delete(imageField(s, "checked"), "observedByThisRun") }, hardwareE2ENodeImageRule},
		{"a read-back image with a range answer", readbackBytes, func(s map[string]any) { imageField(s, "checked")["rangeSupported"] = false }, hardwareE2ENodeImageRule},
		{"a read-back image with no run", readbackBytes, func(s map[string]any) { delete(imageField(s, "checked"), "run") }, hardwareE2ENodeImageRule},
		{"a read-back image citing a run over http", readbackBytes, func(s map[string]any) {
			imageField(s, "checked")["run"] = "http://github.com/dsx-ai-factory/nvkr/actions/runs/9000000001"
		}, hardwareE2ENodeImageRule},
		{"a read-back image citing a page that is not a run", readbackBytes, func(s map[string]any) {
			imageField(s, "checked")["run"] = "https://github.com/dsx-ai-factory/nvkr/pull/543"
		}, hardwareE2ENodeImageRule},
		{"a read-back image citing a run with a query", readbackBytes, func(s map[string]any) { imageField(s, "checked")["run"] = hardwareE2EPublicationRun + "?check_suite=1" }, hardwareE2ENodeImageRule},
		{"a read-back image of zero bytes", readbackBytes, func(s map[string]any) { imageField(s, "checked")["bytes"] = 0 }, hardwareE2ENodeImageRule},
		{"a read-back image of a qcow2 version the lane does not support", readbackBytes, func(s map[string]any) { imageField(s, "checked")["qcow2Version"] = 1 }, hardwareE2ENodeImageRule},
		{"a node image version that differs while no skew is declared", fullBytes, func(s map[string]any) { imageField(s, "source")["version"] = "v0.23.1" }, "nodeImageVersionSkew must say"},
		{"a skew declared while every node image matches", fullBytes, func(s map[string]any) { s["nodeImageVersionSkew"] = true }, "nodeImageVersionSkew must say"},
		{"a skew statement that is not a boolean", fullBytes, func(s map[string]any) { s["nodeImageVersionSkew"] = "false" }, "nodeImageVersionSkew must say"},
		{"the skew statement removed", fullBytes, func(s map[string]any) { delete(s, "nodeImageVersionSkew") }, "unexpected or missing top-level fields"},
		{"the same node image slot twice", fullBytes, func(s map[string]any) {
			s["nodeImages"] = []any{s["nodeImages"].([]any)[0], s["nodeImages"].([]any)[0]}
		}, "each node image slot must appear once"},
		{"a leg architecture with no node image", fullBytes, func(s map[string]any) {
			s["nodeImages"] = s["nodeImages"].([]any)[:1]
		}, "every leg architecture must have its node image"},
		{"retained entries that are not names", fullBytes, func(s map[string]any) { s["retainedFromNvke"] = []any{1} }, "retainedFromNvke must be a list of names"},
		{"no planned streams", fullBytes, func(s map[string]any) { s["plannedStreams"] = 0 }, "plannedStreams must be at least 1"},
		{"no legs", fullBytes, func(s map[string]any) { s["legs"] = []any{} }, "legs must be nonempty"},
		{"a leg with an extra field", fullBytes, func(s map[string]any) { leg(s, 0)["verdict"] = "pass" }, "legs must be nonempty with exactly the leg fields"},
		{"a leg on an unsupported architecture", fullBytes, func(s map[string]any) { leg(s, 0)["arch"] = "ppc64le" }, "legs must be nonempty with exactly the leg fields"},
		{"a leg start that is not a time", fullBytes, func(s map[string]any) { leg(s, 0)["startedAt"] = "yesterday" }, "legs must be nonempty with exactly the leg fields"},
		{"legs out of start order", fullBytes, swap, "legs must be ordered by start time"},
		{"one stream in two legs", fullBytes, func(s map[string]any) {
			leg(s, 1)["stream"], leg(s, 1)["combination"] = leg(s, 0)["stream"], leg(s, 0)["combination"]
		}, "a stream appears in more than one leg"},
		{"arm64 claimed to start after amd64 when it did not", fullBytes, func(s map[string]any) {
			swap(s)
			leg(s, 0)["startedAt"] = "2026-11-18T09:00:00Z"
		}, "armStartedAfterAmd must be true"},
		{"armStartedAfterAmd false", fullBytes, func(s map[string]any) { s["armStartedAfterAmd"] = false }, "armStartedAfterAmd must be true"},
		{"coverage of another stage", fullBytes, func(s map[string]any) { coverage(s, 0)["stage"] = "provision" }, "every leg coverage must match the summary stage"},
		{"coverage not complete at the full stage", fullBytes, func(s map[string]any) { coverage(s, 1)["complete"] = false }, "every leg coverage must match the summary stage"},
		{"coverage with specs that did not pass", fullBytes, func(s map[string]any) { coverage(s, 0)["passed"] = 79 }, "every leg coverage must match the summary stage"},
		{"coverage that selected nothing", fullBytes, func(s map[string]any) { coverage(s, 0)["selected"] = 0 }, "every leg coverage must match the summary stage"},
		{"coverage with an extra field", fullBytes, func(s map[string]any) { coverage(s, 0)["verdict"] = "pass" }, "every leg coverage must match the summary stage"},
		{"controller version check with an unknown value", fullBytes, func(s map[string]any) { staged(s, 0)["controllerVersionCheck"] = "skipped" }, "every leg must record what it staged"},
		{"a leg that staged nothing", fullBytes, func(s map[string]any) { staged(s, 0)["files"] = map[string]any{} }, "every leg must record what it staged"},
		{"a staged file digest that is not hex", fullBytes, func(s map[string]any) { staged(s, 1)["files"].(map[string]any)["controller.tar"] = "nothex" }, "every leg must record what it staged"},
		{"not-NVKR-built inputs that are not a list", fullBytes, func(s map[string]any) { staged(s, 0)["notNvkrBuilt"] = "imager-efi" }, "every leg must record what it staged"},
		{"a complete run reported as incomplete", fullBytes, func(s map[string]any) { s["complete"] = false }, "complete must be true exactly for a full-stage run"},
		{"more planned streams than legs, still complete", fullBytes, func(s map[string]any) { s["plannedStreams"] = 3 }, "complete must be true exactly for a full-stage run"},
		{"an unfinished run reported as complete", provisionBytes, func(s map[string]any) { s["complete"] = true }, "complete must be true exactly for a full-stage run"},
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

	raw := []struct{ name, content string }{
		{"an empty file", ""},
		{"text that is not JSON", "not json\n"},
		{"two documents", string(fullBytes) + string(fullBytes)},
		{"a JSON array", "[]\n"},
		{"a JSON string", "\"verified\"\n"},
		{"JSON null", "null\n"},
	}
	for _, tc := range raw {
		t.Run(tc.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "summary.json")
			if err := os.WriteFile(filename, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			hardwareE2EExpectFailure(t, hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", filename), 22, verify, "")
		})
	}

	t.Run("a missing file", func(t *testing.T) {
		hardwareE2EExpectFailure(t, hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", filepath.Join(t.TempDir(), "absent.json")), 2, "NVKR-E-USAGE", "not a regular file")
	})
	t.Run("a symlink to a valid summary", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "summary.json")
		if err := os.Symlink(fullPath, link); err != nil {
			t.Fatal(err)
		}
		hardwareE2EExpectFailure(t, hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", link), 2, "NVKR-E-USAGE", "not a regular file")
	})
	t.Run("the untampered summaries pass", func(t *testing.T) {
		for _, filename := range []string{fullPath, provisionPath, readbackPath} {
			if result := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", filename); result.exit != 0 {
				t.Errorf("assert %s: exit %d\n%s", filename, result.exit, result.output)
			}
		}
	})
}
