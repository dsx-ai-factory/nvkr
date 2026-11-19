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

// These contracts cover the script that records what a Forge hardware lane run covered and
// refuses a record that claims more. Every input is synthetic or the committed plan fixture;
// nothing here is evidence about a run on hardware.

const (
	forgeE2ESummaryTool        = "scripts/forge-e2e-summary.sh"
	forgeE2EOrchestratorCommit = "81c8f1e2a9b3d4c5e6f708192a3b4c5d6e7f8091"
	forgeE2EOrchestratorRun    = "9876543210"
	forgeE2EAmdStart           = "2026-11-18T10:00:00Z"
	forgeE2EArmStart           = "2026-11-18T12:00:00Z"
)

var forgeE2ESummaryFields = []string{
	"armStartedAfterAmd", "artifactSource", "complete", "harness", "legs", "nodeImageVersionSkew", "nodeImages", "nodeUser",
	"orchestrator", "plan", "plannedStreams", "producer", "qualificationAuthority", "retainedFromNvke",
	"schema", "stage", "subject",
}

var forgeE2ENodeImageSlots = []string{"os-image-amd64", "os-image-arm64"}

type forgeE2ESummary struct {
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
	} `json:"legs"`
}

func forgeE2EReadMap(t *testing.T, filename string) map[string]any {
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

func forgeE2EWriteMap(t *testing.T, filename string, value map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func forgeE2EEditJSON(t *testing.T, filename string, edit func(map[string]any)) {
	t.Helper()
	value := forgeE2EReadMap(t, filename)
	edit(value)
	forgeE2EWriteMap(t, filename, value)
}

func forgeE2EReadSummary(t *testing.T, filename string) forgeE2ESummary {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var summary forgeE2ESummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("decode %s: %v", filename, err)
	}
	return summary
}

func forgeE2EExpectFailure(t *testing.T, result forgeE2EResult, exit int, code, message string) {
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

func forgeE2ECoverage(stage string) map[string]any {
	passed := 80
	if stage != "full" {
		passed = 7
	}
	return map[string]any{
		"schema":       "nvkr.dev/forge-e2e-coverage/v1",
		"stage":        stage,
		"complete":     stage == "full",
		"selected":     80,
		"passed":       passed,
		"specsInSuite": 530,
		"labelFilter":  "site-controller && !nico-dsx",
		"tenantLabel":  "nico-forge",
		"categories":   map[string]any{"provisioning": 7, "site-checks": 31, "tenant": 42},
		"dryRunSha256": forgeE2EDigest("1"),
		"reportSha256": forgeE2EDigest("2"),
	}
}

func forgeE2EStaged(arch string) map[string]any {
	return map[string]any{
		"schema": "nvkr.dev/forge-e2e-staged/v1",
		"arch":   arch,
		"files": map[string]any{
			"controller.tar":        forgeE2EDigest("3"),
			"capi-provisioned.tar":  forgeE2EDigest("4"),
			"nvkectl-linux-" + arch: forgeE2EDigest("5"),
		},
		"source":                 "nvkr-outputs",
		"builtFromCommit":        nil,
		"controllerVersionCheck": "matched",
		"notNvkrBuilt":           []any{"deploy-path", "imager-efi", "site-profiles"},
	}
}

// forgeE2EStagedBuilt is what forge-e2e-build-subject.sh records for a leg: everything built from
// the subject commit, and nothing NVKR-built.
func forgeE2EStagedBuilt(arch string) map[string]any {
	staged := forgeE2EStaged(arch)
	staged["source"] = "build-subject"
	staged["builtFromCommit"] = forgeE2ECommit
	staged["controllerVersionCheck"] = "not-determined"
	staged["notNvkrBuilt"] = []any{"capi-provisioned", "cli", "controller", "deploy-path", "imager-efi", "node-image", "site-profiles"}
	return staged
}

// forgeE2ENodeImageBytes is the size every synthetic node image check reports.
const forgeE2ENodeImageBytes = 538 * 1000 * 1000

// forgeE2ENodeImageRule is the summary rule that holds each node image's check record.
const forgeE2ENodeImageRule = "each node image must record how it was checked, whether this run observed that, and what the check saw"

// forgeE2ENodeImageCheck is what forge-e2e-check-node-image.py writes for a node image, built
// from the same synthetic outputs manifest the substitution script was given.
func forgeE2ENodeImageCheck(manifest map[string]any, slot string) map[string]any {
	image := forgeE2ESlots(manifest)[slot].(map[string]any)
	if verification := image["verification"].(map[string]any); verification["method"] == "s3-readback" {
		// The check does not fetch such an image: it records what the publication run reported.
		return map[string]any{
			"schema":            "nvkr.dev/forge-e2e-node-image-check/v1",
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
		"schema":            "nvkr.dev/forge-e2e-node-image-check/v1",
		"slot":              slot,
		"method":            "http-get",
		"observedByThisRun": true,
		"url":               image["url"],
		"sha256":            image["sha256"],
		"bytes":             forgeE2ENodeImageBytes,
		"rangeSupported":    false,
		"acceptRanges":      "",
		"qcow2":             map[string]any{"version": 3, "virtualSizeBytes": 12 << 30, "clusterSizeBytes": 65536},
	}
}

type forgeE2ESummaryStream struct{ name, arch string }

// forgeE2ESummaryInputs is a scratch directory holding everything `write` consumes: the
// substituted plan and its record (from the real substitution script), the matrix, and one
// stamped, covered and staged directory per stream.
type forgeE2ESummaryInputs struct {
	scratch       string
	stage         string
	source        string
	harnessCommit string
	combination   string
	streams       []forgeE2ESummaryStream
}

func (in forgeE2ESummaryInputs) path(parts ...string) string {
	return filepath.Join(append([]string{in.scratch}, parts...)...)
}

func (in forgeE2ESummaryInputs) leg(arch string) string { return in.path("leg-" + arch) }

func (in forgeE2ESummaryInputs) nodeCheck(slot string) string {
	return in.path("check-" + slot + ".json")
}

func (in forgeE2ESummaryInputs) writeArgs(out string) []string {
	args := []string{
		"write", out,
		"--artifact-source", in.source,
		"--stage", in.stage,
		"--harness-commit", in.harnessCommit,
		"--orchestrator-commit", forgeE2EOrchestratorCommit,
		"--orchestrator-run", forgeE2EOrchestratorRun,
		"--plan", in.path("sub", "plan.json"),
		"--substitutions", in.path("sub", "substitutions.json"),
		"--matrix", in.path("matrix.json"),
	}
	if in.source == "nvkr-outputs" {
		for _, slot := range forgeE2ENodeImageSlots {
			args = append(args, "--node-image-check", in.nodeCheck(slot))
		}
	}
	for _, stream := range in.streams {
		args = append(args, "--leg", in.leg(stream.arch))
	}
	return args
}

func forgeE2EBuildSummaryInputs(t *testing.T, stage, armStart string) forgeE2ESummaryInputs {
	t.Helper()
	return forgeE2EBuildSummaryInputsFrom(t, stage, armStart, forgeE2EOutputs())
}

// forgeE2EBuildSummaryInputsFrom builds the inputs from the given outputs manifest, running the
// real substitution script with any extra leading flags.
func forgeE2EBuildSummaryInputsFrom(t *testing.T, stage, armStart string, manifest map[string]any, substituteFlags ...string) forgeE2ESummaryInputs {
	t.Helper()
	root := forgeE2ERoot(t)
	in := forgeE2ESummaryInputs{scratch: t.TempDir(), stage: stage, source: "nvkr-outputs", harnessCommit: forgeE2ECommit}
	outputs := forgeE2EWriteJSON(t, in.scratch, "outputs.json", manifest)
	for _, slot := range forgeE2ENodeImageSlots {
		forgeE2EWriteJSON(t, in.scratch, "check-"+slot+".json", forgeE2ENodeImageCheck(manifest, slot))
	}
	substituted := forgeE2ERun(t, forgeE2ESubstituteTool, append(slices.Clone(substituteFlags),
		filepath.Join(root, forgeE2EPlanFile), filepath.Join(root, forgeE2EProvenanceFile), outputs, in.path("sub"))...)
	if substituted.exit != 0 {
		t.Fatalf("substitute plan: exit %d\n%s", substituted.exit, substituted.output)
	}
	return forgeE2EFinishSummaryInputs(t, in, armStart)
}

// forgeE2EBuildSubjectSummaryInputs builds the inputs for a run whose artifacts were built from the
// subject commit: the plan is kept as the fixture, there are no node image checks, and each leg
// staged what forge-e2e-build-subject.sh records.
func forgeE2EBuildSubjectSummaryInputs(t *testing.T, stage, armStart string) forgeE2ESummaryInputs {
	t.Helper()
	root := forgeE2ERoot(t)
	return forgeE2EBuildSubjectSummaryInputsFrom(t, stage, armStart,
		filepath.Join(root, forgeE2EPlanFile), filepath.Join(root, forgeE2EProvenanceFile), forgeE2ECommit)
}

// forgeE2EBuildSubjectSummaryInputsFrom takes the plan, its provenance and the harness commit the
// legs were built at, for a test that needs a subject commit it can check out.
func forgeE2EBuildSubjectSummaryInputsFrom(t *testing.T, stage, armStart, planFile, provenanceFile, harnessCommit string) forgeE2ESummaryInputs {
	t.Helper()
	in := forgeE2ESummaryInputs{scratch: t.TempDir(), stage: stage, source: "build-subject", harnessCommit: harnessCommit}
	substituted := forgeE2ERun(t, forgeE2ESubstituteTool, "--build-subject", planFile, provenanceFile, in.path("sub"))
	if substituted.exit != 0 {
		t.Fatalf("keep plan: exit %d\n%s", substituted.exit, substituted.output)
	}
	return forgeE2EFinishSummaryInputs(t, in, armStart)
}

// forgeE2EFinishSummaryInputs reads the plan in in.path("sub") and writes the matrix and one
// stamped, covered and staged directory per stream, staged as the artifact source says.
func forgeE2EFinishSummaryInputs(t *testing.T, in forgeE2ESummaryInputs, armStart string) forgeE2ESummaryInputs {
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
	var amd, arm []forgeE2ESummaryStream
	for _, stream := range plan.Combinations[0].Streams {
		switch {
		case strings.Contains(stream.Name, "-amd64-"):
			amd = append(amd, forgeE2ESummaryStream{stream.Name, "amd64"})
		case strings.Contains(stream.Name, "-arm64-"):
			arm = append(arm, forgeE2ESummaryStream{stream.Name, "arm64"})
		default:
			t.Fatalf("stream %q names no architecture", stream.Name)
		}
	}
	in.streams = append(amd, arm...)

	var matrix []map[string]any
	for _, stream := range in.streams {
		matrix = append(matrix, forgeE2EStream(in.combination, stream.name, stream.arch, "forge"))
		directory := in.leg(stream.arch)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		start := forgeE2EAmdStart
		if stream.arch == "arm64" {
			start = armStart
		}
		stamped := forgeE2ERunEnv(t, []string{"FORGE_E2E_NOW=" + start}, forgeE2ESummaryTool,
			"leg", directory, stream.name, in.combination, stream.arch)
		if stamped.exit != 0 {
			t.Fatalf("stamp %s: exit %d\n%s", stream.name, stamped.exit, stamped.output)
		}
		forgeE2EWriteJSON(t, directory, "coverage.json", forgeE2ECoverage(stage))
		staged := forgeE2EStaged(stream.arch)
		if in.source == "build-subject" {
			staged = forgeE2EStagedBuilt(stream.arch)
		}
		forgeE2EWriteJSON(t, directory, "staged.json", staged)
	}
	forgeE2EMatrixFile(t, in.scratch, matrix...)
	return in
}

func forgeE2ESetFlag(args []string, flag, value string) []string {
	args = slices.Clone(args)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			args[i+1] = value
			return args
		}
	}
	return append(args, flag, value)
}

// forgeE2EDropFlag removes a flag and its value; an empty value removes every occurrence.
func forgeE2EDropFlag(args []string, flag, value string) []string {
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

func forgeE2ERequireSummaryWritten(t *testing.T, in forgeE2ESummaryInputs, args []string) string {
	t.Helper()
	result := forgeE2ERun(t, forgeE2ESummaryTool, args...)
	if result.exit != 0 {
		t.Fatalf("write: exit %d, want 0:\n%s", result.exit, result.output)
	}
	return args[1]
}

func TestForgeE2ESummaryRecordsAFullRunAndDescribesItself(t *testing.T) {
	root := forgeE2ERoot(t)
	in := forgeE2EBuildSummaryInputs(t, "full", forgeE2EArmStart)
	out := in.path("summary.json")

	result := forgeE2ERun(t, forgeE2ESummaryTool, append(in.writeArgs(out), "--node-user", "dgxc-admin")...)
	if result.exit != 0 {
		t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
	}
	if !strings.Contains(result.output, "qualificationAuthority none") {
		t.Errorf("the script should say it recorded no authority:\n%s", result.output)
	}

	summary := forgeE2EReadSummary(t, out)
	plan := forgeE2EReadPlan(t, filepath.Join(root, forgeE2EPlanFile))
	substituted, err := os.ReadFile(in.path("sub", "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join(root, forgeE2EPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	slots := forgeE2ESlots(forgeE2EOutputs())
	image := func(slot string) map[string]any { return slots[slot].(map[string]any) }

	checks := []struct {
		name      string
		got, want any
	}{
		{"schema", summary.Schema, "nvkr.dev/forge-e2e-summary/v1"},
		{"authority", summary.QualificationAuthority, "none"},
		{"stage", summary.Stage, "full"},
		{"complete", summary.Complete, true},
		{"nodeUser", summary.NodeUser, "dgxc-admin"},
		{"harness commit", summary.Harness.Commit, forgeE2ECommit},
		{"subject commit", summary.Subject.Commit, plan.Commit},
		{"subject version", summary.Subject.Version, plan.Version},
		{"orchestrator commit", summary.Orchestrator.Commit, forgeE2EOrchestratorCommit},
		{"orchestrator run", summary.Orchestrator.RunID, forgeE2EOrchestratorRun},
		{"producer run", summary.Producer.RunID, "1234567890"},
		{"substituted plan digest", summary.Plan.SHA256, forgeE2ESHA256(substituted)},
		{"fixture digest", summary.Plan.FixtureSHA256, forgeE2ESHA256(fixture)},
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
		{"first node image source version", summary.NodeImages[0].Source.Version, forgeE2EVersion},
		{"second node image source version", summary.NodeImages[1].Source.Version, forgeE2EVersion},
		{"first node image bytes seen", summary.NodeImages[0].Checked.Bytes, int64(forgeE2ENodeImageBytes)},
		{"first node image qcow2 version", summary.NodeImages[0].Checked.QCOW2Version, 3},
		{"first node image virtual size", summary.NodeImages[0].Checked.VirtualSizeBytes, int64(12 << 30)},
		{"first node image range support", summary.NodeImages[0].Checked.RangeSupported, false},
		{"legs", len(summary.Legs), 2},
		{"first leg arch", summary.Legs[0].Arch, "amd64"},
		{"first leg start", summary.Legs[0].StartedAt, forgeE2EAmdStart},
		{"second leg arch", summary.Legs[1].Arch, "arm64"},
		{"second leg start", summary.Legs[1].StartedAt, forgeE2EArmStart},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}

	if got := slices.Sorted(maps.Keys(forgeE2EReadMap(t, out))); !slices.Equal(got, forgeE2ESummaryFields) {
		t.Errorf("summary fields = %v, want %v", got, forgeE2ESummaryFields)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("summary mode = %v (%v), want 0600", info, err)
	}

	if asserted := forgeE2ERun(t, forgeE2ESummaryTool, "assert", out); asserted.exit != 0 {
		t.Errorf("assert of a written summary: exit %d\n%s", asserted.exit, asserted.output)
	}

	defaulted := forgeE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("default-user.json")))
	if got := forgeE2EReadSummary(t, defaulted).NodeUser; got != "" {
		t.Errorf("nodeUser without --node-user = %q, want the harness default (empty)", got)
	}
}

func TestForgeE2ESummaryLegStampsItsStartAndRefusesToRestamp(t *testing.T) {
	directory := t.TempDir()
	run := func(env []string, args ...string) forgeE2EResult {
		t.Helper()
		return forgeE2ERunEnv(t, env, forgeE2ESummaryTool, append([]string{"leg"}, args...)...)
	}
	fixed := []string{"FORGE_E2E_NOW=" + forgeE2EAmdStart}

	if result := run(fixed, directory, "forge-gb200-amd64", "combination-1", "amd64"); result.exit != 0 {
		t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
	}
	got := forgeE2EReadMap(t, filepath.Join(directory, "leg.json"))
	want := map[string]any{
		"schema":      "nvkr.dev/forge-e2e-leg/v1",
		"stream":      "forge-gb200-amd64",
		"combination": "combination-1",
		"arch":        "amd64",
		"startedAt":   forgeE2EAmdStart,
	}
	if !maps.Equal(got, want) {
		t.Errorf("leg.json = %v, want %v", got, want)
	}
	if info, err := os.Stat(filepath.Join(directory, "leg.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("leg.json mode = %v (%v), want 0600", info, err)
	}

	again := run([]string{"FORGE_E2E_NOW=2026-11-18T13:00:00Z"}, directory, "forge-gb200-amd64", "combination-1", "amd64")
	forgeE2EExpectFailure(t, again, 47, "NVKR-E-IMMUTABLE-VIOLATION", "")
	if after := forgeE2EReadMap(t, filepath.Join(directory, "leg.json")); after["startedAt"] != forgeE2EAmdStart {
		t.Errorf("a refused restamp changed the start to %v", after["startedAt"])
	}

	t.Run("stamps the real clock when none is injected", func(t *testing.T) {
		directory := t.TempDir()
		before := time.Now().UTC().Truncate(time.Second)
		result := run([]string{"FORGE_E2E_NOW="}, directory, "s", "c", "arm64")
		after := time.Now().UTC()
		if result.exit != 0 {
			t.Fatalf("exit %d:\n%s", result.exit, result.output)
		}
		stamped, err := time.Parse(time.RFC3339, forgeE2EReadMap(t, filepath.Join(directory, "leg.json"))["startedAt"].(string))
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
		{"clock that is not a time", []string{"FORGE_E2E_NOW=yesterday"}, func(d string) []string { return []string{d, "s", "c", "amd64"} }, 2, "NVKR-E-USAGE", true},
		{"clock that is not UTC", []string{"FORGE_E2E_NOW=2026-11-18T10:00:00+01:00"}, func(d string) []string { return []string{d, "s", "c", "amd64"} }, 2, "NVKR-E-USAGE", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			forgeE2EExpectFailure(t, run(tc.env, tc.args(directory)...), tc.exit, tc.code, "")
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
		forgeE2EExpectFailure(t, run(fixed, link, "s", "c", "amd64"), 2, "NVKR-E-USAGE", "")
		if _, err := os.Lstat(filepath.Join(target, "leg.json")); err == nil {
			t.Error("a stamp followed a symlink out of the leg directory")
		}
	})
}

func TestForgeE2ESummaryUsage(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"verify"},
		{"assert"},
		{"assert", "a", "b"},
		{"write"},
	} {
		forgeE2EExpectFailure(t, forgeE2ERun(t, forgeE2ESummaryTool, args...), 2, "NVKR-E-USAGE", "")
	}
}

type forgeE2EMutation func(t *testing.T, in forgeE2ESummaryInputs, args []string) []string

// forgeE2EEdit edits one scratch file and leaves the arguments alone.
func forgeE2EEdit(edit func(map[string]any), parts ...string) forgeE2EMutation {
	return func(t *testing.T, in forgeE2ESummaryInputs, args []string) []string {
		t.Helper()
		forgeE2EEditJSON(t, in.path(parts...), edit)
		return args
	}
}

// forgeE2EArgs edits only the arguments.
func forgeE2EArgs(edit func(in forgeE2ESummaryInputs, args []string) []string) forgeE2EMutation {
	return func(t *testing.T, in forgeE2ESummaryInputs, args []string) []string {
		t.Helper()
		return edit(in, args)
	}
}

func forgeE2EReplaced(slot, field string, value any) func(map[string]any) {
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

// forgeE2EReplacedSource edits one field of a node image's source in the substitution record.
func forgeE2EReplacedSource(slot, field string, value any) func(map[string]any) {
	return func(record map[string]any) {
		for _, entry := range record["replaced"].([]any) {
			if replacement := entry.(map[string]any); replacement["slot"] == slot {
				replacement["source"].(map[string]any)[field] = value
			}
		}
	}
}

// forgeE2ENodeCheckEdit edits one node image's check record and leaves the arguments alone.
func forgeE2ENodeCheckEdit(slot string, edit func(map[string]any)) forgeE2EMutation {
	return func(t *testing.T, in forgeE2ESummaryInputs, args []string) []string {
		t.Helper()
		forgeE2EEditJSON(t, in.nodeCheck(slot), edit)
		return args
	}
}

// forgeE2ENodeImageBoth changes a node image in the substitution record and in its check alike.
// A nil value removes the field.
func forgeE2ENodeImageBoth(slot, replacedField, checkField string, value any) forgeE2EMutation {
	return func(t *testing.T, in forgeE2ESummaryInputs, args []string) []string {
		t.Helper()
		forgeE2EEditJSON(t, in.path("sub", "substitutions.json"), forgeE2EReplaced(slot, replacedField, value))
		forgeE2EEditJSON(t, in.nodeCheck(slot), func(check map[string]any) {
			if value == nil {
				delete(check, checkField)
			} else {
				check[checkField] = value
			}
		})
		return args
	}
}

func TestForgeE2ESummaryWriteRefusesWhatItCannotSupport(t *testing.T) {
	const verify = "NVKR-E-VERIFY-FAILED"
	cases := []struct {
		name     string
		stage    string
		armStart string
		mutate   forgeE2EMutation
		exit     int
		code     string
		message  string
	}{
		// Usage.
		{name: "no legs", exit: 2, code: "NVKR-E-USAGE",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string { return forgeE2EDropFlag(a, "--leg", "") })},
		{name: "no stage", exit: 2, code: "NVKR-E-USAGE",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string { return forgeE2EDropFlag(a, "--stage", "") })},
		{name: "no matrix", exit: 2, code: "NVKR-E-USAGE",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string { return forgeE2EDropFlag(a, "--matrix", "") })},
		{name: "flag without a value", exit: 2, code: "NVKR-E-USAGE", message: "needs a value",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string { return append(a[:2:2], "--stage") })},
		{name: "unknown flag", exit: 2, code: "NVKR-E-USAGE", message: "unknown argument",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return append(slices.Clone(a), "--verdict", "pass")
			})},
		{name: "no output path", exit: 2, code: "NVKR-E-USAGE",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, _ []string) []string { return []string{"write"} })},
		{name: "plan is a directory", exit: 2, code: "NVKR-E-USAGE", message: "not a regular file",
			mutate: forgeE2EArgs(func(in forgeE2ESummaryInputs, a []string) []string { return forgeE2ESetFlag(a, "--plan", in.scratch) })},
		{name: "leg directory does not exist", exit: 2, code: "NVKR-E-USAGE", message: "leg directory does not exist",
			mutate: forgeE2EArgs(func(in forgeE2ESummaryInputs, a []string) []string {
				return append(slices.Clone(a), "--leg", in.path("nowhere"))
			})},
		{name: "leg without coverage", exit: 2, code: "NVKR-E-USAGE", message: "not a regular file",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				if err := os.Remove(filepath.Join(in.leg("amd64"), "coverage.json")); err != nil {
					t.Fatal(err)
				}
				return a
			}},

		// Existing output.
		{name: "output already exists", exit: 47, code: "NVKR-E-IMMUTABLE-VIOLATION",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				if err := os.WriteFile(a[1], []byte("keep\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return a
			}},

		// Inputs the script does not understand.
		{name: "substitution record with another schema", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "unsupported schema",
			mutate: forgeE2EEdit(func(m map[string]any) { m["schema"] = "nvkr.dev/forge-e2e-substitutions/v2" }, "sub", "substitutions.json")},
		{name: "matrix naming no streams", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "names no streams",
			mutate: forgeE2EEdit(func(m map[string]any) { m["include"] = []any{} }, "matrix.json")},
		{name: "plan that is not JSON", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "could not be assembled",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				if err := os.WriteFile(in.path("sub", "plan.json"), []byte("not json\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return a
			}},
		{name: "coverage with another schema", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "unsupported schema",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				forgeE2EEditJSON(t, filepath.Join(in.leg("amd64"), "coverage.json"), func(m map[string]any) { m["schema"] = "nvkr.dev/forge-e2e-coverage/v2" })
				return a
			}},
		{name: "staged files for another architecture", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "mixes architectures",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				forgeE2EEditJSON(t, filepath.Join(in.leg("amd64"), "staged.json"), func(m map[string]any) { m["arch"] = "arm64" })
				return a
			}},
		{name: "evidence for a stream that was not dispatched", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "were not dispatched",
			mutate: forgeE2EEdit(func(m map[string]any) { m["include"] = m["include"].([]any)[:1] }, "matrix.json")},

		// A dispatched leg with no evidence.
		{name: "dispatched leg without evidence", exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "no evidence for dispatched legs",
			mutate: forgeE2EArgs(func(in forgeE2ESummaryInputs, a []string) []string {
				return forgeE2EDropFlag(a, "--leg", in.leg("arm64"))
			})},
		{name: "the same leg given twice", exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "no evidence for dispatched legs",
			mutate: forgeE2EArgs(func(in forgeE2ESummaryInputs, a []string) []string {
				return append(slices.Clone(a), "--leg", in.leg("amd64"))
			})},
		{name: "evidence on another architecture than was dispatched", exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "no evidence for dispatched legs",
			mutate: forgeE2EEdit(func(m map[string]any) { m["include"].([]any)[0].(map[string]any)["arch"] = "arm64" }, "matrix.json")},

		// The contract the summary must meet before it is written.
		{name: "a stage the harness does not have", exit: 22, code: verify, message: "stage is not one of the harness stages",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string { return forgeE2ESetFlag(a, "--stage", "nightly") })},
		{name: "coverage of another stage", exit: 22, code: verify, message: "every leg coverage must match the summary stage",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string { return forgeE2ESetFlag(a, "--stage", "provision") })},
		{name: "coverage claiming completeness before the full stage", stage: "provision", exit: 22, code: verify, message: "every leg coverage must match the summary stage",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				forgeE2EEditJSON(t, filepath.Join(in.leg("amd64"), "coverage.json"), func(m map[string]any) { m["complete"] = true })
				return a
			}},
		{name: "full stage with incomplete coverage", exit: 22, code: verify, message: "every leg coverage must match the summary stage",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				forgeE2EEditJSON(t, filepath.Join(in.leg("amd64"), "coverage.json"), func(m map[string]any) { m["complete"] = false })
				return a
			}},
		{name: "full stage where a selected spec did not pass", exit: 22, code: verify, message: "every leg coverage must match the summary stage",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				forgeE2EEditJSON(t, filepath.Join(in.leg("arm64"), "coverage.json"), func(m map[string]any) { m["passed"] = 79 })
				return a
			}},
		{name: "coverage that selected nothing", exit: 22, code: verify, message: "every leg coverage must match the summary stage",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				forgeE2EEditJSON(t, filepath.Join(in.leg("amd64"), "coverage.json"), func(m map[string]any) { m["selected"] = 0 })
				return a
			}},
		{name: "harness commit that is not a full hash", exit: 22, code: verify, message: "harness must name exactly a 40-hex commit",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return forgeE2ESetFlag(a, "--harness-commit", "c044392")
			})},
		{name: "harness named by a ref", exit: 22, code: verify, message: "harness must name exactly a 40-hex commit",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return forgeE2ESetFlag(a, "--harness-commit", "main")
			})},
		{name: "orchestrator commit in upper case", exit: 22, code: verify, message: "orchestrator must name a 40-hex commit and a run id",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return forgeE2ESetFlag(a, "--orchestrator-commit", strings.ToUpper(forgeE2EOrchestratorCommit))
			})},
		{name: "orchestrator run that is not a number", exit: 22, code: verify, message: "orchestrator must name a 40-hex commit and a run id",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return forgeE2ESetFlag(a, "--orchestrator-run", "latest")
			})},
		{name: "node user with a space", exit: 22, code: verify, message: "nodeUser must be empty or a plain account name",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return append(slices.Clone(a), "--node-user", "bad user")
			})},
		{name: "node user that looks like an option", exit: 22, code: verify, message: "nodeUser must be empty or a plain account name",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return append(slices.Clone(a), "--node-user", "-rf")
			})},
		{name: "controller version check with an unknown value", exit: 22, code: verify, message: "every leg must record what it staged",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				forgeE2EEditJSON(t, filepath.Join(in.leg("amd64"), "staged.json"), func(m map[string]any) { m["controllerVersionCheck"] = "skipped" })
				return a
			}},
		{name: "a leg that staged nothing", exit: 22, code: verify, message: "every leg must record what it staged",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				forgeE2EEditJSON(t, filepath.Join(in.leg("arm64"), "staged.json"), func(m map[string]any) { m["files"] = map[string]any{} })
				return a
			}},
		{name: "a staged file without a sha256", exit: 22, code: verify, message: "every leg must record what it staged",
			mutate: func(t *testing.T, in forgeE2ESummaryInputs, a []string) []string {
				t.Helper()
				forgeE2EEditJSON(t, filepath.Join(in.leg("amd64"), "staged.json"), func(m map[string]any) { m["files"].(map[string]any)["controller.tar"] = "not-a-digest" })
				return a
			}},
		{name: "arm64 leg started before the amd64 leg", armStart: "2026-11-18T09:00:00Z", exit: 22, code: verify, message: "armStartedAfterAmd must be true"},
		{name: "arm64 leg started at the same instant as the amd64 leg", armStart: forgeE2EAmdStart, exit: 22, code: verify, message: "armStartedAfterAmd must be true"},
		// The node image the plan carries, and the check that saw it. These change the record and its
		// check together, so the two agree and only the summary contract can refuse the image.
		{name: "node image over plain http", exit: 22, code: verify, message: "nonempty https locations with a sha256 for NVKR outputs",
			mutate: forgeE2ENodeImageBoth("os-image-amd64", "to", "url", "http://cdn.example.test/node.qcow2")},
		{name: "node image URL with credentials", exit: 22, code: verify, message: "nonempty https locations with a sha256 for NVKR outputs",
			mutate: forgeE2ENodeImageBoth("os-image-amd64", "to", "url", "https://user@cdn.example.test/node.qcow2")},
		{name: "node image URL with a query", exit: 22, code: verify, message: "nonempty https locations with a sha256 for NVKR outputs",
			mutate: forgeE2ENodeImageBoth("os-image-arm64", "to", "url", "https://cdn.example.test/node.qcow2?sig=synthetic")},
		{name: "node image without a digest", exit: 22, code: verify, message: "nonempty https locations with a sha256 for NVKR outputs",
			mutate: forgeE2ENodeImageBoth("os-image-amd64", "sha256", "sha256", nil)},

		// The node image checks: required, one per slot, and naming exactly the bytes the plan carries.
		{name: "no node image check", exit: 2, code: "NVKR-E-USAGE",
			mutate: forgeE2EArgs(func(_ forgeE2ESummaryInputs, a []string) []string {
				return forgeE2EDropFlag(a, "--node-image-check", "")
			})},
		{name: "node image check that does not exist", exit: 2, code: "NVKR-E-USAGE", message: "not a regular file",
			mutate: forgeE2EArgs(func(in forgeE2ESummaryInputs, a []string) []string {
				return forgeE2ESetFlag(forgeE2EDropFlag(a, "--node-image-check", in.nodeCheck("os-image-arm64")), "--node-image-check", in.path("nowhere.json"))
			})},
		{name: "node image check with another schema", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "node image check",
			mutate: forgeE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["schema"] = "nvkr.dev/forge-e2e-node-image-check/v2" })},
		{name: "node image check for a slot the plan did not substitute", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "names a slot the plan did not substitute",
			mutate: forgeE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["slot"] = "os-image-ppc64le" })},
		{name: "node image check for a slot that is not a node image", exit: 13, code: "NVKR-E-UNSUPPORTED", message: "names a slot the plan did not substitute",
			mutate: forgeE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["slot"] = "controller" })},
		{name: "node image check of another URL than the plan carries", exit: 22, code: verify, message: "names other bytes than the plan for: os-image-amd64",
			mutate: forgeE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["url"] = "https://cdn.example.test/other.qcow2" })},
		{name: "node image check of other bytes than the plan carries", exit: 22, code: verify, message: "names other bytes than the plan for: os-image-arm64",
			mutate: forgeE2ENodeCheckEdit("os-image-arm64", func(m map[string]any) { m["sha256"] = forgeE2EDigest("9") })},
		{name: "only one node image checked", exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "without exactly one check: os-image-arm64",
			mutate: forgeE2EArgs(func(in forgeE2ESummaryInputs, a []string) []string {
				return forgeE2EDropFlag(a, "--node-image-check", in.nodeCheck("os-image-arm64"))
			})},
		{name: "one node image checked twice", exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "without exactly one check",
			mutate: forgeE2EArgs(func(in forgeE2ESummaryInputs, a []string) []string {
				return append(forgeE2EDropFlag(a, "--node-image-check", in.nodeCheck("os-image-arm64")), "--node-image-check", in.nodeCheck("os-image-amd64"))
			})},
		{name: "node image check that read nothing", exit: 22, code: verify, message: forgeE2ENodeImageRule,
			mutate: forgeE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["bytes"] = 0 })},
		{name: "node image check of a qcow2 version the lane does not support", exit: 22, code: verify, message: forgeE2ENodeImageRule,
			mutate: forgeE2ENodeCheckEdit("os-image-arm64", func(m map[string]any) { m["qcow2"].(map[string]any)["version"] = 4 })},
		{name: "node image check with a range answer that is not a boolean", exit: 22, code: verify, message: forgeE2ENodeImageRule,
			mutate: forgeE2ENodeCheckEdit("os-image-amd64", func(m map[string]any) { m["rangeSupported"] = "yes" })},

		// Where the node image came from, and whether its NVKE version differs from the plan's.
		{name: "node image source of an unknown kind", exit: 22, code: verify, message: "each node image must name its source",
			mutate: forgeE2EEdit(forgeE2EReplacedSource("os-image-amd64", "kind", "docker-hub"), "sub", "substitutions.json")},
		{name: "node image source version that is not a version", exit: 22, code: verify, message: "each node image must name its source",
			mutate: forgeE2EEdit(forgeE2EReplacedSource("os-image-arm64", "version", "latest"), "sub", "substitutions.json")},
		{name: "node image source with a field the contract does not know", exit: 22, code: verify, message: "each node image must name its source",
			mutate: forgeE2EEdit(forgeE2EReplacedSource("os-image-amd64", "trusted", true), "sub", "substitutions.json")},
		{name: "node image with no source", exit: 22, code: verify, message: "each node image must name its source",
			mutate: forgeE2EEdit(forgeE2EReplaced("os-image-amd64", "source", nil), "sub", "substitutions.json")},
		{name: "substitution record that says nothing about version skew", exit: 22, code: verify, message: "nodeImageVersionSkew must say",
			mutate: forgeE2EEdit(func(m map[string]any) { delete(m, "nodeImageVersionSkew") }, "sub", "substitutions.json")},
		{name: "substitution record that claims a skew nobody has", exit: 22, code: verify, message: "nodeImageVersionSkew must say",
			mutate: forgeE2EEdit(func(m map[string]any) { m["nodeImageVersionSkew"] = true }, "sub", "substitutions.json")},
		{name: "substitution record that hides a skew", exit: 22, code: verify, message: "nodeImageVersionSkew must say",
			mutate: forgeE2EEdit(forgeE2EReplacedSource("os-image-arm64", "version", "v0.23.1"), "sub", "substitutions.json")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stage, armStart := tc.stage, tc.armStart
			if stage == "" {
				stage = "full"
			}
			if armStart == "" {
				armStart = forgeE2EArmStart
			}
			in := forgeE2EBuildSummaryInputs(t, stage, armStart)
			out := in.path("summary.json")
			args := in.writeArgs(out)
			if tc.mutate != nil {
				args = tc.mutate(t, in, args)
			}

			forgeE2EExpectFailure(t, forgeE2ERun(t, forgeE2ESummaryTool, args...), tc.exit, tc.code, tc.message)

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

func TestForgeE2ESummaryOfAnUnfinishedRunIsNeverComplete(t *testing.T) {
	for _, stage := range []string{"provision", "bootstrap", "deployment"} {
		t.Run(stage, func(t *testing.T) {
			in := forgeE2EBuildSummaryInputs(t, stage, forgeE2EArmStart)
			out := forgeE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("summary.json")))

			summary := forgeE2EReadSummary(t, out)
			if summary.Complete || summary.Stage != stage {
				t.Errorf("stage %s: complete = %v, stage = %q", stage, summary.Complete, summary.Stage)
			}
			for _, leg := range summary.Legs {
				if leg.Coverage.Complete {
					t.Errorf("stage %s: leg %s claims complete coverage", stage, leg.Stream)
				}
			}
			if asserted := forgeE2ERun(t, forgeE2ESummaryTool, "assert", out); asserted.exit != 0 {
				t.Errorf("assert: exit %d\n%s", asserted.exit, asserted.output)
			}
		})
	}

	t.Run("full stage with one of two streams dispatched", func(t *testing.T) {
		in := forgeE2EBuildSummaryInputs(t, "full", forgeE2EArmStart)
		forgeE2EEditJSON(t, in.path("matrix.json"), func(m map[string]any) { m["include"] = m["include"].([]any)[:1] })
		args := forgeE2EDropFlag(in.writeArgs(in.path("summary.json")), "--leg", in.leg("arm64"))
		out := forgeE2ERequireSummaryWritten(t, in, args)

		summary := forgeE2EReadSummary(t, out)
		if summary.Complete {
			t.Error("a run that covered one of two planned streams is complete")
		}
		if len(summary.Legs) != 1 || summary.PlannedStreams != 2 {
			t.Errorf("legs = %d, planned streams = %d, want 1 and 2", len(summary.Legs), summary.PlannedStreams)
		}
		if asserted := forgeE2ERun(t, forgeE2ESummaryTool, "assert", out); asserted.exit != 0 {
			t.Errorf("assert: exit %d\n%s", asserted.exit, asserted.output)
		}
	})
}

func TestForgeE2ESummaryStatesWhereNodeImagesCameFromAndWhetherTheyAreSkewed(t *testing.T) {
	t.Run("a skewed run is recorded as skewed and still carries no authority", func(t *testing.T) {
		manifest := forgeE2EOutputs()
		forgeE2ENodeSource(manifest, "os-image-amd64")["version"] = "v0.23.1"
		forgeE2ENodeSource(manifest, "os-image-arm64")["version"] = "v0.23.1"
		in := forgeE2EBuildSummaryInputsFrom(t, "full", forgeE2EArmStart, manifest, "--allow-node-image-version-skew")
		out := forgeE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("summary.json")))

		summary := forgeE2EReadSummary(t, out)
		if !summary.NodeImageVersionSkew || summary.Subject.Version != forgeE2EVersion {
			t.Errorf("skew = %v, subject version = %q, want true and %q", summary.NodeImageVersionSkew, summary.Subject.Version, forgeE2EVersion)
		}
		for _, image := range summary.NodeImages {
			if image.Source.Version != "v0.23.1" {
				t.Errorf("%s source version = %q, want the version the image was built at", image.Slot, image.Source.Version)
			}
		}
		if summary.QualificationAuthority != "none" {
			t.Errorf("authority = %q, want none", summary.QualificationAuthority)
		}
		if asserted := forgeE2ERun(t, forgeE2ESummaryTool, "assert", out); asserted.exit != 0 {
			t.Errorf("assert: exit %d\n%s", asserted.exit, asserted.output)
		}
	})

	t.Run("one skewed image is enough to mark the run", func(t *testing.T) {
		manifest := forgeE2EOutputs()
		forgeE2ENodeSource(manifest, "os-image-arm64")["version"] = "v0.23.1"
		in := forgeE2EBuildSummaryInputsFrom(t, "full", forgeE2EArmStart, manifest, "--allow-node-image-version-skew")
		out := forgeE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("summary.json")))

		summary := forgeE2EReadSummary(t, out)
		if !summary.NodeImageVersionSkew || summary.NodeImages[0].Source.Version != forgeE2EVersion {
			t.Errorf("skew = %v, amd64 version = %q", summary.NodeImageVersionSkew, summary.NodeImages[0].Source.Version)
		}
	})

	t.Run("an image from a release names its digest", func(t *testing.T) {
		manifest := forgeE2EOutputs()
		reference := "oci://registry.example.test/team/node-image@sha256:" + forgeE2EDigest("1")
		source := forgeE2ENodeSource(manifest, "os-image-amd64")
		source["kind"], source["reference"] = "nvkr-release", reference
		in := forgeE2EBuildSummaryInputsFrom(t, "full", forgeE2EArmStart, manifest)
		out := forgeE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("summary.json")))

		summary := forgeE2EReadSummary(t, out)
		if got := summary.NodeImages[0].Source; got.Kind != "nvkr-release" || got.Reference != reference {
			t.Errorf("amd64 source = %+v, want the release digest reference", got)
		}
		if got := summary.NodeImages[1].Source.Kind; got != "ci-run" {
			t.Errorf("arm64 source kind = %q, want ci-run: sources are recorded per image", got)
		}
		if summary.NodeImageVersionSkew {
			t.Error("no image differs from the plan, so there is no skew to declare")
		}
	})
}

func TestForgeE2ESummaryStatesHowEachNodeImageWasCheckedAndWhetherThisRunSawIt(t *testing.T) {
	const verify, unsupported = "NVKR-E-VERIFY-FAILED", "NVKR-E-UNSUPPORTED"
	writeSummary := func(t *testing.T, manifest map[string]any) forgeE2ESummary {
		t.Helper()
		in := forgeE2EBuildSummaryInputsFrom(t, "full", forgeE2EArmStart, manifest)
		out := forgeE2ERequireSummaryWritten(t, in, in.writeArgs(in.path("summary.json")))
		if asserted := forgeE2ERun(t, forgeE2ESummaryTool, "assert", out); asserted.exit != 0 {
			t.Fatalf("assert: exit %d\n%s", asserted.exit, asserted.output)
		}
		return forgeE2EReadSummary(t, out)
	}

	t.Run("a fetched image is recorded as observed by this run", func(t *testing.T) {
		summary := writeSummary(t, forgeE2EOutputs())
		for _, image := range summary.NodeImages {
			got := image.Checked
			if got.Method != "http-get" || !got.ObservedByThisRun || got.Run != "" || got.Bytes != forgeE2ENodeImageBytes || got.QCOW2Version != 3 {
				t.Errorf("%s checked = %+v, want a fetch this run observed", image.Slot, got)
			}
		}
	})

	t.Run("a published image is recorded as read back by another run and not observed here", func(t *testing.T) {
		manifest := forgeE2EOutputs()
		forgeE2EUseReadback(manifest)
		summary := writeSummary(t, manifest)
		if len(summary.NodeImages) != 2 {
			t.Fatalf("%d node images, want 2", len(summary.NodeImages))
		}
		for _, image := range summary.NodeImages {
			got := image.Checked
			if got.Method != "s3-readback" || got.ObservedByThisRun || got.Run != forgeE2EPublicationRun ||
				got.Bytes != forgeE2ENodeImageBytes || got.QCOW2Version != 3 || got.VirtualSizeBytes != 12<<30 || got.RangeSupported {
				t.Errorf("%s checked = %+v, want what the publication run reported, not observed here", image.Slot, got)
			}
		}
		if summary.QualificationAuthority != "none" {
			t.Errorf("authority = %q, want none", summary.QualificationAuthority)
		}
	})

	t.Run("each image keeps the method its own output named", func(t *testing.T) {
		manifest := forgeE2EOutputs()
		forgeE2ESlots(manifest)["os-image-arm64"].(map[string]any)["verification"] = forgeE2EReadback()
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
		edit     func(t *testing.T, in forgeE2ESummaryInputs)
		exit     int
		code     string
		message  string
	}{
		{"the plan names a read-back and the check fetched", true, func(t *testing.T, in forgeE2ESummaryInputs) {
			forgeE2EWriteMap(t, in.nodeCheck("os-image-amd64"), forgeE2ENodeImageCheck(forgeE2EOutputs(), "os-image-amd64"))
		}, 22, verify, "disagrees with the verification the plan names for: os-image-amd64"},
		{"the plan names a fetch and the check read back", false, func(t *testing.T, in forgeE2ESummaryInputs) {
			readback := forgeE2EOutputs()
			forgeE2EUseReadback(readback)
			forgeE2EWriteMap(t, in.nodeCheck("os-image-arm64"), forgeE2ENodeImageCheck(readback, "os-image-arm64"))
		}, 22, verify, "disagrees with the verification the plan names for: os-image-arm64"},
		{"the check cites another run than the plan", true, func(t *testing.T, in forgeE2ESummaryInputs) {
			forgeE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) {
				m["run"] = "https://github.com/dsx-ai-factory/nvkr/actions/runs/9000000002"
			})
		}, 22, verify, "disagrees with the verification the plan names for: os-image-amd64"},
		{"the check reports other bytes than the plan's verification", true, func(t *testing.T, in forgeE2ESummaryInputs) {
			forgeE2EEditJSON(t, in.nodeCheck("os-image-arm64"), func(m map[string]any) { m["bytes"] = forgeE2ENodeImageBytes + 1 })
		}, 22, verify, "disagrees with the verification the plan names for: os-image-arm64"},
		{"the check reports another qcow2 version than the plan's verification", true, func(t *testing.T, in forgeE2ESummaryInputs) {
			forgeE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) { m["qcow2"].(map[string]any)["version"] = 2 })
		}, 22, verify, "disagrees with the verification the plan names for: os-image-amd64"},
		{"the check reports another virtual size than the plan's verification", true, func(t *testing.T, in forgeE2ESummaryInputs) {
			forgeE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) { m["qcow2"].(map[string]any)["virtualSizeBytes"] = 1 })
		}, 22, verify, "disagrees with the verification the plan names for: os-image-amd64"},
		{"the plan's record names no verification", true, func(t *testing.T, in forgeE2ESummaryInputs) {
			forgeE2EEditJSON(t, in.path("sub", "substitutions.json"), func(m map[string]any) {
				for _, entry := range m["replaced"].([]any) {
					if entry := entry.(map[string]any); entry["slot"] == "os-image-amd64" {
						delete(entry, "verification")
					}
				}
			})
		}, 22, verify, "disagrees with the verification the plan names for: os-image-amd64"},
		{"a read-back image whose check names other bytes than the plan", true, func(t *testing.T, in forgeE2ESummaryInputs) {
			forgeE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) { m["sha256"] = forgeE2EDigest("9") })
		}, 22, verify, "names other bytes than the plan for: os-image-amd64"},
		{"a check of a method the lane does not know", true, func(t *testing.T, in forgeE2ESummaryInputs) {
			forgeE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) { m["method"] = "head-object" })
		}, 13, unsupported, "unsupported schema or method"},
		{"a check with no method", false, func(t *testing.T, in forgeE2ESummaryInputs) {
			forgeE2EEditJSON(t, in.nodeCheck("os-image-amd64"), func(m map[string]any) { delete(m, "method") })
		}, 13, unsupported, "unsupported schema or method"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			manifest := forgeE2EOutputs()
			if tc.readback {
				forgeE2EUseReadback(manifest)
			}
			in := forgeE2EBuildSummaryInputsFrom(t, "full", forgeE2EArmStart, manifest)
			tc.edit(t, in)
			out := in.path("summary.json")
			forgeE2EExpectFailure(t, forgeE2ERun(t, forgeE2ESummaryTool, in.writeArgs(out)...), tc.exit, tc.code, tc.message)
			if _, err := os.Lstat(out); err == nil {
				t.Errorf("a refused summary left %s behind", out)
			}
		})
	}
}

func TestForgeE2ESummaryAssertRefusesAnySummaryThatClaimsMore(t *testing.T) {
	const verify = "NVKR-E-VERIFY-FAILED"

	full := forgeE2EBuildSummaryInputs(t, "full", forgeE2EArmStart)
	fullPath := forgeE2ERequireSummaryWritten(t, full, full.writeArgs(full.path("summary.json")))
	fullBytes, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	provision := forgeE2EBuildSummaryInputs(t, "provision", forgeE2EArmStart)
	provisionPath := forgeE2ERequireSummaryWritten(t, provision, provision.writeArgs(provision.path("summary.json")))
	provisionBytes, err := os.ReadFile(provisionPath)
	if err != nil {
		t.Fatal(err)
	}
	readbackManifest := forgeE2EOutputs()
	forgeE2EUseReadback(readbackManifest)
	readbackInputs := forgeE2EBuildSummaryInputsFrom(t, "full", forgeE2EArmStart, readbackManifest)
	readbackPath := forgeE2ERequireSummaryWritten(t, readbackInputs, readbackInputs.writeArgs(readbackInputs.path("summary.json")))
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
		{"another schema", fullBytes, func(s map[string]any) { s["schema"] = "nvkr.dev/forge-e2e-summary/v2" }, "schema is not nvkr.dev/forge-e2e-summary/v1"},
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
		{"node image check removed", fullBytes, func(s map[string]any) { delete(image(s), "checked") }, forgeE2ENodeImageRule},
		{"node image check that is null", fullBytes, func(s map[string]any) { image(s)["checked"] = nil }, forgeE2ENodeImageRule},
		{"node image check that is text", fullBytes, func(s map[string]any) { image(s)["checked"] = "fetched" }, forgeE2ENodeImageRule},
		{"node image check that is a number", fullBytes, func(s map[string]any) { image(s)["checked"] = 3 }, forgeE2ENodeImageRule},
		{"node image check that is a list", fullBytes, func(s map[string]any) { image(s)["checked"] = []any{"http-get"} }, forgeE2ENodeImageRule},
		{"node image check of zero bytes", fullBytes, func(s map[string]any) { imageField(s, "checked")["bytes"] = 0 }, forgeE2ENodeImageRule},
		{"node image check of a fractional size", fullBytes, func(s map[string]any) { imageField(s, "checked")["bytes"] = 1.5 }, forgeE2ENodeImageRule},
		{"node image check of a qcow2 version the lane does not support", fullBytes, func(s map[string]any) { imageField(s, "checked")["qcow2Version"] = 4 }, forgeE2ENodeImageRule},
		{"node image check without a virtual size", fullBytes, func(s map[string]any) { imageField(s, "checked")["virtualSizeBytes"] = 0 }, forgeE2ENodeImageRule},
		{"node image check with a range answer that is not a boolean", fullBytes, func(s map[string]any) { imageField(s, "checked")["rangeSupported"] = "yes" }, forgeE2ENodeImageRule},
		{"node image check with an extra field", fullBytes, func(s map[string]any) { imageField(s, "checked")["acceptRanges"] = "bytes" }, forgeE2ENodeImageRule},
		{"node image check with no method", fullBytes, func(s map[string]any) { delete(imageField(s, "checked"), "method") }, forgeE2ENodeImageRule},
		{"node image check of an unknown method", fullBytes, func(s map[string]any) { imageField(s, "checked")["method"] = "head-object" }, forgeE2ENodeImageRule},
		{"node image check of a method in another case", fullBytes, func(s map[string]any) { imageField(s, "checked")["method"] = "HTTP-GET" }, forgeE2ENodeImageRule},
		{"node image check with no statement of who observed it", fullBytes, func(s map[string]any) { delete(imageField(s, "checked"), "observedByThisRun") }, forgeE2ENodeImageRule},
		{"a fetched image said not to be observed by this run", fullBytes, func(s map[string]any) { imageField(s, "checked")["observedByThisRun"] = false }, forgeE2ENodeImageRule},
		{"a fetched image whose observation is not a boolean", fullBytes, func(s map[string]any) { imageField(s, "checked")["observedByThisRun"] = "true" }, forgeE2ENodeImageRule},
		{"a fetched image citing a run", fullBytes, func(s map[string]any) { imageField(s, "checked")["run"] = forgeE2EPublicationRun }, forgeE2ENodeImageRule},
		{"a read-back image said to be observed by this run", readbackBytes, func(s map[string]any) { imageField(s, "checked")["observedByThisRun"] = true }, forgeE2ENodeImageRule},
		{"a read-back image with no statement of who observed it", readbackBytes, func(s map[string]any) { delete(imageField(s, "checked"), "observedByThisRun") }, forgeE2ENodeImageRule},
		{"a read-back image with a range answer", readbackBytes, func(s map[string]any) { imageField(s, "checked")["rangeSupported"] = false }, forgeE2ENodeImageRule},
		{"a read-back image with no run", readbackBytes, func(s map[string]any) { delete(imageField(s, "checked"), "run") }, forgeE2ENodeImageRule},
		{"a read-back image citing a run over http", readbackBytes, func(s map[string]any) {
			imageField(s, "checked")["run"] = "http://github.com/dsx-ai-factory/nvkr/actions/runs/9000000001"
		}, forgeE2ENodeImageRule},
		{"a read-back image citing a page that is not a run", readbackBytes, func(s map[string]any) {
			imageField(s, "checked")["run"] = "https://github.com/dsx-ai-factory/nvkr/pull/543"
		}, forgeE2ENodeImageRule},
		{"a read-back image citing a run with a query", readbackBytes, func(s map[string]any) { imageField(s, "checked")["run"] = forgeE2EPublicationRun + "?check_suite=1" }, forgeE2ENodeImageRule},
		{"a read-back image of zero bytes", readbackBytes, func(s map[string]any) { imageField(s, "checked")["bytes"] = 0 }, forgeE2ENodeImageRule},
		{"a read-back image of a qcow2 version the lane does not support", readbackBytes, func(s map[string]any) { imageField(s, "checked")["qcow2Version"] = 1 }, forgeE2ENodeImageRule},
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
			forgeE2EWriteMap(t, tampered, summary)
			forgeE2EExpectFailure(t, forgeE2ERun(t, forgeE2ESummaryTool, "assert", tampered), 22, verify, tc.message)
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
			forgeE2EExpectFailure(t, forgeE2ERun(t, forgeE2ESummaryTool, "assert", filename), 22, verify, "")
		})
	}

	t.Run("a missing file", func(t *testing.T) {
		forgeE2EExpectFailure(t, forgeE2ERun(t, forgeE2ESummaryTool, "assert", filepath.Join(t.TempDir(), "absent.json")), 2, "NVKR-E-USAGE", "not a regular file")
	})
	t.Run("a symlink to a valid summary", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "summary.json")
		if err := os.Symlink(fullPath, link); err != nil {
			t.Fatal(err)
		}
		forgeE2EExpectFailure(t, forgeE2ERun(t, forgeE2ESummaryTool, "assert", link), 2, "NVKR-E-USAGE", "not a regular file")
	})
	t.Run("the untampered summaries pass", func(t *testing.T) {
		for _, filename := range []string{fullPath, provisionPath, readbackPath} {
			if result := forgeE2ERun(t, forgeE2ESummaryTool, "assert", filename); result.exit != 0 {
				t.Errorf("assert %s: exit %d\n%s", filename, result.exit, result.output)
			}
		}
	})
}
