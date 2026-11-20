#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Check that a hardware E2E Ginkgo JUnit covers what its harness commit selects.

`-ginkgo.fail-on-empty` only fails a run that selects nothing; a label filter that selects less than
the lane intends still passes. This closes that gap without a hand-kept list of spec names. A Ginkgo
dry run (no machine) at the same harness commit, with the same flags, lists the specs the run
intends to execute. The real run's JUnit must then contain every one of them as passed, must have run
nothing the dry run did not select, and must have used the same label filter.

The selected specs fall into three groups by label, so a harness change that renames the labels fails
here instead of silently shrinking the lane:

  provisioning   site-level specs with none of the check labels (provision, bootstrap, deploy)
  site-checks    site-level specs labelled readiness, site-software or site-distro
  tenant         specs labelled with the tenant label (the harness's own, see --tenant-label)

The harness registers a stage's specs by stage: the site checks join at the deployment stage and the
tenant specs at the full stage. A group is therefore required only from the stage that registers it
(REQUIRED_GROUPS), and the dry run is the same run the workflow lists before it takes a machine:
`--dry-only` admits the dry run alone, so a harness change that empties a required group is found before
any hardware is held. Both reports must say which kind they are: the selection is written by a Ginkgo
dry run (DryRun=true) and the execution report by a real run (DryRun=false), so a dry-run report can
never stand in for a run.

This is a coverage check on a report. It does not judge whether a spec asserts anything useful, and
it proves nothing about the hardware: the report is whatever the suite wrote. Below the full stage
the harness skips later specs by design, so only listing and absence of failures are checked there
and the record says it is not complete.

Exit codes: 0 ok, 2 usage, 13 malformed or unsupported report, 41 coverage shortfall.
"""

import argparse
from dataclasses import dataclass
import hashlib
import json
from pathlib import Path
import re
import sys
import xml.etree.ElementTree as ET

SCHEMA = "nvkr.dev/hardware-e2e-coverage/v1"
STAGES = ("provision", "bootstrap", "deployment", "full")
SITE_CHECK_LABELS = frozenset({"readiness", "site-software", "site-distro"})
CATEGORIES = ("provisioning", "site-checks", "tenant")
# From the harness's own stage gating (controller/test/e2e/qualify_e2e_test.go at the pinned commit): the
# site checks are registered from the deployment stage and the tenant specs only at the full stage.
REQUIRED_GROUPS = {
    "provision": ("provisioning",),
    "bootstrap": ("provisioning",),
    "deployment": ("provisioning", "site-checks"),
    "full": CATEGORIES,
}
SITE_CONTROLLER_LABEL = "site-controller"
MAX_REPORT_BYTES = 64 * 1024 * 1024
MAX_FINDINGS = 20
SPEC_NAME = re.compile(r"^\[It\] (?P<title>.*?)(?: \[(?P<labels>[^\[\]]*)\])?$")
# Ginkgo reports these when a spec did not pass; anything but "passed" fails closed.
PASSED = "passed"


class Failure(Exception):
    def __init__(self, code, exit_code, message):
        super().__init__(message)
        self.code = code
        self.exit_code = exit_code


def unsupported(message):
    return Failure("NVKR-E-UNSUPPORTED", 13, message)


@dataclass(frozen=True)
class Case:
    name: str
    status: str  # passed, skipped or failed; failed covers everything else
    labels: frozenset
    is_spec: bool


@dataclass(frozen=True)
class Report:
    cases: tuple
    properties: dict
    sha256: str


def read_report(path):
    path = Path(path)
    try:
        # One byte past the limit is enough to know the report is too large, and no more is ever held.
        with path.open("rb") as handle:
            data = handle.read(MAX_REPORT_BYTES + 1)
    except OSError as error:
        raise unsupported(f"cannot read {path}: {error}") from error
    if len(data) > MAX_REPORT_BYTES:
        raise unsupported(f"{path} exceeds {MAX_REPORT_BYTES} bytes")
    if re.search(rb"<!(DOCTYPE|ENTITY)", data):
        raise unsupported(f"{path} declares a DTD or entity, which a JUnit report never needs")
    try:
        root = ET.fromstring(data)
    except ET.ParseError as error:
        raise unsupported(f"{path} is not well-formed XML: {error}") from error
    suites = [root] if root.tag == "testsuite" else list(root.iter("testsuite"))
    if len(suites) != 1:
        raise unsupported(f"{path} has {len(suites)} test suites; exactly one is expected")
    suite = suites[0]
    properties = {p.get("name", ""): p.get("value", "") for p in suite.iter("property")}
    cases = []
    for element in suite.iter("testcase"):
        name = element.get("name", "")
        if not name:
            raise unsupported(f"{path} has a test case with no name")
        match = SPEC_NAME.match(name)
        labels = frozenset()
        if name.startswith("[It] ") and match and match.group("labels"):
            labels = frozenset(label.strip() for label in match.group("labels").split(",") if label.strip())
        cases.append(Case(name, case_status(element), labels, name.startswith("[It] ")))
    return Report(tuple(cases), properties, hashlib.sha256(data).hexdigest())


def case_status(element):
    children = {child.tag for child in element}
    if children & {"failure", "error"}:
        return "failed"
    status = element.get("status")
    if status is None:
        return "skipped" if "skipped" in children else PASSED
    if status == "skipped":
        return "skipped"
    # A skip marker explains a spec that did not run. It cannot hide a spec that ended any other way.
    if "skipped" in children:
        return "skipped" if status in (PASSED, "pending") else "failed"
    return PASSED if status == PASSED else "failed"


def category(case, tenant_label):
    if tenant_label in case.labels:
        return "tenant"
    if case.labels & SITE_CHECK_LABELS:
        return "site-checks"
    return "provisioning"


def unique_specs(report, what):
    seen = {}
    for case in report.cases:
        if not case.is_spec:
            continue
        if case.name in seen:
            raise unsupported(f"{what} lists the spec {case.name!r} more than once, so its result is ambiguous")
        seen[case.name] = case
    return seen


def require_dry_run_flag(report, want, what):
    """Ginkgo writes DryRun into every report; a report that does not say what it is proves nothing."""
    have = report.properties.get("DryRun")
    if have != want:
        said = f"DryRun={have!r}" if have is not None else "no DryRun property"
        raise unsupported(f"{what} carries {said}, not DryRun={want!r}, so it is not the kind of report this check reads there")


def selected_by_dry_run(dry, tenant_label, stage, expected_filter):
    require_dry_run_flag(dry, "true", "the dry run's report")
    if dry.properties.get("SuiteSucceeded") != "true":
        raise unsupported("the dry run did not succeed")
    label_filter = dry.properties.get("LabelFilter", "")
    if label_filter != expected_filter:
        raise unsupported(f"the dry run's label filter {label_filter!r} is not the {expected_filter!r} the workflow expects")
    specs = unique_specs(dry, "the dry run")
    selected = {name: case for name, case in specs.items() if case.status != "skipped"}
    for name, case in selected.items():
        if case.status != PASSED:
            raise unsupported(f"the dry run reports {name!r} as {case.status}")
        if not case.labels:
            raise unsupported(f"the selected spec {name!r} carries no labels, so it cannot be grouped")
        if SITE_CONTROLLER_LABEL not in case.labels:
            raise unsupported(f"the selected spec {name!r} does not carry the {SITE_CONTROLLER_LABEL} label the filter selects on")
    groups = {c: [n for n, case in selected.items() if category(case, tenant_label) == c] for c in CATEGORIES}
    empty = [c for c in REQUIRED_GROUPS[stage] if not groups[c]]
    if empty:
        raise unsupported(
            f"the dry run at stage {stage} selects no specs for " + ", ".join(empty) + f" (tenant label {tenant_label!r}, "
            f"check labels {', '.join(sorted(SITE_CHECK_LABELS))}); the harness changed, so review this check"
        )
    return label_filter, len(specs), selected, groups


def findings_for(dry_filter, selected, real, stage):
    findings = []
    real_filter = real.properties.get("LabelFilter", "")
    if real_filter != dry_filter:
        findings.append(f"the run's label filter {real_filter!r} is not the dry run's {dry_filter!r}")
    if stage == "full" and real.properties.get("SuiteSucceeded") != "true":
        findings.append("the suite did not report success")
    real_specs = unique_specs(real, "the run")
    for case in real.cases:
        if not case.is_spec and case.status not in ("passed", "skipped"):
            findings.append(f"{case.name} {case.status}")
    for name in selected:
        ran = real_specs.get(name)
        if ran is None:
            findings.append(f"missing from the run's report: {name}")
        elif ran.status == "failed":
            findings.append(f"failed: {name}")
        elif ran.status == "skipped" and stage == "full":
            findings.append(f"skipped at the full stage: {name}")
    for name, case in real_specs.items():
        if name not in selected and case.status != "skipped":
            findings.append(f"ran a spec the dry run did not select: {name}")
    passed = sum(1 for name in selected if real_specs.get(name) and real_specs[name].status == PASSED)
    if stage != "full" and passed == 0:
        findings.append("no selected spec passed")
    return findings, passed, real_specs


def check_selection(dry_path, stage, tenant_label, expected_filter):
    """Admit the dry run alone, before any machine is held."""
    dry = read_report(dry_path)
    return dry, selected_by_dry_run(dry, tenant_label, stage, expected_filter)


def check(dry_path, real_path, stage, tenant_label, expected_filter):
    dry, (dry_filter, in_suite, selected, groups) = check_selection(dry_path, stage, tenant_label, expected_filter)
    real = read_report(real_path)
    require_dry_run_flag(real, "false", "the run's report")
    findings, passed, real_specs = findings_for(dry_filter, selected, real, stage)
    if findings:
        shown = findings[:MAX_FINDINGS]
        more = len(findings) - len(shown)
        detail = "\n  ".join(shown) + (f"\n  ... and {more} more" if more else "")
        raise Failure("NVKR-E-RELEASE-INCOMPLETE", 41, f"coverage shortfall at stage {stage}:\n  {detail}")
    return {
        "schema": SCHEMA,
        "stage": stage,
        "complete": stage == "full",
        "labelFilter": dry_filter,
        "tenantLabel": tenant_label,
        "specsInSuite": in_suite,
        "selected": len(selected),
        "passed": passed,
        "categories": {
            c: {
                "selected": len(groups[c]),
                "passed": sum(1 for n in groups[c] if real_specs[n].status == PASSED),
            }
            for c in CATEGORIES
        },
        "dryRunSha256": dry.sha256,
        "reportSha256": real.sha256,
    }


class Parser(argparse.ArgumentParser):
    def error(self, message):
        print(f"nvkr: [NVKR-E-USAGE] {message}", file=sys.stderr)
        sys.exit(2)


def main(argv):
    parser = Parser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--dry", required=True, help="JUnit from a Ginkgo dry run at the harness commit")
    parser.add_argument("--real", help="JUnit written by the run being checked")
    parser.add_argument("--stage", required=True, choices=STAGES)
    parser.add_argument("--label-filter", required=True,
                        help="the label filter the workflow passes to the suite; the dry run must have used exactly it")
    parser.add_argument("--dry-only", action="store_true",
                        help="admit the dry run and stop, before a machine is held; takes no --real and no --out")
    # Integration boundary: the pinned harness's own name for the tenant label, not NVKR's.
    parser.add_argument("--tenant-label", default="nico-forge")
    parser.add_argument("--out", help="coverage record to create; must not exist")
    args = parser.parse_args(argv)
    if args.dry_only:
        if args.real or args.out:
            parser.error("--dry-only admits the dry run alone and takes neither --real nor --out")
        try:
            check_selection(args.dry, args.stage, args.tenant_label, args.label_filter)
        except Failure as failure:
            print(f"nvkr: [{failure.code}] {failure}", file=sys.stderr)
            return failure.exit_code
        return 0
    if not args.real or not args.out:
        parser.error("--real and --out are required unless --dry-only is given")
    out = Path(args.out)
    try:
        if out.exists() or out.is_symlink():
            raise Failure("NVKR-E-IMMUTABLE-VIOLATION", 47, f"{out} already exists")
        record = check(args.dry, args.real, args.stage, args.tenant_label, args.label_filter)
    except Failure as failure:
        print(f"nvkr: [{failure.code}] {failure}", file=sys.stderr)
        return failure.exit_code
    with out.open("x", encoding="utf-8") as handle:
        json.dump(record, handle, indent=2, sort_keys=True)
        handle.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
