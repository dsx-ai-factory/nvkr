// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDefaultMacBuildRetainsTheProductSelectionAndExplicitInputs(t *testing.T) {
	for _, tc := range []struct {
		name, platform, network, definition, environment, nvkr string
		conformance, wantDefault, reject                       bool
	}{
		{name: "NVIDIA", platform: "darwin/arm64", wantDefault: true},
		{name: "matched upstream", platform: "darwin/arm64", network: "upstream-ovn", wantDefault: true},
		{name: "Linux retained", platform: "linux/arm64"},
		{name: "explicit prepared retained", platform: "darwin/arm64", definition: ".nvkr/explicit/definition.json", environment: ".nvkr/explicit/environment.json", nvkr: "/explicit/nvkr"},
		{name: "Mac conformance", platform: "darwin/arm64", conformance: true, wantDefault: true},
		{name: "Mac upstream conformance", platform: "darwin/arm64", network: "upstream-ovn", conformance: true, wantDefault: true},
		{name: "explicit Linux Environment retained", platform: "darwin/arm64", definition: "inputs/definitions/demo/arm64-upstream-local-kvm.json", environment: "inputs/environments/local-arm64-kvm.json", nvkr: "/explicit/nvkr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := BuildConfig{Network: tc.network, Definition: tc.definition, Environment: tc.environment, NVKR: tc.nvkr, Conformance: tc.conformance}
			original := cfg
			selected, err := defaultMacBuild(&cfg, tc.platform, "/distribution/bin/nvke-demo")
			if selected != tc.wantDefault || (err != nil) != tc.reject {
				t.Fatalf("selection=%v error=%v", selected, err)
			}
			if tc.reject {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
					t.Fatalf("unsupported default error=%v", err)
				}
				return
			}
			if !selected {
				if !reflect.DeepEqual(cfg, original) {
					t.Fatal("explicit input or Linux selection changed")
				}
				return
			}
			definition, _, err := LocalArtifactInputsFor(tc.network, tc.conformance, "darwin")
			if err != nil || cfg.Definition != definition || cfg.Environment != "inputs/environments/local-native-mac.json" || cfg.NVKR != "/distribution/bin/nvkr" {
				t.Fatalf("default substituted product inputs: %+v, %v", cfg, err)
			}
		})
	}
}

func TestDefaultMacPreparationJoinsExistingOwnersAndCapturedInvocation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the installed Apple system-tool contract is Mac-only")
	}
	repo, bin := t.TempDir(), t.TempDir()
	probeEnv, packerVersion, pluginName := macBuildProbeFixtures(t, repo, bin)
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	plugin := filepath.Join(bin, pluginName)
	write(plugin, "fixture", 0700)
	write(plugin+"_SHA256SUM", "fixture", 0600)
	write(filepath.Join(bin, "packer"), "#!/bin/sh\ncase \"$1\" in version) printf '%s\\n' 'Packer v"+packerVersion+"';; plugins) printf '%s\\n' \"$PLUGIN\";; *) exit 23;; esac\n", 0700)
	for _, name := range []string{"qemu-img", "qemu-system-aarch64", "python3", "gdirname", "ginstall", "gmktemp", "grm", "gchmod", "gstat", "gmv", "gsha256sum", "gcat", "gmkdir", "gcp", "gtr", "ghead", "gtail", "gwc", "gcut", "gsort", "gtouch", "gsleep", "greadlink", "gln", "gtee"} {
		write(filepath.Join(bin, name), "#!/bin/sh\nexit 31\n", 0700)
	}
	for _, name := range []string{"edk2-aarch64-code.fd", "edk2-arm-vars.fd", "efi-virtio.rom", "vgabios-virtio.bin", "keymaps/en-us"} {
		write(filepath.Join(filepath.Dir(bin), "share/qemu", name), "fixture", 0600)
	}
	pin := "sha256:" + strings.Repeat("a", 64)
	write(filepath.Join(bin, "bash"), `#!/bin/sh
set -eu
test "${TMPDIR:-}" = "$EXPECTED_WORK"
test -d "$TMPDIR"
test . -ef "$EXPECTED_REPOSITORY"
printf '%s\n' "utility:$*" >>"$CALLS"
printf '%s\n' "$UTILITY"
`, 0700)
	write(filepath.Join(bin, "jq"), `#!/bin/sh
printf '%s\n' "projection:$*" >>"$CALLS"
case "$*" in
 *local_toolchain_scopes*) printf '%s\n' '[{},{},{}]';;
 *local_toolchain_substitutions*) printf '%s\n' '[{},{},{}]';;
 *toolchain_overlay*) printf '%s\n' '{}';;
 *local_toolchain_definition*) printf '%s\n' '{"catalog":{},"outputs":[]}';;
 *) exit 23;;
esac
`, 0700)
	profile, build := nightlyIdentityFixture()
	plan, _ := nightlyCompiledFixture(t, profile, build)
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(repo, "plan.json")
	write(planPath, string(raw), 0600)
	nvkr := filepath.Join(bin, "nvkr")
	write(nvkr, `#!/bin/sh
printf '%s\n' "nvkr:$*" >>"$CALLS"
case "$1:$2" in
 factory:prepare-native-packer) printf '%s\n' "{\"layout\":\".nvkr/mac-build/first/toolchain\",\"bundle\":\"packer-tools\",\"toolchain\":\"$UTILITY\",\"platform\":\"darwin/arm64\"}";;
 factory:prepare-native-packer-inputs) printf '%s\n' '{"definition":".nvkr/mac-build/first/packer-inputs/definition.json","environment":".nvkr/mac-build/first/packer-inputs/environment.json"}';;
 run:plan) /bin/cat "$PLAN";;
 run:local) case "$*" in *--capture-inputs-root=.*) exit 0;; *) exit 24;; esac;;
 *) exit 25;;
esac
`, 0700)
	calls := filepath.Join(repo, "calls")
	work := filepath.Join(repo, ".nvkr/mac-build/first")
	runner := Runner{Env: append(probeEnv, "PLUGIN="+plugin, "CALLS="+calls, "PLAN="+planPath, "UTILITY="+pin, "EXPECTED_WORK="+work, "EXPECTED_REPOSITORY="+repo)}
	cfg := BuildConfig{Repository: repo, Definition: "inputs/definitions/demo/arm64-nvidia-local-kvm.json", Environment: "inputs/environments/local-native-mac.json", NVKR: nvkr, AttemptPrefix: "first", Jobs: 2}
	if err := prepareDefaultMacBuild(t.Context(), runner, &cfg); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(work); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("temporary storage must remain in the private attempt workspace: %v, %v", info, err)
	}
	if cfg.Definition != ".nvkr/mac-build/first/definition.json" || cfg.Environment != ".nvkr/mac-build/first/packer-inputs/environment.json" {
		t.Fatalf("prepared invocation selection: %+v", cfg)
	}
	if err := buildLocalArtifacts(t.Context(), runner, "", cfg); err != nil {
		t.Fatal(err)
	}
	observed, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(string(observed), "\n") {
		if strings.HasPrefix(call, "nvkr:factory prepare-native-packer-inputs ") &&
			(!strings.Contains(" "+call+" ", " --accelerator tcg ") || !strings.Contains(" "+call+" ", " --variable cpu_model=max ") || !strings.Contains(" "+call+" ", " --variable ssh_timeout=180m ") || strings.Contains(call, "kernel_package=")) {
			t.Fatal("normal Mac preparation must use source-compatible TCG without replacing its kernel")
		}
	}
	sequence := []string{"probe:go:env GOVERSION", "probe:docker:buildx version", "probe:docker:version --format {{.Server.Version}}", "probe:clang:--version", "nvkr:factory prepare-native-packer ", "nvkr:factory prepare-native-packer-inputs --definition inputs/definitions/demo/arm64-nvidia-local-kvm.json --environment inputs/environments/local-native-mac.json", "nvkr:run plan ", "local_toolchain_scopes", "utility:toolchains/nvkr-packer/build.sh --arch arm64 --diagnostic-local --print-validated-image-id", "toolchain_overlay", "local_toolchain_definition", "nvkr:run local --definition .nvkr/mac-build/first/definition.json --environment .nvkr/mac-build/first/packer-inputs/environment.json"}
	rest := string(observed)
	for _, expected := range sequence {
		_, remaining, found := strings.Cut(rest, expected)
		if !found {
			t.Fatalf("missing ordered owner invocation %q in %s", expected, observed)
		}
		rest = remaining
	}
	manifest, err := os.ReadFile(filepath.Join(repo, ".nvkr/mac-build/first/native-packer-files.json"))
	if err != nil {
		t.Fatal(err)
	}
	var files struct{ Files []json.RawMessage }
	if err := json.Unmarshal(manifest, &files); err != nil || len(files.Files) != 40 {
		t.Fatalf("captured dependency description files=%d error=%v", len(files.Files), err)
	}
}

func TestDefaultMacPreparationRejectsMissingPrerequisitesBeforeWriting(t *testing.T) {
	for _, mode := range []string{"missing packer", "wrong installed version", "missing reviewed plugin"} {
		t.Run(mode, func(t *testing.T) {
			repo := t.TempDir()
			bin := t.TempDir()
			env, packerVersion, _ := macBuildProbeFixtures(t, repo, bin)
			packer, want := "Packer v"+packerVersion, "QEMU plugin"
			switch mode {
			case "missing packer":
				packer, want = "", "packer"
			case "wrong installed version":
				packer, want = "Packer v1.0.0", "Packer "+packerVersion
			}
			if packer != "" {
				body := "#!/bin/sh\nif test \"$1\" = version; then printf '%s\\n' '" + packer + "'; fi\n"
				if err := os.WriteFile(filepath.Join(bin, "packer"), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cfg := BuildConfig{Repository: repo, AttemptPrefix: "first"}
			err := prepareDefaultMacBuild(t.Context(), Runner{Env: env}, &cfg)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability || !strings.Contains(err.Error(), want) {
				t.Fatalf("prerequisite error=%v", err)
			}
			if _, err := os.Lstat(filepath.Join(repo, ".nvkr")); !os.IsNotExist(err) {
				t.Fatal("missing prerequisite created customer state")
			}
		})
	}
}

func macBuildProbeFixtures(t *testing.T, repo, bin string) ([]string, string, string) {
	t.Helper()
	catalogDirectory := filepath.Join(repo, "inputs", "catalogs", "tools", "native")
	if err := os.MkdirAll(filepath.Dir(catalogDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(catalogDirectory, os.DirFS(filepath.Join("..", "..", "inputs", "catalogs", "tools", "native"))); err != nil {
		t.Fatal(err)
	}
	loaded, err := catalog.LoadContext(t.Context(), catalog.Dir(catalogDirectory))
	if err != nil {
		t.Fatal(err)
	}
	pin, err := os.ReadFile("../../.go-version")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".go-version"), pin, 0600); err != nil {
		t.Fatal(err)
	}
	for name, program := range map[string]string{
		"go": `test "$*" = 'env GOVERSION'
test "$GOTOOLCHAIN:$GOENV:$GOWORK" = local:off:off
test "${PROBE_FAIL:-}" != go
printf '%s\n' "$TEST_GO_VERSION"`,
		"docker": `case "$*" in
 'buildx version') test "${PROBE_FAIL:-}" != buildx; printf '%s\n' buildx;;
 'version --format {{.Server.Version}}') test "${PROBE_FAIL:-}" != daemon; printf '%s\n' server;;
 *) exit 29;;
esac`,
		"clang": `test "$*" = --version
test "${PROBE_FAIL:-}" != clang
printf '%s\n' clang`,
	} {
		body := "#!/bin/sh\nset -eu\nif test -n \"${CALLS:-}\"; then printf '%s\\n' 'probe:" + name + ":'\"$*\" >>\"$CALLS\"; fi\n" + program + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"PATH=" + bin, "TEST_GO_VERSION=go" + strings.TrimSpace(string(pin))},
		strings.TrimPrefix(loaded.Components["packer"].Tag, "v"), filepath.Base(loaded.Tools["packer-plugin-qemu"].ExecutablePath)
}

func TestDefaultMacPreparationRejectsBrokenSDKAndRuntimeBeforeWork(t *testing.T) {
	for _, tc := range []struct{ name, absent, fail string }{
		{name: "missing Go", absent: "go"},
		{name: "broken Go", fail: "go"},
		{name: "wrong Go SDK", fail: "version"},
		{name: "missing Docker", absent: "docker"},
		{name: "unusable Buildx", fail: "buildx"},
		{name: "unreachable daemon", fail: "daemon"},
		{name: "missing Clang", absent: "clang"},
		{name: "unusable Clang", fail: "clang"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, bin := t.TempDir(), t.TempDir()
			env, _, _ := macBuildProbeFixtures(t, repo, bin)
			if tc.absent != "" {
				if err := os.Remove(filepath.Join(bin, tc.absent)); err != nil {
					t.Fatal(err)
				}
			}
			calls := filepath.Join(repo, "calls")
			env = append(env, "PROBE_FAIL="+tc.fail, "CALLS="+calls)
			if tc.fail == "version" {
				env = SetEnv(env, map[string]string{"TEST_GO_VERSION": "go0.0.0"})
			}
			cfg := BuildConfig{Repository: repo, AttemptPrefix: "first", NVKR: "/forbidden/source-preparation"}
			err := prepareDefaultMacBuild(t.Context(), Runner{Env: env}, &cfg)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("prerequisite failure=%v", err)
			}
			if _, err := os.Lstat(filepath.Join(repo, ".nvkr")); !os.IsNotExist(err) {
				t.Fatal("broken SDK/runtime created attempt state")
			}
			if raw, err := os.ReadFile(calls); err == nil && strings.Contains(string(raw), "nvkr:") {
				t.Fatal("broken prerequisite reached source preparation")
			}
		})
	}
}
