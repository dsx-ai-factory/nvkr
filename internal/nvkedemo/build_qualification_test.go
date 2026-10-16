// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBuildQualificationRefusalPreventsProducerExecution(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		name := "missing policy"
		if malformed {
			name = "malformed policy"
		}
		t.Run(name, func(t *testing.T) {
			dir := privateTestDir(t)
			cfg := BuildConfig{Repository: dir, AttemptPrefix: "fresh", Jobs: 1, QualificationPolicy: filepath.Join(dir, "policy.json")}
			if malformed {
				if err := os.WriteFile(cfg.QualificationPolicy, []byte(`{"schemaVersion":"unknown"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeInvocationFixture(t, cfg, preparedInvocationFixture(cfg))
			calls := filepath.Join(dir, "calls")
			launcher := filepath.Join(dir, "launcher.sh")
			for path, body := range map[string]string{
				launcher: "#!/bin/sh\nprintf prepare >>\"$CALLS\"\n",
				filepath.Join(dir, ".nvkr/laptop-bootstrap/fresh/nvkr"): "#!/bin/sh\nprintf producer >>\"$CALLS\"\n",
			} {
				if err := os.WriteFile(path, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := buildLocalArtifacts(t.Context(), Runner{Env: []string{"PATH=/usr/bin:/bin", "CALLS=" + calls}}, launcher, cfg)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeTrustPolicyMissing {
				t.Fatalf("expected policy refusal, got %v", err)
			}
			raw, err := os.ReadFile(calls)
			if err != nil || string(raw) != "prepare" {
				t.Fatalf("policy refusal must prevent producer execution: %q, %v", raw, err)
			}
		})
	}
}

func TestDemoQualificationPolicyConfiguration(t *testing.T) {
	file, err := LoadDemoFile(t.Context(), demoConfigPath(t, "version: 1\nbuild:\n  repo: /native/repo\n  attempt: fresh\n  qualificationPolicy: /trust/policy.json\n"))
	if err != nil {
		t.Fatal(err)
	}
	build, err := file.BuildConfig(nil)
	if err != nil || build.QualificationPolicy != "/trust/policy.json" {
		t.Fatalf("explicit policy was not retained: %+v, %v", build, err)
	}
}
