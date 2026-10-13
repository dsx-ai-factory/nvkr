// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubActionValuesOutputByteBoundary(t *testing.T) {
	empty, err := canonical.Marshal(map[string]string{"part-1": ""})
	if err != nil {
		t.Fatal(err)
	}
	overhead := len("data=\n") + len(empty)
	for _, test := range []struct {
		name    string
		size    int
		unicode bool
	}{
		{name: "below", size: maxGitHubActionDataOutputBytes - 1},
		{name: "exact", size: maxGitHubActionDataOutputBytes},
		{name: "above", size: maxGitHubActionDataOutputBytes + 1},
		{name: "UTF-8 exact", size: maxGitHubActionDataOutputBytes, unicode: true},
		{name: "UTF-8 above", size: maxGitHubActionDataOutputBytes + 1, unicode: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{"part-1": githubActionDataPadding(test.size-overhead, test.unicode)}
			encoded, err := canonical.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			want := "data=" + string(encoded) + "\n"
			if len(want) != test.size {
				t.Fatalf("fixture produces %d bytes, want %d including output key and newline", len(want), test.size)
			}
			for _, existing := range []bool{false, true} {
				name := "new output"
				if existing {
					name = "existing output"
				}
				t.Run(name, func(t *testing.T) {
					output := filepath.Join(t.TempDir(), "github-output")
					var prefix []byte
					if existing {
						prefix = []byte("retained=value\n")
						if err := os.WriteFile(output, prefix, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					err := writeGitHubActionValues(t.Context(), values, output)
					if test.size > maxGitHubActionDataOutputBytes {
						assertFactoryErrorCode(t, err, e.CodeEngineCapability)
						retained, readErr := os.ReadFile(output)
						if existing {
							if readErr != nil || !bytes.Equal(retained, prefix) {
								t.Fatalf("oversize output changed existing bytes: %v", readErr)
							}
						} else if !os.IsNotExist(readErr) {
							t.Fatalf("oversize output opened an absent path: %v", readErr)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					got, err := os.ReadFile(output)
					if err != nil || string(got) != string(prefix)+want {
						t.Fatalf("bounded output did not append the complete canonical line: %v", err)
					}
				})
			}
		})
	}
}

func TestGitHubGenerationAdmitsActionDataPerJobByteBoundary(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	raw := githubActionInterfaceFixture(actions, []githubInterfaceFixture{{name: "capacity", nodes: 1}})
	workflow, files, err := lowerGitHubActions(&Plan{}, raw, actions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shareGitHubActions(&Plan{}, workflow, files, actions); err != nil {
		t.Fatal(err)
	}
	data := sharedGitHubActionData(t, &GeneratedBundle{Files: files})
	encoded, err := canonical.Marshal(data.Entries["capacity"].Values)
	if err != nil {
		t.Fatal(err)
	}
	baseSize := len("data=\n") + len(encoded)
	for _, test := range []struct {
		name    string
		size    int
		unicode bool
	}{
		{name: "below", size: maxGitHubActionDataOutputBytes - 1},
		{name: "exact", size: maxGitHubActionDataOutputBytes},
		{name: "above", size: maxGitHubActionDataOutputBytes + 1},
		{name: "UTF-8 above", size: maxGitHubActionDataOutputBytes + 1, unicode: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			padding := githubActionDataPadding(test.size-baseSize, test.unicode)
			changed := bytes.Replace(raw, []byte("Execute node 0"), []byte("Execute node 0"+padding), 1)
			workflow, files, err := lowerGitHubActions(&Plan{}, changed, actions)
			if err != nil {
				t.Fatal(err)
			}
			_, err = shareGitHubActions(&Plan{}, workflow, files, actions)
			if test.size > maxGitHubActionDataOutputBytes {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if _, exists := files[GitHubActionDataPath]; exists {
					t.Fatal("oversize generation published its action-data member")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data := sharedGitHubActionData(t, &GeneratedBundle{Files: files})
			encoded, err := canonical.Marshal(data.Entries["capacity"].Values)
			if err != nil || len("data=\n")+len(encoded) != test.size {
				t.Fatalf("generation did not exercise the requested complete output byte boundary: %v", err)
			}
		})
	}
}

func TestGitHubActionDataDocumentCanExceedPerJobOutputBound(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	raw := githubActionInterfaceFixture(actions, []githubInterfaceFixture{{name: "first", nodes: 1}, {name: "second", nodes: 1}})
	raw = bytes.ReplaceAll(raw, []byte("Execute node 0"), []byte("Execute node 0"+strings.Repeat("x", maxGitHubActionDataOutputBytes/2)))
	workflow, files, err := lowerGitHubActions(&Plan{}, raw, actions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shareGitHubActions(&Plan{}, workflow, files, actions); err != nil {
		t.Fatal(err)
	}
	if len(files[GitHubActionDataPath]) <= maxGitHubActionDataOutputBytes {
		t.Fatal("fixture does not distinguish complete data-file capacity from the per-job output bound")
	}
	data := sharedGitHubActionData(t, &GeneratedBundle{Files: files})
	if len(data.Entries) != 2 {
		t.Fatal("fixture lost one of its independently bounded jobs")
	}
	for job, entry := range data.Entries {
		encoded, err := canonical.Marshal(entry.Values)
		if err != nil || len("data=\n")+len(encoded) >= maxGitHubActionDataOutputBytes {
			t.Fatalf("job %s does not independently fit the output bound: %v", job, err)
		}
	}
}

func githubActionDataPadding(size int, unicode bool) string {
	if unicode {
		return strings.Repeat("é", size/2) + strings.Repeat("x", size%2)
	}
	return strings.Repeat("x", size)
}
