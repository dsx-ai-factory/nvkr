// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const factoryFixture = `package fixture
import("fmt";"os";"testing")
func TestExact(t *testing.T){t.Parallel();t.Run("child",func(t *testing.T){t.Parallel();if os.Getenv("NVKR_SHARD_FIXTURE_FAIL")=="1"{t.Fatal("injected shard failure")}})}
func TestExactSuffix(t *testing.T){}
func TestMatrix1(t *testing.T){}
func TestMatrix2(t *testing.T){}
func TestMatrix3(t *testing.T){}
func TestMatrix7(t *testing.T){}
func Example(){fmt.Println("example ran")
// Output: example ran
}
func FuzzDecode0(f *testing.F){f.Add("seed");f.Fuzz(func(t *testing.T,value string){if value!="seed"{t.Fatal("unexpected seed")}})}
`
const lifecycleFixture = `package fixture
import "testing"
func TestExact(t *testing.T){}
func TestParallel(t *testing.T){t.Parallel()}
`

func terminalTests(t *testing.T, log string) map[[3]string]int {
	t.Helper()
	rows, err := events(log)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[[3]string]int{}
	for _, e := range rows {
		if e.Test != nil && !strings.Contains(*e.Test, "/") && isTerminal(e.Action) {
			counts[[3]string{e.Package, *e.Test, e.Action}]++
		}
	}
	return counts
}

// All nineteen real shards must produce exactly the same top-level results as
// ordinary go test, including examples, fuzz seeds, parallel tests and subtests.
func TestRealMatrix(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "shard")
	var diagnostics, stderr strings.Builder
	// Nested Go commands share a runner with the outer package shard.
	buildEnv := append(os.Environ(), "GOMAXPROCS=1", "GOFLAGS=-p=1")
	if _, err := captured(t.Context(), []string{"go", "build", "-o", binary, "."}, "", buildEnv, 90*time.Second, false, 32*1024*1024, &diagnostics, &stderr); err != nil {
		t.Fatalf("build: %s\n%s\n%v", diagnostics.String(), stderr.String(), err)
	}
	env := append(buildEnv, "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "NVKR_SHARD_FIXTURE_FAIL=0")
	version, err := os.ReadFile("../../.go-version")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/shardfixture\n\ngo "+strings.TrimSpace(string(version))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"pkg/nvkr/factory":       factoryFixture,
		"pkg/nvkr/lifecycle":     lifecycleFixture,
		"pkg/nvkr/qualification": lifecycleFixture,
		"cmd/nvkr":               lifecycleFixture,
	}
	for _, index := range []int{0, 1, 2, 3, 23} {
		fixtures["ordinary/package"+strconv.Itoa(index)] = "package fixture\nimport \"testing\"\nfunc TestExact(t *testing.T){}\n"
	}
	for pkg, source := range fixtures {
		dir := filepath.Join(root, pkg)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "contract_test.go"), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	execute := func(argv, environment []string) (string, error) {
		t.Helper()
		diagnostics.Reset()
		stderr.Reset()
		log, err := captured(t.Context(), argv, root, environment, 90*time.Second, false, 32*1024*1024, &diagnostics, &stderr)
		if err != nil {
			t.Logf("subprocess stderr: %s", stderr.String())
		}
		return log, err
	}
	baseline, err := execute([]string{"go", "test", "-json", "-count=1", "-timeout=10s", "./..."}, env)
	if err != nil {
		t.Fatalf("baseline %s %v", diagnostics.String(), err)
	}
	expected := terminalTests(t, baseline)
	if len(expected) != 19 {
		t.Fatalf("expected 19 targets: %v", expected)
	}
	for key, n := range expected {
		if key[2] != "pass" || n != 1 {
			t.Fatal(expected)
		}
	}
	for _, name := range []string{`"Test":"TestExact/child"`, `"Test":"FuzzDecode0/seed#0"`} {
		if !strings.Contains(baseline, name) {
			t.Fatalf("missing %s", name)
		}
	}
	actual := map[[3]string]int{}
	var failureCommand []string
	for _, g := range groups {
		for index := 0; index < g.count; index++ {
			argv := []string{binary, "--group", g.name, "--index", strconv.Itoa(index), "--timeout=10s"}
			log, err := execute(argv, env)
			if err != nil {
				t.Fatalf("%v: %s %v", argv, diagnostics.String(), err)
			}
			completed := terminalTests(t, log)
			if len(completed) == 0 {
				t.Fatal("empty fixture shard")
			}
			for key, n := range completed {
				actual[key] += n
			}
			if completed[[3]string{"example.com/shardfixture/pkg/nvkr/factory", "TestExact", "pass"}] > 0 {
				failureCommand = argv
			}
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("sharded %v differs from unsharded %v", actual, expected)
	}
	if failureCommand == nil {
		t.Fatal("missing failure shard")
	}
	failed, err := execute(failureCommand, append(env, "NVKR_SHARD_FIXTURE_FAIL=1"))
	if err == nil {
		t.Fatal("injected failure passed")
	}
	want := map[[3]string]int{{"example.com/shardfixture/pkg/nvkr/factory", "TestExact", "fail"}: 1}
	if !reflect.DeepEqual(terminalTests(t, failed), want) || !strings.Contains(failed, "injected shard failure") {
		t.Fatalf("failure output: %s", failed)
	}
}
