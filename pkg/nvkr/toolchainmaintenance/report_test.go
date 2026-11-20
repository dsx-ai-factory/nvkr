// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func requireReviewError(t *testing.T, err error) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeGateUnmet {
		t.Fatalf("expected gate-unmet, got %v", err)
	}
}
func comparisonFixture(t *testing.T, arch string) (*factory.Plan, []Image) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	raw := fmt.Sprintf(`{"schemaVersion":%q,"builds":[{"platform":"linux/%s","executionGraph":{"nodes":[{"toolchain":"%s/packer-native@%s"},{}]}}],"environment":{"diskInspectionProfiles":{"linux/%s":{"image":"%s/inspector-native@%s"}}}}`, factory.PlanSchemaVersion, arch, Registry, digest, arch, Registry, digest)
	var plan factory.Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	return &plan, []Image{{Arch: arch, Kind: "packer", ManifestDigest: digest}, {Arch: arch, Kind: "inspector", ManifestDigest: digest}}
}
func TestCompareManifests(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, changed := range []string{"", "packer", "inspector"} {
			t.Run(arch+"/"+changed, func(t *testing.T) {
				plan, images := comparisonFixture(t, arch)
				plan.Builds = append(plan.Builds, plan.Builds[0])
				for i := range images {
					if images[i].Kind == changed {
						images[i].ManifestDigest = "sha256:" + strings.Repeat("c", 64)
					}
				}
				got, err := compareManifests(plan, images, arch)
				if err != nil || got != (changed != "") {
					t.Fatalf("%t %v", got, err)
				}
			})
		}
	}
	for _, scenario := range []string{"missing-packer", "ambiguous-packer", "missing-inspector", "wrong-repository", "mutable-pin", "missing-measurement", "duplicate", "wrong-arch", "bad-digest"} {
		t.Run(scenario, func(t *testing.T) {
			plan, images := comparisonFixture(t, "amd64")
			nodes := plan.Builds[0].Graph.Nodes
			profile := plan.Environment.DiskInspectionProfiles["linux/amd64"]
			switch scenario {
			case "missing-packer":
				plan.Builds[0].Graph.Nodes = nil
			case "ambiguous-packer":
				extra := nodes[0]
				extra.Toolchain = Registry + "/packer-native@sha256:" + strings.Repeat("c", 64)
				plan.Builds[0].Graph.Nodes = append(nodes, extra)
			case "missing-inspector":
				profile.Image = ""
			case "wrong-repository":
				profile.Image = "ghcr.io/example/other@sha256:" + strings.Repeat("a", 64)
			case "mutable-pin":
				profile.Image = Registry + "/inspector-native@latest"
			case "missing-measurement":
				images = images[:1]
			case "duplicate":
				images = append(images, images[0])
			case "wrong-arch":
				images[0].Arch = "arm64"
			case "bad-digest":
				images[0].ManifestDigest = "latest"
			}
			plan.Environment.DiskInspectionProfiles["linux/amd64"] = profile
			_, err := compareManifests(plan, images, "amd64")
			requireReviewError(t, err)
		})
	}
}
func TestCompareRejectsUnadmittedPlan(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "plan.json")
	proposal := filepath.Join(root, "proposal.json")
	if err := os.WriteFile(plan, []byte(`{"builds":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proposal, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compare(t.Context(), plan, proposal, "amd64"); err != nil {
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
			t.Fatalf("unadmitted Plan error: %v", err)
		}
	} else {
		t.Fatal("unadmitted Plan accepted")
	}
}

func TestCompareCommittedPlanTree(t *testing.T) {
	planPath := "../../../.github/nvkr-run-nvke/plan.json"
	plan, err := factory.LoadPlanContext(t.Context(), planPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			pins := map[string]string{"inspector": plan.Environment.DiskInspectionProfiles["linux/"+arch].Image}
			for _, build := range plan.Builds {
				if build.Platform != "linux/"+arch || build.Graph == nil {
					continue
				}
				for _, node := range build.Graph.Nodes {
					if strings.HasPrefix(node.Toolchain, Registry+"/packer-native@") {
						pins["packer"] = node.Toolchain
					}
				}
			}
			var images []Image
			for _, kind := range []string{"packer", "inspector"} {
				_, digest, ok := strings.Cut(pins[kind], "@")
				if !ok {
					t.Fatalf("missing %s pin", kind)
				}
				images = append(images, Image{Arch: arch, Kind: kind, ManifestDigest: digest})
			}
			raw, err := json.Marshal(images)
			if err != nil {
				t.Fatal(err)
			}
			proposal := filepath.Join(t.TempDir(), "proposal.json")
			if err := os.WriteFile(proposal, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			changed, err := Compare(t.Context(), planPath, proposal, arch)
			if err != nil || changed {
				t.Fatalf("committed Plan comparison = %t: %v", changed, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := Compare(ctx, planPath, proposal, arch); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled comparison did not preserve cancellation: %v", err)
			}
		})
	}
}
func successfulMeasurements(amd64, arm64 string) map[string]jobResult {
	result := map[string]jobResult{}
	for _, job := range refreshJobs {
		result[job] = jobResult{Result: "skipped"}
	}
	result["measure"] = jobResult{Result: "success", Outputs: map[string]string{"changed_amd64": amd64, "changed_arm64": arm64}}
	return result
}

type reviewWrite struct {
	endpoint string
	body     map[string]any
}
type fakeReports struct {
	t      *testing.T
	issues []refreshIssue
	labels []issueLabel
	writes []reviewWrite
	fail   bool
}

func (f *fakeReports) client() reviewClient {
	if f.issues == nil {
		f.issues = []refreshIssue{}
	}
	if f.labels == nil {
		f.labels = []issueLabel{}
	}
	return reviewClient{invoke: func(_ context.Context, args []string, payload []byte) ([]byte, error) {
		f.t.Helper()
		encode := func(v any) []byte {
			b, err := json.Marshal(v)
			if err != nil {
				f.t.Fatal(err)
			}
			return b
		}
		if payload == nil {
			if len(args) != 5 || strings.Join(args[:4], " ") != "gh api --paginate --slurp" {
				f.t.Fatalf("unexpected pagination %v", args)
			}
			if f.fail {
				return nil, e.New(e.CodeGateUnmet, "synthetic lookup failure")
			}
			endpoint := strings.TrimPrefix(args[4], "repos/"+Repository+"/")
			switch endpoint {
			case "labels?per_page=100":
				return encode([]any{[]any{}, f.labels}), nil
			case "issues?state=open&labels=" + refreshLabel + "&per_page=100":
				labeled := []refreshIssue{}
				for _, issue := range f.issues {
					if hasRefreshLabel(issue.Labels) {
						labeled = append(labeled, issue)
					}
				}
				return encode([]any{[]any{}, labeled}), nil
			case "issues?state=open&per_page=100":
				return encode([]any{[]any{}, f.issues}), nil
			default:
				f.t.Fatalf("unexpected endpoint %s", endpoint)
			}
		}
		if len(args) != 7 || strings.Join(args[3:], " ") != "--method POST --input -" {
			f.t.Fatalf("unexpected write %v", args)
		}
		endpoint := strings.TrimPrefix(args[2], "repos/"+Repository+"/")
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			f.t.Fatal(err)
		}
		f.writes = append(f.writes, reviewWrite{endpoint, body})
		switch {
		case endpoint == "labels":
			f.labels = append(f.labels, issueLabel{Name: refreshLabel})
		case endpoint == "issues":
			f.issues = append(f.issues, refreshIssue{Number: 1000, Title: refreshTitle, Labels: []issueLabel{{Name: refreshLabel}}})
		case strings.HasSuffix(endpoint, "/labels"):
			for i := range f.issues {
				if endpoint == fmt.Sprintf("issues/%d/labels", f.issues[i].Number) {
					f.issues[i].Labels = append(f.issues[i].Labels, issueLabel{Name: refreshLabel})
				}
			}
		case strings.HasSuffix(endpoint, "/comments"):
		default:
			f.t.Fatalf("unexpected mutation %s", endpoint)
		}
		return []byte(`{}`), nil
	}}
}
func TestRefreshReportRouting(t *testing.T) {
	issue := func(n int, title string, labeled bool) refreshIssue {
		v := refreshIssue{Number: n, Title: title, Labels: []issueLabel{}}
		if labeled {
			v.Labels = []issueLabel{{Name: refreshLabel}}
		}
		return v
	}
	for _, tc := range []struct {
		name   string
		issues []refreshIssue
		want   int
	}{
		{"label priority", []refreshIssue{issue(9, "Renamed by owner", true), issue(1, refreshTitle, false)}, 9},
		{"legacy title", []refreshIssue{issue(5, refreshTitle, false)}, 5},
		{"oldest legacy", []refreshIssue{issue(8, refreshTitle+" 123", false), issue(3, refreshTitle+" 456", false)}, 3},
		{"ignore pull requests", []refreshIssue{{Number: 1, Title: refreshTitle, Labels: []issueLabel{{Name: refreshLabel}}, PullRequest: json.RawMessage(`{}`)}, issue(4, refreshTitle, false)}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeReports{t: t, issues: tc.issues}
			if err := f.client().postReport(t.Context(), "actionable report"); err != nil {
				t.Fatal(err)
			}
			last := f.writes[len(f.writes)-1]
			if last.endpoint != fmt.Sprintf("issues/%d/comments", tc.want) || last.body["body"] != "actionable report" {
				t.Fatalf("writes %v", f.writes)
			}
			for _, w := range f.writes {
				if w.endpoint == "issues" {
					t.Fatal("created duplicate issue")
				}
			}
		})
	}
	t.Run("create once then comment", func(t *testing.T) {
		f := fakeReports{t: t, issues: []refreshIssue{issue(1, "Unrelated", false)}}
		c := f.client()
		for _, body := range []string{"first report", "second report"} {
			if err := c.postReport(t.Context(), body); err != nil {
				t.Fatal(err)
			}
		}
		count := 0
		for _, w := range f.writes {
			if w.endpoint == "issues" {
				count++
				if w.body["title"] != refreshTitle || w.body["body"] != "first report" {
					t.Fatal(w)
				}
			}
		}
		if count != 1 || f.writes[len(f.writes)-1].endpoint != "issues/1000/comments" {
			t.Fatal(f.writes)
		}
	})
	t.Run("lookup fails closed", func(t *testing.T) {
		f := fakeReports{t: t, fail: true}
		requireReviewError(t, f.client().postReport(t.Context(), "report"))
		if len(f.writes) != 0 {
			t.Fatal(f.writes)
		}
	})
	t.Run("null legacy item refuses creation", func(t *testing.T) {
		c := reviewClient{invoke: func(_ context.Context, args []string, payload []byte) ([]byte, error) {
			if payload != nil {
				t.Fatal("malformed lookup authorized a remote write")
			}
			if strings.Contains(args[len(args)-1], "labels=") {
				return []byte(`[[]]`), nil
			}
			return []byte(`[[null]]`), nil
		}}
		requireReviewError(t, c.postReport(t.Context(), "report"))
	})
}
func TestIncompleteIssueRowsRefuseWrites(t *testing.T) {
	for _, lookup := range []string{"labeled", "legacy"} {
		for _, row := range []string{
			`{}`, `{"number":1,"labels":[]}`, `{"title":"Unrelated","labels":[]}`,
			`{"number":1,"title":"Unrelated"}`, `{"number":1,"title":"Unrelated","labels":null}`,
			`{"number":1,"title":"Unrelated","labels":[null]}`,
			`{"number":1,"title":"Unrelated","labels":[{}]}`,
		} {
			t.Run(lookup+"/"+row, func(t *testing.T) {
				c := reviewClient{invoke: func(_ context.Context, args []string, payload []byte) ([]byte, error) {
					if payload != nil {
						t.Fatal("incomplete lookup authorized a remote write")
					}
					if lookup == "legacy" && strings.Contains(args[len(args)-1], "labels=") {
						return []byte(`[[]]`), nil
					}
					return []byte(`[[` + row + `]]`), nil
				}}
				requireReviewError(t, c.postReport(t.Context(), "report"))
			})
		}
	}
}
func TestRefreshActionableOutcomes(t *testing.T) {
	type scenario struct {
		name       string
		results    map[string]jobResult
		approval   string
		actionable bool
	}
	cases := []scenario{{"unchanged", successfulMeasurements("false", "false"), "", false}, {"amd64", successfulMeasurements("true", "false"), "", true}, {"arm64", successfulMeasurements("false", "true"), "", true}, {"approved-success", successfulMeasurements("true", "true"), "123", false}}
	for _, job := range refreshJobs {
		r := successfulMeasurements("false", "false")
		r[job] = jobResult{Result: "failure"}
		cases = append(cases, scenario{"failure-" + job, r, "", true})
	}
	r := successfulMeasurements("false", "false")
	r["measure"] = jobResult{Result: "cancelled"} //nolint:misspell // GitHub's job-result value.
	cases = append(cases, scenario{"canceled", r, "", true})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.results)
			if err != nil {
				t.Fatal(err)
			}
			summary := filepath.Join(t.TempDir(), "summary")
			env := map[string]string{"GITHUB_REPOSITORY": Repository, "GITHUB_REF": "refs/heads/main", "RESULTS": string(raw), "APPROVAL_RUN": tc.approval, "RUN_URL": "https://github.com/" + Repository + "/actions/runs/123", "GITHUB_STEP_SUMMARY": summary}
			f := fakeReports{t: t}
			_, err = report(t.Context(), f.client(), func(k string) string { return env[k] })
			if err != nil {
				t.Fatal(err)
			}
			if (len(f.writes) > 0) != tc.actionable {
				t.Fatal(f.writes)
			}
			if !tc.actionable {
				if _, err := os.Stat(summary); !os.IsNotExist(err) {
					t.Fatal("silent outcome wrote summary")
				}
			}
		})
	}
	for _, value := range []string{"", "unknown"} {
		_, err := refreshReason(successfulMeasurements("false", value), "")
		requireReviewError(t, err)
	}
	missing := successfulMeasurements("false", "false")
	delete(missing, "admit")
	_, err := refreshReason(missing, "")
	requireReviewError(t, err)
}
