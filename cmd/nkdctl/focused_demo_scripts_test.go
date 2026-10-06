package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaintainedDemoScriptsAdvertiseRunnableHonestJourneys(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{"demo-catalog-change.sh", []string{"local Git", "preview bump", "UNQUALIFIED"}},
		{"demo-tamper-detection.sh", []string{"changed artifact digests", "escaping symlinks", "UNQUALIFIED"}},
		{"demo-release-consumer.sh", []string{"digest-pinned lifecycle publication", "Verification Policy", "Release capability"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := exec.Command("bash", demoScript(t, test.name), "--help").CombinedOutput()
			if err != nil {
				t.Fatalf("--help failed: %v\n%s", err, output)
			}
			for _, want := range test.want {
				if !strings.Contains(string(output), want) {
					t.Errorf("help is missing %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestReleaseConsumerRequiresOneReferenceAndExplicitPolicy(t *testing.T) {
	raw, err := os.ReadFile(demoScript(t, "demo-release-consumer.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{"release verify", "--reference", "--lane pipeline-attested", "--scope artifact-publication", "--identity", "--issuer", "--cosign-digest", "--definition", "--environment", "--plan"} {
		if !strings.Contains(script, required) {
			t.Errorf("consumer omits %q", required)
		}
	}
	for _, legacy := range []string{"publish pull", "publish extract", "ReleaseSet", "ClusterDefinition"} {
		if strings.Contains(script, legacy) {
			t.Errorf("consumer retains legacy workflow %q", legacy)
		}
	}
}

func TestReleaseConsumerRejectsUnsafeInputsBeforeExecution(t *testing.T) {
	script := demoScript(t, "demo-release-consumer.sh")
	validDigest := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"path escape", []string{"--work", ".nkd/../outside", "--cosign-digest", validDigest}, "unsafe path component"},
		{"nonhex digest", []string{"--work", ".nkd/release-input-test", "--cosign-digest", "sha256:" + strings.Repeat("g", 64)}, "64 lowercase hex"},
		{"partial exact inputs", []string{"--work", ".nkd/release-input-test", "--cosign-digest", validDigest, "--definition", "definition.yaml"}, "must be supplied together"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"--reference", "registry.test/release@" + validDigest, "--identity", "identity", "--issuer", "issuer", "--cosign", "/bin/true"}
			args = append(args, test.args...)
			cmd := exec.Command("bash", append([]string{script}, args...)...)
			cmd.Env = append(os.Environ(), "NKDCTL_BIN=/bin/true")
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("unsafe input was not rejected before execution: %v\n%s", err, output)
			}
		})
	}
}

func TestDemoWorkspaceCanBeRerunOnlyByItsOwner(t *testing.T) {
	common, err := filepath.Abs(filepath.Join("..", "..", "scripts", "_demo-common.sh"))
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$COMMON"
demo_prepare_work "$WORK" "test-owner"
printf stale > "$WORK/stale-output"
demo_prepare_work "$WORK" "test-owner"
test ! -e "$WORK/stale-output"
printf current > "$WORK/current-output"
`)
	cmd.Env = append(os.Environ(), "COMMON="+common, "WORK="+work)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("owned workspace did not support a clean rerun: %v\n%s", runErr, output)
	}
	cmd = exec.Command("bash", "-c", `
set -euo pipefail
source "$COMMON"
demo_prepare_work "$WORK" "different-owner"
`)
	cmd.Env = append(os.Environ(), "COMMON="+common, "WORK="+work)
	if output, runErr := cmd.CombinedOutput(); runErr == nil || !strings.Contains(string(output), "belongs to demo") {
		t.Fatalf("cross-demo workspace reuse was not rejected: %v\n%s", runErr, output)
	}
}

func demoScript(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}
