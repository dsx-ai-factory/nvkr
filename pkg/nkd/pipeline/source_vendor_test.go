// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/sourcegit"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func sourceGitPrivateTestEnvironment(t *testing.T, repository, searchPath string) []string {
	t.Helper()
	baseline, err := sourcegit.Baseline(searchPath)
	if err != nil {
		t.Fatal(err)
	}
	fields := make(map[string]string)
	for _, pair := range baseline {
		key, value, _ := strings.Cut(pair, "=")
		fields[key] = value
	}
	for key, value := range map[string]string{
		"GIT_CONFIG_COUNT": "4", "GIT_CONFIG_KEY_0": "url.https://example.test/mirror.insteadOf", "GIT_CONFIG_VALUE_0": repository,
		"GIT_CONFIG_KEY_1": "credential.helper", "GIT_CONFIG_VALUE_1": "", "GIT_CONFIG_KEY_2": "credential.https://example.test/mirror.helper",
		"GIT_CONFIG_VALUE_2": sourcegit.CredentialHelper, "GIT_CONFIG_KEY_3": "credential.useHttpPath", "GIT_CONFIG_VALUE_3": "true",
		sourcegit.Token: "test-only-synthetic-source-token",
	} {
		fields[key] = value
	}
	result := make([]string, 0, len(fields))
	for key, value := range fields {
		result = append(result, key+"="+value)
	}
	return result
}

type sourceVendorTestRuntime struct {
	name        string
	image       string
	version     []byte
	identity    string
	result      []byte
	input       []byte
	commands    [][]string
	cleaned     [][]string
	beforeStart func() error
	cleanupErr  error
}

func (r *sourceVendorTestRuntime) Endpoint() string { return qualifiedRuntimeEndpoint }
func (r *sourceVendorTestRuntime) RuntimeName() string {
	if r.name == "" {
		return "docker"
	}
	return r.name
}
func (r *sourceVendorTestRuntime) AdmittedIdentity() (string, string) {
	return "sha256:" + strings.Repeat("a", 64), r.identity
}
func (r *sourceVendorTestRuntime) Run(ctx context.Context, stdin io.Reader, stdout, _ io.Writer, args ...string) error {
	r.commands = append(r.commands, append([]string(nil), args...))
	if err := ctx.Err(); err != nil {
		return err
	}
	switch args[0] {
	case "version":
		_, err := stdout.Write(r.version)
		return err
	case "info":
		_, err := io.WriteString(stdout, `"vendor-test-daemon"`)
		return err
	case "image":
		_, err := io.WriteString(stdout, r.image)
		return err
	case "volume":
		_, err := io.WriteString(stdout, args[len(args)-1])
		return err
	case "create":
		_, err := io.WriteString(stdout, "created-container")
		return err
	case "start":
		if r.beforeStart != nil {
			return r.beforeStart()
		}
	case "cp":
		if stdin != nil {
			var err error
			r.input, err = io.ReadAll(stdin)
			return err
		}
		_, err := stdout.Write(r.result)
		return err
	}
	return nil
}
func (r *sourceVendorTestRuntime) Cleanup(_ context.Context, _ time.Duration, _, _ io.Writer, args ...string) error {
	r.cleaned = append(r.cleaned, append([]string(nil), args...))
	return r.cleanupErr
}

func sourceVendorRuntimeFixture(t *testing.T, platform string) *sourceVendorTestRuntime {
	t.Helper()
	r := &sourceVendorTestRuntime{version: []byte(`{"Client":{"Version":"fixture"}}`)}
	var err error
	r.identity, err = runtimeIdentityFromOutput("sha256:"+strings.Repeat("a", 64), qualifiedRuntimeEndpoint,
		"vendor-test-daemon", r.version)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(platform, "/")
	r.image = fmt.Sprintf(`{"Id":%q,"Os":%q,"Architecture":%q,"Config":{}}`,
		"sha256:"+strings.Repeat("b", 64), parts[0], parts[1])
	return r
}

func TestSourceVendorArgumentsPreserveMaintainedEntrypointAndExcludeCredentials(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, network := range []string{ir.NetworkDefault, ir.NetworkNone} {
			t.Run(arch+"/"+network, func(t *testing.T) {
				vendor := &sourceVendor{runtime: &sourceVendorTestRuntime{}, image: "sha256:" + strings.Repeat("b", 64), platform: "linux/" + arch,
					network: network, command: []string{"dependency", "build", "controller/deploy/charts/controller"}}
				args := vendor.createArgs("container", "private-volume")
				if !slices.Equal(args[len(args)-3:], vendor.command) || args[len(args)-4] != vendor.image {
					t.Fatalf("maintained Helm command changed: %v", args)
				}
				for _, required := range []string{"--pull=never", "--network=" + network, "--read-only", "--cap-drop=ALL",
					"--security-opt=no-new-privileges", "--no-healthcheck", "type=volume,source=private-volume,destination=/src,volume-nocopy"} {
					if !slices.Contains(args, required) {
						t.Fatalf("missing containment %s: %v", required, args)
					}
				}
				if slices.Contains(args, "-v") {
					t.Fatalf("host bind argument appeared: %v", args)
				}
				for _, forbidden := range []string{"--entrypoint", "--privileged", "--env-file", "NKD_SOURCE_TOKEN", "GIT_CONFIG", "DOCKER_CONFIG", "SSH_AUTH_SOCK"} {
					if strings.Contains(strings.Join(args, " "), forbidden) {
						t.Fatalf("unexpected entrypoint, bind or credential %s: %v", forbidden, args)
					}
				}
			})
		}
	}
}

func TestSourceVendorRuntimeSpecificVolumeArguments(t *testing.T) {
	for _, test := range []struct{ name, flag, mount string }{
		{"docker", "--mount", "type=volume,source=private-volume,destination=/src,volume-nocopy"},
		{"podman", "--volume", "private-volume:/src:nocopy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			vendor := &sourceVendor{runtime: &sourceVendorTestRuntime{name: test.name}}
			args := vendor.createArgs("container", "private-volume")
			index := slices.Index(args, test.flag)
			if index < 0 || index+1 >= len(args) || args[index+1] != test.mount {
				t.Fatalf("wrong private volume syntax for %s: %v", test.name, args)
			}
			if test.name == "podman" && strings.Contains(strings.Join(args, " "), "volume-nocopy") {
				t.Fatalf("Docker-only mount option reached Podman: %v", args)
			}
		})
	}
}

func TestSourceVendorImageAdmission(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, kind := range []string{"valid", "platform", "identity", "volume", "duplicate JSON"} {
			t.Run(arch+"/"+kind, func(t *testing.T) {
				platform := "linux/" + arch
				r := sourceVendorRuntimeFixture(t, platform)
				switch kind {
				case "platform":
					r.image = strings.Replace(r.image, `"Os":"linux"`, `"Os":"windows"`, 1)
				case "identity":
					r.image = strings.Replace(r.image, "sha256:"+strings.Repeat("b", 64), "tag-not-identity", 1)
				case "volume":
					r.image = strings.Replace(r.image, `"Config":{}`, `"Config":{"Volumes":{"/outside":{}}}`, 1)
				case "duplicate JSON":
					r.image = strings.TrimSuffix(r.image, "}") + `,"Id":"duplicate"}`
				}
				got, err := sourceVendorImage(t.Context(), r, "selected-reference", platform)
				if kind == "valid" {
					if err != nil || got != "sha256:"+strings.Repeat("b", 64) {
						t.Fatalf("native image rejected: %s, %v", got, err)
					}
				} else if code(err) != e.CodeEngineCapability || got != "" {
					t.Fatalf("unsafe image accepted: %s, %v", got, err)
				}
				if len(r.commands) != 1 || r.commands[0][0] != "image" {
					t.Fatalf("image admission performed source/container work: %v", r.commands)
				}
				if !json.Valid([]byte(r.image)) && kind != "duplicate JSON" {
					t.Fatal("fixture did not reach its intended structured image branch")
				}
			})
		}
	}
}

func TestSourceVendorRejectsIncompleteAdmission(t *testing.T) {
	for _, ctx := range []context.Context{nil, t.Context()} {
		got, err := admitSourceVendor(ctx, ir.Node{}, "missing-runtime")
		if got != nil || err == nil {
			t.Fatalf("incomplete vendor admission passed: %v, %v", got, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := admitSourceVendor(ctx, ir.Node{}, "missing-runtime")
	if got != nil || code(err) != e.CodeTimeout {
		t.Fatalf("canceled admission passed: %v, %v", got, err)
	}
}

func TestSourceVendorAdmissionRejectsUnboundedNetworkAndRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("concrete source runtime admission requires Linux")
	}
	for _, test := range []struct {
		name, network, executable string
		bounded                   bool
	}{
		{"unbounded", ir.NetworkDefault, "docker", false},
		{"host network", "host", "docker", true},
		{"unselected runtime", ir.NetworkDefault, "/usr/bin/runc", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			ctx := t.Context()
			if test.bounded {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Minute)
				defer cancel()
			}
			vendor, err := admitSourceVendor(ctx, ir.Node{Network: test.network}, test.executable)
			if vendor != nil || code(err) != e.CodeEngineCapability {
				t.Fatalf("invalid vendor admission returned %v / %v", vendor, err)
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid vendor admission wrote outputs: %v / %v", entries, err)
			}
		})
	}
}

func TestSourceVendorConcreteFetchRejectsUnavailableRuntimeBeforeSourceWork(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("concrete held source runtime admission requires Linux")
	}
	t.Chdir(t.TempDir())
	workspace, err := HoldSourceWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	node := testFetchNode("fetch-source", "https://example.test/source", testSourceCommit, testSourceTree,
		"source", "registry.example/toolchain@sha256:"+strings.Repeat("c", 64), "")
	node.VendorCmd = []string{"dependency", "build", "controller/deploy/charts/controller"}
	node.VendorSpecDigest, err = ir.VendorSpecIdentity(node.VendorCmd, node.VendorImage)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	_, err = (Local{}).ExecNode(node, RunOptions{Ctx: ctx, SourceRoot: "src", SourceWorkspace: workspace,
		Runtime: "/nonexistent/docker", Launch: true, Stdout: io.Discard, Stderr: io.Discard})
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("unavailable vendor runtime admitted: %v", err)
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("runtime refusal performed source work: %v / %v", entries, err)
	}
}

func TestSourceVendorLimitWriterCannotUsePromotedBypass(t *testing.T) {
	var buffer bytes.Buffer
	writer := &packerContainerLimitWriter{writer: &buffer, remaining: 1}
	if _, err := io.WriteString(writer, "xx"); code(err) != e.CodeVerifyFailed || buffer.Len() != 0 {
		t.Fatalf("oversize vendor output accepted: bytes=%d, %v", buffer.Len(), err)
	}
}
