// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReviewPathAdmission(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"inputs/catalogs/node-images/registry.yaml", true}, {"inputs/environments/github-release.yaml", true}, {".github/nvkr-run/plan.json", true}, {".github/workflows/nvkr-run.yml", true},
		{"inputs/catalogs/node-images/pins.lock.yaml", false}, {"inputs/policies/toolchain-maintenance/123.json", false}, {producerOverlayPolicy, true}, {"inputs/policies/other-producer-overlays.json", false}, {localToolchainBindings, true}, {"toolchains/nvkr-packer/toolchain.lock.json", false}, {".github/workflows/merge-gate.yaml", false}, {"inputs/catalogs/../../secret.json", false}, {"/tmp/registry.yaml", false}, {"inputs/catalogs/./registry.yaml", false},
	} {
		if got := allowedReviewPath(tc.path); got != tc.want {
			t.Errorf("%s: got %t want %t", tc.path, got, tc.want)
		}
	}
}
func TestProposalRetryAndRefusal(t *testing.T) {
	for _, scenario := range []string{"create", "retry", "closed", "conflict", "main-advanced", "unexpected-file", "whitespace-changed", "whitespace-untracked", "symlink", "ambiguous-refs", "truncated-pulls", "invalid-bot", "deleted", "executable", "no-change"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			name := "inputs/environments/test.json"
			file := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o600)
			if scenario == "executable" {
				mode = 0o700
			}
			if scenario == "symlink" {
				if err := os.Symlink("/nonexistent", file); err != nil {
					t.Fatal(err)
				}
			} else if scenario != "deleted" {
				if err := os.WriteFile(file, []byte(`{"image":"synthetic"}`), mode); err != nil {
					t.Fatal(err)
				}
			}
			record := filepath.Join(root, ApprovalDirectory, "123.json")
			if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(record, []byte(`{"sourceCommit":"`+strings.Repeat("a", 40)+`","images":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			source := strings.Repeat("b", 40)
			env := map[string]string{"GITHUB_SHA": source, "APPROVAL_RUN": "123", "APP_SLUG": "synthetic-review-bot", "GITHUB_REPOSITORY": Repository, "GITHUB_REF": "refs/heads/main", "GITHUB_WORKFLOW_REF": Repository + "/" + Workflow + "@refs/heads/main", "GITHUB_RUN_ID": "456"}
			var writes []reviewWrite
			encode := func(v any) []byte {
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			create := slices.Contains([]string{"create", "invalid-bot", "deleted", "executable"}, scenario)
			client := reviewClient{root: root, invoke: func(_ context.Context, args []string, payload []byte) ([]byte, error) {
				if args[0] == "git" {
					switch strings.Join(args[1:], " ") {
					case "rev-parse HEAD":
						return []byte(source + "\n"), nil
					case "rev-parse HEAD^{tree}":
						return []byte("base-tree\n"), nil
					case "diff --name-only -z HEAD":
						if scenario == "no-change" {
							return nil, nil
						}
						if scenario == "whitespace-changed" {
							return []byte(" " + name + "\x00"), nil
						}
						return []byte(name + "\x00"), nil
					case "ls-files --others --exclude-standard -z":
						if scenario == "unexpected-file" {
							return []byte("secret.json\x00"), nil
						}
						if scenario == "whitespace-untracked" {
							return []byte("\t" + name + "\x00"), nil
						}
						return []byte(".nvkr-inputs/source/ignored\x00"), nil
					default:
						t.Fatalf("unexpected Git %v", args)
					}
				}
				if len(args) < 3 || args[0] != "gh" || args[1] != "api" {
					t.Fatalf("unexpected command %v", args)
				}
				endpoint := strings.TrimPrefix(args[2], "repos/"+Repository+"/")
				var body map[string]any
				if payload != nil {
					if err := json.Unmarshal(payload, &body); err != nil {
						t.Fatal(err)
					}
					writes = append(writes, reviewWrite{endpoint, body})
					if strings.Join(args[3:], " ") != "--method POST --input -" {
						t.Fatal(args)
					}
				}
				switch {
				case endpoint == "git/ref/heads/main":
					sha := source
					if scenario == "main-advanced" {
						sha = strings.Repeat("c", 40)
					}
					return encode(map[string]any{"object": map[string]string{"sha": sha}}), nil
				case strings.HasPrefix(endpoint, "git/matching-refs/"):
					if create {
						return []byte(`[]`), nil
					}
					ref := gitReference{Ref: "refs/heads/automation/toolchain-refresh-123", Object: gitObject{SHA: "candidate"}}
					refs := []gitReference{ref}
					if scenario == "ambiguous-refs" {
						refs = append(refs, ref)
					}
					return encode(refs), nil
				case endpoint == "git/blobs":
					if body["encoding"] != "base64" || body["content"] == "" {
						t.Fatal(body)
					}
					return []byte(`{"sha":"blob"}`), nil
				case endpoint == "git/trees":
					if body["base_tree"] != "base-tree" {
						t.Fatal("rev-parse line ending was not removed", body)
					}
					entries, ok := body["tree"].([]any)
					if !ok || len(entries) != 1 {
						t.Fatal(body)
					}
					entry := entries[0].(map[string]any)
					if scenario == "deleted" && entry["sha"] != nil {
						t.Fatal("deletion requires null sha")
					}
					if scenario == "executable" && entry["mode"] != "100755" {
						t.Fatal("executable mode lost")
					}
					return []byte(`{"sha":"candidate-tree"}`), nil
				case endpoint == "git/commits/candidate":
					tree := "candidate-tree"
					if scenario == "conflict" {
						tree = "different"
					}
					return encode(map[string]any{"tree": map[string]string{"sha": tree}}), nil
				case endpoint == "users/synthetic-review-bot%5Bbot%5D":
					if scenario == "invalid-bot" {
						return []byte(`{"id":true}`), nil
					}
					return []byte(`{"id":1234}`), nil
				case endpoint == "git/commits":
					if !strings.Contains(body["message"].(string), "Signed-off-by: synthetic-review-bot[bot]") {
						t.Fatal(body)
					}
					if body["parents"].([]any)[0] != source {
						t.Fatal(body)
					}
					return []byte(`{"sha":"candidate"}`), nil
				case endpoint == "git/refs":
					if body["ref"] != "refs/heads/automation/toolchain-refresh-123" {
						t.Fatal(body)
					}
					return []byte(`{}`), nil
				case strings.HasPrefix(endpoint, "pulls?"):
					if create {
						return []byte(`[]`), nil
					}
					existing := map[string]any{"head": map[string]any{"repo": map[string]string{"full_name": Repository}}, "html_url": "https://github.com/dsx-ai-factory/nvkr/pull/999999", "state": scenario}
					if scenario == "truncated-pulls" {
						many := make([]any, 100)
						for i := range many {
							many[i] = existing
						}
						return encode(many), nil
					}
					return encode([]any{existing}), nil
				case endpoint == "pulls":
					if body["draft"] != false || body["base"] != "main" {
						t.Fatal(body)
					}
					return []byte(`{"html_url":"https://github.com/dsx-ai-factory/nvkr/pull/999999"}`), nil
				default:
					t.Fatalf("unexpected endpoint %s", endpoint)
				}
				return nil, nil
			}}
			result, err := propose(t.Context(), client, func(k string) string { return env[k] })
			failure := slices.Contains([]string{"conflict", "main-advanced", "unexpected-file", "whitespace-changed", "whitespace-untracked", "symlink", "ambiguous-refs", "truncated-pulls", "invalid-bot"}, scenario)
			if failure {
				requireReviewError(t, err)
			} else if err != nil || result == "" {
				t.Fatalf("%q %v", result, err)
			}
			for _, w := range writes {
				if _, ok := w.body["force"]; ok {
					t.Fatal("force update attempted")
				}
				if !create && slices.Contains([]string{"pulls", "git/refs", "git/commits"}, w.endpoint) {
					t.Fatalf("retry mutated branch/proposal: %+v", w)
				}
			}
			if slices.Contains([]string{"main-advanced", "unexpected-file", "whitespace-changed", "whitespace-untracked", "symlink", "ambiguous-refs", "no-change"}, scenario) && len(writes) != 0 {
				t.Fatalf("rejected candidate wrote: %+v", writes)
			}
		})
	}
}
func TestProposalAdmissionBeforeWork(t *testing.T) {
	_, err := propose(t.Context(), reviewClient{invoke: func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("invoked before admission")
		return nil, nil
	}}, func(string) string { return "" })
	requireReviewError(t, err)
}
