// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These contracts cover the Forge hardware lane's offline pieces: the committed
// plan fixture and the plan substitution script. They do not exercise Forge, the
// object store, a registry or a hosted run; docs/qualify-e2e.md states that scope.

const (
	forgeE2EPlanFile       = "inputs/qualification/nvke-plan-v0.24.0-beta.12.json"
	forgeE2EProvenanceFile = "inputs/qualification/nvke-plan-v0.24.0-beta.12.provenance.json"
	forgeE2ESubstituteTool = "scripts/forge-e2e-substitute-plan.sh"
	forgeE2ECommit         = "c044392984cdd74149d00110cbb68adfd1075024"
	forgeE2EVersion        = "v0.24.0-beta.12"
)

func forgeE2ERoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func forgeE2ERequireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("forge e2e contract tests require %s: %v", tool, err)
		}
	}
}

func forgeE2EDigest(character string) string { return strings.Repeat(character, 64) }

func forgeE2ESHA256(data []byte) string {
	sum := sha256.Sum256(data) // blob integrity of a committed test fixture, not a typed identity
	return hex.EncodeToString(sum[:])
}

type forgeE2EResult struct {
	output string
	exit   int
}

func forgeE2ERun(t *testing.T, script string, args ...string) forgeE2EResult {
	t.Helper()
	return forgeE2ERunEnv(t, nil, script, args...)
}

// forgeE2ERunEnv runs a script with extra environment entries; FORGE_E2E_NOW fixes the
// clock of the summary script.
func forgeE2ERunEnv(t *testing.T, env []string, script string, args ...string) forgeE2EResult {
	t.Helper()
	forgeE2ERequireTools(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(forgeE2ERoot(t), script), args...)
	command.Dir = forgeE2ERoot(t)
	command.Env = append(os.Environ(), env...)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s exceeded test bound: %v\n%s", script, ctx.Err(), output)
	}
	if err == nil {
		return forgeE2EResult{output: string(output)}
	}
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		t.Fatalf("execute %s: %v", script, err)
	}
	return forgeE2EResult{output: string(output), exit: exited.ExitCode()}
}

// forgeE2EOutputs is an accepted outputs manifest built only from synthetic values.
func forgeE2EOutputs() map[string]any {
	return map[string]any{
		"schema":        "nvkr.dev/forge-e2e-outputs/v1",
		"producerRunId": "1234567890",
		"outputs": map[string]any{
			"controller":       map[string]any{"url": "oci://registry.example.test/team/controller@sha256:" + forgeE2EDigest("a")},
			"capi-provisioned": map[string]any{"url": "oci://registry.example.test/team/capi@sha256:" + forgeE2EDigest("b")},
			"cli-amd64":        map[string]any{"url": "oci://registry.example.test/team/cli@sha256:" + forgeE2EDigest("c")},
			"cli-arm64":        map[string]any{"url": "oci://registry.example.test/team/cli-arm64@sha256:" + forgeE2EDigest("d")},
			"os-image-amd64":   forgeE2ENodeImage("https://cdn.example.test/nvkr/run-1/amd64/node.qcow2", forgeE2EDigest("e"), forgeE2EVersion),
			"os-image-arm64":   forgeE2ENodeImage("https://cdn.example.test/nvkr/run-1/arm64/node.qcow2", forgeE2EDigest("f"), forgeE2EVersion),
		},
	}
}

// forgeE2ENodeSource returns a node image output's source object, for a test to edit in place.
func forgeE2ENodeSource(manifest map[string]any, slot string) map[string]any {
	return forgeE2ESlots(manifest)[slot].(map[string]any)["source"].(map[string]any)
}

// forgeE2ENodeImage is a node image output whose source is a CI run at the given NVKE version and
// whose bytes the lane fetches itself.
func forgeE2ENodeImage(url, digest, version string) map[string]any {
	return map[string]any{"url": url, "sha256": digest, "verification": map[string]any{"method": "http-get"}, "source": map[string]any{
		"kind": "ci-run", "reference": "https://ci.example.test/runs/9001", "version": version,
	}}
}

// forgeE2EPublicationRun is the synthetic run a read-back verification cites.
const forgeE2EPublicationRun = "https://github.com/dsx-ai-factory/nvkr/actions/runs/9000000001/attempts/1"

// forgeE2EReadback is the verification the publication workflow records for an image it read back.
func forgeE2EReadback() map[string]any {
	return map[string]any{
		"method": "s3-readback", "run": forgeE2EPublicationRun, "bytes": forgeE2ENodeImageBytes,
		"qcow2Version": 3, "virtualSizeBytes": 12 << 30,
	}
}

// forgeE2EVerification returns a node image output's verification object, for a test to edit in place.
func forgeE2EVerification(manifest map[string]any, slot string) map[string]any {
	return forgeE2ESlots(manifest)[slot].(map[string]any)["verification"].(map[string]any)
}

// forgeE2EUseReadback makes every node image in the manifest a published, read-back one.
func forgeE2EUseReadback(manifest map[string]any) {
	for _, slot := range []string{"os-image-amd64", "os-image-arm64"} {
		forgeE2ESlots(manifest)[slot].(map[string]any)["verification"] = forgeE2EReadback()
	}
}

func forgeE2ESlots(manifest map[string]any) map[string]any {
	return manifest["outputs"].(map[string]any)
}

type forgeE2EEntry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	URL     string `json:"url"`
}

type forgeE2EPlan struct {
	Subject      string `json:"subject"`
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	Scope        string `json:"scope"`
	Combinations []struct {
		Components   []forgeE2EEntry `json:"components"`
		Dependencies []forgeE2EEntry `json:"dependencies"`
		Streams      []struct {
			Name         string          `json:"name"`
			Dependencies []forgeE2EEntry `json:"dependencies"`
		} `json:"streams"`
	} `json:"combinations"`
}

func forgeE2EReadPlan(t *testing.T, filename string) forgeE2EPlan {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var plan forgeE2EPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode %s: %v", filename, err)
	}
	return plan
}

func TestForgeE2EPlanFixtureMatchesItsProvenance(t *testing.T) {
	root := forgeE2ERoot(t)
	planBytes, err := os.ReadFile(filepath.Join(root, forgeE2EPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	var provenance struct {
		Schema string `json:"schema"`
		Source struct {
			Repository  string `json:"repository"`
			WorkflowRun string `json:"workflowRun"`
			Commit      string `json:"commit"`
			Tag         string `json:"tag"`
			Scope       string `json:"scope"`
		} `json:"source"`
		RetrievedOn    string `json:"retrievedOn"`
		OriginalSHA256 string `json:"originalSha256"`
		Fixture        struct {
			File       string   `json:"file"`
			SHA256     string   `json:"sha256"`
			Redactions []string `json:"redactions"`
		} `json:"fixture"`
	}
	provenanceBytes, err := os.ReadFile(filepath.Join(root, forgeE2EProvenanceFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(provenanceBytes, &provenance); err != nil {
		t.Fatal(err)
	}

	if provenance.Schema != "nvkr.dev/forge-e2e-plan-provenance/v1" {
		t.Errorf("provenance schema = %q", provenance.Schema)
	}
	if got := forgeE2ESHA256(planBytes); got != provenance.Fixture.SHA256 {
		t.Errorf("fixture sha256 = %s, provenance records %s; update the record only with a reviewed fixture change", got, provenance.Fixture.SHA256)
	}
	if provenance.Fixture.File != filepath.Base(forgeE2EPlanFile) {
		t.Errorf("provenance fixture file = %q", provenance.Fixture.File)
	}
	if len(provenance.OriginalSHA256) != 64 || provenance.OriginalSHA256 == provenance.Fixture.SHA256 {
		t.Errorf("originalSha256 %q must be the distinct hash of the unredacted artifact", provenance.OriginalSHA256)
	}
	if provenance.Source.Repository != "dsx-ai-factory/nvke" || provenance.RetrievedOn == "" ||
		!strings.HasPrefix(provenance.Source.WorkflowRun, "https://github.com/dsx-ai-factory/nvke/actions/runs/") {
		t.Errorf("provenance must name the NVKE run and the retrieval date: %+v", provenance)
	}
	if len(provenance.Fixture.Redactions) == 0 {
		t.Error("the fixture is redacted, so the provenance record must say what was redacted")
	}

	plan := forgeE2EReadPlan(t, filepath.Join(root, forgeE2EPlanFile))
	if plan.Subject != "nvke" || plan.Commit != forgeE2ECommit || plan.Commit != provenance.Source.Commit ||
		plan.Version != provenance.Source.Tag || plan.Scope != provenance.Source.Scope {
		t.Errorf("plan identity %s %s %s disagrees with provenance %+v", plan.Subject, plan.Version, plan.Commit, provenance.Source)
	}
	if len(plan.Combinations) != 1 || len(plan.Combinations[0].Streams) != 2 {
		t.Fatalf("plan shape changed: %d combinations", len(plan.Combinations))
	}
	// Streams are listed amd64 first; the workflow's ordering rule relies on the harness
	// matrix, but a fixture that lists them the other way round would be a review signal.
	streams := plan.Combinations[0].Streams
	if !strings.Contains(streams[0].Name, "-amd64-") || !strings.Contains(streams[1].Name, "-arm64-") {
		t.Errorf("fixture streams = %q, %q; want amd64 then arm64", streams[0].Name, streams[1].Name)
	}
}

// The NGC organization identifier is withheld from the repository. The fixture may name
// nvcr.io only through the placeholder, and the substitution tool must never forward an
// NVKE-owned NVCR reference into an NVKR run.
func TestForgeE2EPlanFixtureWithholdsTheOrganization(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(forgeE2ERoot(t), forgeE2EPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "nvcr.io/<ngc-org>/") {
		t.Fatal("fixture no longer carries the redaction placeholder")
	}
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "nvcr.io/"); i >= 0 && !strings.HasPrefix(line[i:], "nvcr.io/<ngc-org>/") {
			t.Errorf("fixture names an unredacted nvcr.io organization: %s", strings.TrimSpace(line))
		}
	}
}

func forgeE2EWriteJSON(t *testing.T, directory, name string, value any) string {
	t.Helper()
	filename := filepath.Join(directory, name)
	fixtureJSON(t, filename, value)
	return filename
}

func TestForgeE2ESubstitutePlanAccepts(t *testing.T) {
	root := forgeE2ERoot(t)
	scratch := t.TempDir()
	manifest := forgeE2EOutputs()
	outputs := forgeE2EWriteJSON(t, scratch, "outputs.json", manifest)
	out := filepath.Join(scratch, "out")

	result := forgeE2ERun(t, forgeE2ESubstituteTool,
		filepath.Join(root, forgeE2EPlanFile), filepath.Join(root, forgeE2EProvenanceFile), outputs, out)
	if result.exit != 0 {
		t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
	}

	slots := forgeE2ESlots(manifest)
	want := func(slot string) string { return slots[slot].(map[string]any)["url"].(string) }
	plan := forgeE2EReadPlan(t, filepath.Join(out, "plan.json"))
	combination := plan.Combinations[0]
	urls := map[string]string{}
	for _, entry := range combination.Components {
		urls[entry.Name] = entry.URL
	}
	for _, entry := range combination.Dependencies {
		urls[entry.Name] = entry.URL
	}
	for _, slot := range []string{"controller", "capi-provisioned", "cli-amd64", "cli-arm64"} {
		if urls[slot] != want(slot) {
			t.Errorf("%s url = %q, want %q", slot, urls[slot], want(slot))
		}
	}
	for _, stream := range combination.Streams {
		for _, entry := range stream.Dependencies {
			if strings.HasPrefix(entry.Name, "os-image-") && entry.URL != want(entry.Name) {
				t.Errorf("stream %s %s url = %q, want %q", stream.Name, entry.Name, entry.URL, want(entry.Name))
			}
		}
	}
	planBytes, err := os.ReadFile(filepath.Join(out, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(planBytes), "nvcr.io") {
		t.Error("substituted plan still references an nvcr.io artifact")
	}
	if plan.Commit != forgeE2ECommit || plan.Version != forgeE2EVersion {
		t.Errorf("substitution must leave the subject identity alone: %s %s", plan.Version, plan.Commit)
	}

	var record struct {
		Schema        string `json:"schema"`
		ProducerRunID string `json:"producerRunId"`
		Plan          struct {
			Commit        string `json:"commit"`
			FixtureSHA256 string `json:"fixtureSha256"`
		} `json:"plan"`
		NodeImageVersionSkew *bool `json:"nodeImageVersionSkew"`
		Replaced             []struct {
			Slot   string         `json:"slot"`
			From   string         `json:"from"`
			To     string         `json:"to"`
			Source map[string]any `json:"source"`
		} `json:"replaced"`
		Retained []forgeE2EEntry `json:"retainedFromNvke"`
	}
	data, err := os.ReadFile(filepath.Join(out, "substitutions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Schema != "nvkr.dev/forge-e2e-substitutions/v1" || record.ProducerRunID != "1234567890" || record.Plan.Commit != forgeE2ECommit {
		t.Errorf("record identity = %+v", record)
	}
	if len(record.Replaced) != 6 {
		t.Fatalf("replaced %d slots, want 6", len(record.Replaced))
	}
	for _, replaced := range record.Replaced {
		if replaced.To != want(replaced.Slot) || replaced.From == "" || replaced.From == replaced.To {
			t.Errorf("replaced %+v", replaced)
		}
		wantSource, isImage := slots[replaced.Slot].(map[string]any)["source"], strings.HasPrefix(replaced.Slot, "os-image-")
		if isImage != (replaced.Source != nil) || (isImage && !reflect.DeepEqual(any(replaced.Source), wantSource)) {
			t.Errorf("replaced %s source = %v, want %v for node images only", replaced.Slot, replaced.Source, wantSource)
		}
	}
	if record.NodeImageVersionSkew == nil || *record.NodeImageVersionSkew {
		t.Errorf("nodeImageVersionSkew = %v, want an explicit false when every image is at the plan version", record.NodeImageVersionSkew)
	}
	// What NVKR did not build must be named, so the summary can state it.
	retained := map[string]string{}
	for _, entry := range record.Retained {
		retained[entry.Name] = entry.URL + entry.Version
	}
	for _, name := range []string{"os-image-bootloader-amd64", "os-image-bootloader-arm64", "kind", "clusterctl", "nerdctl", "cni-ovn", "kube"} {
		if retained[name] == "" {
			t.Errorf("retainedFromNvke lacks %s: %v", name, retained)
		}
	}
	for _, name := range []string{"controller", "capi-provisioned", "cli-amd64", "cli-arm64", "os-image-amd64", "os-image-arm64"} {
		if _, found := retained[name]; found {
			t.Errorf("retainedFromNvke lists replaced slot %s", name)
		}
	}
}

func TestForgeE2ESubstitutePlanRejects(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(manifest map[string]any)
		wantExit int
		wantCode string
	}{
		{"OCI output pinned by tag", func(m map[string]any) {
			forgeE2ESlots(m)["controller"] = map[string]any{"url": "oci://registry.example.test/team/controller:v0.24.0-beta.12"}
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"OCI output with neither tag nor digest", func(m map[string]any) {
			forgeE2ESlots(m)["controller"] = map[string]any{"url": "oci://registry.example.test/team/controller"}
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"OCI output with a short digest", func(m map[string]any) {
			forgeE2ESlots(m)["cli-arm64"] = map[string]any{"url": "oci://registry.example.test/team/cli@sha256:abc123"}
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"OCI output with a plain https scheme", func(m map[string]any) {
			forgeE2ESlots(m)["capi-provisioned"] = map[string]any{"url": "https://registry.example.test/team/capi@sha256:" + forgeE2EDigest("b")}
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"node image over http", func(m map[string]any) {
			forgeE2ESlots(m)["os-image-amd64"] = map[string]any{"url": "http://cdn.example.test/node.qcow2", "sha256": forgeE2EDigest("e")}
		}, 29, "NVKR-E-ENGINE-CAPABILITY"},
		{"node image as an OCI artifact", func(m map[string]any) {
			forgeE2ESlots(m)["os-image-arm64"] = map[string]any{"url": "oci://registry.example.test/team/node@sha256:" + forgeE2EDigest("f"), "sha256": forgeE2EDigest("f")}
		}, 29, "NVKR-E-ENGINE-CAPABILITY"},
		{"node image that is not a qcow2", func(m map[string]any) {
			forgeE2ESlots(m)["os-image-arm64"] = map[string]any{"url": "https://cdn.example.test/node.img", "sha256": forgeE2EDigest("f")}
		}, 29, "NVKR-E-ENGINE-CAPABILITY"},
		{"node image URL with a query string", func(m map[string]any) {
			forgeE2ESlots(m)["os-image-arm64"] = map[string]any{"url": "https://cdn.example.test/node.qcow2?token=synthetic", "sha256": forgeE2EDigest("f")}
		}, 29, "NVKR-E-ENGINE-CAPABILITY"},
		{"node image without a sha256", func(m map[string]any) {
			forgeE2ESlots(m)["os-image-amd64"] = map[string]any{"url": "https://cdn.example.test/amd64/node.qcow2"}
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"node image with no source", func(m map[string]any) {
			delete(forgeE2ESlots(m)["os-image-amd64"].(map[string]any), "source")
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"node image source of an unknown kind", func(m map[string]any) {
			forgeE2ENodeSource(m, "os-image-amd64")["kind"] = "docker-hub"
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"CI run source that names an OCI reference", func(m map[string]any) {
			forgeE2ENodeSource(m, "os-image-amd64")["reference"] = "oci://registry.example.test/team/node@sha256:" + forgeE2EDigest("e")
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"release source that names an https reference", func(m map[string]any) {
			source := forgeE2ENodeSource(m, "os-image-arm64")
			source["kind"] = "nvkr-release"
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"release source pinned by tag", func(m map[string]any) {
			source := forgeE2ENodeSource(m, "os-image-arm64")
			source["kind"], source["reference"] = "nvkr-release", "oci://registry.example.test/team/node:"+forgeE2EVersion
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"source version without the v prefix", func(m map[string]any) {
			forgeE2ENodeSource(m, "os-image-amd64")["version"] = "0.24.0"
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"source version of latest", func(m map[string]any) {
			forgeE2ENodeSource(m, "os-image-amd64")["version"] = "latest"
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"source with a field the lane does not know", func(m map[string]any) {
			forgeE2ENodeSource(m, "os-image-amd64")["trusted"] = true
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"source reference with credentials", func(m map[string]any) {
			forgeE2ENodeSource(m, "os-image-amd64")["reference"] = "https://user@ci.example.test/runs/1"
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"source reference with whitespace", func(m map[string]any) {
			forgeE2ENodeSource(m, "os-image-amd64")["reference"] = "https://ci.example.test/runs/1 and more"
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"source that is not an object", func(m map[string]any) {
			forgeE2ESlots(m)["os-image-amd64"].(map[string]any)["source"] = "https://ci.example.test/runs/1"
		}, 49, "NVKR-E-UNPINNED-INPUT"},
		{"node image built at another NVKE version", func(m map[string]any) {
			forgeE2ENodeSource(m, "os-image-amd64")["version"] = "v0.23.1"
		}, 13, "NVKR-E-UNSUPPORTED"},
		{"only one node image built at another NVKE version", func(m map[string]any) {
			forgeE2ENodeSource(m, "os-image-arm64")["version"] = "v0.23.1"
		}, 13, "NVKR-E-UNSUPPORTED"},
		{"missing slot", func(m map[string]any) { delete(forgeE2ESlots(m), "cli-arm64") }, 41, "NVKR-E-RELEASE-INCOMPLETE"},
		{"unknown slot", func(m map[string]any) {
			forgeE2ESlots(m)["kind"] = map[string]any{"url": "oci://registry.example.test/team/kind@sha256:" + forgeE2EDigest("a")}
		}, 13, "NVKR-E-UNSUPPORTED"},
		{"output without a url", func(m map[string]any) { forgeE2ESlots(m)["controller"] = map[string]any{} }, 2, "NVKR-E-USAGE"},
		{"non-numeric producer run id", func(m map[string]any) { m["producerRunId"] = "latest" }, 2, "NVKR-E-USAGE"},
		{"unsupported manifest schema", func(m map[string]any) { m["schema"] = "nvkr.dev/forge-e2e-outputs/v2" }, 2, "NVKR-E-USAGE"},
	}
	root := forgeE2ERoot(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scratch := t.TempDir()
			manifest := forgeE2EOutputs()
			test.mutate(manifest)
			outputs := forgeE2EWriteJSON(t, scratch, "outputs.json", manifest)
			out := filepath.Join(scratch, "out")
			result := forgeE2ERun(t, forgeE2ESubstituteTool,
				filepath.Join(root, forgeE2EPlanFile), filepath.Join(root, forgeE2EProvenanceFile), outputs, out)
			if result.exit != test.wantExit || !strings.Contains(result.output, "["+test.wantCode+"]") {
				t.Fatalf("exit %d, want %d with %s:\n%s", result.exit, test.wantExit, test.wantCode, result.output)
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a rejected substitution must leave no partial output: %v", err)
			}
		})
	}
}

func TestForgeE2ESubstitutePlanRequiresEachNodeImageToNameHowItIsChecked(t *testing.T) {
	const message = "node image outputs must name their verification"
	editReadback := func(slot string, edit func(verification map[string]any)) func(map[string]any) {
		return func(m map[string]any) {
			verification := forgeE2EReadback()
			edit(verification)
			forgeE2ESlots(m)[slot].(map[string]any)["verification"] = verification
		}
	}
	tests := []struct {
		name   string
		mutate func(manifest map[string]any)
	}{
		{"no verification on amd64", func(m map[string]any) { delete(forgeE2ESlots(m)["os-image-amd64"].(map[string]any), "verification") }},
		{"no verification on arm64", func(m map[string]any) { delete(forgeE2ESlots(m)["os-image-arm64"].(map[string]any), "verification") }},
		{"verification that is not an object", func(m map[string]any) {
			forgeE2ESlots(m)["os-image-amd64"].(map[string]any)["verification"] = "http-get"
		}},
		{"verification that is null", func(m map[string]any) { forgeE2ESlots(m)["os-image-amd64"].(map[string]any)["verification"] = nil }},
		{"verification with no method", func(m map[string]any) {
			forgeE2ESlots(m)["os-image-amd64"].(map[string]any)["verification"] = map[string]any{}
		}},
		{"verification of an unknown method", func(m map[string]any) {
			forgeE2ESlots(m)["os-image-amd64"].(map[string]any)["verification"] = map[string]any{"method": "head-object"}
		}},
		{"verification of a method in another case", func(m map[string]any) {
			forgeE2ESlots(m)["os-image-amd64"].(map[string]any)["verification"] = map[string]any{"method": "HTTP-GET"}
		}},
		{"a fetch that cites a run", func(m map[string]any) { forgeE2EVerification(m, "os-image-amd64")["run"] = forgeE2EPublicationRun }},
		{"a read-back with no run", editReadback("os-image-amd64", func(v map[string]any) { delete(v, "run") })},
		{"a read-back with no bytes", editReadback("os-image-amd64", func(v map[string]any) { delete(v, "bytes") })},
		{"a read-back with no qcow2 version", editReadback("os-image-arm64", func(v map[string]any) { delete(v, "qcow2Version") })},
		{"a read-back with no virtual size", editReadback("os-image-arm64", func(v map[string]any) { delete(v, "virtualSizeBytes") })},
		{"a read-back with a field the lane does not know", editReadback("os-image-amd64", func(v map[string]any) { v["trusted"] = true })},
		{"a read-back run over http", editReadback("os-image-amd64", func(v map[string]any) { v["run"] = "http://github.com/dsx-ai-factory/nvkr/actions/runs/9" })},
		{"a read-back run on another host", editReadback("os-image-amd64", func(v map[string]any) { v["run"] = "https://ci.example.test/dsx-ai-factory/nvkr/actions/runs/9" })},
		{"a read-back that cites a pull request", editReadback("os-image-amd64", func(v map[string]any) { v["run"] = "https://github.com/dsx-ai-factory/nvkr/pull/543" })},
		{"a read-back run with a query", editReadback("os-image-amd64", func(v map[string]any) { v["run"] = forgeE2EPublicationRun + "?x=1" })},
		{"a read-back run of zero", editReadback("os-image-amd64", func(v map[string]any) { v["run"] = "https://github.com/dsx-ai-factory/nvkr/actions/runs/0" })},
		{"a read-back attempt of zero", editReadback("os-image-amd64", func(v map[string]any) { v["run"] = "https://github.com/dsx-ai-factory/nvkr/actions/runs/9/attempts/0" })},
		{"a read-back run that is not text", editReadback("os-image-amd64", func(v map[string]any) { v["run"] = 9 })},
		{"a read-back of zero bytes", editReadback("os-image-amd64", func(v map[string]any) { v["bytes"] = 0 })},
		{"a read-back of negative bytes", editReadback("os-image-amd64", func(v map[string]any) { v["bytes"] = -5 })},
		{"a read-back of fractional bytes", editReadback("os-image-amd64", func(v map[string]any) { v["bytes"] = 1.5 })},
		{"a read-back whose bytes are text", editReadback("os-image-amd64", func(v map[string]any) { v["bytes"] = "538" })},
		{"a read-back with a qcow2 version of one", editReadback("os-image-amd64", func(v map[string]any) { v["qcow2Version"] = 1 })},
		{"a read-back with a qcow2 version of four", editReadback("os-image-amd64", func(v map[string]any) { v["qcow2Version"] = 4 })},
		{"a read-back whose qcow2 version is text", editReadback("os-image-amd64", func(v map[string]any) { v["qcow2Version"] = "3" })},
		{"a read-back of zero virtual size", editReadback("os-image-amd64", func(v map[string]any) { v["virtualSizeBytes"] = 0 })},
		{"a read-back whose virtual size is text", editReadback("os-image-amd64", func(v map[string]any) { v["virtualSizeBytes"] = "12" })},
	}
	root := forgeE2ERoot(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scratch := t.TempDir()
			manifest := forgeE2EOutputs()
			test.mutate(manifest)
			out := filepath.Join(scratch, "out")
			result := forgeE2ERun(t, forgeE2ESubstituteTool, filepath.Join(root, forgeE2EPlanFile),
				filepath.Join(root, forgeE2EProvenanceFile), forgeE2EWriteJSON(t, scratch, "outputs.json", manifest), out)
			forgeE2EExpectFailure(t, result, 49, "NVKR-E-UNPINNED-INPUT", message)
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a rejected substitution must leave no partial output: %v", err)
			}
		})
	}
}

func TestForgeE2ESubstitutePlanCarriesEachNodeImageVerificationAndOnlyThose(t *testing.T) {
	root := forgeE2ERoot(t)
	manifest := forgeE2EOutputs()
	forgeE2ESlots(manifest)["os-image-arm64"].(map[string]any)["verification"] = forgeE2EReadback()
	scratch := t.TempDir()
	out := filepath.Join(scratch, "out")
	result := forgeE2ERun(t, forgeE2ESubstituteTool, filepath.Join(root, forgeE2EPlanFile),
		filepath.Join(root, forgeE2EProvenanceFile), forgeE2EWriteJSON(t, scratch, "outputs.json", manifest), out)
	if result.exit != 0 {
		t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
	}
	var record struct {
		Replaced []struct {
			Slot         string         `json:"slot"`
			Verification map[string]any `json:"verification"`
		} `json:"replaced"`
	}
	data, err := os.ReadFile(filepath.Join(out, "substitutions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	// The record is JSON, so compare through JSON: Go's ints and the decoded float64s are one value.
	normalize := func(value map[string]any) map[string]any {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	want := map[string]map[string]any{
		"os-image-amd64": {"method": "http-get"},
		"os-image-arm64": normalize(forgeE2EReadback()),
	}
	seen := 0
	for _, replaced := range record.Replaced {
		expected, isImage := want[replaced.Slot]
		switch {
		case isImage:
			seen++
			if !reflect.DeepEqual(replaced.Verification, expected) {
				t.Errorf("%s verification = %v, want %v", replaced.Slot, replaced.Verification, expected)
			}
		case replaced.Verification != nil:
			t.Errorf("%s is not a node image and must carry no verification, got %v", replaced.Slot, replaced.Verification)
		}
	}
	if seen != 2 {
		t.Errorf("the record names verification for %d node images, want 2", seen)
	}
}

func TestForgeE2ESubstitutePlanNodeImageVersionSkew(t *testing.T) {
	const older = "v0.23.1"
	root := forgeE2ERoot(t)
	run := func(t *testing.T, flag bool, amd64, arm64 string) (forgeE2EResult, string) {
		t.Helper()
		scratch := t.TempDir()
		manifest := forgeE2EOutputs()
		forgeE2ENodeSource(manifest, "os-image-amd64")["version"] = amd64
		forgeE2ENodeSource(manifest, "os-image-arm64")["version"] = arm64
		out := filepath.Join(scratch, "out")
		args := []string{filepath.Join(root, forgeE2EPlanFile), filepath.Join(root, forgeE2EProvenanceFile),
			forgeE2EWriteJSON(t, scratch, "outputs.json", manifest), out}
		if flag {
			args = append([]string{"--allow-node-image-version-skew"}, args...)
		}
		return forgeE2ERun(t, forgeE2ESubstituteTool, args...), out
	}

	accepted := []struct {
		name         string
		flag         bool
		amd64, arm64 string
		wantSkew     bool
	}{
		{"aligned, no flag", false, forgeE2EVersion, forgeE2EVersion, false},
		{"aligned, flag given: the flag permits skew, it does not claim it", true, forgeE2EVersion, forgeE2EVersion, false},
		{"amd64 skewed", true, older, forgeE2EVersion, true},
		{"arm64 skewed", true, forgeE2EVersion, older, true},
		{"both skewed", true, older, older, true},
		{"skewed to a newer version", true, "v0.25.0", forgeE2EVersion, true},
	}
	for _, test := range accepted {
		t.Run(test.name, func(t *testing.T) {
			result, out := run(t, test.flag, test.amd64, test.arm64)
			if result.exit != 0 {
				t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
			}
			data, err := os.ReadFile(filepath.Join(out, "substitutions.json"))
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				Skew *bool `json:"nodeImageVersionSkew"`
			}
			if err := json.Unmarshal(data, &record); err != nil || record.Skew == nil || *record.Skew != test.wantSkew {
				t.Errorf("nodeImageVersionSkew = %v (%v), want %v", record.Skew, err, test.wantSkew)
			}
		})
	}

	refused := []struct {
		name         string
		amd64, arm64 string
		wantNamed    string
	}{
		{"amd64 skewed", older, forgeE2EVersion, "os-image-amd64 is " + older},
		{"arm64 skewed", forgeE2EVersion, older, "os-image-arm64 is " + older},
		{"both skewed", older, older, "os-image-amd64 is " + older + ", os-image-arm64 is " + older},
	}
	for _, test := range refused {
		t.Run("refused without the flag: "+test.name, func(t *testing.T) {
			result, out := run(t, false, test.amd64, test.arm64)
			if result.exit != 13 || !strings.Contains(result.output, "[NVKR-E-UNSUPPORTED]") ||
				!strings.Contains(result.output, test.wantNamed) ||
				!strings.Contains(result.output, "--allow-node-image-version-skew") ||
				!strings.Contains(result.output, "the plan's "+forgeE2EVersion) {
				t.Fatalf("exit %d, want 13 naming %q and the flag:\n%s", result.exit, test.wantNamed, result.output)
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a refused skew must leave no output: %v", err)
			}
		})
	}
}

func TestForgeE2ESubstitutePlanRejectsAChangedFixture(t *testing.T) {
	root := forgeE2ERoot(t)
	planBytes, err := os.ReadFile(filepath.Join(root, forgeE2EPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	provenanceBytes, err := os.ReadFile(filepath.Join(root, forgeE2EProvenanceFile))
	if err != nil {
		t.Fatal(err)
	}
	otherCommit := strings.Repeat("0", 40)

	tests := []struct {
		name     string
		plan     func() []byte
		prov     func(plan []byte) []byte
		wantExit int
		wantCode string
	}{
		{"fixture bytes changed after the record was made",
			func() []byte { return append(append([]byte{}, planBytes...), '\n') },
			func([]byte) []byte { return provenanceBytes },
			22, "NVKR-E-VERIFY-FAILED"},
		{"fixture commit disagrees with the provenance commit",
			func() []byte { return []byte(strings.Replace(string(planBytes), forgeE2ECommit, otherCommit, 1)) },
			func(plan []byte) []byte {
				return []byte(strings.Replace(string(provenanceBytes), forgeE2ESHA256(planBytes), forgeE2ESHA256(plan), 1))
			},
			13, "NVKR-E-UNSUPPORTED"},
		{"fixture no longer declares a slot as an OCI artifact",
			func() []byte {
				return []byte(strings.Replace(string(planBytes), `"locationStrategy": "ociArtifact"`, `"locationStrategy": "url"`, 1))
			},
			func(plan []byte) []byte {
				return []byte(strings.Replace(string(provenanceBytes), forgeE2ESHA256(planBytes), forgeE2ESHA256(plan), 1))
			},
			13, "NVKR-E-UNSUPPORTED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scratch := t.TempDir()
			plan := test.plan()
			planFile := filepath.Join(scratch, "plan.json")
			provenanceFile := filepath.Join(scratch, "provenance.json")
			if err := os.WriteFile(planFile, plan, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(provenanceFile, test.prov(plan), 0o600); err != nil {
				t.Fatal(err)
			}
			outputs := forgeE2EWriteJSON(t, scratch, "outputs.json", forgeE2EOutputs())
			result := forgeE2ERun(t, forgeE2ESubstituteTool, planFile, provenanceFile, outputs, filepath.Join(scratch, "out"))
			if result.exit != test.wantExit || !strings.Contains(result.output, "["+test.wantCode+"]") {
				t.Fatalf("exit %d, want %d with %s:\n%s", result.exit, test.wantExit, test.wantCode, result.output)
			}
		})
	}
}

func TestForgeE2ESubstitutePlanRefusesUsageAndExistingOutput(t *testing.T) {
	root := forgeE2ERoot(t)
	scratch := t.TempDir()
	outputs := forgeE2EWriteJSON(t, scratch, "outputs.json", forgeE2EOutputs())
	plan, provenance := filepath.Join(root, forgeE2EPlanFile), filepath.Join(root, forgeE2EProvenanceFile)

	if result := forgeE2ERun(t, forgeE2ESubstituteTool, plan, provenance, outputs); result.exit != 2 ||
		!strings.Contains(result.output, "[NVKR-E-USAGE]") {
		t.Errorf("missing OUT argument: exit %d\n%s", result.exit, result.output)
	}
	existing := filepath.Join(scratch, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"the skew flag after the positional arguments": {plan, provenance, outputs, filepath.Join(scratch, "a"), "--allow-node-image-version-skew"},
		"an unknown flag":                  {"--force", plan, provenance, outputs, filepath.Join(scratch, "b")},
		"the skew flag with a missing OUT": {"--allow-node-image-version-skew", plan, provenance, outputs},
		"the skew flag twice":              {"--allow-node-image-version-skew", "--allow-node-image-version-skew", plan, provenance, outputs, filepath.Join(scratch, "c")},
	} {
		if result := forgeE2ERun(t, forgeE2ESubstituteTool, args...); result.exit != 2 || !strings.Contains(result.output, "[NVKR-E-USAGE]") {
			t.Errorf("%s: exit %d\n%s", name, result.exit, result.output)
		}
	}
	if result := forgeE2ERun(t, forgeE2ESubstituteTool, plan, provenance, outputs, existing); result.exit != 47 ||
		!strings.Contains(result.output, "[NVKR-E-IMMUTABLE-VIOLATION]") {
		t.Errorf("existing OUT: exit %d\n%s", result.exit, result.output)
	}
	if entries, err := os.ReadDir(existing); err != nil || len(entries) != 0 {
		t.Errorf("an existing output directory must stay untouched: %v %v", entries, err)
	}
}

const forgeE2EMatrixTool = "scripts/forge-e2e-matrix.sh"

// forgeE2EFakeHarness writes a stand-in for release/hack/qualify-e2e.sh. Like the real
// harness it changes into its own directory before it reads its plan argument, and for
// the `matrix` step it prints that file, so a test's "plan" is the matrix it wants back.
func forgeE2EFakeHarness(t *testing.T, dir string, exit int) string {
	t.Helper()
	body := "#!/usr/bin/env bash\nset -euo pipefail\ncd \"$(dirname \"$0\")\"\n" +
		"[[ $1 == matrix ]] || exit 99\n"
	if exit != 0 {
		body += "echo 'harness: no such plan' >&2\nexit " + strconv.Itoa(exit) + "\n"
	} else {
		body += "cat \"$2\"\n"
	}
	name := filepath.Join(dir, "qualify-e2e.sh")
	if err := os.WriteFile(name, []byte(body), 0o700); err != nil { //nolint:gosec // test stand-in must be executable
		t.Fatal(err)
	}
	return name
}

func forgeE2EStream(combination, stream, arch, provider string) map[string]any {
	return map[string]any{"combination": combination, "stream": stream, "arch": arch, "provider": provider}
}

func forgeE2EMatrixFile(t *testing.T, dir string, streams ...map[string]any) string {
	t.Helper()
	return forgeE2EWriteJSON(t, dir, "matrix.json", map[string]any{"include": streams})
}

func TestForgeE2EMatrixRunsAmd64BeforeArm64AndKeepsPlanOrderWithinAnArchitecture(t *testing.T) {
	scratch := t.TempDir()
	harness := forgeE2EFakeHarness(t, scratch, 0)
	plan := forgeE2EMatrixFile(t, scratch,
		forgeE2EStream("c", "arm64-first", "arm64", "forge"),
		forgeE2EStream("c", "amd64-first", "amd64", "forge"),
		forgeE2EStream("c", "arm64-second", "arm64", "forge"),
		forgeE2EStream("c", "amd64-second", "amd64", "forge"),
	)
	result := forgeE2ERun(t, forgeE2EMatrixTool, harness, plan)
	if result.exit != 0 {
		t.Fatalf("exit %d:\n%s", result.exit, result.output)
	}
	var matrix struct {
		Include []struct {
			Stream string `json:"stream"`
		} `json:"include"`
	}
	if err := json.Unmarshal([]byte(result.output), &matrix); err != nil {
		t.Fatalf("the script must print the matrix and nothing else: %v\n%s", err, result.output)
	}
	var got []string
	for _, entry := range matrix.Include {
		got = append(got, entry.Stream)
	}
	want := []string{"amd64-first", "amd64-second", "arm64-first", "arm64-second"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("streams %v, want %v", got, want)
	}
	if strings.Count(strings.TrimSpace(result.output), "\n") != 0 {
		t.Errorf("the matrix must be one compact line for $GITHUB_OUTPUT:\n%s", result.output)
	}
}

// The harness changes directory before it opens the plan, so a path relative to the
// caller must still reach the file the caller named.
func TestForgeE2EMatrixResolvesRelativePathsBeforeTheHarnessChangesDirectory(t *testing.T) {
	root := forgeE2ERoot(t)
	scratch := t.TempDir()
	harness := forgeE2EFakeHarness(t, scratch, 0)
	plan := forgeE2EMatrixFile(t, scratch, forgeE2EStream("c", "amd64", "amd64", "forge"))
	relativePlan, err := filepath.Rel(root, plan)
	if err != nil {
		t.Fatal(err)
	}
	relativeHarness, err := filepath.Rel(root, harness)
	if err != nil {
		t.Fatal(err)
	}
	if result := forgeE2ERun(t, forgeE2EMatrixTool, relativeHarness, relativePlan); result.exit != 0 ||
		!strings.Contains(result.output, `"stream":"amd64"`) {
		t.Fatalf("exit %d:\n%s", result.exit, result.output)
	}
}

func TestForgeE2EMatrixRejectsStreamsTheLaneCannotRun(t *testing.T) {
	tests := []struct {
		name     string
		streams  []map[string]any
		raw      string
		wantExit int
		wantCode string
		wantText string
	}{
		{name: "non-Forge stream", wantExit: 13, wantCode: "NVKR-E-UNSUPPORTED", wantText: "dsx-stream",
			streams: []map[string]any{forgeE2EStream("c", "forge-stream", "amd64", "forge"), forgeE2EStream("c", "dsx-stream", "amd64", "dsx")}},
		{name: "missing provider", wantExit: 13, wantCode: "NVKR-E-UNSUPPORTED", wantText: "no-provider",
			streams: []map[string]any{{"combination": "c", "stream": "no-provider", "arch": "amd64"}}},
		{name: "unknown architecture", wantExit: 13, wantCode: "NVKR-E-UNSUPPORTED", wantText: "riscv=riscv64",
			streams: []map[string]any{forgeE2EStream("c", "riscv", "riscv64", "forge")}},
		{name: "missing architecture", wantExit: 13, wantCode: "NVKR-E-UNSUPPORTED", wantText: "blank=",
			streams: []map[string]any{forgeE2EStream("c", "blank", "", "forge")}},
		{name: "duplicate stream in one combination", wantExit: 13, wantCode: "NVKR-E-UNSUPPORTED", wantText: "twice",
			streams: []map[string]any{forgeE2EStream("c", "twice", "amd64", "forge"), forgeE2EStream("c", "twice", "amd64", "forge")}},
		{name: "no streams", wantExit: 13, wantCode: "NVKR-E-UNSUPPORTED", wantText: "named no streams",
			streams: []map[string]any{}},
		{name: "no include key", wantExit: 13, wantCode: "NVKR-E-UNSUPPORTED", raw: `{"streams":[]}`, wantText: "named no streams"},
		{name: "not JSON", wantExit: 13, wantCode: "NVKR-E-UNSUPPORTED", raw: `not json`, wantText: "named no streams"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scratch := t.TempDir()
			harness := forgeE2EFakeHarness(t, scratch, 0)
			plan := filepath.Join(scratch, "matrix.json")
			if test.raw != "" {
				if err := os.WriteFile(plan, []byte(test.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				plan = forgeE2EMatrixFile(t, scratch, test.streams...)
			}
			result := forgeE2ERun(t, forgeE2EMatrixTool, harness, plan)
			if result.exit != test.wantExit || !strings.Contains(result.output, "["+test.wantCode+"]") ||
				!strings.Contains(result.output, test.wantText) {
				t.Fatalf("exit %d, want %d with %s containing %q:\n%s", result.exit, test.wantExit, test.wantCode, test.wantText, result.output)
			}
			if strings.HasPrefix(strings.TrimSpace(result.output), "{") {
				t.Errorf("a rejected matrix must print no matrix:\n%s", result.output)
			}
		})
	}
}

func TestForgeE2EMatrixFailsClosedOnTheHarnessAndItsArguments(t *testing.T) {
	scratch := t.TempDir()
	plan := forgeE2EMatrixFile(t, scratch, forgeE2EStream("c", "amd64", "amd64", "forge"))
	failing := forgeE2EFakeHarness(t, t.TempDir(), 3)

	notExecutable := filepath.Join(t.TempDir(), "qualify-e2e.sh")
	if err := os.WriteFile(notExecutable, []byte("#!/usr/bin/env bash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "qualify-e2e.sh")
	if err := os.Symlink(forgeE2EFakeHarness(t, t.TempDir(), 0), link); err != nil {
		t.Fatal(err)
	}
	planLink := filepath.Join(linkDir, "plan.json")
	if err := os.Symlink(plan, planLink); err != nil {
		t.Fatal(err)
	}
	good := forgeE2EFakeHarness(t, t.TempDir(), 0)

	tests := []struct {
		name     string
		args     []string
		wantExit int
		wantCode string
	}{
		{"harness step fails", []string{failing, plan}, 17, "NVKR-E-PREFLIGHT-FAILED"},
		{"harness is not executable", []string{notExecutable, plan}, 2, "NVKR-E-USAGE"},
		{"harness is a symlink", []string{link, plan}, 2, "NVKR-E-USAGE"},
		{"harness is missing", []string{filepath.Join(scratch, "absent.sh"), plan}, 2, "NVKR-E-USAGE"},
		{"plan is a symlink", []string{good, planLink}, 2, "NVKR-E-USAGE"},
		{"plan is missing", []string{good, filepath.Join(scratch, "absent.json")}, 2, "NVKR-E-USAGE"},
		{"too few arguments", []string{good}, 2, "NVKR-E-USAGE"},
		{"too many arguments", []string{good, plan, "extra"}, 2, "NVKR-E-USAGE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := forgeE2ERun(t, forgeE2EMatrixTool, test.args...)
			if result.exit != test.wantExit || !strings.Contains(result.output, "["+test.wantCode+"]") {
				t.Fatalf("exit %d, want %d with %s:\n%s", result.exit, test.wantExit, test.wantCode, result.output)
			}
		})
	}
}
