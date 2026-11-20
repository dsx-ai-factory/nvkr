// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func eventLog(rows ...map[string]string) string {
	var out strings.Builder
	for _, row := range rows {
		raw, _ := json.Marshal(row)
		out.Write(raw)
		out.WriteByte('\n')
	}
	return out.String()
}
func TestPartition(t *testing.T) {
	names := []string{"Example_contract", "FuzzDecoder", "TestCase"}
	for i := 0; i < 1000; i++ {
		names = append(names, fmt.Sprintf("TestCase%d", i))
	}
	for _, count := range []int{1, 2, 4, 5, 8, 15, 34} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			seen := map[string]int{}
			for index := 0; index < count; index++ {
				got, err := partition(names, index, count)
				if err != nil {
					t.Fatal(err)
				}
				reversed := slices.Clone(names)
				slices.Reverse(reversed)
				reordered, err := partition(reversed, index, count)
				if err != nil || !slices.Equal(got, reordered) {
					t.Fatal("unstable order", err)
				}
				expanded, err := partition(append(slices.Clone(names), "TestFutureContract"), index, count)
				if err != nil {
					t.Fatal(err)
				}
				expanded = slices.DeleteFunc(expanded, func(s string) bool { return s == "TestFutureContract" })
				if !slices.Equal(got, expanded) {
					t.Fatal("adding a test moved existing assignments")
				}
				for _, name := range got {
					seen[name]++
				}
			}
			if len(seen) != len(names) {
				t.Fatal("missing tests")
			}
			for _, n := range seen {
				if n != 1 {
					t.Fatal("duplicate test")
				}
			}
		})
	}
	// These values were assigned by the original Python runner.
	for _, tc := range []struct {
		name  string
		index int
	}{{"TestExact", 4}, {"TestExactSuffix", 2}} {
		got, err := partition([]string{tc.name}, tc.index, 8)
		if err != nil || len(got) != 1 {
			t.Fatalf("assignment changed for %s: %v %v", tc.name, got, err)
		}
	}
}
func TestPackages(t *testing.T) {
	packages := []string{"m/pkg/nvkr/factory", "m/pkg/nvkr/lifecycle", "m/cmd/nvkr", "m/pkg/nvkr/qualification"}
	for i := 0; i < 100; i++ {
		packages = append(packages, fmt.Sprintf("m/new/package%d", i))
	}
	counts := map[string]int{}
	for _, g := range groups {
		for i := 0; i < g.count; i++ {
			got, err := packagesForGroup(packages, "m", g.name, i)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range got {
				counts[p]++
			}
		}
	}
	for _, p := range packages {
		want := 1
		if strings.HasSuffix(p, "/factory") {
			want = 34
		}
		if strings.HasSuffix(p, "/lifecycle") {
			want = 2
		}
		if p == "m/cmd/nvkr" || p == "m/pkg/nvkr/qualification" {
			want = 4
		}
		if counts[p] != want {
			t.Fatalf("%s count %d want %d", p, counts[p], want)
		}
	}
}
func TestInvalidSelections(t *testing.T) {
	for _, tc := range []struct {
		names        []string
		index, count int
	}{{[]string{"x"}, -1, 2}, {[]string{"x"}, 2, 2}, {[]string{"x"}, 0, 0}, {[]string{"x", "x"}, 0, 2}} {
		if _, err := partition(tc.names, tc.index, tc.count); err == nil {
			t.Fatal("accepted invalid partition")
		}
	}
	for _, tc := range []struct {
		packages []string
		group    string
		index    int
	}{{nil, "factory", 0}, {[]string{"m/pkg/nvkr/factory"}, "packages", 0}, {nil, "unknown", 0}, {[]string{"m/pkg/nvkr/factory", "m/pkg/nvkr/lifecycle", "m/pkg/nvkr/lifecycle"}, "packages", 0}} {
		if _, err := packagesForGroup(tc.packages, "m", tc.group, tc.index); err == nil {
			t.Fatal("accepted invalid group")
		}
	}
}
func TestDiscovery(t *testing.T) {
	names := []string{"TestExact", "TestExactSuffix", "Example_contract", "FuzzDecode"}
	rows := []map[string]string{}
	for _, name := range names {
		rows = append(rows, map[string]string{"Action": "output", "Package": "p", "Output": name + "\n"})
	}
	rows = append(rows, map[string]string{"Action": "output", "Package": "p", "Output": "BenchmarkDecode\n"}, map[string]string{"Action": "pass", "Package": "p"})
	got, err := listedTests(eventLog(rows...), "p")
	if err != nil || !slices.Equal(got, names) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"", "not-json", eventLog(rows[:len(rows)-1]...), eventLog(rows...) + eventLog(rows[0]), eventLog(rows...) + eventLog(map[string]string{"Action": "pass", "Package": "other"}), eventLog(rows[:len(rows)-1]...) + eventLog(map[string]string{"Action": "fail", "Package": "p"})} {
		if _, err := listedTests(bad, "p"); err == nil {
			t.Fatalf("accepted discovery %s", bad)
		}
	}
}
func TestResults(t *testing.T) {
	rows := []map[string]string{{"Action": "pass", "Package": "p", "Test": "TestA/child"}, {"Action": "pass", "Package": "p", "Test": "TestA"}, {"Action": "skip", "Package": "p", "Test": "FuzzB"}, {"Action": "pass", "Package": "p"}}
	log := eventLog(rows...)
	if err := validateResults(log, []string{"p"}, []string{"TestA", "FuzzB"}); err != nil {
		t.Fatal(err)
	}
	if err := validateResults(log+eventLog(map[string]string{"Action": "pass", "Package": "q", "Test": "TestA"}, map[string]string{"Action": "pass", "Package": "q"}), []string{"p", "q"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		log      string
		selected []string
	}{
		{eventLog(rows[:3]...), []string{"TestA", "FuzzB"}}, {log, []string{"TestA"}}, {log, []string{"TestA", "FuzzB", "TestOmitted"}},
		{log + eventLog(rows[3]), []string{"TestA", "FuzzB"}}, {log + eventLog(rows[1]), []string{"TestA", "FuzzB"}},
		{eventLog(rows[:3]...) + eventLog(map[string]string{"Action": "fail", "Package": "p"}), []string{"TestA", "FuzzB"}},
		{log + eventLog(map[string]string{"Action": "pass", "Package": "other"}), []string{"TestA", "FuzzB"}},
	} {
		if err := validateResults(tc.log, []string{"p"}, tc.selected); err == nil {
			t.Fatalf("accepted %s", tc.log)
		}
	}
}

func TestBuildEvents(t *testing.T) {
	valid := eventLog(map[string]string{"Action": "output", "Package": "p", "Output": "TestA\n"}, map[string]string{"Action": "pass", "Package": "p", "Test": "TestA"}, map[string]string{"Action": "pass", "Package": "p"})
	for _, tc := range []struct {
		name      string
		row       map[string]string
		wantError bool
	}{
		{"compiler diagnostic", map[string]string{"Action": "build-output", "ImportPath": "dependency", "Output": "TestNotRegistered\n"}, false},
		{"failed build", map[string]string{"Action": "build-fail", "ImportPath": "dependency"}, true},
		{"missing build package", map[string]string{"Action": "build-output", "Output": "warning"}, true},
		{"mixed event", map[string]string{"Action": "build-output", "ImportPath": "dependency", "Package": "p"}, true},
		{"build test", map[string]string{"Action": "build-output", "ImportPath": "dependency", "Test": "TestA"}, true},
		{"foreign test output", map[string]string{"Action": "output", "Package": "dependency", "Output": "TestNotRegistered\n"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := eventLog(tc.row) + valid
			names, err := listedTests(log, "p")
			if (err != nil) != tc.wantError || (!tc.wantError && !slices.Equal(names, []string{"TestA"})) {
				t.Fatalf("discovery %v: %v", names, err)
			}
			if err := validateResults(log, []string{"p"}, []string{"TestA"}); (err != nil) != tc.wantError {
				t.Fatalf("results: %v", err)
			}
		})
	}
}
func TestMatrix(t *testing.T) {
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--matrix"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var matrix struct {
		Include []struct {
			Group string
			Index int
		}
	}
	if err := json.Unmarshal(out.Bytes(), &matrix); err != nil {
		t.Fatal(err)
	}
	if len(matrix.Include) != 49 {
		t.Fatalf("matrix %s", out.String())
	}
	n := 0
	for _, g := range groups {
		for i := 0; i < g.count; i++ {
			if matrix.Include[n].Group != g.name || matrix.Include[n].Index != i {
				t.Fatal("matrix mismatch")
			}
			n++
		}
	}
	for _, args := range [][]string{nil, {"--group", "factory", "--index", "-1"}, {"--group", "packages", "--index", "5"}, {"--group", "unknown", "--index", "0"}, {"--matrix", "unexpected"}} {
		if err := run(t.Context(), args, io.Discard, io.Discard); nerrors.ExitCode(err) != 2 {
			t.Fatalf("expected coded usage error for %v, got %v", args, err)
		}
	}
	if err := run(t.Context(), []string{"--help"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}
