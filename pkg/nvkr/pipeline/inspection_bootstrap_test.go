// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestInspectionBootstrapSelectionPins(t *testing.T) {
	for _, variant := range []string{"exact", "missing interpreter", "missing program", "changed program", "legacy"} {
		t.Run(variant, func(t *testing.T) {
			selection := DiskInspectionSelection{BootstrapInterpreterDigest: FileDigest([]byte("interpreter")), BootstrapProgramDigest: InspectionBootstrapProgramDigest()}
			switch variant {
			case "missing interpreter":
				selection.BootstrapInterpreterDigest = ""
			case "missing program":
				selection.BootstrapProgramDigest = ""
			case "changed program":
				selection.BootstrapProgramDigest = FileDigest([]byte("changed"))
			case "legacy":
				selection = DiskInspectionSelection{}
			}
			err := validateInspectionBootstrap(selection)
			if variant == "exact" || variant == "legacy" {
				if err != nil {
					t.Fatal(err)
				}
			} else if code(err) != e.CodeUnpinnedInput {
				t.Fatalf("pin refusal=%v", err)
			}
		})
	}
}

// This tests the bootstrap protocol on the native kernel, separately from the
// container and static-producer contracts. /bin/cat is an image-independent
// protocol fixture; this test grants no static-helper or appliance evidence.
func TestInspectionBootstrapSealedExecution(t *testing.T) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("native Linux descriptor execution")
	}
	interpreter, err := exec.LookPath("perl")
	if err != nil {
		t.Skip("Perl is unavailable on this test host")
	}
	interpreterBytes, err := os.ReadFile(interpreter)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := os.ReadFile("/bin/cat")
	if err != nil {
		t.Fatal(err)
	}
	subject := []byte("subject suffix\x00\xff\n")
	for _, variant := range []string{"probe", "exact", "changed helper", "truncated helper", "changed interpreter", "wrong architecture", "wrong size"} {
		t.Run(variant, func(t *testing.T) {
			selection := DiskInspectionSelection{Platform: "linux/" + runtime.GOARCH, BootstrapInterpreterDigest: FileDigest(interpreterBytes),
				streamBinding: ProducedExecutableBinding{Digest: FileDigest(helper), Size: int64(len(helper))}}
			input := append(bytes.Clone(helper), subject...)
			wantExit := 22
			switch variant {
			case "probe":
				selection.bootstrapProbe, wantExit = true, 0
				input = nil
			case "exact":
				wantExit = 0
			case "changed helper":
				input[0] ^= 1
			case "truncated helper":
				input = input[:20]
			case "changed interpreter":
				selection.BootstrapInterpreterDigest = FileDigest([]byte("foreign"))
			case "wrong architecture":
				selection.Platform, wantExit = "linux/unsupported", 29
			case "wrong size":
				selection.streamBinding.Size++
			}
			args := inspectionBootstrapArgs(selection)
			if !selection.bootstrapProbe {
				args = append(args, "-")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, interpreter, args...)
			command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
			command.Stdin = bytes.NewReader(input)
			var output, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &output, &diagnostic
			err := ownedcmd.Run(ctx, command)
			if wantExit != 0 {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != wantExit || output.Len() != 0 {
					t.Fatalf("refusal exit=%v output=%q diagnostics=%s", err, output.Bytes(), diagnostic.Bytes())
				}
				return
			}
			want := subject
			if selection.bootstrapProbe {
				want = []byte("nvkr-sealed-exec-v1\n")
			}
			if err != nil || diagnostic.Len() != 0 || !bytes.Equal(output.Bytes(), want) {
				t.Fatalf("sealed execution error=%v output=%q diagnostics=%s", err, output.Bytes(), diagnostic.Bytes())
			}
		})
	}
}

func TestInspectionBootstrapContainerRejectsSubstitution(t *testing.T) {
	for _, variant := range []string{"exact", "command", "entrypoint", "host mount", "environment", "network", "root", "memory", "scratch"} {
		t.Run(variant, func(t *testing.T) {
			selection := testDiskInspectionSelection(t)
			selection.bootstrapCommand = "fixed\x00bootstrap"
			var observed toolSmokeInspection
			observed.Config.Image, observed.Config.User, observed.Config.OpenStdin = selection.Image, "65534:65534", true
			observed.Config.Env = []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "LC_ALL=C", "TZ=UTC", "PERL5OPT=", "PERL5LIB=", "PERLLIB="}
			observed.Config.Entrypoint, observed.Config.Cmd = []string{inspectionBootstrapInterpreter}, []string{"fixed", "bootstrap"}
			h := &observed.HostConfig
			init, pids := true, int64(diskInspectionPIDsLimit)
			h.Init, h.PidsLimit = &init, &pids
			h.NetworkMode, h.ReadonlyRootfs = "none", true
			h.CapDrop, h.SecurityOpt = []string{"ALL"}, []string{"no-new-privileges"}
			h.Memory, h.MemorySwap = int64(selection.Resources.MemoryMiB<<20), int64(selection.Resources.MemoryMiB<<20)
			h.CPUPeriod, h.CPUQuota, h.LogConfig.Type = 100000, int64(selection.Resources.CPUMillis*100), "none"
			h.Tmpfs = map[string]string{"/tmp": "rw,nosuid,nodev,size=" + strconv.FormatUint(selection.Resources.MemoryMiB, 10) + "m,mode=1777"}
			switch variant {
			case "command":
				observed.Config.Cmd[0] = "foreign"
			case "entrypoint":
				observed.Config.Entrypoint[0] = "/bin/sh"
			case "host mount":
				h.Binds = []string{"/host:/tmp"}
			case "environment":
				observed.Config.Env = append(observed.Config.Env, "PERL5OPT=-d")
			case "network":
				h.NetworkMode = "host"
			case "root":
				observed.Config.User = "0:0"
			case "memory":
				h.Memory++
			case "scratch":
				h.Tmpfs["/tmp"] = "rw"
			}
			err := verifyInspectionBootstrapContainer(selection, observed)
			if variant == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if code(err) != e.CodeVerifyFailed && code(err) != e.CodeEngineCapability {
				t.Fatalf("substitution refusal=%v", err)
			}
		})
	}
}
