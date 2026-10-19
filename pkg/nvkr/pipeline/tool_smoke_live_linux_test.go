// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"golang.org/x/sys/unix"
)

// The test binary doubles as a controlled delivered ELF only at the fixed
// in-container payload path. It exercises a real local daemon and isolation,
// without claiming that a customer payload or hosted qualification was tested.
func init() {
	executable, err := os.Executable()
	if err != nil || executable != "/work/tool" {
		return
	}
	fail := func(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(99) }
	if os.Getuid() != 65534 || os.Getgid() != 65534 {
		fail("unexpected subject identity")
	}
	if _, err := os.Stat("/var/run/docker.sock"); !os.IsNotExist(err) {
		fail("subject received a daemon socket")
	}
	if !toolSmokeSameStrings(os.Environ(), append(toolSmokeEnvironment(), "PWD=/work")) && !toolSmokeSameStrings(os.Environ(), toolSmokeEnvironment()) {
		fail("subject received unexpected environment")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		fail("cannot observe subject privileges")
	}
	if !strings.Contains(string(status), "NoNewPrivs:\t1") || !strings.Contains(string(status), "CapEff:\t0000000000000000") {
		fail("subject retained privileges")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		fail("cannot observe subject network")
	}
	for _, iface := range interfaces {
		if iface.Name == "lo" {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil || iface.Flags&net.FlagUp != 0 || len(addresses) != 0 {
			fail("subject retained an active or addressed network interface")
		}
	}
	var filesystem unix.Statfs_t
	if err := unix.Statfs("/", &filesystem); err != nil || filesystem.Flags&unix.ST_RDONLY == 0 {
		fail("subject root mount was not read-only")
	}
	if err := os.WriteFile("/nvkr-smoke-forbidden", []byte("forbidden"), 0o600); err == nil {
		fail("subject root filesystem was writable")
	}
	if err := os.WriteFile("/work/scratch", []byte("private"), 0o600); err != nil {
		fail("subject private tmpfs was not writable")
	}
	switch {
	case slices.Equal(os.Args[1:], []string{"version", "--output", "json"}):
		fmt.Printf("{\"buildVersion\":\"v0.0.0-fixture\",\"gitCommit\":\"fixture\",\"platform\":%q}\n", "linux/"+runtime.GOARCH)
	case slices.Equal(os.Args[1:], []string{"version", "--template", "{{.Version}}"}):
		fmt.Print("v0.0.0-fixture")
	case slices.Equal(os.Args[1:], []string{"version", "--client", "--output", "json"}):
		fmt.Printf("{\"clientVersion\":{\"gitVersion\":\"v0.0.0-fixture\",\"gitCommit\":\"fixture\",\"platform\":%q}}\n", "linux/"+runtime.GOARCH)
	case slices.Equal(os.Args[1:], []string{"version", "--output=json"}):
		fmt.Printf("{\"clusterctl\":{\"gitVersion\":\"v0.0.0-fixture\",\"platform\":%q}}\n", "linux/"+runtime.GOARCH)
	case slices.Equal(os.Args[1:], []string{"version", "-q"}):
		fmt.Println("0.0.0-fixture")
	case slices.Equal(os.Args[1:], []string{"version"}):
		fmt.Printf("Version: 0.0.0-fixture\nOS/Arch: %s\n", "linux/"+runtime.GOARCH)
	default:
		fail("subject received an unknown command")
	}
	_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"uid": os.Getuid(), "gid": os.Getgid(), "network": "none", "readOnlyRoot": true, "noNewPrivileges": true, "capabilities": "none", "socket": false})
	os.Exit(0)
}

func TestToolSmokeRealDocker(t *testing.T) {
	image := os.Getenv("NVKR_TEST_TOOL_SMOKE_IMAGE")
	docker := os.Getenv("NVKR_TEST_TOOL_SMOKE_DOCKER")
	if image == "" || docker == "" {
		t.Skip("requires explicit local pinned image and native Docker executable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	owner, err := OpenToolSmokeContainer(ctx, ToolSmokeSelection{RuntimeExecutable: docker, Image: image, Platform: "linux/" + runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	observedRuntime := &toolSmokeLiveObservedRuntime{packerContainerRuntime: owner.runtime}
	owner.runtime = observedRuntime
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	subject, err := heldfile.OpenContext(ctx, executable, heldfile.Options{Label: "live-smoke-test-ELF", MaxBytes: MaxToolSmokeExecutableBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	for _, profile := range []ToolSmokeProfile{ToolSmokeNVKECTL, ToolSmokeHelm, ToolSmokeKubectl, ToolSmokeKind, ToolSmokeClusterctl, ToolSmokeORAS, ToolSmokeNICoSim} {
		t.Run(string(profile), func(t *testing.T) {
			observation, err := owner.RunHeld(ctx, subject, profile)
			if err != nil {
				t.Logf("container observation: %s", observedRuntime.inspection.Bytes())
				t.Fatal(err)
			}
			if !strings.Contains(string(observation.Stdout()), "0.0.0-fixture") {
				t.Fatalf("unexpected fixture version: %q", observation.Stdout())
			}
			var facts struct {
				UID             int    `json:"uid"`
				GID             int    `json:"gid"`
				Network         string `json:"network"`
				ReadOnlyRoot    bool   `json:"readOnlyRoot"`
				NoNewPrivileges bool   `json:"noNewPrivileges"`
				Capabilities    string `json:"capabilities"`
				Socket          bool   `json:"socket"`
			}
			if err := json.Unmarshal(observation.Stderr(), &facts); err != nil {
				t.Fatal(err)
			}
			if facts.UID != 65534 || facts.GID != 65534 || facts.Network != "none" || !facts.ReadOnlyRoot || !facts.NoNewPrivileges || facts.Capabilities != "none" || facts.Socket {
				t.Fatalf("isolation facts: %+v", facts)
			}
			t.Logf("held fixture %s (%d bytes), image %s, platform %s, runtime %s", subject.Digest(), subject.Size(), observation.Image(), observation.Platform(), observation.RuntimeIdentity())
		})
	}
}

// Retain only the fixed test container's bounded inspection for failure diagnosis.
type toolSmokeLiveObservedRuntime struct {
	packerContainerRuntime
	inspection bytes.Buffer
}

func (r *toolSmokeLiveObservedRuntime) Run(ctx context.Context, in io.Reader, out, diagnostic io.Writer, args ...string) error {
	if len(args) > 1 && args[0] == "container" && args[1] == "inspect" {
		r.inspection.Reset()
		out = io.MultiWriter(out, &r.inspection)
	}
	return r.packerContainerRuntime.Run(ctx, in, out, diagnostic, args...)
}
