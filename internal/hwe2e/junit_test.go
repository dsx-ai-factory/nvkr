// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// Contracts for the hardware E2E JUnit coverage check.
//
// The fixture is a trimmed Ginkgo dry run of NVKE's controller/test/e2e suite at the harness commit, so the
// group sizes below are facts about that report. Every test builds its "real run" report by editing a copy of
// it; none of this says anything about hardware.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	fixturePath = "../../scripts/testdata/hardware-e2e/ginkgo-dry-run.junit.xml"
	// What the workflow passes the suite: the fixture's own filter.
	fixtureFilter = "site-controller && !nico-dsx"

	provisioningSpec = "[It] NVKE e2e qualification provisions a node [site-controller, needs:cpu=2, needs:gpu=1]"
	siteCheckSpec    = "[It] NVKE e2e qualification runs CoreDNS [site-controller, needs:cpu=2, needs:gpu=1, readiness, site-distro]"
	// The fixture is a trimmed copy of the harness's Ginkgo output, so its specs carry the tenant label as the
	// harness writes it.
	tenantSpec = "[It] NVKE e2e qualification tenant on the built site " + defaultTenantLabel + " creates the cluster " +
		"[site-controller, needs:cpu=2, needs:gpu=1, " + defaultTenantLabel + ", lifecycle]"
	notSelectedSpec = "[It] NVKE e2e qualification a spec the dry run never selected [site-controller, nico-dsx]"
)

// editableReport is an editable copy of the fixture, written to a temporary file on demand. The fixture is a dry
// run's report; a copy stands in for a real run's report unless it is made with dry set, so it is stamped
// DryRun=false the way Ginkgo stamps a real run.
type editableReport struct {
	root  *xmlElement
	suite *xmlElement
}

func newReport(t *testing.T, dry bool) *editableReport {
	t.Helper()
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseXML(data)
	if err != nil {
		t.Fatal(err)
	}
	suite := root
	if root.name != "testsuite" {
		suite = root.descendants("testsuite")[0]
	}
	r := &editableReport{root: root, suite: suite}
	r.setProp(t, "DryRun", strconv.FormatBool(dry))
	return r
}

func (r *editableReport) find(t *testing.T, name string) *xmlElement {
	t.Helper()
	for _, element := range r.suite.descendants("testcase") {
		if element.attrs["name"] == name {
			return element
		}
	}
	t.Fatalf("no test case %q", name)
	return nil
}

func (r *editableReport) remove(t *testing.T, name string) {
	t.Helper()
	target := r.find(t, name)
	kept := r.suite.children[:0:0]
	for _, child := range r.suite.children {
		if child != target {
			kept = append(kept, child)
		}
	}
	r.suite.children = kept
}

func (r *editableReport) skip(t *testing.T, name, message string) {
	t.Helper()
	element := r.find(t, name)
	element.attrs["status"] = "skipped"
	element.children = append(element.children, &xmlElement{name: "skipped", attrs: map[string]string{"message": message}})
}

func (r *editableReport) fail(t *testing.T, name string) {
	t.Helper()
	element := r.find(t, name)
	element.attrs["status"] = "failed"
	element.children = append(element.children, &xmlElement{name: "failure", attrs: map[string]string{"message": "boom"}})
}

func (r *editableReport) setStatus(t *testing.T, name, status string) {
	t.Helper()
	r.find(t, name).attrs["status"] = status
}

func (r *editableReport) addPassed(name string) {
	r.suite.children = append(r.suite.children, &xmlElement{
		name:  "testcase",
		attrs: map[string]string{"name": name, "classname": "NVKE E2E Suite", "status": "passed"},
	})
}

func (r *editableReport) setProp(t *testing.T, name, value string) {
	t.Helper()
	for _, element := range r.suite.descendants("property") {
		if element.attrs["name"] == name {
			element.attrs["value"] = value
			return
		}
	}
	t.Fatalf("no property %q", name)
}

func (r *editableReport) dropProp(t *testing.T, name string) {
	t.Helper()
	for _, properties := range r.suite.descendants("properties") {
		for index, element := range properties.children {
			if element.attrs["name"] == name {
				properties.children = append(properties.children[:index:index], properties.children[index+1:]...)
				return
			}
		}
	}
	t.Fatalf("no property %q", name)
}

func (r *editableReport) names(keep func(string) bool) []string {
	var names []string
	for _, element := range r.suite.descendants("testcase") {
		if name := element.attrs["name"]; keep(name) {
			names = append(names, name)
		}
	}
	return names
}

func (r *editableReport) write(t *testing.T, dir, name string) string {
	t.Helper()
	var builder strings.Builder
	r.root.serialize(&builder)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (e *xmlElement) serialize(builder *strings.Builder) {
	builder.WriteString("<" + e.name)
	keys := make([]string, 0, len(e.attrs))
	for key := range e.attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		builder.WriteString(" " + key + `="`)
		_ = xml.EscapeText(builder, []byte(e.attrs[key]))
		builder.WriteString(`"`)
	}
	builder.WriteString(">")
	for _, child := range e.children {
		child.serialize(builder)
	}
	builder.WriteString("</" + e.name + ">")
}

func hasChild(element *xmlElement, name string) bool {
	for _, child := range element.children {
		if child.name == name {
			return true
		}
	}
	return false
}

// execute runs the command the way the workflow does and returns its exit status and standard error.
func execute(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return code, stderr.String()
}

type checkOptions struct {
	dry, stage, out, filter string
	extra                   []string
}

type checkRun struct {
	code   int
	stderr string
	out    string
}

func (r checkRun) record(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(r.out)
	if err != nil {
		t.Fatalf("no coverage record: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func withDry(path string) func(*checkOptions)    { return func(o *checkOptions) { o.dry = path } }
func withStage(stage string) func(*checkOptions) { return func(o *checkOptions) { o.stage = stage } }
func withOut(path string) func(*checkOptions)    { return func(o *checkOptions) { o.out = path } }
func withExtra(args ...string) func(*checkOptions) {
	return func(o *checkOptions) { o.extra = args }
}

func runCheck(t *testing.T, dir, realPath string, options ...func(*checkOptions)) checkRun {
	t.Helper()
	o := checkOptions{dry: fixturePath, stage: "full", out: filepath.Join(dir, "coverage.json"), filter: fixtureFilter}
	for _, option := range options {
		option(&o)
	}
	args := []string{"check-junit", "--dry", o.dry, "--real", realPath, "--stage", o.stage, "--label-filter", o.filter, "--out", o.out}
	code, stderr := execute(t, append(args, o.extra...)...)
	return checkRun{code: code, stderr: stderr, out: o.out}
}

func assertRejected(t *testing.T, r checkRun, code nerrors.Code, exit int, fragments ...string) {
	t.Helper()
	if r.code != exit {
		t.Errorf("exit = %d, want %d; stderr: %s", r.code, exit, r.stderr)
	}
	if !strings.Contains(r.stderr, "["+string(code)+"]") {
		t.Errorf("stderr lacks the code %s: %s", code, r.stderr)
	}
	for _, fragment := range fragments {
		if !strings.Contains(r.stderr, fragment) {
			t.Errorf("stderr lacks %q: %s", fragment, r.stderr)
		}
	}
	if pathExists(r.out) {
		t.Error("a rejected report must leave no coverage record")
	}
}

// realReport writes a run's report with edit applied to a copy of the fixture.
func realReport(t *testing.T, dir string, edit func(*testing.T, *editableReport)) string {
	t.Helper()
	r := newReport(t, false)
	if edit != nil {
		edit(t, r)
	}
	return r.write(t, dir, "real.junit.xml")
}

func removeNamed(t *testing.T, r *editableReport, keep func(string) bool) {
	t.Helper()
	for _, name := range r.names(keep) {
		r.remove(t, name)
	}
}

func TestAcceptedRuns(t *testing.T) {
	t.Run("a complete full run is recorded with its groups", func(t *testing.T) {
		r := runCheck(t, t.TempDir(), realReport(t, t.TempDir(), nil))
		if r.code != 0 {
			t.Fatalf("exit = %d: %s", r.code, r.stderr)
		}
		record := r.record(t)
		want := map[string]any{
			"schema":       "nvkr.dev/hardware-e2e-coverage/v1",
			"stage":        "full",
			"complete":     true,
			"labelFilter":  fixtureFilter,
			"tenantLabel":  defaultTenantLabel,
			"selected":     float64(80),
			"passed":       float64(80),
			"specsInSuite": float64(82),
		}
		for key, value := range want {
			if record[key] != value {
				t.Errorf("%s = %v, want %v", key, record[key], value)
			}
		}
		groups, _ := json.Marshal(record["categories"])
		const wantGroups = `{"provisioning":{"passed":7,"selected":7},"site-checks":{"passed":31,"selected":31},"tenant":{"passed":42,"selected":42}}`
		if string(groups) != wantGroups {
			t.Errorf("categories = %s, want %s", groups, wantGroups)
		}
		for _, key := range []string{"dryRunSha256", "reportSha256"} {
			if digest, _ := record[key].(string); len(digest) != 64 {
				t.Errorf("%s = %v, want a SHA-256 hex digest", key, record[key])
			}
		}
	})

	t.Run("the record is canonical JSON with sorted keys and a trailing newline", func(t *testing.T) {
		r := runCheck(t, t.TempDir(), realReport(t, t.TempDir(), nil))
		data, err := os.ReadFile(r.out)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		again, err := marshalRecord(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, again) {
			t.Errorf("record is not canonical:\n%s\nwant\n%s", data, again)
		}
		if !strings.HasPrefix(string(data), "{\n  \"categories\": {\n    \"provisioning\"") || !bytes.HasSuffix(data, []byte("}\n")) {
			t.Errorf("record is not two-space indented with a trailing newline:\n%s", data)
		}
		if !strings.Contains(string(data), `"labelFilter": "site-controller && !nico-dsx"`) {
			t.Errorf("a label filter must keep its && unescaped:\n%s", data)
		}
	})

	t.Run("a report without the filtered-out specs is accepted", func(t *testing.T) {
		// The suite can also write a report of only the selected specs.
		path := realReport(t, t.TempDir(), func(t *testing.T, r *editableReport) {
			for _, name := range r.names(func(string) bool { return true }) {
				if hasChild(r.find(t, name), "skipped") {
					r.remove(t, name)
				}
			}
		})
		if r := runCheck(t, t.TempDir(), path); r.code != 0 {
			t.Errorf("exit = %d: %s", r.code, r.stderr)
		}
	})

	t.Run("hooks are not specs and may be absent", func(t *testing.T) {
		path := realReport(t, t.TempDir(), func(t *testing.T, r *editableReport) {
			removeNamed(t, r, func(name string) bool { return !strings.HasPrefix(name, "[It] ") })
		})
		r := runCheck(t, t.TempDir(), path)
		if r.code != 0 {
			t.Fatalf("exit = %d: %s", r.code, r.stderr)
		}
		if got := r.record(t)["selected"]; got != float64(80) {
			t.Errorf("selected = %v, want 80", got)
		}
	})

	t.Run("a renamed tenant label is accepted when the caller says so", func(t *testing.T) {
		rename := func(r *editableReport) {
			for _, element := range r.suite.descendants("testcase") {
				element.attrs["name"] = strings.ReplaceAll(element.attrs["name"], defaultTenantLabel+",", "nico-other,")
			}
		}
		dir := t.TempDir()
		dry := newReport(t, true)
		rename(dry)
		actual := newReport(t, false)
		rename(actual)
		r := runCheck(t, dir, actual.write(t, dir, "real.junit.xml"),
			withDry(dry.write(t, dir, "dry.junit.xml")), withExtra("--tenant-label", "nico-other"))
		if r.code != 0 {
			t.Fatalf("exit = %d: %s", r.code, r.stderr)
		}
		groups := r.record(t)["categories"].(map[string]any)["tenant"].(map[string]any)
		if groups["selected"] != float64(42) {
			t.Errorf("tenant group = %v, want 42 selected", groups)
		}
	})

	t.Run("specs the filter left out are not required", func(t *testing.T) {
		// The fixture lists two filtered-out specs as skipped; neither is expected or counted.
		r := runCheck(t, t.TempDir(), realReport(t, t.TempDir(), nil))
		if r.record(t)["selected"] != float64(80) {
			t.Errorf("selected = %v, want 80", r.record(t)["selected"])
		}
	})
}

func TestCoverageShortfalls(t *testing.T) {
	underSelect := func(t *testing.T, r *editableReport) {
		removeNamed(t, r, func(name string) bool { return strings.Contains(name, defaultTenantLabel) })
	}

	t.Run("a filter that selects less than the dry run is caught", func(t *testing.T) {
		// The case -ginkgo.fail-on-empty cannot see: the run selects some specs, just not all of them.
		r := runCheck(t, t.TempDir(), realReport(t, t.TempDir(), underSelect))
		assertRejected(t, r, nerrors.CodeReleaseIncomplete, 41, "coverage shortfall at stage full", "missing from the run's report")
		if !strings.Contains(r.stderr, tenantSpec) {
			t.Errorf("stderr does not name %q: %s", tenantSpec, r.stderr)
		}
	})

	t.Run("shortfalls are listed and the list is bounded", func(t *testing.T) {
		r := runCheck(t, t.TempDir(), realReport(t, t.TempDir(), underSelect))
		if want := fmt.Sprintf("... and %d more", 42-maxFindings); !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q: %s", want, r.stderr)
		}
		if got := strings.Count(r.stderr, "missing from the run's report"); got != maxFindings {
			t.Errorf("listed %d findings, want %d", got, maxFindings)
		}
	})

	cases := []struct {
		name     string
		edit     func(*testing.T, *editableReport)
		fragment string
	}{
		{"a selected spec is missing", func(t *testing.T, r *editableReport) { r.remove(t, provisioningSpec) },
			"missing from the run's report: " + provisioningSpec},
		{"a selected spec is skipped at the full stage", func(t *testing.T, r *editableReport) { r.skip(t, siteCheckSpec, "not configured") },
			"skipped at the full stage: " + siteCheckSpec},
		{"a selected spec failed", func(t *testing.T, r *editableReport) { r.fail(t, tenantSpec) }, "failed: " + tenantSpec},
		{"a spec is panicked", func(t *testing.T, r *editableReport) { r.setStatus(t, tenantSpec, "panicked") }, "failed: " + tenantSpec},
		{"a spec was interrupted", func(t *testing.T, r *editableReport) { r.setStatus(t, tenantSpec, "interrupted") }, "failed: " + tenantSpec},
		{"a spec is pending", func(t *testing.T, r *editableReport) { r.setStatus(t, tenantSpec, "pending") }, "failed: " + tenantSpec},
		{"a spec has an unknown status", func(t *testing.T, r *editableReport) { r.setStatus(t, tenantSpec, "mystery") }, "failed: " + tenantSpec},
		{"a spec the dry run did not select ran", func(_ *testing.T, r *editableReport) { r.addPassed(notSelectedSpec) },
			"ran a spec the dry run did not select: " + notSelectedSpec},
		{"the label filter differs", func(t *testing.T, r *editableReport) { r.setProp(t, "LabelFilter", "site-controller") },
			`label filter "site-controller" is not the dry run's`},
		{"the suite did not report success", func(t *testing.T, r *editableReport) { r.setProp(t, "SuiteSucceeded", "false") },
			"the suite did not report success"},
		{"a hook failed", func(t *testing.T, r *editableReport) { r.fail(t, "[AfterSuite]") }, "[AfterSuite] failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runCheck(t, t.TempDir(), realReport(t, t.TempDir(), tc.edit))
			assertRejected(t, r, nerrors.CodeReleaseIncomplete, 41, tc.fragment)
		})
	}

	t.Run("a spec the run chose to skip is still a shortfall at full", func(t *testing.T) {
		path := realReport(t, t.TempDir(), func(t *testing.T, r *editableReport) { r.skip(t, provisioningSpec, "stage") })
		assertRejected(t, runCheck(t, t.TempDir(), path), nerrors.CodeReleaseIncomplete, 41, "skipped at the full stage")
	})

	t.Run("an extra spec that did not run is not a shortfall", func(t *testing.T) {
		path := realReport(t, t.TempDir(), func(t *testing.T, r *editableReport) {
			r.addPassed(notSelectedSpec)
			r.skip(t, notSelectedSpec, "")
		})
		if r := runCheck(t, t.TempDir(), path); r.code != 0 {
			t.Errorf("exit = %d: %s", r.code, r.stderr)
		}
	})

	t.Run("a duplicate spec in the run is ambiguous", func(t *testing.T) {
		path := realReport(t, t.TempDir(), func(_ *testing.T, r *editableReport) { r.addPassed(tenantSpec) })
		assertRejected(t, runCheck(t, t.TempDir(), path), nerrors.CodeUnsupported, 13, "more than once", tenantSpec)
	})
}

func TestEarlierStages(t *testing.T) {
	stageReport := func(t *testing.T, failed string, passedOnly func(string) bool) string {
		return realReport(t, t.TempDir(), func(t *testing.T, r *editableReport) {
			for _, element := range r.suite.descendants("testcase") {
				name := element.attrs["name"]
				if !strings.HasPrefix(name, "[It] ") || hasChild(element, "skipped") {
					continue
				}
				if passedOnly != nil && !passedOnly(name) {
					element.attrs["status"] = "skipped"
					element.children = append(element.children, &xmlElement{name: "skipped", attrs: map[string]string{"message": "later stage"}})
				}
			}
			if failed != "" {
				r.fail(t, failed)
			}
		})
	}

	t.Run("later specs may skip and the record says it is incomplete", func(t *testing.T) {
		path := stageReport(t, "", func(name string) bool { return name == provisioningSpec || name == siteCheckSpec })
		r := runCheck(t, t.TempDir(), path, withStage("provision"))
		if r.code != 0 {
			t.Fatalf("exit = %d: %s", r.code, r.stderr)
		}
		record := r.record(t)
		if record["complete"] != false || record["stage"] != "provision" || record["passed"] != float64(2) || record["selected"] != float64(80) {
			t.Errorf("record = %v", record)
		}
		tenant, _ := json.Marshal(record["categories"].(map[string]any)["tenant"])
		if string(tenant) != `{"passed":0,"selected":42}` {
			t.Errorf("tenant group = %s", tenant)
		}
	})

	t.Run("the same report is not accepted at the full stage", func(t *testing.T) {
		path := stageReport(t, "", func(name string) bool { return name == provisioningSpec })
		dir := t.TempDir()
		if r := runCheck(t, dir, path, withStage("provision")); r.code != 0 {
			t.Fatalf("provision: exit = %d: %s", r.code, r.stderr)
		}
		assertRejected(t, runCheck(t, dir, path, withStage("full"), withOut(filepath.Join(dir, "full.json"))),
			nerrors.CodeReleaseIncomplete, 41, "skipped at the full stage")
	})

	t.Run("a failure is a shortfall at every stage", func(t *testing.T) {
		for _, stage := range []string{"provision", "bootstrap", "deployment"} {
			t.Run(stage, func(t *testing.T) {
				r := runCheck(t, t.TempDir(), stageReport(t, provisioningSpec, nil), withStage(stage))
				assertRejected(t, r, nerrors.CodeReleaseIncomplete, 41, "failed: "+provisioningSpec)
			})
		}
	})

	t.Run("every selected spec must still be listed", func(t *testing.T) {
		path := realReport(t, t.TempDir(), func(t *testing.T, r *editableReport) { r.remove(t, tenantSpec) })
		assertRejected(t, runCheck(t, t.TempDir(), path, withStage("deployment")),
			nerrors.CodeReleaseIncomplete, 41, "missing from the run's report: "+tenantSpec)
	})

	t.Run("a stage run where nothing passed is a shortfall", func(t *testing.T) {
		path := stageReport(t, "", func(string) bool { return false })
		assertRejected(t, runCheck(t, t.TempDir(), path, withStage("provision")),
			nerrors.CodeReleaseIncomplete, 41, "no selected spec passed")
	})

	t.Run("suite success is not required below the full stage", func(t *testing.T) {
		path := realReport(t, t.TempDir(), func(t *testing.T, r *editableReport) {
			r.setProp(t, "SuiteSucceeded", "false")
			for _, name := range r.names(func(n string) bool { return strings.HasPrefix(n, "[It] ") && !strings.Contains(n, "provisions a node") }) {
				if !hasChild(r.find(t, name), "skipped") {
					r.skip(t, name, "later stage")
				}
			}
		})
		if r := runCheck(t, t.TempDir(), path, withStage("provision")); r.code != 0 {
			t.Errorf("exit = %d: %s", r.code, r.stderr)
		}
	})
}

func TestDryRunAdmission(t *testing.T) {
	cases := []struct {
		name     string
		edit     func(*testing.T, *editableReport)
		fragment string
	}{
		{"the dry run did not succeed", func(t *testing.T, r *editableReport) { r.setProp(t, "SuiteSucceeded", "false") }, "the dry run did not succeed"},
		{"the filter is not the one the workflow passes", func(t *testing.T, r *editableReport) { r.setProp(t, "LabelFilter", "!nico-dsx") },
			`is not the "site-controller && !nico-dsx" the workflow expects`},
		{"a selected spec lacks the site-controller label", func(t *testing.T, r *editableReport) {
			r.find(t, provisioningSpec).attrs["name"] = "[It] NVKE e2e qualification provisions a node [needs:cpu=2, needs:gpu=1]"
		}, "does not carry the site-controller label"},
		{"a selected spec has no labels", func(t *testing.T, r *editableReport) {
			r.find(t, tenantSpec).attrs["name"] = "[It] NVKE e2e qualification tenant on the built site " + defaultTenantLabel + " creates the cluster"
		}, "carries no labels"},
		{"a selected spec failed in the dry run", func(t *testing.T, r *editableReport) { r.fail(t, tenantSpec) }, "the dry run reports"},
		{"a spec is listed twice", func(_ *testing.T, r *editableReport) { r.addPassed(tenantSpec) }, "more than once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			dry := newReport(t, true)
			tc.edit(t, dry)
			r := runCheck(t, dir, realReport(t, dir, nil), withDry(dry.write(t, dir, "dry.junit.xml")))
			assertRejected(t, r, nerrors.CodeUnsupported, 13, tc.fragment)
		})
	}

	t.Run("an empty group means the harness changed", func(t *testing.T) {
		// Rename the labels the groups are built from; the lane must not silently shrink.
		groups := []struct {
			group string
			extra []string
			edit  func(*editableReport)
		}{
			{"tenant", []string{"--tenant-label", "nico-missing"}, func(*editableReport) {}},
			{"site-checks", nil, func(r *editableReport) {
				for _, element := range r.suite.descendants("testcase") {
					for label := range siteCheckLabels {
						element.attrs["name"] = strings.ReplaceAll(element.attrs["name"], ", "+label, "")
					}
				}
			}},
			{"provisioning", nil, func(r *editableReport) {
				for _, element := range r.suite.descendants("testcase") {
					name := element.attrs["name"]
					if !strings.HasPrefix(name, "[It] ") || strings.Contains(name, defaultTenantLabel) || !strings.HasSuffix(name, "]") {
						continue
					}
					site := false
					for label := range siteCheckLabels {
						site = site || strings.Contains(name, label)
					}
					if !site {
						element.attrs["name"] = strings.Replace(name, "]", ", readiness]", 1)
					}
				}
			}},
		}
		for _, g := range groups {
			t.Run(g.group, func(t *testing.T) {
				dir := t.TempDir()
				dry := newReport(t, true)
				g.edit(dry)
				r := runCheck(t, dir, realReport(t, dir, nil), withDry(dry.write(t, dir, "dry.junit.xml")), withExtra(g.extra...))
				assertRejected(t, r, nerrors.CodeUnsupported, 13, "selects no specs for", g.group, "the harness changed")
			})
		}
	})
}

func TestStageRegistration(t *testing.T) {
	// The harness registers specs by stage, so a lower stage's dry run has no tenant or site-check specs at all.
	// These dry runs are trimmed the way the harness trims them (the specs are absent, not skipped), which is what
	// the stage-by-stage coverage check has to accept.
	trimmed := func(t *testing.T, dir string, groups ...string) (dry, actual string) {
		t.Helper()
		probe, err := readReport(newReport(t, true).write(t, dir, "probe.xml"))
		if err != nil {
			t.Fatal(err)
		}
		var doomed []string
		for _, c := range probe.cases {
			if !c.isSpec || c.status == statusSkipped {
				continue
			}
			for _, group := range groups {
				if categoryOf(c, defaultTenantLabel) == group {
					doomed = append(doomed, c.name)
				}
			}
		}
		dryReport, realRun := newReport(t, true), newReport(t, false)
		for _, name := range doomed {
			dryReport.remove(t, name)
			realRun.remove(t, name)
		}
		return dryReport.write(t, dir, "dry.junit.xml"), realRun.write(t, dir, "real.junit.xml")
	}

	t.Run("each stage is asked only for what it registers", func(t *testing.T) {
		cases := []struct {
			stage          string
			absent, active []string
		}{
			{"provision", []string{"tenant", "site-checks"}, []string{"provisioning"}},
			{"bootstrap", []string{"tenant", "site-checks"}, []string{"provisioning"}},
			{"deployment", []string{"tenant"}, []string{"provisioning", "site-checks"}},
		}
		for _, tc := range cases {
			t.Run(tc.stage, func(t *testing.T) {
				dir := t.TempDir()
				dry, actual := trimmed(t, dir, tc.absent...)
				r := runCheck(t, dir, actual, withDry(dry), withStage(tc.stage))
				if r.code != 0 {
					t.Fatalf("exit = %d: %s", r.code, r.stderr)
				}
				record := r.record(t)
				if record["complete"] != false || record["selected"].(float64) <= 0 || record["passed"] != record["selected"] {
					t.Errorf("record = %v", record)
				}
				categories := record["categories"].(map[string]any)
				for _, group := range tc.absent {
					if got, _ := json.Marshal(categories[group]); string(got) != `{"passed":0,"selected":0}` {
						t.Errorf("%s group = %s, want none", group, got)
					}
				}
				for _, group := range tc.active {
					if categories[group].(map[string]any)["selected"].(float64) <= 0 {
						t.Errorf("%s group is empty", group)
					}
				}
			})
		}
	})

	t.Run("the full stage still needs all three groups", func(t *testing.T) {
		for _, absent := range []string{"tenant", "site-checks", "provisioning"} {
			t.Run(absent, func(t *testing.T) {
				dir := t.TempDir()
				dry, actual := trimmed(t, dir, absent)
				r := runCheck(t, dir, actual, withDry(dry), withStage("full"))
				assertRejected(t, r, nerrors.CodeUnsupported, 13, "the dry run at stage full selects no specs for", absent)
			})
		}
	})

	t.Run("a stage still needs the groups it registers", func(t *testing.T) {
		cases := []struct{ stage, absent string }{
			{"deployment", "site-checks"}, {"deployment", "provisioning"}, {"provision", "provisioning"}, {"bootstrap", "provisioning"},
		}
		for _, tc := range cases {
			t.Run(tc.stage+" without "+tc.absent, func(t *testing.T) {
				dir := t.TempDir()
				dry, actual := trimmed(t, dir, tc.absent)
				r := runCheck(t, dir, actual, withDry(dry), withStage(tc.stage))
				assertRejected(t, r, nerrors.CodeUnsupported, 13, "the dry run at stage "+tc.stage+" selects no specs for", tc.absent)
			})
		}
	})

	t.Run("the required groups are the harness's stage gates", func(t *testing.T) {
		if len(requiredGroups) != len(stages) {
			t.Fatalf("requiredGroups has %d stages, want %d", len(requiredGroups), len(stages))
		}
		for _, stage := range stages {
			if _, ok := requiredGroups[stage]; !ok {
				t.Errorf("no required groups for stage %s", stage)
			}
		}
		if strings.Join(requiredGroups["full"], ",") != strings.Join(categories, ",") {
			t.Errorf("full requires %v, want every category %v", requiredGroups["full"], categories)
		}
		for _, group := range requiredGroups["deployment"] {
			if group == "tenant" {
				t.Error("the deployment stage must not require the tenant group")
			}
		}
		for _, group := range requiredGroups["bootstrap"] {
			if group == "site-checks" {
				t.Error("the bootstrap stage must not require the site-checks group")
			}
		}
	})

	t.Run("the stages are the harness's", func(t *testing.T) {
		if got := strings.Join(stages, ","); got != "provision,bootstrap,deployment,full" {
			t.Errorf("stages = %s", got)
		}
	})
}

func TestDryRunFlag(t *testing.T) {
	// A report says which kind it is, and each place the check reads one demands the right kind.
	t.Run("a dry run report cannot stand in for the run", func(t *testing.T) {
		// The committed fixture is a dry run's report; passed as both, it must not read as complete coverage.
		r := runCheck(t, t.TempDir(), fixturePath)
		assertRejected(t, r, nerrors.CodeUnsupported, 13, `the run's report carries DryRun="true", not DryRun="false"`)
	})

	t.Run("the selection must be a dry run report", func(t *testing.T) {
		dir := t.TempDir()
		selection := newReport(t, false).write(t, dir, "selection.junit.xml")
		r := runCheck(t, dir, realReport(t, dir, nil), withDry(selection))
		assertRejected(t, r, nerrors.CodeUnsupported, 13, `the dry run's report carries DryRun="false", not DryRun="true"`)
	})

	t.Run("a report that does not say what it is is refused", func(t *testing.T) {
		dir := t.TempDir()
		path := realReport(t, dir, func(t *testing.T, r *editableReport) { r.dropProp(t, "DryRun") })
		assertRejected(t, runCheck(t, dir, path), nerrors.CodeUnsupported, 13, "the run's report carries no DryRun property")

		dry := newReport(t, true)
		dry.dropProp(t, "DryRun")
		r := runCheck(t, dir, newReport(t, false).write(t, dir, "r.xml"), withDry(dry.write(t, dir, "d.xml")), withOut(filepath.Join(dir, "b.json")))
		assertRejected(t, r, nerrors.CodeUnsupported, 13, "the dry run's report carries no DryRun property")
	})
}

func TestDryOnly(t *testing.T) {
	// The dry run is admitted before a machine is held, so a harness that changed is found for free.
	dryArgs := func(dry, stage string, extra ...string) []string {
		return append([]string{"check-junit", "--dry", dry, "--stage", stage, "--label-filter", fixtureFilter, "--dry-only"}, extra...)
	}

	t.Run("a usable dry run is admitted and nothing is written", func(t *testing.T) {
		dir := t.TempDir()
		before, _ := os.ReadDir(dir)
		code, stderr := execute(t, dryArgs(fixturePath, "full")...)
		after, _ := os.ReadDir(dir)
		if code != 0 || stderr != "" || len(before) != len(after) {
			t.Errorf("exit = %d, stderr = %q, files %d -> %d", code, stderr, len(before), len(after))
		}
	})

	t.Run("a dry run that will fail the check is refused before any machine", func(t *testing.T) {
		cases := []struct {
			name, stage, fragment string
			edit                  func(*testing.T, *editableReport)
			extra                 []string
		}{
			{"the harness changed its tenant label", "full", "tenant", func(*testing.T, *editableReport) {}, []string{"--tenant-label", "nico-missing"}},
			{"the filter is not the one passed", "full", "is not the", func(t *testing.T, r *editableReport) { r.setProp(t, "LabelFilter", "site-controller") }, nil},
			{"the dry run failed", "provision", "the dry run did not succeed", func(t *testing.T, r *editableReport) { r.setProp(t, "SuiteSucceeded", "false") }, nil},
			{"the report is a real run's", "provision", `DryRun="false"`, func(t *testing.T, r *editableReport) { r.setProp(t, "DryRun", "false") }, nil},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				report := newReport(t, true)
				tc.edit(t, report)
				code, stderr := execute(t, dryArgs(report.write(t, t.TempDir(), "dry.xml"), tc.stage, tc.extra...)...)
				if code != 13 || !strings.Contains(stderr, "[NVKR-E-UNSUPPORTED]") || !strings.Contains(stderr, tc.fragment) {
					t.Errorf("exit = %d, stderr = %s", code, stderr)
				}
			})
		}
	})

	t.Run("a lower stage dry run with only the groups it registers is admitted", func(t *testing.T) {
		probe, err := readReport(fixturePath)
		if err != nil {
			t.Fatal(err)
		}
		keep := newReport(t, true)
		for _, c := range probe.cases {
			if c.isSpec && c.status != statusSkipped && categoryOf(c, defaultTenantLabel) != "provisioning" {
				keep.remove(t, c.name)
			}
		}
		path := keep.write(t, t.TempDir(), "provision-dry.xml")
		if code, stderr := execute(t, dryArgs(path, "provision")...); code != 0 {
			t.Errorf("provision: exit = %d: %s", code, stderr)
		}
		if code, _ := execute(t, dryArgs(path, "full")...); code != 13 {
			t.Errorf("full: exit = %d, want 13", code)
		}
	})
}

func TestMalformedReports(t *testing.T) {
	good := newReport(t, true).write(t, t.TempDir(), "good.junit.xml")

	t.Run("unusable reports are unsupported", func(t *testing.T) {
		dir := t.TempDir()
		text := func(name, content string) string {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}
		cases := []struct{ name, path, fragment string }{
			{"not XML", text("not-xml.xml", "not xml"), "not well-formed XML"},
			{"DTD", text("dtd.xml", `<!DOCTYPE x [<!ENTITY a "b">]><testsuite name="s"/>`), "declares a DTD or entity"},
			{"entity only", text("entity.xml", `<!ENTITY a "b"><testsuite name="s"/>`), "declares a DTD or entity"},
			{"two suites", text("two.xml", "<testsuites><testsuite name='a'/><testsuite name='b'/></testsuites>"), "2 test suites"},
			{"no suite", text("none.xml", "<testsuites/>"), "0 test suites"},
			{"unnamed case", text("unnamed.xml", "<testsuite name='s'><testcase/></testsuite>"), "no name"},
			{"junk after the document", text("junk.xml", "<testsuite name='s'/><testsuite name='t'/>"), "not well-formed XML"},
			{"text after the document", text("tail.xml", "<testsuite name='s'/>tail"), "not well-formed XML"},
			{"truncated", text("truncated.xml", "<testsuite name='s'><testcase name='a'>"), "not well-formed XML"},
			{"missing file", filepath.Join(dir, "absent.xml"), "cannot read"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				r := runCheck(t, t.TempDir(), tc.path, withDry(good))
				assertRejected(t, r, nerrors.CodeUnsupported, 13, tc.fragment)
			})
		}
	})

	t.Run("a malformed dry run is rejected the same way", func(t *testing.T) {
		dir := t.TempDir()
		bad := filepath.Join(dir, "dry.xml")
		if err := os.WriteFile(bad, []byte("not xml"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := runCheck(t, dir, realReport(t, dir, nil), withDry(bad))
		assertRejected(t, r, nerrors.CodeUnsupported, 13, "not well-formed XML")
	})

	t.Run("an oversized report is refused before parsing", func(t *testing.T) {
		_, err := readReportLimited(good, 100)
		assertCoded(t, err, nerrors.CodeUnsupported, "exceeds 100 bytes")
	})

	t.Run("a report is never read past the limit", func(t *testing.T) {
		// /dev/zero never ends: a reader that held the whole file would not return.
		if _, err := os.Stat("/dev/zero"); err != nil {
			t.Skip("no /dev/zero on this system")
		}
		_, err := readReportLimited("/dev/zero", 100)
		assertCoded(t, err, nerrors.CodeUnsupported, "exceeds 100 bytes")
	})
}

func assertCoded(t *testing.T, err error, code nerrors.Code, fragment string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s %q", code, fragment)
	}
	if !strings.HasPrefix(err.Error(), "["+string(code)+"]") || !strings.Contains(err.Error(), fragment) {
		t.Errorf("error = %q, want code %s containing %q", err, code, fragment)
	}
}

func TestCaseStatus(t *testing.T) {
	cases := []struct {
		attrs    map[string]string
		children []string
		want     string
	}{
		{map[string]string{"status": "passed"}, nil, "passed"},
		{map[string]string{"status": "skipped"}, nil, "skipped"},
		{map[string]string{"status": "skipped"}, []string{"skipped"}, "skipped"},
		{map[string]string{"status": "passed"}, []string{"skipped"}, "skipped"},
		{map[string]string{"status": "failed"}, nil, "failed"},
		{map[string]string{"status": "passed"}, []string{"failure"}, "failed"},
		{map[string]string{"status": "passed"}, []string{"error"}, "failed"},
		{map[string]string{"status": "panicked"}, nil, "failed"},
		{map[string]string{"status": "unheard-of"}, nil, "failed"},
		{map[string]string{}, nil, "passed"},
		{map[string]string{}, []string{"skipped"}, "skipped"},
		{map[string]string{}, []string{"failure"}, "failed"},
		{map[string]string{"status": "skipped"}, []string{"failure"}, "failed"},
		// A skip marker explains a spec that did not run; it cannot hide one that ended another way.
		{map[string]string{"status": "interrupted"}, []string{"skipped"}, "failed"},
		{map[string]string{"status": "panicked"}, []string{"skipped"}, "failed"},
		{map[string]string{"status": "unheard-of"}, []string{"skipped"}, "failed"},
		{map[string]string{"status": "pending"}, []string{"skipped"}, "skipped"},
		{map[string]string{"status": "pending"}, nil, "failed"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%v/%v", tc.attrs, tc.children), func(t *testing.T) {
			element := &xmlElement{name: "testcase", attrs: tc.attrs}
			for _, child := range tc.children {
				element.children = append(element.children, &xmlElement{name: child, attrs: map[string]string{}})
			}
			if got := caseStatus(element); got != tc.want {
				t.Errorf("caseStatus = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCommandLine(t *testing.T) {
	t.Run("an existing record is never replaced", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "coverage.json")
		if err := os.WriteFile(out, []byte("kept"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := runCheck(t, dir, realReport(t, dir, nil), withOut(out))
		if r.code != 47 || !strings.Contains(r.stderr, "[NVKR-E-IMMUTABLE-VIOLATION]") {
			t.Errorf("exit = %d, stderr = %s", r.code, r.stderr)
		}
		if got, _ := os.ReadFile(out); string(got) != "kept" {
			t.Errorf("the record was replaced: %q", got)
		}
	})

	t.Run("a symlink at the output is not followed", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("kept"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "coverage.json")
		if err := os.Symlink(target, out); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
		r := runCheck(t, dir, realReport(t, dir, nil), withOut(out))
		if r.code != 47 {
			t.Errorf("exit = %d, want 47: %s", r.code, r.stderr)
		}
		if got, _ := os.ReadFile(target); string(got) != "kept" {
			t.Errorf("the link target was written: %q", got)
		}
	})

	t.Run("a dangling symlink at the output is not followed", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "coverage.json")
		target := filepath.Join(dir, "never-created")
		if err := os.Symlink(target, out); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
		r := runCheck(t, dir, realReport(t, dir, nil), withOut(out))
		if r.code != 47 || pathExistsFollowing(target) {
			t.Errorf("exit = %d, target created = %v: %s", r.code, pathExistsFollowing(target), r.stderr)
		}
	})

	t.Run("usage errors are coded and exit 2", func(t *testing.T) {
		dir := t.TempDir()
		realPath := realReport(t, dir, nil)
		out := filepath.Join(dir, "x")
		base := []string{"check-junit", "--dry", fixturePath, "--label-filter", fixtureFilter}
		with := func(extra ...string) []string { return append(append([]string{}, base...), extra...) }
		cases := []struct {
			name string
			argv []string
		}{
			{"unknown stage", with("--real", realPath, "--stage", "rollout", "--out", out)},
			{"missing stage", with("--real", realPath, "--out", out)},
			{"missing out", with("--real", realPath, "--stage", "full")},
			{"missing real", with("--stage", "full", "--out", out)},
			{"missing label filter", []string{"check-junit", "--dry", fixturePath, "--real", realPath, "--stage", "full", "--out", out}},
			{"missing dry", []string{"check-junit", "--label-filter", fixtureFilter, "--real", realPath, "--stage", "full", "--out", out}},
			{"unknown flag", with("--real", realPath, "--stage", "full", "--out", out, "--force")},
			{"a positional argument", with("--real", realPath, "--stage", "full", "--out", out, "extra")},
			{"dry-only with a real report", with("--dry-only", "--stage", "full", "--real", realPath)},
			{"dry-only with an output", with("--dry-only", "--stage", "full", "--out", out)},
			{"no subcommand", nil},
			{"unknown subcommand", []string{"check-everything"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				code, stderr := execute(t, tc.argv...)
				if code != 2 || !strings.Contains(stderr, "[NVKR-E-USAGE]") || strings.Count(stderr, "\n") != 1 {
					t.Errorf("exit = %d, stderr = %q; want one coded usage line and exit 2", code, stderr)
				}
			})
		}
	})

	t.Run("help prints usage and exits 0", func(t *testing.T) {
		code, stderr := execute(t, "check-junit", "-h")
		if code != 0 || !strings.Contains(stderr, "usage: hwe2e check-junit") {
			t.Errorf("exit = %d, stderr = %q", code, stderr)
		}
	})
}

func pathExistsFollowing(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
