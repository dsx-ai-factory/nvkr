// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These contracts cover artifact source "build-subject": the plan kept as the fixture, the script
// that builds from the subject commit, and the summary that says nothing in the run was NVKR-built.
// The container engine, `make` and `timeout` are fakes; git is real, on a repository the test
// makes. They prove what the scripts refuse and record. They do not prove that NVKE's build
// succeeds, that its images boot on hardware, or what a hosted runner can build.

const hardwareE2EBuildTool = "scripts/hardware-e2e-build-subject.sh"

func TestHardwareE2ESubstitutePlanRecordsTheArtifactSource(t *testing.T) {
	root := hardwareE2ERoot(t)
	scratch := t.TempDir()
	outputs := hardwareE2EWriteJSON(t, scratch, "outputs.json", hardwareE2EOutputs())
	out := filepath.Join(scratch, "out")
	if result := hardwareE2ERun(t, hardwareE2ESubstituteTool,
		filepath.Join(root, hardwareE2EPlanFile), filepath.Join(root, hardwareE2EProvenanceFile), outputs, out); result.exit != 0 {
		t.Fatalf("exit %d\n%s", result.exit, result.output)
	}
	if got := hardwareE2EReadMap(t, filepath.Join(out, "substitutions.json"))["artifactSource"]; got != "nvkr-outputs" {
		t.Errorf("artifactSource = %v, want nvkr-outputs", got)
	}
}

func TestHardwareE2ESubstitutePlanBuildSubjectKeepsThePlanAndSaysNothingIsNvkrBuilt(t *testing.T) {
	root := hardwareE2ERoot(t)
	fixture, err := os.ReadFile(filepath.Join(root, hardwareE2EPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	result := hardwareE2ERun(t, hardwareE2ESubstituteTool, "--build-subject",
		filepath.Join(root, hardwareE2EPlanFile), filepath.Join(root, hardwareE2EProvenanceFile), out)
	if result.exit != 0 {
		t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
	}
	if !strings.Contains(result.output, "not by NVKR") {
		t.Errorf("the script should say NVKR built nothing:\n%s", result.output)
	}
	if got := hardwareE2EReadFile(t, filepath.Join(out, "plan.json")); got != string(fixture) {
		t.Error("the plan was changed; a build from the subject commit must keep the fixture byte for byte")
	}
	for filename, mask := range map[string]os.FileMode{out: 0o077, filepath.Join(out, "plan.json"): 0o077, filepath.Join(out, "substitutions.json"): 0o077} {
		if info, err := os.Stat(filename); err != nil || info.Mode().Perm()&mask != 0 {
			t.Errorf("%s mode = %v (%v), want no group or other access", filename, info, err)
		}
	}

	record := hardwareE2EReadMap(t, filepath.Join(out, "substitutions.json"))
	plan := record["plan"].(map[string]any)
	for name, check := range map[string][2]any{
		"schema":         {record["schema"], "nvkr.dev/hardware-e2e-substitutions/v1"},
		"artifact":       {record["artifactSource"], "build-subject"},
		"producer":       {record["producerRunId"], nil},
		"skew":           {record["nodeImageVersionSkew"], false},
		"plan commit":    {plan["commit"], hardwareE2ECommit},
		"plan version":   {plan["version"], hardwareE2EVersion},
		"fixture digest": {plan["fixtureSha256"], hardwareE2ESHA256(fixture)},
	} {
		if check[0] != check[1] {
			t.Errorf("%s = %v, want %v", name, check[0], check[1])
		}
	}
	if replaced := record["replaced"].([]any); len(replaced) != 0 {
		t.Errorf("replaced = %v, want nothing: no NVKR output was substituted", replaced)
	}
	var retained []string
	for _, entry := range record["retainedFromNvke"].([]any) {
		retained = append(retained, entry.(map[string]any)["name"].(string))
	}
	for _, want := range []string{"controller", "capi-provisioned", "cli-amd64", "cli-arm64", "os-image-amd64", "os-image-arm64"} {
		if !slices.Contains(retained, want) {
			t.Errorf("retainedFromNvke lacks %s: with nothing substituted, everything the plan names is NVKE's", want)
		}
	}
}

func TestHardwareE2ESubstitutePlanBuildSubjectRefuses(t *testing.T) {
	root := hardwareE2ERoot(t)
	planBytes, err := os.ReadFile(filepath.Join(root, hardwareE2EPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	provenanceBytes, err := os.ReadFile(filepath.Join(root, hardwareE2EProvenanceFile))
	if err != nil {
		t.Fatal(err)
	}
	rederive := func(plan []byte) []byte {
		return []byte(strings.Replace(string(provenanceBytes), hardwareE2ESHA256(planBytes), hardwareE2ESHA256(plan), 1))
	}
	tests := []struct {
		name     string
		plan     []byte
		prov     []byte
		wantExit int
		wantCode string
	}{
		{"fixture bytes changed after the record was made", append(append([]byte{}, planBytes...), '\n'), provenanceBytes, 22, "NVKR-E-VERIFY-FAILED"},
		{"fixture commit disagrees with the provenance commit",
			[]byte(strings.Replace(string(planBytes), hardwareE2ECommit, strings.Repeat("0", 40), 1)),
			rederive([]byte(strings.Replace(string(planBytes), hardwareE2ECommit, strings.Repeat("0", 40), 1))), 13, "NVKR-E-UNSUPPORTED"},
		{"fixture no longer declares a slot as an OCI artifact",
			[]byte(strings.Replace(string(planBytes), `"locationStrategy": "ociArtifact"`, `"locationStrategy": "url"`, 1)),
			rederive([]byte(strings.Replace(string(planBytes), `"locationStrategy": "ociArtifact"`, `"locationStrategy": "url"`, 1))), 13, "NVKR-E-UNSUPPORTED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scratch := t.TempDir()
			planFile, provenanceFile := filepath.Join(scratch, "plan.json"), filepath.Join(scratch, "provenance.json")
			if err := os.WriteFile(planFile, test.plan, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(provenanceFile, test.prov, 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(scratch, "out")
			result := hardwareE2ERun(t, hardwareE2ESubstituteTool, "--build-subject", planFile, provenanceFile, out)
			if result.exit != test.wantExit || !strings.Contains(result.output, "["+test.wantCode+"]") {
				t.Fatalf("exit %d, want %d with %s:\n%s", result.exit, test.wantExit, test.wantCode, result.output)
			}
			if _, err := os.Lstat(out); err == nil {
				t.Error("a refusal left an output directory")
			}
		})
	}

	scratch := t.TempDir()
	outputs := hardwareE2EWriteJSON(t, scratch, "outputs.json", hardwareE2EOutputs())
	plan, provenance := filepath.Join(root, hardwareE2EPlanFile), filepath.Join(root, hardwareE2EProvenanceFile)
	for name, args := range map[string][]string{
		"no OUT":                      {"--build-subject", plan, provenance},
		"an outputs manifest":         {"--build-subject", plan, provenance, outputs, filepath.Join(scratch, "a")},
		"the skew flag with it":       {"--build-subject", "--allow-node-image-version-skew", plan, provenance, filepath.Join(scratch, "b")},
		"the flag twice":              {"--build-subject", "--build-subject", plan, provenance, filepath.Join(scratch, "c")},
		"a missing plan":              {"--build-subject", filepath.Join(scratch, "absent.json"), provenance, filepath.Join(scratch, "d")},
		"the flag after the operands": {plan, provenance, filepath.Join(scratch, "e"), "--build-subject"},
	} {
		if result := hardwareE2ERun(t, hardwareE2ESubstituteTool, args...); result.exit != 2 || !strings.Contains(result.output, "[NVKR-E-USAGE]") {
			t.Errorf("%s: exit %d\n%s", name, result.exit, result.output)
		}
	}
	existing := filepath.Join(scratch, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if result := hardwareE2ERun(t, hardwareE2ESubstituteTool, "--build-subject", plan, provenance, existing); result.exit != 47 ||
		!strings.Contains(result.output, "[NVKR-E-IMMUTABLE-VIOLATION]") {
		t.Errorf("existing OUT: exit %d\n%s", result.exit, result.output)
	}
	if entries, err := os.ReadDir(existing); err != nil || len(entries) != 0 {
		t.Errorf("an existing output directory must stay untouched: %v %v", entries, err)
	}
}

// The fake make records its arguments and writes, omits, empties or links what NVKE's
// `make site-inputs` would, as FAKE_MAKE says.
const hardwareE2EFakeMake = `#!/usr/bin/env bash
printf 'make %s\n' "$*" >>"$FAKE_LOG"
inputs='' arch=''
for argument in "$@"; do
  case $argument in
    SITE_INPUTS=*) inputs=${argument#SITE_INPUTS=} ;;
    SITE_ARCH=*) arch=${argument#SITE_ARCH=} ;;
  esac
done
[[ ${FAKE_MAKE:-ok} != fail ]] || { echo 'fake make: build failed' >&2; exit 1; }
mkdir -p "$inputs" "$FAKE_HARNESS/cli/dist"
for archive in controller.tar capi-provisioned.tar profiles.tar; do
  [[ ${FAKE_MAKE:-} == "omit-$archive" ]] || printf 'archive %s\n' "$archive" >"$inputs/$archive"
done
[[ ${FAKE_MAKE:-} != empty-controller.tar ]] || : >"$inputs/controller.tar"
nvkectl=$FAKE_HARNESS/cli/dist/nvkectl-linux-$arch
case ${FAKE_MAKE:-ok} in
  omit-nvkectl) ;;
  empty-nvkectl) : >"$nvkectl" ;;
  link-nvkectl) ln -s /etc/hosts "$nvkectl" ;;
  *) printf 'nvkectl %s\n' "$arch" >"$nvkectl" ;;
esac
case ${FAKE_MAKE:-ok} in
  dirty) printf 'changed\n' >>"$FAKE_HARNESS/tracked.txt" ;;
  newcommit) git -C "$FAKE_HARNESS" -c user.name=T -c user.email=t@example.test -c commit.gpgsign=false commit -q --allow-empty -m later ;;
esac
`

type hardwareE2EBuildRun struct {
	t       *testing.T
	scratch string
	bin     string
	harness string
	plan    string
	inputs  string
	staged  string
	stepEnv string
	log     string
	arch    string
	head    string
	env     []string
	args    []string
}

// hardwareE2EBuildGit runs git on the repository the test made, with no configuration but its own.
func hardwareE2EBuildGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("hardware E2E contract tests require git: %v", err)
	}
	command := exec.CommandContext(t.Context(), "git", append([]string{
		"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.test",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main",
	}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func hardwareE2EBuildBin(t *testing.T, dir string, skip []string) {
	t.Helper()
	fakes := map[string]string{"docker": hardwareE2EFakeOras, "timeout": hardwareE2EFakeTimeout, "make": hardwareE2EFakeMake}
	for name, contents := range fakes {
		if !slices.Contains(skip, name) {
			hardwareE2EWriteExecutable(t, filepath.Join(dir, name), contents)
		}
	}
	for _, name := range []string{"bash", "jq", "cut", "rm", "mkdir", "ln", "git", "sha256sum", "shasum"} {
		if slices.Contains(skip, name) {
			continue
		}
		real, err := exec.LookPath(name)
		if err != nil {
			continue // a platform has one of the two digest tools, not both
		}
		if err := os.Symlink(real, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

// hardwareE2ENewBuildRun builds a leg's scratch directory with an accepted scenario: a real git
// repository standing in for the NVKE checkout, a plan naming its HEAD as the subject commit,
// and an existing step environment file the script must append to without disturbing.
func hardwareE2ENewBuildRun(t *testing.T, arch string, skip ...string) *hardwareE2EBuildRun {
	t.Helper()
	scratch := t.TempDir()
	r := &hardwareE2EBuildRun{
		t: t, scratch: scratch, arch: arch,
		bin:     filepath.Join(scratch, "bin"),
		harness: filepath.Join(scratch, "nvke"),
		plan:    filepath.Join(scratch, "plan.json"),
		inputs:  filepath.Join(scratch, "site-inputs"),
		staged:  filepath.Join(scratch, "staged.json"),
		stepEnv: filepath.Join(scratch, "step-env"),
		log:     filepath.Join(scratch, "calls.log"),
	}
	hardwareE2EBuildBin(t, r.bin, skip)
	if err := os.MkdirAll(filepath.Join(r.harness, "controller", "test", "e2e"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		filepath.Join("controller", "test", "e2e", "Makefile"): "# stands in for NVKE's Makefile\n",
		"tracked.txt": "tracked\n",
	} {
		if err := os.WriteFile(filepath.Join(r.harness, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	hardwareE2EBuildGit(t, r.harness, "init", "-q")
	hardwareE2EBuildGit(t, r.harness, "add", "-A")
	hardwareE2EBuildGit(t, r.harness, "commit", "-q", "-m", "subject")
	r.head = hardwareE2EBuildGit(t, r.harness, "rev-parse", "HEAD")
	r.writePlan(r.head)
	if err := os.WriteFile(r.stepEnv, []byte(hardwareE2EStageEnvSeed), 0o600); err != nil {
		t.Fatal(err)
	}
	r.env = []string{
		"PATH=" + r.bin, "FAKE_LOG=" + r.log, "FAKE_HARNESS=" + r.harness,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	}
	r.args = []string{
		"--arch", arch, "--harness", r.harness, "--plan", r.plan,
		"--controller-version", hardwareE2EStageVersion, "--capi-version", hardwareE2EStageVersion,
		"--inputs", r.inputs, "--staged", r.staged, "--step-env", r.stepEnv,
	}
	return r
}

func (r *hardwareE2EBuildRun) writePlan(commit any) {
	r.t.Helper()
	hardwareE2EWriteJSON(r.t, r.scratch, "plan.json", map[string]any{"subject": "nvke", "version": hardwareE2EVersion, "commit": commit})
}

func (r *hardwareE2EBuildRun) run() hardwareE2EResult {
	r.t.Helper()
	return hardwareE2ERunEnv(r.t, r.env, hardwareE2EBuildTool, r.args...)
}

func (r *hardwareE2EBuildRun) calls() []string {
	r.t.Helper()
	data, err := os.ReadFile(r.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (r *hardwareE2EBuildRun) madeAnything() bool {
	for _, call := range r.calls() {
		if strings.HasPrefix(call, "make ") {
			return true
		}
	}
	return false
}

func TestHardwareE2EBuildSubjectBuildsFromTheSubjectCommitAndRecordsThatNothingIsNvkrBuilt(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			r := hardwareE2ENewBuildRun(t, arch)
			result := r.run()
			if result.exit != 0 {
				t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
			}
			if !strings.Contains(result.output, "not NVKR-built") {
				t.Errorf("the script should say NVKR built nothing:\n%s", result.output)
			}
			short := r.head[:12]
			target := "make -C " + r.harness + "/controller/test/e2e site-inputs SITE_ARCH=" + arch + " SITE_INPUTS=" + r.inputs +
				" SITE_IMAGE_REGISTRY=localhost/nvke SITE_NVKE_CONTROLLER_VERSION=" + hardwareE2EStageVersion +
				" SITE_NVKE_CONTROLLER_REPOSITORY=nvke-controller SITE_NVKE_CONTROLLER_COMMIT=" + short +
				" SITE_NVKE_CONTROLLER_BUILD=1 SITE_PROVISIONED_VERSION=" + hardwareE2EStageVersion + " SITE_PROVISIONED_BUILD=1 SITE_CLI_BUILD=1"
			calls := r.calls()
			hardwareE2ERequireCall(t, calls, target)
			hardwareE2ERequireCall(t, calls, "timeout --kill-after=60s 120m "+target)
			for _, gate := range []string{"rev-parse HEAD", "status --porcelain --untracked-files=no"} {
				call := "timeout 2m git -C " + r.harness + " " + gate
				if got := strings.Count(strings.Join(calls, "\n"), call); got != 2 {
					t.Errorf("%q ran %d times, want twice: before and after the build", call, got)
				}
			}

			var record struct {
				Schema                 string            `json:"schema"`
				Arch                   string            `json:"arch"`
				Source                 string            `json:"source"`
				BuiltFromCommit        string            `json:"builtFromCommit"`
				ControllerVersionCheck string            `json:"controllerVersionCheck"`
				Files                  map[string]string `json:"files"`
				ImagePrefix            string            `json:"imagePrefix"`
				NotNvkrBuilt           []string          `json:"notNvkrBuilt"`
			}
			if err := json.Unmarshal([]byte(hardwareE2EReadFile(t, r.staged)), &record); err != nil {
				t.Fatal(err)
			}
			if record.Schema != "nvkr.dev/hardware-e2e-staged/v1" || record.Arch != arch || record.Source != "build-subject" {
				t.Errorf("record schema/arch/source = %q/%q/%q", record.Schema, record.Arch, record.Source)
			}
			if record.BuiltFromCommit != r.head {
				t.Errorf("builtFromCommit = %q, want %q", record.BuiltFromCommit, r.head)
			}
			if record.ControllerVersionCheck != "not-determined" {
				t.Errorf("controllerVersionCheck = %q: the script reads no label, so it cannot say matched", record.ControllerVersionCheck)
			}
			if record.ImagePrefix != "localhost/nvke" {
				t.Errorf("imagePrefix = %q", record.ImagePrefix)
			}
			if want := []string{"capi-provisioned", "cli", "controller", "deploy-path", "imager-efi", "node-image", "site-profiles"}; !slices.Equal(record.NotNvkrBuilt, want) {
				t.Errorf("notNvkrBuilt = %v, want %v", record.NotNvkrBuilt, want)
			}
			nvkectl := r.harness + "/cli/dist/nvkectl-linux-" + arch
			files := map[string]string{
				"controller.tar":        r.inputs + "/controller.tar",
				"capi-provisioned.tar":  r.inputs + "/capi-provisioned.tar",
				"profiles.tar":          r.inputs + "/profiles.tar",
				"nvkectl-linux-" + arch: nvkectl,
			}
			if len(record.Files) != len(files) {
				t.Errorf("files = %v, want exactly %d entries", record.Files, len(files))
			}
			for name, filename := range files {
				if want := hardwareE2ESHA256([]byte(hardwareE2EReadFile(t, filename))); record.Files[name] != want {
					t.Errorf("files[%s] = %q, want the digest of %s (%q)", name, record.Files[name], filename, want)
				}
			}

			wantEnv := hardwareE2EStageEnvSeed + "SITE_IMAGES=localhost/nvke\nSITE_NVKE_CONTROLLER_REPOSITORY=nvke-controller\n" +
				"SITE_NVKE_CONTROLLER_BUILD=1\nSITE_PROVISIONED_BUILD=1\nSITE_CLI_BUILD=1\nCLI_VERSION=" + short + "\nSITE_NVKECTL=" + nvkectl + "\n"
			if got := hardwareE2EReadFile(t, r.stepEnv); got != wantEnv {
				t.Errorf("step env = %q, want %q", got, wantEnv)
			}
			for filename, want := range map[string]os.FileMode{r.staged: 0o600, r.inputs: 0o700} {
				if info, err := os.Stat(filename); err != nil || info.Mode().Perm() != want {
					t.Errorf("%s mode = %v (%v), want %o", filename, info, err, want)
				}
			}
		})
	}

	t.Run("a chosen localhost prefix and no step environment file", func(t *testing.T) {
		r := hardwareE2ENewBuildRun(t, "amd64")
		r.args = hardwareE2EDropFlag(r.args, "--step-env", "")
		r.args = append(r.args, "--image-prefix", "localhost/nvkr-hardware-e2e")
		if result := r.run(); result.exit != 0 {
			t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
		}
		if got := hardwareE2EReadFile(t, r.stepEnv); got != hardwareE2EStageEnvSeed {
			t.Errorf("step env = %q, want it untouched", got)
		}
		hardwareE2ERequireCall(t, r.calls(), "make -C "+r.harness+"/controller/test/e2e site-inputs SITE_ARCH=amd64 SITE_INPUTS="+r.inputs+
			" SITE_IMAGE_REGISTRY=localhost/nvkr-hardware-e2e SITE_NVKE_CONTROLLER_VERSION="+hardwareE2EStageVersion+
			" SITE_NVKE_CONTROLLER_REPOSITORY=nvke-controller SITE_NVKE_CONTROLLER_COMMIT="+r.head[:12]+
			" SITE_NVKE_CONTROLLER_BUILD=1 SITE_PROVISIONED_VERSION="+hardwareE2EStageVersion+" SITE_PROVISIONED_BUILD=1 SITE_CLI_BUILD=1")
	})
}

type hardwareE2EBuildRefusal struct {
	name     string
	skip     []string
	prepare  func(t *testing.T, r *hardwareE2EBuildRun)
	exit     int
	code     string
	message  string
	wantMake bool
}

func hardwareE2EBuildArgs(edit func(args []string) []string) func(*testing.T, *hardwareE2EBuildRun) {
	return func(t *testing.T, r *hardwareE2EBuildRun) { t.Helper(); r.args = edit(r.args) }
}

func hardwareE2EBuildFake(scenario string) func(*testing.T, *hardwareE2EBuildRun) {
	return func(t *testing.T, r *hardwareE2EBuildRun) { t.Helper(); r.env = append(r.env, "FAKE_MAKE="+scenario) }
}

func hardwareE2EBuildRefusals() []hardwareE2EBuildRefusal {
	usage := func(name, message string, prepare func(*testing.T, *hardwareE2EBuildRun)) hardwareE2EBuildRefusal {
		return hardwareE2EBuildRefusal{name: name, prepare: prepare, exit: 2, code: "NVKR-E-USAGE", message: message}
	}
	unsupported := func(name, message string, prepare func(*testing.T, *hardwareE2EBuildRun)) hardwareE2EBuildRefusal {
		return hardwareE2EBuildRefusal{name: name, prepare: prepare, exit: 13, code: "NVKR-E-UNSUPPORTED", message: message}
	}
	engine := func(name, tool string, skip ...string) hardwareE2EBuildRefusal {
		return hardwareE2EBuildRefusal{name: name, skip: skip, exit: 29, code: "NVKR-E-ENGINE-CAPABILITY", message: "missing " + tool}
	}
	verify := func(name, message string, wantMake bool, prepare func(*testing.T, *hardwareE2EBuildRun)) hardwareE2EBuildRefusal {
		return hardwareE2EBuildRefusal{name: name, prepare: prepare, exit: 22, code: "NVKR-E-VERIFY-FAILED", message: message, wantMake: wantMake}
	}
	planCommit := func(commit any) func(*testing.T, *hardwareE2EBuildRun) {
		return func(t *testing.T, r *hardwareE2EBuildRun) { t.Helper(); r.writePlan(commit) }
	}

	refusals := []hardwareE2EBuildRefusal{
		usage("no arguments", "hardware-e2e-build-subject.sh --arch", hardwareE2EBuildArgs(func([]string) []string { return nil })),
		usage("unknown argument", "unknown argument: --bogus", hardwareE2EBuildArgs(func(a []string) []string { return append(a, "--bogus", "x") })),
		usage("flag without a value", "--arch needs a value", hardwareE2EBuildArgs(func([]string) []string { return []string{"--arch"} })),
		usage("a commit flag is not accepted", "unknown argument: --subject-commit", hardwareE2EBuildArgs(func(a []string) []string {
			return append(a, "--subject-commit", strings.Repeat("a", 40))
		})),
		usage("plan missing", "not a regular file", hardwareE2EBuildArgs(func(a []string) []string {
			return hardwareE2ESetFlag(a, "--plan", "/nonexistent/plan.json")
		})),
		usage("plan is a link", "not a regular file", func(t *testing.T, r *hardwareE2EBuildRun) {
			link := filepath.Join(r.scratch, "linked.json")
			if err := os.Symlink(r.plan, link); err != nil {
				t.Fatal(err)
			}
			r.args = hardwareE2ESetFlag(r.args, "--plan", link)
		}),
		usage("harness without the Makefile", "is not an NVKE checkout", func(t *testing.T, r *hardwareE2EBuildRun) {
			if err := os.Remove(filepath.Join(r.harness, "controller", "test", "e2e", "Makefile")); err != nil {
				t.Fatal(err)
			}
		}),
		usage("harness is a link", "is not an NVKE checkout", func(t *testing.T, r *hardwareE2EBuildRun) {
			link := filepath.Join(r.scratch, "linked-nvke")
			if err := os.Symlink(r.harness, link); err != nil {
				t.Fatal(err)
			}
			r.args = hardwareE2ESetFlag(r.args, "--harness", link)
		}),
		usage("step environment is not a file", "--step-env is not a file", hardwareE2EBuildArgs(func(a []string) []string {
			return hardwareE2ESetFlag(a, "--step-env", "/nonexistent/step-env")
		})),

		unsupported("an architecture other than amd64 or arm64", "unsupported architecture: s390x", hardwareE2EBuildArgs(func(a []string) []string {
			return hardwareE2ESetFlag(a, "--arch", "s390x")
		})),
		unsupported("a prefix a registry could answer to", "must be a localhost/ name", hardwareE2EBuildArgs(func(a []string) []string {
			return append(a, "--image-prefix", "ghcr.io/example/nvke")
		})),
		unsupported("an uppercase prefix", "must be a localhost/ name", hardwareE2EBuildArgs(func(a []string) []string {
			return append(a, "--image-prefix", "localhost/Nvke")
		})),
		unsupported("a prefix with a trailing slash", "must be a localhost/ name", hardwareE2EBuildArgs(func(a []string) []string {
			return append(a, "--image-prefix", "localhost/nvke/")
		})),
		unsupported("a plan without a commit", "names no 40-hex subject commit", func(t *testing.T, r *hardwareE2EBuildRun) {
			hardwareE2EWriteJSON(t, r.scratch, "plan.json", map[string]any{"subject": "nvke"})
		}),
		unsupported("a short commit", "names no 40-hex subject commit", planCommit("c044392")),
		unsupported("an upper case commit", "names no 40-hex subject commit", planCommit(strings.Repeat("A", 40))),
		unsupported("a commit that is not text", "names no 40-hex subject commit", planCommit(12345)),
		unsupported("a branch name instead of a commit", "names no 40-hex subject commit", planCommit("main")),

		engine("no container engine", "docker", "docker"),
		engine("no git", "git", "git"),
		engine("no make", "make", "make"),
		engine("no timeout", "timeout", "timeout"),
		engine("no jq", "jq", "jq"),
		engine("no digest tool", "sha256sum or shasum", "sha256sum", "shasum"),

		{name: "an inputs directory that exists", prepare: func(t *testing.T, r *hardwareE2EBuildRun) {
			if err := os.Mkdir(r.inputs, 0o700); err != nil {
				t.Fatal(err)
			}
		}, exit: 47, code: "NVKR-E-IMMUTABLE-VIOLATION", message: "refusing to replace"},
		{name: "a staged record that exists", prepare: func(t *testing.T, r *hardwareE2EBuildRun) {
			if err := os.WriteFile(r.staged, []byte("earlier\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, exit: 47, code: "NVKR-E-IMMUTABLE-VIOLATION", message: "refusing to replace"},

		verify("the checkout is another commit than the plan's subject", "but the plan's subject commit is "+strings.Repeat("0", 40), false, planCommit(strings.Repeat("0", 40))),
		verify("a tracked file is modified before the build", "before the build: the checkout has uncommitted changes", false, func(t *testing.T, r *hardwareE2EBuildRun) {
			if err := os.WriteFile(filepath.Join(r.harness, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}),
		verify("the checkout is not a git repository", "could not read the checkout's HEAD", false, func(t *testing.T, r *hardwareE2EBuildRun) {
			if err := os.RemoveAll(filepath.Join(r.harness, ".git")); err != nil {
				t.Fatal(err)
			}
		}),
		{name: "the build fails", prepare: hardwareE2EBuildFake("fail"), exit: 21, code: "NVKR-E-BUILD-FAILED", message: "could not build the site inputs from", wantMake: true},
		verify("the build modifies a tracked file", "after the build: the checkout has uncommitted changes", true, hardwareE2EBuildFake("dirty")),
		verify("the build moves HEAD", "after the build: the checkout is at", true, hardwareE2EBuildFake("newcommit")),
		verify("the controller archive is missing", "the build left no controller.tar", true, hardwareE2EBuildFake("omit-controller.tar")),
		verify("the capi-provisioned archive is missing", "the build left no capi-provisioned.tar", true, hardwareE2EBuildFake("omit-capi-provisioned.tar")),
		verify("the profiles archive is missing", "the build left no profiles.tar", true, hardwareE2EBuildFake("omit-profiles.tar")),
		verify("the controller archive is empty", "the build left no controller.tar", true, hardwareE2EBuildFake("empty-controller.tar")),
		verify("nvkectl is missing", "the build left no nvkectl", true, hardwareE2EBuildFake("omit-nvkectl")),
		verify("nvkectl is empty", "the build left no nvkectl", true, hardwareE2EBuildFake("empty-nvkectl")),
		verify("nvkectl is a link", "the build left no nvkectl", true, hardwareE2EBuildFake("link-nvkectl")),
	}
	for _, flag := range []string{"--arch", "--harness", "--plan", "--controller-version", "--capi-version", "--inputs", "--staged"} {
		refusals = append(refusals, usage("missing "+flag, "hardware-e2e-build-subject.sh --arch", hardwareE2EBuildArgs(func(a []string) []string {
			return hardwareE2EDropFlag(a, flag, "")
		})))
	}
	for _, flag := range []string{"--controller-version", "--capi-version"} {
		for _, version := range []string{"0.24.0", "latest", "v1.2", "v1.2.3-", "v1.2.3+build"} {
			refusals = append(refusals, unsupported(flag+" "+version, "not a release version: "+version, hardwareE2EBuildArgs(func(a []string) []string {
				return hardwareE2ESetFlag(a, flag, version)
			})))
		}
	}
	return refusals
}

func TestHardwareE2EBuildSubjectRefusesAnythingButTheSubjectCommitAndLeavesNothingBehind(t *testing.T) {
	for _, tt := range hardwareE2EBuildRefusals() {
		t.Run(tt.name, func(t *testing.T) {
			r := hardwareE2ENewBuildRun(t, "amd64", tt.skip...)
			if tt.prepare != nil {
				tt.prepare(t, r)
			}
			_, inputsExisted := os.Lstat(r.inputs)
			stagedBefore, stagedErr := os.ReadFile(r.staged)

			hardwareE2EExpectFailure(t, r.run(), tt.exit, tt.code, tt.message)

			if _, err := os.Lstat(r.inputs); (err == nil) != (inputsExisted == nil) {
				t.Errorf("inputs directory existence changed across the refusal (before %v, after %v)", inputsExisted, err)
			}
			if stagedErr == nil {
				if got := hardwareE2EReadFile(t, r.staged); got != string(stagedBefore) {
					t.Errorf("a refusal rewrote the staged record: %q", got)
				}
			} else if _, err := os.Lstat(r.staged); err == nil {
				t.Error("a refusal left a staged record")
			}
			if got := hardwareE2EReadFile(t, r.stepEnv); got != hardwareE2EStageEnvSeed {
				t.Errorf("a refusal wrote to the step environment: %q", got)
			}
			if got := r.madeAnything(); got != tt.wantMake {
				t.Errorf("make ran = %v, want %v:\n%s", got, tt.wantMake, strings.Join(r.calls(), "\n"))
			}
		})
	}
}

func TestHardwareE2EBuildSubjectKeepsAnExistingInputsDirectoryItRefusedToReplace(t *testing.T) {
	r := hardwareE2ENewBuildRun(t, "amd64")
	if err := os.Mkdir(r.inputs, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(r.inputs, "earlier.tar")
	if err := os.WriteFile(sentinel, []byte("earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hardwareE2EExpectFailure(t, r.run(), 47, "NVKR-E-IMMUTABLE-VIOLATION", "refusing to replace")
	if got := hardwareE2EReadFile(t, sentinel); got != "earlier\n" {
		t.Errorf("the refusal changed an existing inputs directory: %q", got)
	}
}

// hardwareE2ESubjectFixture is the plan fixture and its provenance re-derived for a subject commit
// the test can check out: same plan, one commit, digests recomputed.
func hardwareE2ESubjectFixture(t *testing.T, directory, commit string) (planFile, provenanceFile string) {
	t.Helper()
	root := hardwareE2ERoot(t)
	plan, err := os.ReadFile(filepath.Join(root, hardwareE2EPlanFile))
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := os.ReadFile(filepath.Join(root, hardwareE2EProvenanceFile))
	if err != nil {
		t.Fatal(err)
	}
	rederived := strings.ReplaceAll(string(plan), hardwareE2ECommit, commit)
	prov := strings.ReplaceAll(string(provenance), hardwareE2ECommit, commit)
	prov = strings.Replace(prov, hardwareE2ESHA256(plan), hardwareE2ESHA256([]byte(rederived)), 1)
	planFile, provenanceFile = filepath.Join(directory, "subject-plan.json"), filepath.Join(directory, "subject-provenance.json")
	if err := os.WriteFile(planFile, []byte(rederived), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provenanceFile, []byte(prov), 0o600); err != nil {
		t.Fatal(err)
	}
	return planFile, provenanceFile
}

// The staged records are what the run summary reads, so the real build script's output must
// satisfy the real summary's contract, not a fixture written to match it.
func TestHardwareE2EBuildSubjectRecordsSatisfyTheSummaryContract(t *testing.T) {
	seed := hardwareE2ENewBuildRun(t, "amd64")
	planFile, provenanceFile := hardwareE2ESubjectFixture(t, seed.scratch, seed.head)
	in := hardwareE2EBuildSubjectSummaryInputsFrom(t, "full", hardwareE2EArmStart, planFile, provenanceFile, seed.head)
	for _, stream := range in.streams {
		r := hardwareE2ENewBuildRun(t, stream.arch)
		planFile, _ := hardwareE2ESubjectFixture(t, r.scratch, r.head)
		staged := filepath.Join(in.leg(stream.arch), "staged.json")
		if err := os.Remove(staged); err != nil {
			t.Fatal(err)
		}
		r.args = hardwareE2ESetFlag(r.args, "--plan", planFile)
		r.args = hardwareE2ESetFlag(r.args, "--staged", staged)
		if result := r.run(); result.exit != 0 {
			t.Fatalf("build %s: exit %d\n%s", stream.arch, result.exit, result.output)
		}
		// Each leg's repository has its own HEAD, so point the summary at the one the legs share.
		if r.head != seed.head {
			hardwareE2EEditJSON(t, staged, func(record map[string]any) { record["builtFromCommit"] = seed.head })
		}
	}

	out := in.path("summary.json")
	hardwareE2ERequireSummaryWritten(t, in, in.writeArgs(out))
	if asserted := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", out); asserted.exit != 0 {
		t.Fatalf("assert of a summary built from build-subject records: exit %d\n%s", asserted.exit, asserted.output)
	}
}
