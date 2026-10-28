// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ghIdentityStub answers the one bot user lookup the identity step makes. FAIL makes it fail; BOT_ID is the
// id it reports.
const ghIdentityStub = `echo "gh $*" >> "$LOG"
[ -z "${FAIL:-}" ] || exit 1
echo "${BOT_ID-123}"
`

// The identity step names the App's bot user for the DCO sign-off, and refuses a slug or id that could
// reach the step output as anything but a name and a number.
func TestAppBotIdentityStep(t *testing.T) {
	requireTools(t, "bash")
	script := stepScript(t, "pull-request", "Resolve the App bot identity")
	for _, tc := range []struct {
		name, slug, fail, botID string
		ok                      bool
	}{
		{name: "resolves", slug: "release-bot", botID: "123", ok: true},
		{name: "empty slug", slug: "", botID: "123"},
		{name: "slug that could reach the output", slug: "bot\ntoken=x", botID: "123"},
		{name: "lookup failure", slug: "release-bot", fail: "1", botID: "123"},
		{name: "non-numeric bot user id", slug: "release-bot", botID: "1x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			temp := t.TempDir()
			bin := filepath.Join(temp, "bin")
			if err := os.MkdirAll(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/bash\n"+ghIdentityStub), 0o700); err != nil {
				t.Fatal(err)
			}
			logPath, out := filepath.Join(temp, "log"), filepath.Join(temp, "output")
			output, err := runScript(t, temp, script, "PATH="+bin+":"+os.Getenv("PATH"), "LOG="+logPath, "GITHUB_OUTPUT="+out,
				"APP_SLUG="+tc.slug, "FAIL="+tc.fail, "BOT_ID="+tc.botID)
			got, _ := os.ReadFile(out)
			if !tc.ok {
				if err == nil || len(got) != 0 {
					t.Fatalf("failure wrote %q: %v %s", got, err, output)
				}
				return
			}
			want := "name=release-bot[bot]\nemail=123+release-bot[bot]@users.noreply.github.com"
			if err != nil || strings.TrimSpace(string(got)) != want {
				t.Fatalf("identity not written: %v %s %s", err, output, got)
			}
			if log, _ := os.ReadFile(logPath); strings.TrimSpace(string(log)) != "gh api users/release-bot%5Bbot%5D --jq .id" {
				t.Fatalf("unexpected lookup: %s", log)
			}
		})
	}
}
