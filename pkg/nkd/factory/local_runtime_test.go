package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Scripted process boundaries exercise orchestration and argument/environment
// contracts; these tests do not claim a real Go build or container execution.
func TestPrepareLocalRunnersBindsPlatformAndExactPlan(t *testing.T) {
	plan := githubTestPlan(t)
	root := t.TempDir()
	t.Chdir(root)
	compiler := filepath.Join(root, "compiler")
	script := `#!/bin/sh
[ "$1" = build ] && [ "$2" = -trimpath ] && [ "$3" = -ldflags ] && [ "$5" = -o ] && [ "$7" = ./cmd/nkdctl ] || exit 91
[ "$GOOS" = linux ] && [ "$CGO_ENABLED" = 0 ] || exit 92
printf '%s/%s/%s\n' "$GOOS" "$GOARCH" "$CGO_ENABLED" > "$6"
`
	if err := os.WriteFile(compiler, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/test", RunnerSource: root, Go: compiler})
	runners, err := prepareLocalRunners(t.Context(), plan, options, []string{"linux/amd64", "linux/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	for platform, path := range runners {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != platform+"/0\n" {
			t.Fatalf("runner %s = %q, %v", platform, raw, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o555 {
			t.Fatalf("runner protection: %v, %v", info, err)
		}
	}
	if len(runners) != 2 {
		t.Fatalf("runners = %v", runners)
	}
	raw, err := os.ReadFile(filepath.Join(options.Workspace, localPlanPath))
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatal("runner plan differs from exact compiled plan")
	}
	entries, err := os.ReadDir(filepath.Join(options.Workspace, ".runner"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("staging files leaked: %v", entries)
	}
}

func TestLocalFactoryContainerArgumentsAndCleanup(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "runtime")
	log := filepath.Join(root, "calls")
	t.Setenv("NKD_TEST_RUNTIME_LOG", log)
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\nprintf '%s\\n' \"CALL\" \"$@\" >> \"$NKD_TEST_RUNTIME_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	options := withLocalDefaults(LocalRunOptions{Workspace: filepath.Join(root, "workspace"), Runtime: runtime})
	node := Node{ID: "package/tools", Kind: NodePackage, Platform: "linux/amd64", Network: ir.NetworkNone, Container: "example.invalid/tool@sha256:" + strings.Repeat("a", 64)}
	err := executeLocalFactoryNode(t.Context(), nil, node, options, nil)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("missing runner launched runtime")
	}
	if err := executeLocalFactoryNode(t.Context(), nil, node, options, map[string]string{"linux/amd64": "runner"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"CALL\nrun\n--rm\n--name\n", "--network\nnone\n--platform\nlinux/amd64\n", "-v\n" + options.Workspace + ":/workspace/.nkd/factory\n", "-w\n/workspace\n", node.Container + "\n", "/workspace/.nkd/factory/.runner/nkd-exec-amd64\nfactory\nexecute-node\n", "--plan\n/workspace/.nkd/factory/.runner/plan.json\n--node\npackage/tools\n", "CALL\nrm\n-f\nnkd-factory-"} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("missing runtime contract %q in %s", expected, raw)
		}
	}
}

func TestLocalRunnerAdmissionRejectsUnavailableCapabilities(t *testing.T) {
	plan := githubTestPlan(t)
	root := t.TempDir()
	runtime := filepath.Join(root, "runtime")
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, runtime, compiler, source string }{
		{"missing runtime", filepath.Join(root, "missing"), runtime, root},
		{"missing compiler", runtime, filepath.Join(root, "missing"), root},
		{"missing source", runtime, runtime, root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := withLocalDefaults(LocalRunOptions{Runtime: tc.runtime, Go: tc.compiler, RunnerSource: tc.source})
			assertFactoryErrorCode(t, admitLocalRunners(context.Background(), plan, options, []string{"linux/amd64"}), e.CodeEngineCapability)
		})
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := admitLocalRunners(t.Context(), plan, withLocalDefaults(LocalRunOptions{Runtime: runtime, Go: runtime, RunnerSource: root}), []string{"linux/amd64"}); err != nil {
		t.Fatal(err)
	}
}
