// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVulncheckToolsReports(t *testing.T) {
	makefile, err := filepath.Abs("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		first, second int
		incomplete    bool
		install       int
	}{{0, 0, false, 0}, {3, 0, false, 0}, {0, 3, false, 0}, {1, 0, true, 0}, {0, 2, true, 0}, {127, 3, true, 0}, {0, 0, true, 42}} {
		t.Run(fmt.Sprintf("%d/%d/install=%d", tc.first, tc.second, tc.install), func(t *testing.T) {
			work := t.TempDir()
			bin := filepath.Join(work, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(work, ".go-version"), readFile(t, "../../.go-version"), 0o600); err != nil {
				t.Fatal(err)
			}
			fakeGo := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = env ]; then printf 'go%%s\\n' \"$(cat .go-version)\"; exit 0; fi\nprintf '%%s\\n' \"$*\" > install-calls\nexit %d\n", tc.install)
			fakeScanner := fmt.Sprintf("#!/bin/sh\nprintf '%%s %%s %%s\\n' \"$1\" \"$2\" \"$3\" >> calls\ncase \"$2\" in toolchains/actionlint) exit %d;; *) exit %d;; esac\n", tc.first, tc.second)
			for name, body := range map[string]string{"go": fakeGo, "govulncheck": fakeScanner} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := makeTarget(t, makefile, work, "vulncheck-tools")
			if (err != nil) != tc.incomplete {
				t.Fatalf("unexpected exit: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(readFile(t, filepath.Join(work, "install-calls")))); got != "-C toolchains/govulncheck install tool" {
				t.Fatal(got)
			}
			if tc.install != 0 {
				if _, err := os.Stat(filepath.Join(work, "calls")); !os.IsNotExist(err) {
					t.Fatal("stale scanner ran after install failure")
				}
				for _, text := range []string{"Scanning", "complete, no reachable findings", "findings (report-only)"} {
					if strings.Contains(output, text) {
						t.Fatalf("unexpected %q: %s", text, output)
					}
				}
				return
			}
			want := "-C toolchains/actionlint github.com/rhysd/actionlint/cmd/actionlint\n-C toolchains/govulncheck golang.org/x/vuln/cmd/govulncheck\n"
			if got := string(readFile(t, filepath.Join(work, "calls"))); got != want {
				t.Fatalf("calls %q", got)
			}
			if strings.Contains(output, "INCOMPLETE") != tc.incomplete || strings.Contains(output, "findings (report-only)") != (tc.first == 3 || tc.second == 3) {
				t.Fatal(output)
			}
		})
	}
}
