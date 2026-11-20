// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func swapRun(t *testing.T, input, name string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	// Bound pipe draining even if a descendant inherits the output descriptors.
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if e, ok := err.(*exec.ExitError); ok {
		return string(out), e.ExitCode()
	}
	t.Fatal(err)
	return "", -1
}

func swapWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
}

func swapRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMaintainedGeneration(t *testing.T) {
	source := swapRead(t, "generate-maintained.sh")
	for _, tc := range []struct {
		name, change string
		args         []string
		status       int
		want         string
	}{
		{"generate", "", nil, 0, "--root\n.\n"},
		{"stdin-reading generator", "stdin", []string{"--check"}, 0, "--check\n"},
		{"check runtime", "runtime", []string{"--check", "--projection-pilot", "in-process"}, 0, "--check\n--runtime\n"},
		{"pilot", "", []string{"--check", "--projection-pilot", "in-process"}, 0, "--check\n--projection-pilot\nin-process\n"},
		{"policy", "policy", nil, 0, "--qualification-policy\npolicy.yaml\n"},
		{"filter", "", []string{"--group", "nightly"}, 0, ""},
		{"equals options", "", []string{"--group=main", "--projection-pilot=in-process"}, 0, "--projection-pilot\nin-process\n"},
		{"unknown option", "", []string{"--nope"}, 2, ""},
		{"unknown group", "", []string{"--group", "split"}, 2, ""},
		{"missing option value", "", []string{"--group"}, 2, ""},
		{"empty", "empty", nil, 1, ""},
		{"multiple documents", "multiple documents", nil, 1, ""},
		{"bundle newline", "bundle newline", nil, 1, ""},
		{"duplicate", "duplicate", nil, 1, ""},
		{"unsorted", "unsorted", nil, 1, ""},
		{"unknown field", "unknown", nil, 1, ""},
		{"unknown inventory group", "group", nil, 1, ""},
		{"invalid runtime", "runtime string", nil, 1, ""},
		{"missing input", "missing", nil, 1, ""},
		{"invalid input type", "null", nil, 1, ""},
		{"traversal", "traversal", nil, 1, ""},
		{"absolute", "absolute", nil, 1, ""},
		{"validate unselected group", "missing", []string{"--group", "nightly"}, 1, ""},
		{"later invalid selection", "later missing", nil, 1, ""},
		{"child error", "child error", nil, 1, "--root\n.\n"},
		{"timeout", "timeout", nil, 1, ""},
		{"whitespace path", "whitespace", nil, 0, "--definition\na b\n.yaml\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			entry := map[string]any{"bundle": "nvkr-run", "group": "main", "definition": "definition.yaml", "environment": "environment.yaml"}
			entries := []map[string]any{entry}
			switch tc.change {
			case "stdin":
				for _, bundle := range []string{"nvkr-two", "nvkr-z"} {
					entries = append(entries, map[string]any{"bundle": bundle, "group": "main", "definition": "definition.yaml", "environment": "environment.yaml"})
				}
			case "runtime":
				entry["runtime"] = true
			case "bundle newline":
				entry["bundle"] = "nvkr-run\n"
			case "policy":
				entry["qualificationPolicy"] = "policy.yaml"
			case "empty":
				entries = []map[string]any{}
			case "duplicate":
				entries = append(entries, entry)
			case "unsorted":
				entries = append(entries, map[string]any{"bundle": "nvkr-a", "group": "main", "definition": "definition.yaml", "environment": "environment.yaml"})
			case "unknown":
				entry["unknown"] = true
			case "group":
				entry["group"] = "typo"
			case "runtime string":
				entry["runtime"] = "false"
			case "missing":
				entry["definition"] = "missing.yaml"
			case "null":
				entry["definition"] = nil
			case "traversal":
				entry["definition"] = "../definition.yaml"
			case "absolute":
				entry["definition"] = "/definition.yaml"
			case "later missing":
				entries = append(entries, map[string]any{"bundle": "nvkr-z", "group": "main", "definition": "missing.yaml", "environment": "environment.yaml"})
			case "whitespace":
				entry["definition"] = "a b\n.yaml"
			}
			raw, err := json.Marshal(entries)
			if err != nil {
				t.Fatal(err)
			}
			if tc.change == "multiple documents" {
				raw = append(raw, raw...)
			}
			swapWrite(t, filepath.Join(root, "inputs/policies/maintained-generation.json"), string(raw))
			swapWrite(t, filepath.Join(root, "scripts/generate-maintained.sh"), source)
			for _, p := range []string{"definition.yaml", "environment.yaml", "policy.yaml", "a b\n.yaml"} {
				swapWrite(t, filepath.Join(root, p), "fixture")
			}
			stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> calls\n"
			if tc.change == "stdin" {
				stub += "cat >/dev/null\n"
			}
			if tc.change == "child error" {
				stub += "exit 17\n"
			}
			swapWrite(t, filepath.Join(root, "bin/nvkr-exec"), stub)
			if tc.change == "timeout" {
				swapWrite(t, filepath.Join(root, "bin/timeout"), "#!/bin/sh\nexit 124\n")
				t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			args := append([]string{filepath.Join(root, "scripts/generate-maintained.sh")}, tc.args...)
			out, status := swapRun(t, "", "bash", args...)
			if status != tc.status {
				t.Fatalf("exit %d, want %d: %s", status, tc.status, out)
			}
			if status == 1 && !strings.Contains(out, "NVKR-E-PIPELINE-DRIFT") {
				t.Fatalf("missing coded error: %s", out)
			}
			if tc.change == "timeout" && !strings.Contains(out, "bundle nvkr-run, exit status 124") {
				t.Fatalf("missing timeout context: %s", out)
			}
			calls, _ := os.ReadFile(filepath.Join(root, "calls"))
			if tc.change == "stdin" && strings.Count(string(calls), "--check\n") != 3 {
				t.Fatalf("stdin-reading child skipped bundles: %s", calls)
			}
			if tc.want == "" && len(calls) != 0 {
				t.Fatalf("unexpected generator call: %s", calls)
			}
			if tc.want != "" && !strings.Contains(string(calls), tc.want) {
				t.Fatalf("calls %q lack %q", calls, tc.want)
			}
			if tc.change == "runtime" && strings.Contains(string(calls), "--projection-pilot") {
				t.Fatal("runtime selection received projection pilot")
			}
		})
	}
}

func TestCITestSummary(t *testing.T) {
	t.Run("escaping and decimals", func(t *testing.T) {
		input := `{"Action":"pass","Package":"p'\"&<>|\r\n","Elapsed":1.234}`
		out, status := swapRun(t, input, "jq", "-nRr", "-f", "ci-test-summary.jq")
		if status != 0 || !strings.Contains(out, "| 1.234 | pass | <code>p&#x27;&quot;&amp;&lt;&gt;&#124;  </code> |") {
			t.Fatalf("incorrect escaped row (%d): %s", status, out)
		}
	})
	for _, action := range []string{"pass", "fail", "skip"} {
		t.Run(action, func(t *testing.T) {
			input := "go test -json\n" + `{"Action":"` + action + `","Package":"p","Test":"Test<unsafe>|/child","Elapsed":2}` + "\n" +
				`{"Action":"` + action + `","Package":"p","Elapsed":3}` + "\n" + `{"Action":"pass","Package":"slow","Elapsed":10}` + "\ntotal: 80%\n"
			out, status := swapRun(t, input, "jq", "-nRr", "-f", "ci-test-summary.jq")
			failures := "0"
			if action == "fail" {
				failures = "1"
			}
			if status != 0 || !strings.Contains(out, "Test&lt;unsafe&gt;&#124;/child") ||
				!strings.Contains(out, "| 2.000 |") || !strings.Contains(out, "failed packages: "+failures) ||
				strings.Index(out, "<code>slow</code>") > strings.Index(out, "<code>p</code>") {
				t.Fatalf("bad summary (%d): %s", status, out)
			}
		})
	}
	for _, input := range []string{"", "make: build failed\n", `{"Action":`, `{"Action":"pass","Test":"orphan"}`, `{"Action":"pass","Package":"p","Elapsed":null}`} {
		if out, status := swapRun(t, input, "jq", "-nRr", "-f", "ci-test-summary.jq"); status == 0 {
			t.Fatalf("accepted %q: %s", input, out)
		}
	}
}

func TestDemoShellTools(t *testing.T) {
	t.Run("registry config admission", func(t *testing.T) {
		body := swapRead(t, "demo-release-consumer.sh")
		filter := regexp.MustCompile(`(?s)jq -se '(.*?)' "\$REGISTRY_CONFIG/config.json"`).FindStringSubmatch(body)
		if len(filter) != 2 {
			t.Fatal("missing registry admission filter")
		}
		for _, tc := range []struct {
			input string
			valid bool
		}{
			{`{}`, true}, {`{"auths":{"registry.invalid":{"auth":"c3ludGhldGljOmZpeHR1cmU="}}}`, true},
			{`{"credsStore":"","credHelpers":{}}`, true},
			{`null`, false}, {`{} {}`, false}, {`[]`, false}, {`"config"`, false}, {``, false},
			{`{"credsStore":"desktop"}`, false}, {`{"credHelpers":{"registry.invalid":"desktop"}}`, false},
		} {
			out, status := swapRun(t, tc.input, "jq", "-se", filter[1])
			if (status == 0) != tc.valid {
				t.Fatalf("config %s: status %d: %s", tc.input, status, out)
			}
		}
	})
	t.Run("demo summaries", func(t *testing.T) {
		fixture := `{"outputs":[{"name":"disk","format":"qcow2","platform":"linux/arm64","kind":"disk-image","os":"linux","arch":"arm64","digest":"sha256:fixture","components":["one"],"disk":{"payloadDigest":"sha256:payload","format":"qcow2","size":1073741824,"evidenceMode":"build-provenance-v1"}}],"totalOutputs":1,"rootDigest":"sha256:root","metadata":{"invocationId":"run"},"builds":[{"executionGraph":{"nodes":[{"component":"disk","outputType":"disk-image","packer":{},"disk":{"format":"qcow2"},"materials":[{"binding":"rootfs","component":"one"}],"requirements":{"resources":{"cpuMillis":4000,"memoryMiB":8192,"ephemeralStorageMiB":16384}}}]}}],"graph":{"nodes":[],"artifacts":[]},"qualification":"static-unqualified"}`
		pattern := regexp.MustCompile(`(?s)jq(?: -L "\$DEMO_DIR")? -r(?: --arg output "\$OUTPUT")? '(.*?)' "\$OUT/[^\"]+"`)
		for _, name := range []string{"catalog", "os", "release"} {
			matches := pattern.FindAllStringSubmatch(swapRead(t, "../demos/demo-team-"+name+".sh"), -1)
			if len(matches) != 2 {
				t.Fatalf("%s: expected two summary filters, got %d", name, len(matches))
			}
			for _, match := range matches {
				out, status := swapRun(t, fixture, "jq", "-L", "../demos", "-r", "--arg", "output", "disk", match[1])
				if status != 0 || out == "" {
					t.Fatalf("%s: summary failed (%d): %s", name, status, out)
				}
			}
		}
	})
	t.Run("release terminal escaping", func(t *testing.T) {
		out, status := swapRun(t, `"é\u202e\u007f\u001b😀"`, "jq", "-L", "../demos", "-r", `include "demo-json"; safe`)
		if status != 0 || out != "\\u00e9\\u202e\\u007f\\u001b\\ud83d\\ude00\n" {
			t.Fatalf("unsafe release text (%d): %q", status, out)
		}
	})
	t.Run("thumbprint", func(t *testing.T) {
		source := swapRead(t, "../demos/nvke/internal/nvkedemo/host_bootstrap.go")
		start := strings.Index(source, "thumbprint=$(get_tpm_pubhash)")
		if start < 0 {
			t.Fatal("missing thumbprint guard")
		}
		body := strings.SplitN(source[start:], "\ncurl ", 2)[0]
		for _, value := range []string{strings.Repeat("a", 64), strings.Repeat("F", 64), "", "123", strings.Repeat("z", 64), `"}; touch injected; #`} {
			dir := t.TempDir()
			cmd := exec.Command("bash", "-euc", "get_tpm_pubhash() { printf '%s' \"$1\"; }; "+strings.Replace(body, "$(get_tpm_pubhash)", "$(get_tpm_pubhash \"$1\")", 1), "fixture", value)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			valid := value == strings.Repeat("a", 64) || value == strings.Repeat("F", 64)
			if (err == nil) != valid {
				t.Fatalf("value %q: %v %s", value, err, out)
			}
			raw, readErr := os.ReadFile(filepath.Join(dir, "site-prepared.json"))
			if !valid && !os.IsNotExist(readErr) {
				t.Fatal("invalid thumbprint wrote output")
			}
			if valid && string(raw) != `{"tpmEkThumbprint": "`+value+`"}` {
				t.Fatalf("incorrect identity JSON %s", raw)
			}
		}
	})
	t.Run("uri", func(t *testing.T) {
		workflow := swapRead(t, "../.github/workflows/fern-docs-preview-comment.yml")
		prefix := "| jq -sRr '"
		start := strings.Index(workflow, prefix)
		if start < 0 {
			t.Fatal("missing URI filter")
		}
		filter := strings.SplitN(workflow[start+len(prefix):], "'", 2)[0]
		out, status := swapRun(t, "docs/a b.md,docs/é?#%.md", "jq", "-sRr", filter)
		if status != 0 || out != "docs/a%20b.md,docs/%C3%A9%3F%23%25.md\n" {
			t.Fatalf("URI result %d: %q", status, out)
		}
	})
}
