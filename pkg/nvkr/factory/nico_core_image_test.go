// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
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
	buildDigest, runtimeDigest := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	for _, arch := range []struct{ name, suffix, arg string }{
		{"amd64", "x86_64", "X86_64"},
		{"arm64", "aarch64", "AARCH64"},
	} {
		for _, tc := range []struct {
			name, build, runtime string
			fails                bool
		}{
			{"complete", nicoCoreMetadata(buildDigest), nicoCoreMetadata(runtimeDigest), false},
			{"failed build base", "fail", nicoCoreMetadata(runtimeDigest), true},
			{"failed runtime base", nicoCoreMetadata(buildDigest), "fail", true},
			{"missing digest", nicoCoreMetadata(buildDigest), nicoCoreMetadata(), true},
			{"duplicate digest", nicoCoreMetadata(buildDigest, buildDigest), nicoCoreMetadata(runtimeDigest), true},
			{"short digest", nicoCoreMetadata(buildDigest), nicoCoreMetadata("sha256:bbbb"), true},
		} {
			t.Run(arch.name+"/"+tc.name, func(t *testing.T) {
				fixture, stub := t.TempDir(), t.TempDir()
				argsFile := filepath.Join(stub, "args")
				if err := os.WriteFile(filepath.Join(stub, "buildctl-daemonless.sh"), []byte(nicoCoreBuildKitStub), 0o755); err != nil {
					t.Fatal(err)
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
				out, err := cmd.CombinedOutput()
				recorded, readErr := os.ReadFile(argsFile)
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
					if !slices.Contains(call, "filename=Dockerfile."+stage+"-container-"+arch.suffix) || !slices.Contains(call, "platform=linux/"+arch.name) {
						t.Fatalf("%s base invocation does not select the native %s Dockerfile: %q", stage, arch.name, call)
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
					// The upstream version helper stamps this revision; it must follow the locked pin.
					"build-arg:CI_COMMIT_SHORT_SHA=" + pin.Commit[:8],
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
