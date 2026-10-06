package factory

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func nativeUtilityNode(t *testing.T, plan *Plan) Node {
	t.Helper()
	for _, node := range plan.Graph.Nodes {
		if node.IRNode == "" && node.Container != "" {
			return node
		}
	}
	t.Fatal("fixture has no utility container")
	return Node{}
}

func TestUtilityPlatformAdmissionBeforeNodeEffects(t *testing.T) {
	plan := nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
	node := nativeUtilityNode(t, plan)
	for _, tc := range []struct {
		name, daemon, image, calls string
		code                       e.Code
	}{
		{"native", "amd64", "amd64", "info\npull\nimage\n", ""},
		{"foreign daemon", "arm64", "amd64", "info\n", e.CodeEngineCapability},
		{"foreign image", "amd64", "arm64", "info\npull\nimage\n", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			t.Chdir(work)
			tools := t.TempDir()
			log := filepath.Join(tools, "calls")
			runtimePath := filepath.Join(tools, "docker")
			script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + shellQuote(log) + "\ncase \"$1\" in\ninfo) printf '%s\\n' '{\"OSType\":\"linux\",\"Architecture\":\"" + tc.daemon + "\"}';;\npull) exit 0;;\nimage) printf '%s\\n' '{\"Os\":\"linux\",\"Architecture\":\"" + tc.image + "\",\"RepoDigests\":[\"" + node.Container + "\"]}';;\n*) exit 99;;\nesac\n"
			if err := os.WriteFile(runtimePath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			err := checkNodePlatform(t.Context(), plan, node.ID, "linux/amd64", func(ctx context.Context, runtime, image, platform string) error {
				if runtime != plan.Environment.RuntimeBinary() || image != node.Container || platform != node.Platform {
					t.Fatal("admission changed the selected utility")
				}
				return pipeline.CheckContainerPlatform(ctx, runtimePath, image, platform)
			})
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, tc.code)
			}
			calls, err := os.ReadFile(log)
			if err != nil || string(calls) != tc.calls {
				t.Fatalf("runtime calls: %q, %v", calls, err)
			}
			entries, err := os.ReadDir(work)
			if err != nil || len(entries) != 0 {
				t.Fatalf("preflight created node output: %v %v", entries, err)
			}
		})
	}
}

func TestNodePlatformRevalidatesPlanAndExactNode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*Plan, *string)
		observed string
		code     e.Code
	}{
		{"invalid full Plan", func(plan *Plan, _ *string) { plan.SchemaVersion = "other" }, "linux/amd64", e.CodeInvalidCatalog},
		{"missing selected node", func(_ *Plan, node *string) { *node = "absent" }, "linux/amd64", e.CodeInvalidCatalog},
		{"foreign helper", func(_ *Plan, _ *string) {}, "linux/arm64", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
			selected := nativeUtilityNode(t, plan).ID
			tc.mutate(plan, &selected)
			called := false
			err := checkNodePlatform(t.Context(), plan, selected, tc.observed, func(context.Context, string, string, string) error { called = true; return nil })
			assertFactoryErrorCode(t, err, tc.code)
			if called {
				t.Fatal("invalid selection reached runtime work")
			}
		})
	}
	assertFactoryErrorCode(t, CheckNodePlatform(t.Context(), nil, "node"), e.CodeUsage)
	assertFactoryErrorCode(t, CheckNodePlatform(nil, nil, "node"), e.CodeUsage) //nolint:staticcheck // Intentional nil-context admission test.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertFactoryErrorCode(t, CheckNodePlatform(ctx, nil, "node"), e.CodeTimeout)
}

func TestGitHubUtilityHostPreflightBeforeContainerLaunch(t *testing.T) {
	plan := nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
	// Select a utility platform foreign to this test host. Admission must reject
	// before looking for the on-disk Plan, launching a runtime, or creating output.
	foreign := "linux/arm64"
	if runtime.GOOS+"/"+runtime.GOARCH == foreign {
		foreign = "linux/amd64"
	}
	node := nativeUtilityNode(t, plan)
	node.Platform = foreign
	t.Chdir(t.TempDir())
	err := executeGitHubNode(t.Context(), plan, node, GitHubStepOptions{})
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if !strings.Contains(err.Error(), "native helper platform") {
		t.Fatalf("wrong rejection: %v", err)
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("foreign host created output: %v %v", entries, err)
	}
}
