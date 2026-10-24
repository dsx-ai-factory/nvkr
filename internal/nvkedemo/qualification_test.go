// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestDemoQualificationGate(t *testing.T) {
	buildDockerFixture(t, "")
	for _, tc := range []struct {
		name                      string
		status, attempt, exit     string
		prefix, suffix            string
		existing, malformed, pass bool
	}{
		{name: "success", status: "passed", attempt: "fresh", exit: "0", pass: true},
		{name: "extra newline", status: "passed", attempt: "fresh", exit: "0", suffix: "\n"},
		{name: "trailing space", status: "passed", attempt: "fresh", exit: "0", suffix: " "},
		{name: "leading space", status: "passed", attempt: "fresh", exit: "0", prefix: " "},
		{name: "second document", status: "passed", attempt: "fresh", exit: "0", suffix: "{}"},
		{name: "failed check", status: "failed", attempt: "fresh", exit: "0"},
		{name: "wrong attempt", status: "passed", attempt: "other", exit: "0"},
		{name: "failed process", status: "passed", attempt: "fresh", exit: "1"},
		{name: "existing transcript", status: "passed", attempt: "fresh", exit: "0", existing: true},
		{name: "malformed result", exit: "0", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTestDir(t)
			cfg := BuildConfig{Repository: root, AttemptPrefix: "fresh", QualificationPolicy: "/trust/policy.json", Qualification: QualificationFile{
				BuilderKey: "/trust/builder.key", SmokeKey: "/trust/smoke.key", QualifierKey: "/trust/qualifier.key", Cosign: "/tools/cosign", Repository: "localhost:5000/m3", Scratch: "/scratch", EvidenceOut: "/scratch/evidence.json", PlainHTTP: true, Anonymous: true,
			}}
			bootstrap := filepath.Dir(builtInputs(root, "fresh").NVKR)
			if err := os.MkdirAll(bootstrap, 0700); err != nil {
				t.Fatal(err)
			}
			result := lifecycle.LocalQualificationResult{SchemaVersion: "nvkr.dev/local-qualification-result/v1", Lane: "unqualified-local-execution", Status: tc.status, Attempt: tc.attempt, ContentSubject: "sha256:" + strings.Repeat("a", 64), Publication: lifecycle.LocalQualificationReferences{Evidence: "localhost:5000/m3@sha256:" + strings.Repeat("b", 64)}}
			raw, err := canonical.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			raw = []byte(tc.prefix + string(raw) + "\n" + tc.suffix)
			if tc.malformed {
				raw = []byte("not JSON")
			}
			if err := os.WriteFile(filepath.Join(bootstrap, "response.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			// A process fixture exercises child exit handling and transcript checks;
			// it makes no claim that smoke/signing or registry I/O ran.
			body := "#!/bin/sh\nset -eu\n[ -z \"${GH_TOKEN:-}\" ]\n[ \"$1\" = run ]\n[ \"$2\" = qualify-local ]\n[ \"$3\" = --workspace ]\n[ \"$4\" = .nvkr/runs/fresh ]\n[ -d .nvkr/laptop-bootstrap/fresh ]\ncat \"$(dirname \"$0\")/response.json\"\nexit " + tc.exit + "\n"
			body = strings.Replace(body, "set -eu\n", "set -eu\n: > \"$(dirname \"$0\")/child-started\"\n", 1)
			body = strings.Replace(body, "set -eu\n", "set -eu\n[ \"$HOME\" = \"$(dirname \"$0\")\" ]\nprintf 'private qualification diagnostic\\n' >&2\n", 1)
			if err := os.WriteFile(filepath.Join(bootstrap, "nvkr"), []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GH_TOKEN", "must-not-reach-qualification")
			t.Setenv("HOME", bootstrap)
			transcript := filepath.Join(bootstrap, "qualification-result.json")
			if tc.existing {
				if err := os.WriteFile(transcript, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = qualifyBuiltArtifacts(t.Context(), cfg)
			_, startedErr := os.Stat(filepath.Join(bootstrap, "child-started"))
			if tc.existing {
				if !errors.Is(startedErr, os.ErrNotExist) {
					t.Fatalf("qualification child ran with an existing transcript: %v", startedErr)
				}
			} else if startedErr != nil {
				t.Fatalf("qualification child did not run: %v", startedErr)
			}
			if !tc.existing {
				logPath := filepath.Join(bootstrap, "qualification-diagnostics", "qualify-local.log")
				log, logErr := os.ReadFile(logPath)
				if logErr != nil || !strings.Contains(string(log), "private qualification diagnostic") {
					t.Fatalf("qualification stderr not retained: %q, %v", log, logErr)
				}
				info, statErr := os.Stat(logPath)
				if statErr != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("qualification log must be private: %v", statErr)
				}
			}
			if tc.pass {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeBuildFailed {
					t.Fatalf("expected coded refusal: %v", err)
				}
			}
			retained, err := os.ReadFile(transcript)
			if err != nil {
				t.Fatal(err)
			}
			want := string(raw)
			if tc.existing {
				want = "preserve"
			}
			if string(retained) != want {
				t.Fatalf("transcript mismatch: %q", retained)
			}
		})
	}
}

func TestDemoQualificationConfigRequiresCompleteExplicitInputs(t *testing.T) {
	valid := "version: 1\nbuild:\n  repo: /repo\n  attempt: fresh\n  qualificationPolicy: /trust/policy.json\n  qualification:\n    builderKey: /trust/builder.key\n    smokeKey: /trust/smoke.key\n    qualifierKey: /trust/qualifier.key\n    cosign: /tools/cosign\n    repository: localhost:5000/m3\n    scratch: /scratch\n    evidenceOut: /scratch/evidence.json\n    plainHTTP: true\n    anonymous: true\n"
	for _, tc := range []struct {
		name, from, to string
		pass           bool
	}{
		{"complete", "", "", true},
		{"missing policy", "  qualificationPolicy: /trust/policy.json\n", "", false},
		{"missing key", "    qualifierKey: /trust/qualifier.key\n", "", false},
		{"relative scratch", "scratch: /scratch", "scratch: relative", false},
		{"missing registry", "    repository: localhost:5000/m3\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := valid
			if tc.from != "" {
				raw = strings.Replace(raw, tc.from, tc.to, 1)
			}
			file, err := LoadDemoFile(t.Context(), demoConfigPath(t, raw))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := file.BuildConfig(nil)
			if tc.pass {
				if err != nil || !cfg.Qualification.Anonymous || !cfg.Qualification.PlainHTTP || cfg.Qualification.QualifierKey != "/trust/qualifier.key" {
					t.Fatalf("configuration lost: %+v %v", cfg, err)
				}
			} else {
				assertConfigUsage(t, err)
			}
		})
	}
}
