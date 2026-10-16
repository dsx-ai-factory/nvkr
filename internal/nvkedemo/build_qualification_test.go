// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
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

// These bounded owner fixtures exercise orchestration and real YAML loading,
// not native builders, signing or a registry daemon.
func TestAutomaticQualificationTransition(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn"} {
		for _, mode := range []string{"success", "prepare", "setup", "yaml", "build", "qualify", "cancel", "stop"} {
			t.Run(network+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				cfg := BuildConfig{Repository: privateTestDir(t), AttemptPrefix: "fresh", Jobs: 2, Network: network, QualifyLocal: true}
				var calls []string
				refusal := nvkrerr.New(nvkrerr.CodeBuildFailed, "fixture refusal")
				step := func(name string) error {
					calls = append(calls, name)
					if mode == name {
						return refusal
					}
					return nil
				}
				stopped := false
				steps := buildQualificationSteps{
					prepare: func(_ context.Context, _ Runner, _ string, got BuildConfig) error {
						if got.Network != network || got.Prepared {
							t.Fatal("invalid preparation selection")
						}
						return step("prepare")
					},
					setup: func(_ context.Context, file DemoFile, out string, _ io.Writer) (string, error) {
						if file.Run.Network != network || file.Build.QualifyLocal || file.Build.Attempt != "fresh" {
							t.Fatal("setup lost base selection")
						}
						if err := step("setup"); err != nil {
							return "", err
						}
						if err := os.MkdirAll(out, 0700); err != nil {
							t.Fatal(err)
						}
						file.Build.Prepared = true
						file.Build.QualificationPolicy = "/trust/policy.json"
						file.Build.Qualification = QualificationFile{BuilderKey: "/trust/builder", SmokeKey: "/trust/smoke", QualifierKey: "/trust/qualifier", Cosign: "/tools/cosign", Repository: "localhost:5000/test", Scratch: "/scratch", EvidenceOut: "/scratch/evidence"}
						raw, err := yaml.Marshal(file)
						if err != nil {
							t.Fatal(err)
						}
						if mode == "yaml" {
							raw = []byte("version: [invalid]")
						}
						path := filepath.Join(out, "demo.yaml")
						if err := os.WriteFile(path, raw, 0600); err != nil {
							t.Fatal(err)
						}
						return path, nil
					},
					build: func(_ context.Context, _ Runner, _ string, got BuildConfig) error {
						if !got.Prepared || got.QualifyLocal || got.Network != network || got.QualificationPolicy != "/trust/policy.json" {
							t.Fatal("generated config lost")
						}
						return step("build")
					},
					qualify: func(operation context.Context, got BuildConfig) error {
						if got.Network != network || got.Qualification.BuilderKey != "/trust/builder" {
							t.Fatal("qualification lost selection")
						}
						if err := step("qualify"); err != nil {
							return err
						}
						if mode == "cancel" {
							cancel()
							return operation.Err()
						}
						return nil
					},
					stop: func(cleanup context.Context, out string) error {
						deadline, ok := cleanup.Deadline()
						if cleanup.Err() != nil || !ok || time.Until(deadline) > time.Minute || out != filepath.Join(cfg.Repository, ".nvkr/laptop-bootstrap/fresh/qualification") {
							t.Fatal("cleanup lifetime or ownership changed")
						}
						stopped = true
						return step("stop")
					},
				}
				got, err := executeBuildQualification(ctx, Runner{}, "launcher", cfg, steps)
				want := []string{"prepare", "setup", "build", "qualify", "stop"}
				switch mode {
				case "prepare":
					want = want[:1]
				case "setup":
					want = want[:2]
				case "yaml":
					want = []string{"prepare", "setup", "stop"}
				case "build":
					want = []string{"prepare", "setup", "build", "stop"}
				}
				if !slices.Equal(calls, want) {
					t.Fatalf("transition=%v, want %v", calls, want)
				}
				if mode == "success" {
					if err != nil || !stopped || got != builtInputs(cfg.Repository, cfg.AttemptPrefix) {
						t.Fatalf("handoff before successful stop: %+v %v", got, err)
					}
				} else if err == nil || got != (BuiltInputs{}) {
					t.Fatalf("failed transition returned inputs: %+v %v", got, err)
				}
			})
		}
	}
}
