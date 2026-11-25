// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// check-junit: check that a hardware E2E Ginkgo JUnit covers what its harness commit selects.
//
// -ginkgo.fail-on-empty only fails a run that selects nothing; a label filter that selects less than the lane
// intends still passes. This closes that gap without a hand-kept list of spec names. A Ginkgo dry run (no
// machine) at the same harness commit, with the same flags, lists the specs the run intends to execute. The
// real run's JUnit must then contain every one of them as passed, must have run nothing the dry run did not
// select, and must have used the same label filter.
//
// The selected specs fall into three groups by label, so a harness change that renames the labels fails here
// instead of silently shrinking the lane:
//
//	provisioning  site-level specs with none of the check labels (provision, bootstrap, deploy)
//	site-checks   site-level specs labeled readiness, site-software or site-distro
//	tenant        specs labeled with the tenant label (the harness's own, see --tenant-label)
//
// The harness registers a stage's specs by stage: the site checks join at the deployment stage and the tenant
// specs at the full stage. A group is therefore required only from the stage that registers it
// (requiredGroups), and the dry run is the same run the workflow lists before it takes a machine: --dry-only
// admits the dry run alone, so a harness change that empties a required group is found before any hardware is
// held. Both reports must say which kind they are: the selection is written by a Ginkgo dry run (DryRun=true)
// and the execution report by a real run (DryRun=false), so a dry-run report can never stand in for a run.
//
// This is a coverage check on a report. It does not judge whether a spec asserts anything useful, and it
// proves nothing about the hardware: the report is whatever the suite wrote. Below the full stage the harness
// skips later specs by design, so only listing and absence of failures are checked there and the record says
// it is not complete.
//
// Exit codes: 0 ok, 2 usage, 13 malformed or unsupported report, 41 coverage shortfall, 47 the output exists.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	coverageSchema      = "nvkr.dev/hardware-e2e-coverage/v1"
	maxReportBytes      = 64 * 1024 * 1024
	maxFindings         = 20
	siteControllerLabel = "site-controller"
	statusPassed        = "passed"
	statusSkipped       = "skipped"
	statusFailed        = "failed"
	// Integration boundary: the pinned harness's own name for the tenant label, not NVKR's.
	defaultTenantLabel = "nico-forge"
)

var (
	stages          = []string{"provision", "bootstrap", "deployment", "full"}
	categories      = []string{"provisioning", "site-checks", "tenant"}
	siteCheckLabels = map[string]bool{"readiness": true, "site-software": true, "site-distro": true}
	// From the harness's own stage gating (controller/test/e2e/qualify_e2e_test.go at the pinned commit): the
	// site checks are registered from the deployment stage and the tenant specs only at the full stage.
	requiredGroups = map[string][]string{
		"provision":  {"provisioning"},
		"bootstrap":  {"provisioning"},
		"deployment": {"provisioning", "site-checks"},
		"full":       {"provisioning", "site-checks", "tenant"},
	}
	specName    = regexp.MustCompile(`^\[It\] (.*?)(?: \[([^\[\]]*)\])?$`)
	doctypeMark = regexp.MustCompile(`<!(DOCTYPE|ENTITY)`)
)

// specCase is one test case of a report. Its status is passed, skipped or failed; failed covers everything else.
type specCase struct {
	name   string
	status string
	labels map[string]bool
	isSpec bool
}

// junitReport is what one Ginkgo JUnit file says, and the digest of its exact bytes.
type junitReport struct {
	cases      []specCase
	properties map[string]string
	sha256     string
}

// specSet keeps specs by name in the order the report listed them, so every finding is deterministic.
type specSet struct {
	order  []string
	byName map[string]specCase
}

func (s specSet) get(name string) (specCase, bool) {
	c, ok := s.byName[name]
	return c, ok
}

// xmlElement is the part of an XML element this check reads.
type xmlElement struct {
	name     string
	attrs    map[string]string
	children []*xmlElement
}

func (e *xmlElement) attr(name string) (string, bool) {
	v, ok := e.attrs[name]
	return v, ok
}

// descendants lists every element below e, in document order, that has the given name.
func (e *xmlElement) descendants(name string) []*xmlElement {
	var found []*xmlElement
	for _, child := range e.children {
		if child.name == name {
			found = append(found, child)
		}
		found = append(found, child.descendants(name)...)
	}
	return found
}

func qualified(name xml.Name) string {
	if name.Space == "" {
		return name.Local
	}
	return "{" + name.Space + "}" + name.Local
}

// parseXML builds the element tree of a single well-formed document and refuses anything after its root.
func parseXML(data []byte) (*xmlElement, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var root *xmlElement
	var open []*xmlElement
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			element := &xmlElement{name: qualified(t.Name), attrs: map[string]string{}}
			for _, a := range t.Attr {
				element.attrs[qualified(a.Name)] = a.Value
			}
			if len(open) == 0 {
				if root != nil {
					return nil, errors.New("junk after document element")
				}
				root = element
			} else {
				parent := open[len(open)-1]
				parent.children = append(parent.children, element)
			}
			open = append(open, element)
		case xml.EndElement:
			open = open[:len(open)-1]
		case xml.CharData:
			if len(open) == 0 && len(bytes.TrimSpace(t)) > 0 {
				return nil, errors.New("text outside the document element")
			}
		}
	}
	if root == nil {
		return nil, errors.New("no element found")
	}
	if len(open) != 0 {
		return nil, errors.New("unexpected end of document")
	}
	return root, nil
}

func readReport(path string) (*junitReport, error) {
	return readReportLimited(path, maxReportBytes)
}

func readReportLimited(path string, limit int) (*junitReport, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, unsupported("cannot read %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	// One byte past the limit is enough to know the report is too large, and no more is ever held.
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, unsupported("cannot read %s: %v", path, err)
	}
	if len(data) > limit {
		return nil, unsupported("%s exceeds %d bytes", path, limit)
	}
	if doctypeMark.Match(data) {
		return nil, unsupported("%s declares a DTD or entity, which a JUnit report never needs", path)
	}
	root, err := parseXML(data)
	if err != nil {
		return nil, unsupported("%s is not well-formed XML: %v", path, err)
	}
	suites := []*xmlElement{root}
	if root.name != "testsuite" {
		suites = root.descendants("testsuite")
	}
	if len(suites) != 1 {
		return nil, unsupported("%s has %d test suites; exactly one is expected", path, len(suites))
	}
	suite := suites[0]
	properties := map[string]string{}
	for _, p := range suite.descendants("property") {
		name, _ := p.attr("name")
		value, _ := p.attr("value")
		properties[name] = value
	}
	var cases []specCase
	for _, element := range suite.descendants("testcase") {
		name, _ := element.attr("name")
		if name == "" {
			return nil, unsupported("%s has a test case with no name", path)
		}
		isSpec := strings.HasPrefix(name, "[It] ")
		labels := map[string]bool{}
		if match := specName.FindStringSubmatch(name); isSpec && match != nil && match[2] != "" {
			for _, label := range strings.Split(match[2], ",") {
				if label = strings.TrimSpace(label); label != "" {
					labels[label] = true
				}
			}
		}
		cases = append(cases, specCase{name: name, status: caseStatus(element), labels: labels, isSpec: isSpec})
	}
	digest := sha256.Sum256(data)
	return &junitReport{cases: cases, properties: properties, sha256: hex.EncodeToString(digest[:])}, nil
}

// caseStatus reads a test case as passed, skipped or failed. Anything that is not clearly one of the first two is
// a failure.
func caseStatus(element *xmlElement) string {
	children := map[string]bool{}
	for _, child := range element.children {
		children[child.name] = true
	}
	if children["failure"] || children["error"] {
		return statusFailed
	}
	status, has := element.attr("status")
	if !has {
		if children["skipped"] {
			return statusSkipped
		}
		return statusPassed
	}
	if status == "skipped" {
		return statusSkipped
	}
	// A skip marker explains a spec that did not run. It cannot hide a spec that ended any other way.
	if children["skipped"] {
		if status == statusPassed || status == "pending" {
			return statusSkipped
		}
		return statusFailed
	}
	if status == statusPassed {
		return statusPassed
	}
	return statusFailed
}

func categoryOf(c specCase, tenantLabel string) string {
	if c.labels[tenantLabel] {
		return "tenant"
	}
	for label := range c.labels {
		if siteCheckLabels[label] {
			return "site-checks"
		}
	}
	return "provisioning"
}

// uniqueSpecs lists the specs of a report by name and refuses a report that lists one twice, because its result
// would then be ambiguous.
func uniqueSpecs(r *junitReport, what string) (specSet, error) {
	set := specSet{byName: map[string]specCase{}}
	for _, c := range r.cases {
		if !c.isSpec {
			continue
		}
		if _, seen := set.byName[c.name]; seen {
			return specSet{}, unsupported("%s lists the spec %q more than once, so its result is ambiguous", what, c.name)
		}
		set.byName[c.name] = c
		set.order = append(set.order, c.name)
	}
	return set, nil
}

// requireDryRunFlag checks the DryRun property: Ginkgo writes it into every report, and a report that does not
// say what it is proves nothing.
func requireDryRunFlag(r *junitReport, want, what string) error {
	have, ok := r.properties["DryRun"]
	if ok && have == want {
		return nil
	}
	said := "no DryRun property"
	if ok {
		said = fmt.Sprintf("DryRun=%q", have)
	}
	return unsupported("%s carries %s, not DryRun=%q, so it is not the kind of report this check reads there", what, said, want)
}

// dryRunSelection is what a dry run says the real run must cover.
type dryRunSelection struct {
	labelFilter string
	inSuite     int
	selected    specSet
	groups      map[string][]string
}

func selectedByDryRun(dry *junitReport, tenantLabel, stage, expectedFilter string) (*dryRunSelection, error) {
	if err := requireDryRunFlag(dry, "true", "the dry run's report"); err != nil {
		return nil, err
	}
	if dry.properties["SuiteSucceeded"] != "true" {
		return nil, unsupported("the dry run did not succeed")
	}
	labelFilter := dry.properties["LabelFilter"]
	if labelFilter != expectedFilter {
		return nil, unsupported("the dry run's label filter %q is not the %q the workflow expects", labelFilter, expectedFilter)
	}
	specs, err := uniqueSpecs(dry, "the dry run")
	if err != nil {
		return nil, err
	}
	selected := specSet{byName: map[string]specCase{}}
	for _, name := range specs.order {
		if c := specs.byName[name]; c.status != statusSkipped {
			selected.byName[name] = c
			selected.order = append(selected.order, name)
		}
	}
	for _, name := range selected.order {
		c := selected.byName[name]
		if c.status != statusPassed {
			return nil, unsupported("the dry run reports %q as %s", name, c.status)
		}
		if len(c.labels) == 0 {
			return nil, unsupported("the selected spec %q carries no labels, so it cannot be grouped", name)
		}
		if !c.labels[siteControllerLabel] {
			return nil, unsupported("the selected spec %q does not carry the %s label the filter selects on", name, siteControllerLabel)
		}
	}
	groups := map[string][]string{}
	for _, category := range categories {
		groups[category] = nil
	}
	for _, name := range selected.order {
		category := categoryOf(selected.byName[name], tenantLabel)
		groups[category] = append(groups[category], name)
	}
	var empty []string
	for _, category := range requiredGroups[stage] {
		if len(groups[category]) == 0 {
			empty = append(empty, category)
		}
	}
	if len(empty) > 0 {
		checks := make([]string, 0, len(siteCheckLabels))
		for label := range siteCheckLabels {
			checks = append(checks, label)
		}
		sort.Strings(checks)
		return nil, unsupported("the dry run at stage %s selects no specs for %s (tenant label %q, check labels %s); the harness changed, so review this check",
			stage, strings.Join(empty, ", "), tenantLabel, strings.Join(checks, ", "))
	}
	return &dryRunSelection{labelFilter: labelFilter, inSuite: len(specs.order), selected: selected, groups: groups}, nil
}

// findingsFor lists every way the run's report falls short of the dry run's selection.
func findingsFor(dryFilter string, selected specSet, actual *junitReport, stage string) ([]string, int, specSet, error) {
	var findings []string
	if realFilter := actual.properties["LabelFilter"]; realFilter != dryFilter {
		findings = append(findings, fmt.Sprintf("the run's label filter %q is not the dry run's %q", realFilter, dryFilter))
	}
	if stage == "full" && actual.properties["SuiteSucceeded"] != "true" {
		findings = append(findings, "the suite did not report success")
	}
	realSpecs, err := uniqueSpecs(actual, "the run")
	if err != nil {
		return nil, 0, specSet{}, err
	}
	for _, c := range actual.cases {
		if !c.isSpec && c.status != statusPassed && c.status != statusSkipped {
			findings = append(findings, fmt.Sprintf("%s %s", c.name, c.status))
		}
	}
	for _, name := range selected.order {
		ran, ok := realSpecs.get(name)
		switch {
		case !ok:
			findings = append(findings, "missing from the run's report: "+name)
		case ran.status == statusFailed:
			findings = append(findings, "failed: "+name)
		case ran.status == statusSkipped && stage == "full":
			findings = append(findings, "skipped at the full stage: "+name)
		}
	}
	for _, name := range realSpecs.order {
		if _, picked := selected.get(name); !picked && realSpecs.byName[name].status != statusSkipped {
			findings = append(findings, "ran a spec the dry run did not select: "+name)
		}
	}
	passed := 0
	for _, name := range selected.order {
		if ran, ok := realSpecs.get(name); ok && ran.status == statusPassed {
			passed++
		}
	}
	if stage != "full" && passed == 0 {
		findings = append(findings, "no selected spec passed")
	}
	return findings, passed, realSpecs, nil
}

// checkSelection admits the dry run alone, before any machine is held.
func checkSelection(dryPath, stage, tenantLabel, expectedFilter string) (*junitReport, *dryRunSelection, error) {
	dry, err := readReport(dryPath)
	if err != nil {
		return nil, nil, err
	}
	selection, err := selectedByDryRun(dry, tenantLabel, stage, expectedFilter)
	if err != nil {
		return nil, nil, err
	}
	return dry, selection, nil
}

func checkCoverage(dryPath, realPath, stage, tenantLabel, expectedFilter string) (map[string]any, error) {
	dry, selection, err := checkSelection(dryPath, stage, tenantLabel, expectedFilter)
	if err != nil {
		return nil, err
	}
	actual, err := readReport(realPath)
	if err != nil {
		return nil, err
	}
	if err := requireDryRunFlag(actual, "false", "the run's report"); err != nil {
		return nil, err
	}
	findings, passed, realSpecs, err := findingsFor(selection.labelFilter, selection.selected, actual, stage)
	if err != nil {
		return nil, err
	}
	if len(findings) > 0 {
		shown := findings
		if len(shown) > maxFindings {
			shown = shown[:maxFindings]
		}
		detail := strings.Join(shown, "\n  ")
		if more := len(findings) - len(shown); more > 0 {
			detail += fmt.Sprintf("\n  ... and %d more", more)
		}
		return nil, nerrors.New(nerrors.CodeReleaseIncomplete, "coverage shortfall at stage %s:\n  %s", stage, detail)
	}
	groups := map[string]any{}
	for _, category := range categories {
		groupPassed := 0
		for _, name := range selection.groups[category] {
			if realSpecs.byName[name].status == statusPassed {
				groupPassed++
			}
		}
		groups[category] = map[string]any{"selected": len(selection.groups[category]), "passed": groupPassed}
	}
	return map[string]any{
		"schema":       coverageSchema,
		"stage":        stage,
		"complete":     stage == "full",
		"labelFilter":  selection.labelFilter,
		"tenantLabel":  tenantLabel,
		"specsInSuite": selection.inSuite,
		"selected":     len(selection.selected.order),
		"passed":       passed,
		"categories":   groups,
		"dryRunSha256": dry.sha256,
		"reportSha256": actual.sha256,
	}, nil
}

const checkJUnitUsage = "--dry FILE --stage STAGE --label-filter FILTER (--dry-only | --real FILE --out FILE) [--tenant-label LABEL]"

func runCheckJUnit(_ context.Context, args []string, _, stderr io.Writer) error {
	set := newCommandFlags("check-junit")
	dry := set.fs.String("dry", "", "JUnit from a Ginkgo dry run at the harness commit")
	realPath := set.fs.String("real", "", "JUnit written by the run being checked")
	stage := set.choice("stage", "the stage the run was dispatched with", stages)
	labelFilter := set.fs.String("label-filter", "", "the label filter the workflow passes to the suite; the dry run must have used exactly it")
	dryOnly := set.fs.Bool("dry-only", false, "admit the dry run and stop, before a machine is held; takes no --real and no --out")
	tenantLabel := set.fs.String("tenant-label", defaultTenantLabel, "the harness's own name for the tenant label")
	out := set.fs.String("out", "", "coverage record to create; must not exist")
	if err := set.parse(args, stderr, checkJUnitUsage); err != nil {
		return err
	}
	given := set.given()
	if err := requireFlags(given, "dry", "stage", "label-filter"); err != nil {
		return err
	}
	if *dryOnly {
		if given["real"] || given["out"] {
			return usageError("--dry-only admits the dry run alone and takes neither --real nor --out")
		}
		_, _, err := checkSelection(*dry, *stage, *tenantLabel, *labelFilter)
		return err
	}
	if *realPath == "" || *out == "" {
		return usageError("--real and --out are required unless --dry-only is given")
	}
	if pathExists(*out) {
		return immutable("%s already exists", *out)
	}
	record, err := checkCoverage(*dry, *realPath, *stage, *tenantLabel, *labelFilter)
	if err != nil {
		return err
	}
	data, err := marshalRecord(record)
	if err != nil {
		return nerrors.Wrap(nerrors.CodeInternal, err, "encode the coverage record")
	}
	return writeNewFile(*out, data, 0o644)
}
