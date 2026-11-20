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
	"sync"
	"testing"
)

// These contracts cover the script that stages a leg's controller, capi-provisioned and nvkectl
// inputs from the digests the substituted plan names. The container engine, the registry client,
// the harness's fetch script and `timeout` are fakes on a PATH that holds nothing else the script
// could fall back to, so these tests prove what the script asks for and what it refuses. They do
// not prove that Docker, a registry or a hardware node accepts what it stages.

const (
	hardwareE2EStageTool    = "scripts/hardware-e2e-stage-inputs.sh"
	hardwareE2EStageVersion = hardwareE2EVersion
	hardwareE2EStageLabel   = hardwareE2EVersion
	hardwareE2EStageEnvSeed = "EXISTING=1\n"
)

// The fake engine records every call, then succeeds, fails or lies as the scenario says.
// FAKE_FAIL_VERB and FAKE_FAIL_MATCH fail one verb whose arguments contain the match.
// FAKE_OVERRIDE is "FORMAT-TOKEN|REFERENCE-SUBSTRING|OUTPUT" and replaces one inspect answer.
const hardwareE2EFakeDocker = `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$FAKE_LOG"
verb=$1
[[ $verb != image ]] || verb=$2
if [[ -n ${FAKE_FAIL_VERB:-} && $verb == "$FAKE_FAIL_VERB" && $* == *"${FAKE_FAIL_MATCH:-}"* ]]; then
  echo "fake docker: $verb failed" >&2
  exit 1
fi
case $1 in
  pull | tag | buildx) exit 0 ;;
  save)
    file='' tag=''
    shift
    while [[ $# -gt 0 ]]; do
      case $1 in
        -o) file=$2; shift 2 ;;
        --platform) shift 2 ;;
        *) tag=$1; shift ;;
      esac
    done
    if [[ -n ${FAKE_SAVE_EMPTY_MATCH:-} && $tag == *"$FAKE_SAVE_EMPTY_MATCH"* ]]; then
      : >"$file"
    else
      printf 'archive %s\n' "$tag" >"$file"
    fi
    exit 0 ;;
  image)
    format=$4 reference=$5
    case $format in
      *RepoDigests*) out="[\"$reference\"]" ;;
      *Architecture*) out=$FAKE_PLATFORM ;;
      *Labels*) out=$FAKE_LABEL ;;
      *) echo "fake docker: unexpected format $format" >&2; exit 64 ;;
    esac
    if [[ -n ${FAKE_OVERRIDE:-} ]]; then
      IFS='|' read -r token match value <<<"$FAKE_OVERRIDE"
      if [[ $format == *"$token"* && $reference == *"$match"* ]]; then out=$value; fi
    fi
    printf '%s\n' "$out"
    exit 0 ;;
esac
echo "fake docker: unexpected command $*" >&2
exit 64
`

// The fake timeout records the bound it was given and then runs the command, so a test can
// see that every external call the script makes is bounded.
const hardwareE2EFakeTimeout = `#!/usr/bin/env bash
printf 'timeout %s\n' "$*" >>"$FAKE_LOG"
while [[ $1 == --* ]]; do shift; done
shift
exec "$@"
`

// The fake fetch script records the digest reference it was handed and writes, omits or
// links the binary as the scenario says.
const hardwareE2EFakeFetcher = `#!/usr/bin/env bash
printf 'fetch version=%s arch=%s dir=%s ref=%s\n' "$1" "$2" "$3" "${SITE_CLI_REF-}" >>"$FAKE_LOG"
mkdir -p "$3"
case ${FAKE_FETCH:-ok} in
  fail) exit 1 ;;
  nofile) ;;
  symlink) ln -s /etc/hosts "$3/nvkectl-linux-$2" ;;
  *) printf 'nvkectl %s\n' "$2" >"$3/nvkectl-linux-$2" ;;
esac
`

const hardwareE2EFakeOras = "#!/usr/bin/env bash\nexit 0\n"

type hardwareE2EStageRun struct {
	t       *testing.T
	scratch string
	bin     string
	harness string
	subs    string
	inputs  string
	staged  string
	stepEnv string
	log     string
	arch    string
	env     []string
	args    []string
}

func hardwareE2EWriteExecutable(t *testing.T, filename, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

// hardwareE2EStageBin builds the only directory the script's PATH holds: the fakes, and links to
// the few real tools it and the fakes need. A tool named in skip is left out.
func hardwareE2EStageBin(t *testing.T, dir string, skip []string) {
	t.Helper()
	fakes := map[string]string{"docker": hardwareE2EFakeDocker, "timeout": hardwareE2EFakeTimeout, "oras": hardwareE2EFakeOras}
	for name, contents := range fakes {
		if !slices.Contains(skip, name) {
			hardwareE2EWriteExecutable(t, filepath.Join(dir, name), contents)
		}
	}
	for _, name := range []string{"bash", "jq", "cut", "rm", "mkdir", "ln", "sha256sum", "shasum"} {
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

var (
	hardwareE2EStageSubstitutionsOnce  sync.Once
	hardwareE2EStageSubstitutionsBytes []byte
)

// hardwareE2EStageSubstitutions is the record the real substitution script writes for the synthetic
// outputs. Every scenario edits its own copy, so the script runs once per test process.
func hardwareE2EStageSubstitutions(t *testing.T, root string) []byte {
	t.Helper()
	hardwareE2EStageSubstitutionsOnce.Do(func() {
		scratch, err := os.MkdirTemp("", "hardware-e2e-stage-subs-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(scratch)
		outputs := hardwareE2EWriteJSON(t, scratch, "outputs.json", hardwareE2EOutputs())
		substituted := hardwareE2ERun(t, hardwareE2ESubstituteTool,
			filepath.Join(root, hardwareE2EPlanFile), filepath.Join(root, hardwareE2EProvenanceFile), outputs, filepath.Join(scratch, "sub"))
		if substituted.exit != 0 {
			t.Fatalf("substitute plan: exit %d\n%s", substituted.exit, substituted.output)
		}
		hardwareE2EStageSubstitutionsBytes = []byte(hardwareE2EReadFile(t, filepath.Join(scratch, "sub", "substitutions.json")))
	})
	if len(hardwareE2EStageSubstitutionsBytes) == 0 {
		t.Fatal("the substitution record could not be built")
	}
	return hardwareE2EStageSubstitutionsBytes
}

// hardwareE2ENewStageRun builds a leg's scratch directory with an accepted scenario: the harness
// pieces the script needs, a real substitution record for the synthetic outputs, and an existing
// step environment file the script must append to without disturbing.
func hardwareE2ENewStageRun(t *testing.T, arch string, skip ...string) *hardwareE2EStageRun {
	t.Helper()
	root := hardwareE2ERoot(t)
	scratch := t.TempDir()
	r := &hardwareE2EStageRun{
		t: t, scratch: scratch, arch: arch,
		bin:     filepath.Join(scratch, "bin"),
		harness: filepath.Join(scratch, "nvke"),
		subs:    filepath.Join(scratch, "sub", "substitutions.json"),
		inputs:  filepath.Join(scratch, "site-inputs"),
		staged:  filepath.Join(scratch, "staged.json"),
		stepEnv: filepath.Join(scratch, "step-env"),
		log:     filepath.Join(scratch, "calls.log"),
	}
	hardwareE2EStageBin(t, r.bin, skip)
	hardwareE2EWriteExecutable(t, filepath.Join(r.harness, "controller", "test", "e2e", "hack", "fetch-nvkectl.sh"), hardwareE2EFakeFetcher)
	if err := os.MkdirAll(filepath.Join(r.harness, "controller", "test", "fixtures"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.stepEnv, []byte(hardwareE2EStageEnvSeed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(r.subs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.subs, hardwareE2EStageSubstitutions(t, root), 0o600); err != nil {
		t.Fatal(err)
	}
	r.env = []string{"PATH=" + r.bin, "FAKE_LOG=" + r.log, "FAKE_PLATFORM=linux/" + arch, "FAKE_LABEL=" + hardwareE2EStageLabel}
	r.args = []string{
		"--arch", arch, "--harness", r.harness, "--substitutions", r.subs,
		"--controller-version", hardwareE2EStageVersion, "--capi-version", hardwareE2EStageVersion, "--cli-version", hardwareE2EStageVersion,
		"--inputs", r.inputs, "--staged", r.staged, "--step-env", r.stepEnv,
	}
	return r
}

func (r *hardwareE2EStageRun) run() hardwareE2EResult {
	r.t.Helper()
	return hardwareE2ERunEnv(r.t, r.env, hardwareE2EStageTool, r.args...)
}

func (r *hardwareE2EStageRun) fetcher() string {
	return filepath.Join(r.harness, "controller", "test", "e2e", "hack", "fetch-nvkectl.sh")
}

// calls returns what the fakes recorded; a script that refused before any external work leaves none.
func (r *hardwareE2EStageRun) calls() []string {
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

func (r *hardwareE2EStageRun) refs() (controller, capi, cli string) {
	r.t.Helper()
	record := hardwareE2EReadMap(r.t, r.subs)
	find := func(slot string) string {
		for _, entry := range record["replaced"].([]any) {
			if replacement := entry.(map[string]any); replacement["slot"] == slot {
				return replacement["to"].(string)
			}
		}
		r.t.Fatalf("the substitution record has no %s", slot)
		return ""
	}
	return find("controller"), find("capi-provisioned"), find("cli-" + r.arch)
}

func hardwareE2ERequireCall(t *testing.T, calls []string, want string) {
	t.Helper()
	if !slices.Contains(calls, want) {
		t.Errorf("no call %q in:\n%s", want, strings.Join(calls, "\n"))
	}
}

type hardwareE2EStagedRecord struct {
	Schema                 string            `json:"schema"`
	Arch                   string            `json:"arch"`
	Source                 string            `json:"source"`
	BuiltFromCommit        *string           `json:"builtFromCommit"`
	ControllerVersionCheck string            `json:"controllerVersionCheck"`
	Files                  map[string]string `json:"files"`
	ImagePrefix            string            `json:"imagePrefix"`
	PulledByDigest         map[string]string `json:"pulledByDigest"`
	NotNvkrBuilt           []string          `json:"notNvkrBuilt"`
}

func hardwareE2EReadStaged(t *testing.T, filename string) hardwareE2EStagedRecord {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var record hardwareE2EStagedRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode %s: %v", filename, err)
	}
	return record
}

func hardwareE2EReadFile(t *testing.T, filename string) string {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The staged record names the leg's node image as not NVKR-built unless the substitution record gives
// that image an NVKR release as its source. Any other kind, an unknown one and a missing one all count as
// not NVKR-built, and another architecture's image says nothing about this leg's.
func TestHardwareE2EStageInputsNamesTheNodeImageAsNotNvkrBuiltUnlessItIsAnNvkrRelease(t *testing.T) {
	notBuilt := []string{"deploy-path", "imager-efi", "node-image", "site-profiles"}
	built := []string{"deploy-path", "imager-efi", "site-profiles"}
	release := func(replacement map[string]any) {
		replacement["source"] = map[string]any{
			"kind": "nvkr-release", "reference": "oci://registry.example.test/team/node@sha256:" + hardwareE2EDigest("1"),
			"version": hardwareE2EVersion,
		}
	}
	cases := []struct {
		name string
		edit func(own bool, replacement map[string]any)
		want []string
	}{
		{"an NVKR release", func(own bool, r map[string]any) {
			if own {
				release(r)
			}
		}, built},
		{"another CI run", func(bool, map[string]any) {}, notBuilt},
		{"a source of a kind it does not know", func(own bool, r map[string]any) {
			if own {
				r["source"].(map[string]any)["kind"] = "docker-hub"
			}
		}, notBuilt},
		{"a source with no kind", func(own bool, r map[string]any) {
			if own {
				delete(r["source"].(map[string]any), "kind")
			}
		}, notBuilt},
		{"no source", func(own bool, r map[string]any) {
			if own {
				delete(r, "source")
			}
		}, notBuilt},
		{"an NVKR release for the other architecture only", func(own bool, r map[string]any) {
			if !own {
				release(r)
			}
		}, notBuilt},
	}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, tc := range cases {
			t.Run(arch+"/"+tc.name, func(t *testing.T) {
				r := hardwareE2ENewStageRun(t, arch)
				hardwareE2EEditJSON(t, r.subs, func(record map[string]any) {
					for _, entry := range record["replaced"].([]any) {
						replacement := entry.(map[string]any)
						if slot, _ := replacement["slot"].(string); strings.HasPrefix(slot, "os-image-") {
							tc.edit(slot == "os-image-"+arch, replacement)
						}
					}
				})
				if result := r.run(); result.exit != 0 {
					t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
				}
				if got := hardwareE2EReadStaged(t, r.staged).NotNvkrBuilt; !slices.Equal(got, tc.want) {
					t.Errorf("notNvkrBuilt = %v, want %v", got, tc.want)
				}
			})
		}
	}
}

func TestHardwareE2EStageInputsPullsByDigestStagesLocalArchivesAndRecordsIt(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			r := hardwareE2ENewStageRun(t, arch)
			result := r.run()
			if result.exit != 0 {
				t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
			}
			controller, capi, cli := r.refs()
			pinned := func(ref string) string { return strings.TrimPrefix(ref, "oci://") }
			const prefix = "localhost/nvke"
			controllerTag := prefix + "/nvke-controller:" + hardwareE2EStageVersion
			capiTag := prefix + "/cluster-api-provider-provisioned:" + hardwareE2EStageVersion

			calls := r.calls()
			for _, want := range []string{
				"timeout --kill-after=30s 30m docker pull --quiet --platform linux/" + arch + " " + pinned(controller),
				"timeout --kill-after=30s 30m docker pull --quiet --platform linux/" + arch + " " + pinned(capi),
				"timeout 2m docker tag " + pinned(controller) + " " + controllerTag,
				"timeout 2m docker tag " + pinned(capi) + " " + capiTag,
				"timeout --kill-after=30s 15m docker save --platform linux/" + arch + " -o " + r.inputs + "/controller.tar " + controllerTag,
				"timeout --kill-after=30s 15m docker save --platform linux/" + arch + " -o " + r.inputs + "/capi-provisioned.tar " + capiTag,
				"timeout --kill-after=30s 30m docker buildx build --platform linux/" + arch + " --load -t nke-e2e-profiles:local " + r.harness + "/controller/test/fixtures",
				"timeout --kill-after=30s 15m docker save --platform linux/" + arch + " -o " + r.inputs + "/profiles.tar nke-e2e-profiles:local",
				"timeout --kill-after=30s 30m " + r.fetcher() + " " + hardwareE2EStageVersion + " " + arch + " " + r.inputs + "/nvkectl",
				"fetch version=" + hardwareE2EStageVersion + " arch=" + arch + " dir=" + r.inputs + "/nvkectl ref=" + pinned(cli),
			} {
				hardwareE2ERequireCall(t, calls, want)
			}
			// The fake timeout logs the bound it was given and then runs the command, which logs its own
			// call, so each engine call must directly follow the timeout line that wrapped it. Looking
			// for any matching line would let one wrapper vouch for two identical calls.
			engineCalls := 0
			for index, call := range calls {
				if !strings.HasPrefix(call, "docker ") {
					continue
				}
				engineCalls++
				if index == 0 || !strings.HasPrefix(calls[index-1], "timeout ") || !strings.HasSuffix(calls[index-1], " "+call) {
					t.Errorf("an engine call ran outside its own timeout: %q", call)
				}
			}
			if engineCalls == 0 {
				t.Error("the fake engine recorded no calls")
			}
			for _, ref := range []string{pinned(controller), pinned(capi)} {
				hardwareE2ERequireCall(t, calls, "timeout 2m docker image inspect --format {{json .RepoDigests}} "+ref)
			}

			record := hardwareE2EReadStaged(t, r.staged)
			if record.Schema != "nvkr.dev/hardware-e2e-staged/v1" || record.Arch != arch {
				t.Errorf("record schema/arch = %q/%q", record.Schema, record.Arch)
			}
			if record.Source != "nvkr-outputs" {
				t.Errorf("source = %q, want nvkr-outputs", record.Source)
			}
			if record.BuiltFromCommit != nil {
				t.Errorf("builtFromCommit = %q, want null: nothing here proves which NVKE commit NVKR built these from", *record.BuiltFromCommit)
			}
			if record.ControllerVersionCheck != "matched" {
				t.Errorf("controllerVersionCheck = %q, want matched", record.ControllerVersionCheck)
			}
			if record.ImagePrefix != prefix {
				t.Errorf("imagePrefix = %q, want %q", record.ImagePrefix, prefix)
			}
			// The synthetic manifest's node images come from another CI run, not an NVKR release, so the
			// record does not claim them as NVKR-built.
			if want := []string{"deploy-path", "imager-efi", "node-image", "site-profiles"}; !slices.Equal(record.NotNvkrBuilt, want) {
				t.Errorf("notNvkrBuilt = %v, want %v", record.NotNvkrBuilt, want)
			}
			wantPulled := map[string]string{"controller": controller, "capi-provisioned": capi, "cli-" + arch: cli}
			if len(record.PulledByDigest) != len(wantPulled) {
				t.Errorf("pulledByDigest = %v, want %v", record.PulledByDigest, wantPulled)
			}
			for slot, want := range wantPulled {
				if record.PulledByDigest[slot] != want {
					t.Errorf("pulledByDigest[%s] = %q, want %q", slot, record.PulledByDigest[slot], want)
				}
			}

			files := map[string]string{
				"controller.tar":        r.inputs + "/controller.tar",
				"capi-provisioned.tar":  r.inputs + "/capi-provisioned.tar",
				"profiles.tar":          r.inputs + "/profiles.tar",
				"nvkectl-linux-" + arch: r.inputs + "/nvkectl/nvkectl-linux-" + arch,
			}
			if len(record.Files) != len(files) {
				t.Errorf("files = %v, want exactly %d entries", record.Files, len(files))
			}
			for name, filename := range files {
				if want := hardwareE2ESHA256([]byte(hardwareE2EReadFile(t, filename))); record.Files[name] != want {
					t.Errorf("files[%s] = %q, want the digest of %s (%q)", name, record.Files[name], filename, want)
				}
			}
			// The controller archive holds the localhost name, which no registry answers to.
			if got, want := hardwareE2EReadFile(t, files["controller.tar"]), "archive "+controllerTag+"\n"; got != want {
				t.Errorf("controller.tar = %q, want %q", got, want)
			}
			if got, want := hardwareE2EReadFile(t, files["capi-provisioned.tar"]), "archive "+capiTag+"\n"; got != want {
				t.Errorf("capi-provisioned.tar = %q, want %q", got, want)
			}

			if got, want := hardwareE2EReadFile(t, r.stepEnv), hardwareE2EStageEnvSeed+"SITE_IMAGES=localhost/nvke\nSITE_NVKE_CONTROLLER_REPOSITORY=nvke-controller\n"; got != want {
				t.Errorf("step env = %q, want %q", got, want)
			}
			for filename, want := range map[string]os.FileMode{r.staged: 0o600, r.inputs: 0o700} {
				if info, err := os.Stat(filename); err != nil || info.Mode().Perm() != want {
					t.Errorf("%s mode = %v (%v), want %o", filename, info, err, want)
				}
			}
		})
	}
}

func TestHardwareE2EStageInputsReportsWhatItCouldNotCheck(t *testing.T) {
	tests := []struct {
		name   string
		label  string
		prefix string
		want   string
	}{
		{name: "label with the leading v", label: hardwareE2EStageVersion, want: "matched"},
		{name: "label without the leading v", label: strings.TrimPrefix(hardwareE2EStageVersion, "v"), want: "matched"},
		{name: "no label says it could not decide", label: "", want: "not-determined"},
		{name: "a chosen localhost prefix", label: hardwareE2EStageVersion, prefix: "localhost/nvkr-hardware-e2e", want: "matched"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := hardwareE2ENewStageRun(t, "amd64")
			r.env = append(r.env, "FAKE_LABEL="+tt.label)
			wantPrefix := "localhost/nvke"
			if tt.prefix != "" {
				r.args = append(r.args, "--image-prefix", tt.prefix)
				wantPrefix = tt.prefix
			}
			result := r.run()
			if result.exit != 0 {
				t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
			}
			record := hardwareE2EReadStaged(t, r.staged)
			if record.ControllerVersionCheck != tt.want {
				t.Errorf("controllerVersionCheck = %q, want %q", record.ControllerVersionCheck, tt.want)
			}
			if !strings.Contains(result.output, "controller version check: "+tt.want) {
				t.Errorf("the script should say what it could check:\n%s", result.output)
			}
			if got, want := hardwareE2EReadFile(t, r.stepEnv), hardwareE2EStageEnvSeed+"SITE_IMAGES="+wantPrefix+"\nSITE_NVKE_CONTROLLER_REPOSITORY=nvke-controller\n"; got != want {
				t.Errorf("step env = %q, want %q", got, want)
			}
			controller, _, _ := r.refs()
			hardwareE2ERequireCall(t, r.calls(), "timeout 2m docker tag "+strings.TrimPrefix(controller, "oci://")+" "+wantPrefix+"/nvke-controller:"+hardwareE2EStageVersion)
		})
	}

	t.Run("without a step environment file nothing is appended", func(t *testing.T) {
		r := hardwareE2ENewStageRun(t, "amd64")
		r.args = hardwareE2EDropFlag(r.args, "--step-env", "")
		if result := r.run(); result.exit != 0 {
			t.Fatalf("exit %d, want 0:\n%s", result.exit, result.output)
		}
		if got := hardwareE2EReadFile(t, r.stepEnv); got != hardwareE2EStageEnvSeed {
			t.Errorf("step env = %q, want it untouched", got)
		}
	})
}

type hardwareE2EStageRefusal struct {
	name    string
	skip    []string
	prepare func(t *testing.T, r *hardwareE2EStageRun)
	exit    int
	code    string
	message string
	// touched is true for a refusal the script can only reach after it started external work.
	touched bool
}

const (
	hardwareE2EStageDigestF = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

func hardwareE2EStageSetArgs(edit func(args []string) []string) func(*testing.T, *hardwareE2EStageRun) {
	return func(t *testing.T, r *hardwareE2EStageRun) { t.Helper(); r.args = edit(r.args) }
}

func hardwareE2EStageEnv(entries ...string) func(*testing.T, *hardwareE2EStageRun) {
	return func(t *testing.T, r *hardwareE2EStageRun) { t.Helper(); r.env = append(r.env, entries...) }
}

func hardwareE2EStageReplaced(slot, field string, value any) func(*testing.T, *hardwareE2EStageRun) {
	return func(t *testing.T, r *hardwareE2EStageRun) {
		t.Helper()
		hardwareE2EEditJSON(t, r.subs, hardwareE2EReplaced(slot, field, value))
	}
}

func hardwareE2EStageRefusals() []hardwareE2EStageRefusal {
	usage := func(name, message string, prepare func(*testing.T, *hardwareE2EStageRun)) hardwareE2EStageRefusal {
		return hardwareE2EStageRefusal{name: name, prepare: prepare, exit: 2, code: "NVKR-E-USAGE", message: message}
	}
	unsupported := func(name, message string, prepare func(*testing.T, *hardwareE2EStageRun)) hardwareE2EStageRefusal {
		return hardwareE2EStageRefusal{name: name, prepare: prepare, exit: 13, code: "NVKR-E-UNSUPPORTED", message: message}
	}
	unpinned := func(name, ref string) hardwareE2EStageRefusal {
		return hardwareE2EStageRefusal{
			name: name, prepare: hardwareE2EStageReplaced("capi-provisioned", "to", ref),
			exit: 49, code: "NVKR-E-UNPINNED-INPUT", message: "capi-provisioned is not an oci:// reference pinned by digest",
		}
	}
	engine := func(name, tool string, skip ...string) hardwareE2EStageRefusal {
		return hardwareE2EStageRefusal{name: name, skip: skip, exit: 29, code: "NVKR-E-ENGINE-CAPABILITY", message: "missing " + tool}
	}
	runtime := func(name string, exit int, code, message string, prepare func(*testing.T, *hardwareE2EStageRun)) hardwareE2EStageRefusal {
		return hardwareE2EStageRefusal{name: name, prepare: prepare, exit: exit, code: code, message: message, touched: true}
	}
	fail := func(verb, match string) func(*testing.T, *hardwareE2EStageRun) {
		return hardwareE2EStageEnv("FAKE_FAIL_VERB="+verb, "FAKE_FAIL_MATCH="+match)
	}
	override := func(token, match, value string) func(*testing.T, *hardwareE2EStageRun) {
		return hardwareE2EStageEnv("FAKE_OVERRIDE=" + token + "|" + match + "|" + value)
	}
	otherDigest := func(repo string) string {
		return `["registry.example.test/team/` + repo + `@sha256:` + hardwareE2EStageDigestF + `"]`
	}

	refusals := []hardwareE2EStageRefusal{
		usage("no arguments", "hardware-e2e-stage-inputs.sh --arch", hardwareE2EStageSetArgs(func([]string) []string { return nil })),
		usage("unknown argument", "unknown argument: --bogus", hardwareE2EStageSetArgs(func(a []string) []string { return append(a, "--bogus", "x") })),
		usage("flag without a value", "--arch needs a value", hardwareE2EStageSetArgs(func([]string) []string { return []string{"--arch"} })),
		usage("substitutions missing", "not a regular file", hardwareE2EStageSetArgs(func(a []string) []string {
			return hardwareE2ESetFlag(a, "--substitutions", "/nonexistent/substitutions.json")
		})),
		usage("step environment is not a file", "--step-env is not a file", hardwareE2EStageSetArgs(func(a []string) []string {
			return hardwareE2ESetFlag(a, "--step-env", "/nonexistent/step-env")
		})),
		usage("substitutions is a link", "not a regular file", func(t *testing.T, r *hardwareE2EStageRun) {
			link := filepath.Join(r.scratch, "linked.json")
			if err := os.Symlink(r.subs, link); err != nil {
				t.Fatal(err)
			}
			r.args = hardwareE2ESetFlag(r.args, "--substitutions", link)
		}),
		usage("harness without its fetch script", "is not an NVKE checkout", func(t *testing.T, r *hardwareE2EStageRun) {
			if err := os.Remove(r.fetcher()); err != nil {
				t.Fatal(err)
			}
		}),
		usage("harness fetch script is not executable", "is not an NVKE checkout", func(t *testing.T, r *hardwareE2EStageRun) {
			if err := os.Chmod(r.fetcher(), 0o600); err != nil {
				t.Fatal(err)
			}
		}),
		usage("harness without the profiles fixture", "is not an NVKE checkout", func(t *testing.T, r *hardwareE2EStageRun) {
			if err := os.Remove(filepath.Join(r.harness, "controller", "test", "fixtures")); err != nil {
				t.Fatal(err)
			}
		}),
		usage("harness is a link", "is not an NVKE checkout", func(t *testing.T, r *hardwareE2EStageRun) {
			link := filepath.Join(r.scratch, "linked-nvke")
			if err := os.Symlink(r.harness, link); err != nil {
				t.Fatal(err)
			}
			r.args = hardwareE2ESetFlag(r.args, "--harness", link)
		}),

		unsupported("an architecture other than amd64 or arm64", "unsupported architecture: s390x", hardwareE2EStageSetArgs(func(a []string) []string {
			return hardwareE2ESetFlag(a, "--arch", "s390x")
		})),
		unsupported("substitution record of another schema", "unsupported schema", func(t *testing.T, r *hardwareE2EStageRun) {
			hardwareE2EEditJSON(t, r.subs, func(record map[string]any) { record["schema"] = "nvkr.dev/hardware-e2e-substitutions/v0" })
		}),
		unsupported("a prefix a registry could answer to", "must be a localhost/ name", hardwareE2EStageSetArgs(func(a []string) []string {
			return append(a, "--image-prefix", "ghcr.io/example/nvke")
		})),
		unsupported("an uppercase prefix", "must be a localhost/ name", hardwareE2EStageSetArgs(func(a []string) []string {
			return append(a, "--image-prefix", "localhost/Nvke")
		})),
		unsupported("a prefix with a trailing slash", "must be a localhost/ name", hardwareE2EStageSetArgs(func(a []string) []string {
			return append(a, "--image-prefix", "localhost/nvke/")
		})),
		unsupported("a prefix of only localhost", "must be a localhost/ name", hardwareE2EStageSetArgs(func(a []string) []string {
			return append(a, "--image-prefix", "localhost")
		})),

		unpinned("a tag instead of a digest", "oci://registry.example.test/team/capi:v1"),
		unpinned("no oci scheme", "registry.example.test/team/capi@sha256:"+hardwareE2EDigest("b")),
		unpinned("another scheme", "https://registry.example.test/team/capi@sha256:"+hardwareE2EDigest("b")),
		unpinned("a short digest", "oci://registry.example.test/team/capi@sha256:"+hardwareE2EDigest("b")[:63]),
		unpinned("an uppercase digest", "oci://registry.example.test/team/capi@sha256:"+strings.ToUpper(hardwareE2EDigest("b"))),
		unpinned("a digest on no repository", "oci://registry.example.test@sha256:"+hardwareE2EDigest("b")),
		{
			name: "controller not pinned", prepare: hardwareE2EStageReplaced("controller", "to", "oci://registry.example.test/team/controller:latest"),
			exit: 49, code: "NVKR-E-UNPINNED-INPUT", message: "controller is not an oci:// reference pinned by digest",
		},
		{
			name: "cli not pinned", prepare: func(t *testing.T, r *hardwareE2EStageRun) {
				hardwareE2EStageReplaced("cli-"+r.arch, "to", "oci://registry.example.test/team/cli:latest")(t, r)
			},
			exit: 49, code: "NVKR-E-UNPINNED-INPUT", message: "is not an oci:// reference pinned by digest",
		},

		{
			name: "no capi-provisioned artifact", prepare: hardwareE2EStageReplaced("capi-provisioned", "slot", "something-else"),
			exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "carries no single capi-provisioned artifact",
		},
		{
			name: "two controller artifacts", prepare: func(t *testing.T, r *hardwareE2EStageRun) {
				hardwareE2EEditJSON(t, r.subs, func(record map[string]any) {
					for _, entry := range record["replaced"].([]any) {
						if entry.(map[string]any)["slot"] == "controller" {
							record["replaced"] = append(record["replaced"].([]any), entry)
							return
						}
					}
				})
			},
			exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "carries no single controller artifact",
		},
		{
			name: "no cli for this architecture", prepare: func(t *testing.T, r *hardwareE2EStageRun) {
				hardwareE2EStageReplaced("cli-"+r.arch, "slot", "something-else")(t, r)
			},
			exit: 41, code: "NVKR-E-RELEASE-INCOMPLETE", message: "carries no single cli-amd64 artifact",
		},

		engine("no container engine", "docker", "docker"),
		engine("no registry client", "oras", "oras"),
		engine("no timeout", "timeout", "timeout"),
		engine("no jq", "jq", "jq"),
		engine("no digest tool", "sha256sum or shasum", "sha256sum", "shasum"),

		{
			name: "an inputs directory that exists", prepare: func(t *testing.T, r *hardwareE2EStageRun) {
				if err := os.Mkdir(r.inputs, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			exit: 47, code: "NVKR-E-IMMUTABLE-VIOLATION", message: "refusing to replace",
		},
		{
			name: "a staged record that exists", prepare: func(t *testing.T, r *hardwareE2EStageRun) {
				if err := os.WriteFile(r.staged, []byte("earlier\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			exit: 47, code: "NVKR-E-IMMUTABLE-VIOLATION", message: "refusing to replace",
		},

		runtime("controller pull fails", 20, "NVKR-E-FETCH-FAILED", "could not pull controller", fail("pull", "team/controller")),
		runtime("capi-provisioned pull fails", 20, "NVKR-E-FETCH-FAILED", "could not pull capi-provisioned", fail("pull", "team/capi")),
		runtime("digest inspection fails", 22, "NVKR-E-VERIFY-FAILED", "could not inspect controller", fail("inspect", "RepoDigests")),
		runtime("controller reports another digest", 22, "NVKR-E-VERIFY-FAILED", "does not include the pinned",
			override("RepoDigests", "team/controller", otherDigest("controller"))),
		runtime("capi-provisioned reports another digest", 22, "NVKR-E-VERIFY-FAILED", "capi-provisioned was pulled as",
			override("RepoDigests", "team/capi", otherDigest("capi"))),
		runtime("digests are null", 22, "NVKR-E-VERIFY-FAILED", "does not include the pinned", override("RepoDigests", "team/controller", "null")),
		runtime("digests are empty", 22, "NVKR-E-VERIFY-FAILED", "does not include the pinned", override("RepoDigests", "team/controller", "[]")),
		runtime("platform inspection fails", 22, "NVKR-E-VERIFY-FAILED", "could not inspect the platform", fail("inspect", "Architecture")),
		runtime("controller is another architecture", 22, "NVKR-E-VERIFY-FAILED", "controller is linux/s390x, not linux/amd64",
			override("Architecture", "team/controller", "linux/s390x")),
		runtime("capi-provisioned is another architecture", 22, "NVKR-E-VERIFY-FAILED", "capi-provisioned is linux/s390x, not linux/amd64",
			override("Architecture", "team/capi", "linux/s390x")),
		runtime("controller is another operating system", 22, "NVKR-E-VERIFY-FAILED", "controller is windows/amd64, not linux/amd64",
			override("Architecture", "team/controller", "windows/amd64")),
		runtime("tag fails", 22, "NVKR-E-VERIFY-FAILED", "could not tag controller", fail("tag", "")),
		runtime("saving the controller fails", 20, "NVKR-E-FETCH-FAILED", "could not save", fail("save", "nvke-controller")),
		runtime("saving the controller writes nothing", 22, "NVKR-E-VERIFY-FAILED", "wrote nothing", hardwareE2EStageEnv("FAKE_SAVE_EMPTY_MATCH=nvke-controller")),
		runtime("saving capi-provisioned writes nothing", 22, "NVKR-E-VERIFY-FAILED", "wrote nothing", hardwareE2EStageEnv("FAKE_SAVE_EMPTY_MATCH=cluster-api-provider-provisioned")),
		runtime("the controller image reports another version", 22, "NVKR-E-VERIFY-FAILED", "the controller image reports version v0.99.0, but the plan names "+hardwareE2EStageVersion,
			hardwareE2EStageEnv("FAKE_LABEL=v0.99.0")),
		runtime("the controller labels cannot be read", 22, "NVKR-E-VERIFY-FAILED", "could not read the controller image labels", fail("inspect", "Labels")),
		runtime("the profiles build fails", 21, "NVKR-E-BUILD-FAILED", "could not build the site profiles fixture", fail("buildx", "")),
		runtime("saving the profiles fails", 20, "NVKR-E-FETCH-FAILED", "could not save the site profiles image", fail("save", "nke-e2e-profiles")),
		runtime("saving the profiles writes nothing", 22, "NVKR-E-VERIFY-FAILED", "saving the site profiles image wrote nothing", hardwareE2EStageEnv("FAKE_SAVE_EMPTY_MATCH=nke-e2e-profiles")),
		runtime("the nvkectl fetch fails", 20, "NVKR-E-FETCH-FAILED", "could not stage nvkectl from oci://", hardwareE2EStageEnv("FAKE_FETCH=fail")),
		runtime("the nvkectl fetch leaves no binary", 22, "NVKR-E-VERIFY-FAILED", "left no regular file", hardwareE2EStageEnv("FAKE_FETCH=nofile")),
		runtime("the nvkectl fetch leaves a link", 22, "NVKR-E-VERIFY-FAILED", "left no regular file", hardwareE2EStageEnv("FAKE_FETCH=symlink")),
	}
	for _, flag := range []string{"--arch", "--harness", "--substitutions", "--controller-version", "--capi-version", "--cli-version", "--inputs", "--staged"} {
		refusals = append(refusals, usage("missing "+flag, "hardware-e2e-stage-inputs.sh --arch", hardwareE2EStageSetArgs(func(a []string) []string {
			return hardwareE2EDropFlag(a, flag, "")
		})))
	}
	for _, flag := range []string{"--controller-version", "--capi-version", "--cli-version"} {
		for _, version := range []string{"0.24.0", "latest", "v1.2", "v1.2.3-", "v1.2.3+build"} {
			refusals = append(refusals, unsupported(flag+" "+version, "not a release version: "+version, hardwareE2EStageSetArgs(func(a []string) []string {
				return hardwareE2ESetFlag(a, flag, version)
			})))
		}
	}
	return refusals
}

func TestHardwareE2EStageInputsRefusesWhatItCannotStageAndLeavesNothingBehind(t *testing.T) {
	for _, tt := range hardwareE2EStageRefusals() {
		t.Run(tt.name, func(t *testing.T) {
			r := hardwareE2ENewStageRun(t, "amd64", tt.skip...)
			if tt.prepare != nil {
				tt.prepare(t, r)
			}
			// What existed before the run is what must exist after a refusal.
			_, inputsExisted := os.Lstat(r.inputs)
			stagedBefore, stagedErr := os.ReadFile(r.staged)

			result := r.run()
			hardwareE2EExpectFailure(t, result, tt.exit, tt.code, tt.message)

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
			if calls := r.calls(); !tt.touched && len(calls) > 0 {
				t.Errorf("a refusal that needs no external work still made calls:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

func TestHardwareE2EStageInputsKeepsAnExistingInputsDirectoryItRefusedToReplace(t *testing.T) {
	r := hardwareE2ENewStageRun(t, "amd64")
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

// The staged record is what the run summary reads, so the real script's output must satisfy
// the real summary's contract, not a fixture written to match it.
func TestHardwareE2EStagedRecordsSatisfyTheSummaryContract(t *testing.T) {
	in := hardwareE2EBuildSummaryInputs(t, "full", hardwareE2EArmStart)
	for _, stream := range in.streams {
		r := hardwareE2ENewStageRun(t, stream.arch)
		staged := filepath.Join(in.leg(stream.arch), "staged.json")
		if err := os.Remove(staged); err != nil {
			t.Fatal(err)
		}
		r.args = hardwareE2ESetFlag(r.args, "--substitutions", in.path("sub", "substitutions.json"))
		r.args = hardwareE2ESetFlag(r.args, "--staged", staged)
		if result := r.run(); result.exit != 0 {
			t.Fatalf("stage %s: exit %d\n%s", stream.arch, result.exit, result.output)
		}
	}

	out := in.path("summary.json")
	hardwareE2ERequireSummaryWritten(t, in, in.writeArgs(out))
	if asserted := hardwareE2ERun(t, hardwareE2ESummaryTool, "assert", out); asserted.exit != 0 {
		t.Fatalf("assert of a summary built from staged records: exit %d\n%s", asserted.exit, asserted.output)
	}
}
