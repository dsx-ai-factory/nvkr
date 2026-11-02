// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
)

// Records every BuildKit invocation as NUL-terminated arguments with an empty
// argument between invocations. Base stages write the metadata the test supplies.
const nicoCoreBuildKitStub = `#!/bin/sh
set -eu
printf '%s\0' "$@" >> "$BUILDKIT_ARGS"
printf '\0' >> "$BUILDKIT_ARGS"
file= meta=
while [ $# -gt 0 ]; do
  case "$1" in
    --opt) case "$2" in filename=*) file=${2#filename=} ;; esac; shift ;;
    --metadata-file) meta=$2; shift ;;
  esac
  shift
done
case "$file" in
  Dockerfile.build-*) metadata=$STUB_BUILD ;;
  Dockerfile.runtime-*) metadata=$STUB_RUNTIME ;;
  *) exit 0 ;;
esac
[ "$metadata" != fail ] || exit 7
printf '%s\n' "$metadata" > "$meta"
`

// Pretty-printed like BuildKit's metadata file; the config digest must not be selected.
func nicoCoreMetadata(digests ...string) string {
	lines := []string{`  "containerimage.config.digest": "sha256:` + strings.Repeat("c", 64) + `"`}
	for _, digest := range digests {
		lines = append(lines, `  "containerimage.digest": "`+digest+`"`)
	}
	return "{\n" + strings.Join(lines, ",\n") + "\n}"
}

// The producer receives its rendered versionStamp as environment; this mirrors
// that handoff and requires the stamps to follow the locked pin.
func nicoStampEnv(t *testing.T, cat *catalog.Catalog, component string, want map[string]string) []string {
	t.Helper()
	stamps := cat.Components[component].VersionStamp
	if !maps.Equal(stamps, want) {
		t.Fatalf("%s versionStamp = %v, want %v", component, stamps, want)
	}
	env := make([]string, 0, len(stamps))
	for name, value := range stamps {
		env = append(env, name+"="+value)
	}
	slices.Sort(env)
	return env
}

// Execute the authored chained Core command; BuildKit itself is an external gate.
func TestNICoCoreImageChainsNativeBases(t *testing.T) {
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "..", "inputs/catalogs/nico")))
	if err != nil {
		t.Fatal(err)
	}
	command := cat.Components["nico-core-image"].BuildCommand
	if len(command) != 3 || command[0] != "sh" {
		t.Fatalf("unexpected Core build command shape %q", command)
	}
	pin, err := cat.SourcePin("nico")
	if err != nil {
		t.Fatal(err)
	}
	date := catalog.EpochDate(pin.SourceDateEpoch)
	stamps := nicoStampEnv(t, cat, "nico-core-image", map[string]string{"NICO_VERSION": pin.Tag, "NICO_COMMIT": pin.Commit, "NICO_DATE": date})
	const keaVersion = "2.2.0-6"
	const digestDiagnostic = "base metadata must name exactly one sha256 image digest"
	buildDigest, runtimeDigest := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	for _, arch := range []struct{ name, suffix, arg string }{
		{"amd64", "x86_64", "X86_64"},
		{"arm64", "aarch64", "AARCH64"},
	} {
		for _, tc := range []struct {
			name, build, runtime string
			fails                bool
			// A base command failure stops before digest selection, so only
			// metadata failures report the digest diagnostic.
			diagnostic string
			// Non-nil replaces the rendered stamps; any stamp failure must stop before BuildKit.
			stampEnv []string
			// Non-empty replaces the source's valid kea.version content; "-" leaves the file absent.
			// Either Kea failure must stop before BuildKit.
			kea string
		}{
			{"complete", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), false, "", nil, ""},
			{"missing stamps", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), true, "missing NICo version stamp", []string{}, ""},
			{"missing version stamp", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), true, "missing NICo version stamp", []string{"NICO_COMMIT=" + pin.Commit, "NICO_DATE=" + date}, ""},
			{"empty version stamp", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), true, "missing NICo version stamp", []string{"NICO_COMMIT=" + pin.Commit, "NICO_DATE=" + date, "NICO_VERSION="}, ""},
			{"missing commit stamp", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), true, "missing NICo commit stamp", []string{"NICO_DATE=" + date, "NICO_VERSION=" + pin.Tag}, ""},
			{"empty commit stamp", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), true, "missing NICo commit stamp", []string{"NICO_COMMIT=", "NICO_DATE=" + date, "NICO_VERSION=" + pin.Tag}, ""},
			{"missing date stamp", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), true, "missing NICo date stamp", []string{"NICO_COMMIT=" + pin.Commit, "NICO_VERSION=" + pin.Tag}, ""},
			{"empty date stamp", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), true, "missing NICo date stamp", []string{"NICO_COMMIT=" + pin.Commit, "NICO_DATE=", "NICO_VERSION=" + pin.Tag}, ""},
			{"missing Kea version", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), true, "", nil, "-"},
			{"empty Kea version", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), true, "kea.version must name a Kea package version", nil, "\n"},
			{"failed build base", "fail", nicoCoreMetadata(runtimeDigest), true, "", nil, ""},
			{"failed runtime base", nicoCoreMetadata(buildDigest), "fail", true, "", nil, ""},
			{"missing digest", nicoCoreMetadata(buildDigest), nicoCoreMetadata(), true, "NICo Core runtime " + digestDiagnostic, nil, ""},
			{"duplicate digest", nicoCoreMetadata(buildDigest, buildDigest), nicoCoreMetadata(runtimeDigest), true, "NICo Core build " + digestDiagnostic, nil, ""},
			{"short digest", nicoCoreMetadata(buildDigest), nicoCoreMetadata("sha256:bbbb"), true, "NICo Core runtime " + digestDiagnostic, nil, ""},
		} {
			t.Run(arch.name+"/"+tc.name, func(t *testing.T) {
				fixture, stub := t.TempDir(), t.TempDir()
				argsFile := filepath.Join(stub, "args")
				if err := os.WriteFile(filepath.Join(stub, "buildctl-daemonless.sh"), []byte(nicoCoreBuildKitStub), 0o755); err != nil {
					t.Fatal(err)
				}
				if tc.kea != "-" {
					kea := tc.kea
					if kea == "" {
						kea = keaVersion + "\n"
					}
					if err := os.MkdirAll(filepath.Join(fixture, "dev", "docker"), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(fixture, "dev", "docker", "kea.version"), []byte(kea), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				script := strings.ReplaceAll(strings.ReplaceAll(command[2], "/src", fixture), "{{.Arch}}", arch.name)
				cmd := exec.CommandContext(ctx, command[0], command[1], script)
				cmd.Dir, cmd.WaitDelay = fixture, time.Second
				cmd.Env = []string{
					"PATH=" + stub + string(os.PathListSeparator) + os.Getenv("PATH"),
					"TMPDIR=" + t.TempDir(),
					"BUILDKIT_ARGS=" + argsFile,
					"SOURCE_DATE_EPOCH=1791331916",
					"STUB_BUILD=" + tc.build,
					"STUB_RUNTIME=" + tc.runtime,
				}
				if tc.stampEnv == nil {
					cmd.Env = append(cmd.Env, stamps...)
				} else {
					cmd.Env = append(cmd.Env, tc.stampEnv...)
				}
				out, err := cmd.CombinedOutput()
				recorded, readErr := os.ReadFile(argsFile)
				if tc.stampEnv != nil || tc.kea != "" {
					if err == nil || !os.IsNotExist(readErr) || !strings.Contains(string(out), tc.diagnostic) {
						t.Fatalf("Core build without stamps or Kea version reached BuildKit or lacked %q (err=%v, args=%v):\n%s", tc.diagnostic, err, readErr, out)
					}
					return
				}
				if readErr != nil {
					t.Fatal(readErr)
				}
				var calls [][]string
				for _, call := range bytes.Split(bytes.TrimSuffix(recorded, []byte{0, 0}), []byte{0, 0}) {
					calls = append(calls, strings.Split(string(call), "\x00"))
				}
				if tc.fails {
					if err == nil {
						t.Fatalf("invalid base stage reached a successful Core build:\n%s", out)
					}
					if tc.diagnostic == "" && strings.Contains(string(out), digestDiagnostic) {
						t.Fatalf("base command failure reached digest selection:\n%s", out)
					}
					if !strings.Contains(string(out), tc.diagnostic) {
						t.Fatalf("Core build did not report %q:\n%s", tc.diagnostic, out)
					}
					for _, call := range calls {
						if slices.Contains(call, "filename=Dockerfile.release-container-"+arch.suffix) {
							t.Fatalf("release stage ran after an invalid base stage:\n%s", out)
						}
					}
					return
				}
				if err != nil {
					t.Fatalf("Core build failed: %v\n%s", err, out)
				}
				if len(calls) != 3 {
					t.Fatalf("BuildKit invocations = %d, want build, runtime and release", len(calls))
				}
				layouts := map[string]string{}
				for i, stage := range []string{"build", "runtime"} {
					call := calls[i]
					if !slices.Contains(call, "filename=Dockerfile."+stage+"-container-"+arch.suffix) || !slices.Contains(call, "platform=linux/"+arch.name) ||
						!slices.Contains(call, "build-arg:KEA_VERSION="+keaVersion) {
						t.Fatalf("%s base invocation does not select the native %s Dockerfile with the Kea version: %q", stage, arch.name, call)
					}
					for _, value := range call {
						if dest, ok := strings.CutPrefix(value, "type=oci,dest="); ok {
							layouts[stage] = strings.TrimSuffix(dest, ",tar=false")
						}
					}
					if layouts[stage] == "" {
						t.Fatalf("%s base invocation has no OCI layout output: %q", stage, call)
					}
				}
				release := calls[2]
				for _, want := range []string{
					"filename=Dockerfile.release-container-" + arch.suffix,
					"platform=linux/" + arch.name,
					"build=" + layouts["build"],
					"runtime=" + layouts["runtime"],
					"build-arg:CONTAINER_BUILD_" + arch.arg + "=nico-core-build",
					"context:nico-core-build=oci-layout://build@" + buildDigest,
					"build-arg:CONTAINER_RUNTIME_" + arch.arg + "=nico-core-runtime",
					"context:nico-core-runtime=oci-layout://runtime@" + runtimeDigest,
					// The upstream version helper stamps these; both must follow the locked pin.
					"build-arg:VERSION=" + pin.Tag,
					"build-arg:CI_COMMIT_SHORT_SHA=" + pin.Commit[:8],
					"build-arg:CI_COMMIT_TIMESTAMP=" + date,
					"type=oci,dest=" + fixture + "/bin/nico-core-" + arch.name + ".oci.tar,oci-mediatypes=true,rewrite-timestamp=true",
				} {
					if !slices.Contains(release, want) {
						t.Fatalf("release invocation lacks %q: %q", want, release)
					}
				}
			})
		}
	}
}

// Flow stamps only the pin-derived version; without it the command must not reach BuildKit.
func TestNICoFlowImageStampsPinnedVersion(t *testing.T) {
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "..", "inputs/catalogs/nico")))
	if err != nil {
		t.Fatal(err)
	}
	pin, err := cat.SourcePin("nico")
	if err != nil {
		t.Fatal(err)
	}
	stamps := nicoStampEnv(t, cat, "nico-flow-image", map[string]string{"NICO_VERSION": pin.Tag})
	command := cat.Components["nico-flow-image"].BuildCommand
	if len(command) != 3 || command[0] != "sh" {
		t.Fatalf("unexpected Flow build command shape %q", command)
	}
	for _, tc := range []struct {
		name     string
		stampEnv []string
	}{
		{"stamped", stamps},
		{"missing version stamp", nil},
		{"empty version stamp", []string{"NICO_VERSION="}},
	} {
		stamped := tc.name == "stamped"
		t.Run(tc.name, func(t *testing.T) {
			fixture, stub := t.TempDir(), t.TempDir()
			argsFile := filepath.Join(stub, "args")
			if err := os.WriteFile(filepath.Join(stub, "buildctl-daemonless.sh"), []byte(nicoCoreBuildKitStub), 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			script := strings.ReplaceAll(strings.ReplaceAll(command[2], "/src", fixture), "{{.Arch}}", "arm64")
			cmd := exec.CommandContext(ctx, command[0], command[1], script)
			cmd.Dir, cmd.WaitDelay = fixture, time.Second
			cmd.Env = []string{
				"PATH=" + stub + string(os.PathListSeparator) + os.Getenv("PATH"),
				"BUILDKIT_ARGS=" + argsFile,
				"SOURCE_DATE_EPOCH=1791331916",
			}
			cmd.Env = append(cmd.Env, tc.stampEnv...)
			out, err := cmd.CombinedOutput()
			recorded, readErr := os.ReadFile(argsFile)
			if !stamped {
				if err == nil || !os.IsNotExist(readErr) || !strings.Contains(string(out), "missing NICo version stamp") {
					t.Fatalf("unstamped Flow build reached BuildKit (err=%v, args=%v):\n%s", err, readErr, out)
				}
				return
			}
			if err != nil || readErr != nil {
				t.Fatalf("Flow build failed: %v, %v\n%s", err, readErr, out)
			}
			if args := strings.Split(string(recorded), "\x00"); !slices.Contains(args, "build-arg:VERSION="+pin.Tag) {
				t.Fatalf("Flow invocation lacks the pinned version: %q", args)
			}
		})
	}
}
