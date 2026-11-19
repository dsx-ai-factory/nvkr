# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Contracts for the Forge hardware lane JUnit coverage check.

The fixture is a trimmed Ginkgo dry run of NVKE's controller/test/e2e suite at the harness commit, so
the group sizes below are facts about that report. Every test builds its "real run" report by editing
a copy of it; none of this says anything about hardware.
"""

import contextlib
import copy
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
import xml.etree.ElementTree as ET

HERE = Path(__file__).parent
FIXTURE = HERE / "testdata" / "forge-e2e" / "ginkgo-dry-run.junit.xml"

spec = importlib.util.spec_from_file_location("checker", HERE / "forge-e2e-check-junit.py")
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)

PROVISIONING = "[It] NVKE e2e qualification provisions a node [site-controller, needs:cpu=2, needs:gpu=1]"
SITE_CHECK = (
    "[It] NVKE e2e qualification runs CoreDNS [site-controller, needs:cpu=2, needs:gpu=1, readiness, site-distro]"
)
TENANT = (
    "[It] NVKE e2e qualification tenant on the built site nico-forge creates the cluster "
    "[site-controller, needs:cpu=2, needs:gpu=1, nico-forge, lifecycle]"
)
NOT_SELECTED = "[It] NVKE e2e qualification a spec the dry run never selected [site-controller, nico-dsx]"


class Report:
    """An editable copy of the fixture, written to a temporary file on demand."""

    def __init__(self, tree=None):
        self.root = copy.deepcopy(tree) if tree is not None else ET.parse(FIXTURE).getroot()
        self.suite = self.root if self.root.tag == "testsuite" else next(self.root.iter("testsuite"))

    def case(self, name):
        for element in self.suite.iter("testcase"):
            if element.get("name") == name:
                return element
        raise KeyError(name)

    def remove(self, name):
        self.suite.remove(self.case(name))

    def skip(self, name, message="not configured"):
        element = self.case(name)
        element.set("status", "skipped")
        ET.SubElement(element, "skipped", {"message": message})

    def fail(self, name, status="failed"):
        element = self.case(name)
        element.set("status", status)
        ET.SubElement(element, "failure", {"message": "boom"})

    def set_status(self, name, status):
        self.case(name).set("status", status)

    def add_passed(self, name):
        ET.SubElement(self.suite, "testcase", {"name": name, "classname": "NVKE E2E Suite", "status": "passed"})

    def prop(self, name, value):
        for element in self.suite.iter("property"):
            if element.get("name") == name:
                element.set("value", value)
                return
        raise KeyError(name)

    def labels_of(self, predicate):
        return [e.get("name") for e in self.suite.iter("testcase") if predicate(e.get("name"))]

    def write(self, directory, name="real.junit.xml"):
        path = Path(directory) / name
        path.write_text(ET.tostring(self.root, encoding="unicode"), encoding="utf-8")
        return path


class Run:
    def __init__(self, exit_code, stderr, out):
        self.exit_code, self.stderr, self.out = exit_code, stderr, out

    @property
    def record(self):
        return json.loads(self.out.read_text(encoding="utf-8"))


class CheckerCase(unittest.TestCase):
    def setUp(self):
        self._directory = tempfile.TemporaryDirectory()
        self.addCleanup(self._directory.cleanup)
        self.directory = Path(self._directory.name)

    def run_check(self, real, *, dry=FIXTURE, stage="full", extra=(), out=None):
        out = out or self.directory / "coverage.json"
        argv = ["--dry", str(dry), "--real", str(real), "--stage", stage, "--out", str(out), *extra]
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            try:
                code = checker.main(argv)
            except SystemExit as exit_:
                code = exit_.code
        return Run(code, stderr.getvalue(), out)

    def real_from(self, edit=None, **kwargs):
        report = Report()
        if edit:
            edit(report)
        return report.write(self.directory), kwargs

    def assertRejected(self, run, code, exit_code, *fragments):
        self.assertEqual(run.exit_code, exit_code, run.stderr)
        self.assertIn(f"[{code}]", run.stderr)
        for fragment in fragments:
            self.assertIn(fragment, run.stderr)
        self.assertFalse(run.out.exists(), "a rejected report must leave no coverage record")


class AcceptedRuns(CheckerCase):
    def test_complete_full_run_is_recorded_with_its_groups(self):
        run = self.run_check(Report().write(self.directory))
        self.assertEqual(run.exit_code, 0, run.stderr)
        record = run.record
        self.assertEqual(record["schema"], "nvkr.dev/forge-e2e-coverage/v1")
        self.assertEqual((record["stage"], record["complete"]), ("full", True))
        self.assertEqual(record["labelFilter"], "site-controller && !nico-dsx")
        self.assertEqual((record["selected"], record["passed"]), (80, 80))
        self.assertEqual(
            record["categories"],
            {
                "provisioning": {"selected": 7, "passed": 7},
                "site-checks": {"selected": 31, "passed": 31},
                "tenant": {"selected": 42, "passed": 42},
            },
        )
        self.assertEqual(len(record["dryRunSha256"]), 64)
        self.assertEqual(len(record["reportSha256"]), 64)

    def test_record_is_canonical_json_with_a_trailing_newline(self):
        run = self.run_check(Report().write(self.directory))
        text = run.out.read_text(encoding="utf-8")
        self.assertTrue(text.endswith("\n"))
        self.assertEqual(text, json.dumps(json.loads(text), indent=2, sort_keys=True) + "\n")

    def test_specs_the_filter_left_out_are_not_required(self):
        # The fixture lists two filtered-out specs as skipped; neither is expected or counted.
        run = self.run_check(Report().write(self.directory))
        self.assertEqual(run.record["selected"], 80)

    def test_report_without_the_filtered_out_specs_is_accepted(self):
        # The suite can also write a report of only the selected specs.
        def drop_skipped(report):
            for element in list(report.suite.iter("testcase")):
                if element.find("skipped") is not None:
                    report.suite.remove(element)

        path, _ = self.real_from(drop_skipped)
        self.assertEqual(self.run_check(path).exit_code, 0)

    def test_hooks_are_not_specs_and_may_be_absent(self):
        def drop_hooks(report):
            for name in report.labels_of(lambda n: not n.startswith("[It] ")):
                report.remove(name)

        path, _ = self.real_from(drop_hooks)
        run = self.run_check(path)
        self.assertEqual((run.exit_code, run.record["selected"]), (0, 80), run.stderr)

    def test_a_renamed_tenant_label_is_accepted_when_the_caller_says_so(self):
        def rename(report):
            for element in report.suite.iter("testcase"):
                element.set("name", element.get("name").replace("nico-forge,", "nico-other,"))

        dry = Report()
        rename(dry)
        dry_path = dry.write(self.directory, "dry.junit.xml")
        real = Report()
        rename(real)
        run = self.run_check(real.write(self.directory), dry=dry_path, extra=("--tenant-label", "nico-other"))
        self.assertEqual((run.exit_code, run.record["categories"]["tenant"]["selected"]), (0, 42), run.stderr)


class CoverageShortfalls(CheckerCase):
    def test_a_filter_that_selects_less_than_the_dry_run_is_caught(self):
        # The case `-ginkgo.fail-on-empty` cannot see: the run selects some specs, just not all of them.
        def under_select(report):
            for name in report.labels_of(lambda n: "nico-forge" in n):
                report.remove(name)

        path, _ = self.real_from(under_select)
        run = self.run_check(path)
        self.assertRejected(run, "NVKR-E-RELEASE-INCOMPLETE", 41, "coverage shortfall at stage full", "missing from the run's report")
        self.assertIn(TENANT, run.stderr)

    def test_shortfalls_are_listed_and_the_list_is_bounded(self):
        def under_select(report):
            for name in report.labels_of(lambda n: "nico-forge" in n):
                report.remove(name)

        path, _ = self.real_from(under_select)
        run = self.run_check(path)
        self.assertIn(f"... and {42 - checker.MAX_FINDINGS} more", run.stderr)

    def test_individual_shortfalls(self):
        cases = [
            ("a selected spec is missing", lambda r: r.remove(PROVISIONING), "missing from the run's report: " + PROVISIONING),
            ("a selected spec is skipped at the full stage", lambda r: r.skip(SITE_CHECK), "skipped at the full stage: " + SITE_CHECK),
            ("a selected spec failed", lambda r: r.fail(TENANT), "failed: " + TENANT),
            ("a spec is panicked", lambda r: r.set_status(TENANT, "panicked"), "failed: " + TENANT),
            ("a spec was interrupted", lambda r: r.set_status(TENANT, "interrupted"), "failed: " + TENANT),
            ("a spec is pending", lambda r: r.set_status(TENANT, "pending"), "failed: " + TENANT),
            ("a spec has an unknown status", lambda r: r.set_status(TENANT, "mystery"), "failed: " + TENANT),
            ("a spec the dry run did not select ran", lambda r: r.add_passed(NOT_SELECTED), "ran a spec the dry run did not select: " + NOT_SELECTED),
            ("the label filter differs", lambda r: r.prop("LabelFilter", "site-controller"), "label filter 'site-controller' is not the dry run's"),
            ("the suite did not report success", lambda r: r.prop("SuiteSucceeded", "false"), "the suite did not report success"),
            ("a hook failed", lambda r: r.fail("[AfterSuite]"), "[AfterSuite] failed"),
        ]
        for name, edit, fragment in cases:
            with self.subTest(name):
                path, _ = self.real_from(edit)
                run = self.run_check(path, out=self.directory / f"{abs(hash(name))}.json")
                self.assertRejected(run, "NVKR-E-RELEASE-INCOMPLETE", 41, fragment)

    def test_a_skipped_spec_the_run_chose_to_skip_is_still_a_shortfall_at_full(self):
        path, _ = self.real_from(lambda r: r.skip(PROVISIONING, "stage"))
        self.assertRejected(self.run_check(path), "NVKR-E-RELEASE-INCOMPLETE", 41, "skipped at the full stage")

    def test_an_extra_spec_that_did_not_run_is_not_a_shortfall(self):
        def add_skipped(report):
            report.add_passed(NOT_SELECTED)
            report.skip(NOT_SELECTED, "")

        path, _ = self.real_from(add_skipped)
        run = self.run_check(path)
        self.assertEqual(run.exit_code, 0, run.stderr)

    def test_a_duplicate_spec_in_the_run_is_ambiguous(self):
        path, _ = self.real_from(lambda r: r.add_passed(TENANT))
        self.assertRejected(self.run_check(path), "NVKR-E-UNSUPPORTED", 13, "more than once", TENANT)


class EarlierStages(CheckerCase):
    def stage_report(self, *, failed=None, passed_only=None):
        report = Report()
        for element in report.suite.iter("testcase"):
            name = element.get("name")
            if not name.startswith("[It] ") or element.find("skipped") is not None:
                continue
            keep = passed_only is None or passed_only(name)
            if not keep:
                element.set("status", "skipped")
                ET.SubElement(element, "skipped", {"message": "later stage"})
        if failed:
            report.fail(failed)
        return report.write(self.directory)

    def test_later_specs_may_skip_and_the_record_says_it_is_incomplete(self):
        path = self.stage_report(passed_only=lambda n: n in (PROVISIONING, SITE_CHECK))
        run = self.run_check(path, stage="provision")
        self.assertEqual(run.exit_code, 0, run.stderr)
        self.assertFalse(run.record["complete"])
        self.assertEqual(run.record["stage"], "provision")
        self.assertEqual((run.record["passed"], run.record["selected"]), (2, 80))
        self.assertEqual(run.record["categories"]["tenant"], {"selected": 42, "passed": 0})

    def test_the_same_report_is_not_accepted_at_the_full_stage(self):
        path = self.stage_report(passed_only=lambda n: n == PROVISIONING)
        self.assertEqual(self.run_check(path, stage="provision").exit_code, 0)
        self.assertRejected(
            self.run_check(path, stage="full", out=self.directory / "full.json"),
            "NVKR-E-RELEASE-INCOMPLETE", 41, "skipped at the full stage",
        )

    def test_a_failure_is_a_shortfall_at_every_stage(self):
        for stage in ("provision", "bootstrap", "deployment"):
            with self.subTest(stage):
                path = self.stage_report(failed=PROVISIONING)
                run = self.run_check(path, stage=stage, out=self.directory / f"{stage}.json")
                self.assertRejected(run, "NVKR-E-RELEASE-INCOMPLETE", 41, "failed: " + PROVISIONING)

    def test_every_selected_spec_must_still_be_listed(self):
        report = Report()
        report.remove(TENANT)
        self.assertRejected(
            self.run_check(report.write(self.directory), stage="deployment"),
            "NVKR-E-RELEASE-INCOMPLETE", 41, "missing from the run's report: " + TENANT,
        )

    def test_a_stage_run_where_nothing_passed_is_a_shortfall(self):
        path = self.stage_report(passed_only=lambda n: False)
        self.assertRejected(self.run_check(path, stage="provision"), "NVKR-E-RELEASE-INCOMPLETE", 41, "no selected spec passed")

    def test_suite_success_is_not_required_below_the_full_stage(self):
        report = Report()
        report.prop("SuiteSucceeded", "false")
        for name in report.labels_of(lambda n: n.startswith("[It] ") and "provisions a node" not in n):
            if report.case(name).find("skipped") is None:
                report.skip(name, "later stage")
        self.assertEqual(self.run_check(report.write(self.directory), stage="provision").exit_code, 0)


class DryRunAdmission(CheckerCase):
    def dry(self, edit):
        report = Report()
        edit(report)
        return report.write(self.directory, "dry.junit.xml")

    def test_a_dry_run_that_is_not_usable_is_unsupported(self):
        real = Report().write(self.directory)
        cases = [
            ("the dry run did not succeed", lambda r: r.prop("SuiteSucceeded", "false"), "the dry run did not succeed"),
            ("filter does not select site-controller", lambda r: r.prop("LabelFilter", "!nico-dsx"), "does not select the site-controller specs"),
            ("a selected spec has no labels", lambda r: r.case(TENANT).set("name", "[It] NVKE e2e qualification tenant on the built site nico-forge creates the cluster"), "carries no labels"),
            ("a selected spec failed in the dry run", lambda r: r.fail(TENANT), "the dry run reports"),
            ("a spec is listed twice", lambda r: r.add_passed(TENANT), "more than once"),
        ]
        for name, edit, fragment in cases:
            with self.subTest(name):
                run = self.run_check(real, dry=self.dry(edit), out=self.directory / f"{abs(hash(name))}.json")
                self.assertRejected(run, "NVKR-E-UNSUPPORTED", 13, fragment)

    def test_an_empty_group_means_the_harness_changed(self):
        # Rename the labels the groups are built from; the lane must not silently shrink.
        real = Report().write(self.directory)
        cases = [
            ("no tenant specs", ("--tenant-label", "nico-missing"), "tenant"),
            ("no site checks", (), "site-checks"),
            ("no provisioning specs", (), "provisioning"),
        ]
        for name, extra, group in cases:
            with self.subTest(name):
                report = Report()
                if group == "site-checks":
                    for element in report.suite.iter("testcase"):
                        for label in checker.SITE_CHECK_LABELS:
                            element.set("name", element.get("name").replace(f", {label}", ""))
                if group == "provisioning":
                    for element in report.suite.iter("testcase"):
                        n = element.get("name")
                        if n.startswith("[It] ") and "nico-forge" not in n and not any(l in n for l in checker.SITE_CHECK_LABELS):
                            element.set("name", n.replace("]", ", readiness]") if n.endswith("]") else n)
                dry = report.write(self.directory, "dry.junit.xml")
                run = self.run_check(real, dry=dry, extra=extra, out=self.directory / f"{group}.json")
                self.assertRejected(run, "NVKR-E-UNSUPPORTED", 13, "the dry run selects no specs for", group, "the harness changed")


class MalformedReports(CheckerCase):
    def write(self, text, name="real.junit.xml"):
        path = self.directory / name
        path.write_text(text, encoding="utf-8")
        return path

    def test_unusable_reports_are_unsupported(self):
        good = Report().write(self.directory, "good.junit.xml")
        # Each report needs its own file: the list is built before any case runs.
        cases = [
            ("not XML", self.write("not xml", "not-xml.xml"), "not well-formed XML"),
            ("DTD", self.write('<!DOCTYPE x [<!ENTITY a "b">]><testsuite name="s"/>', "dtd.xml"), "declares a DTD or entity"),
            ("entity only", self.write('<!ENTITY a "b"><testsuite name="s"/>', "entity.xml"), "declares a DTD or entity"),
            ("two suites", self.write("<testsuites><testsuite name='a'/><testsuite name='b'/></testsuites>", "two.xml"), "2 test suites"),
            ("no suite", self.write("<testsuites/>", "none.xml"), "0 test suites"),
            ("unnamed case", self.write("<testsuite name='s'><testcase/></testsuite>", "unnamed.xml"), "no name"),
            ("missing file", self.directory / "absent.xml", "cannot read"),
        ]
        for name, path, fragment in cases:
            with self.subTest(name):
                run = self.run_check(path, dry=good, out=self.directory / f"{abs(hash(name))}.json")
                self.assertRejected(run, "NVKR-E-UNSUPPORTED", 13, fragment)

    def test_a_malformed_dry_run_is_rejected_the_same_way(self):
        run = self.run_check(Report().write(self.directory), dry=self.write("not xml", "dry.xml"))
        self.assertRejected(run, "NVKR-E-UNSUPPORTED", 13, "not well-formed XML")

    def test_an_oversized_report_is_refused_before_parsing(self):
        original = checker.MAX_REPORT_BYTES
        checker.MAX_REPORT_BYTES = 100
        self.addCleanup(setattr, checker, "MAX_REPORT_BYTES", original)
        self.assertRejected(self.run_check(Report().write(self.directory)), "NVKR-E-UNSUPPORTED", 13, "exceeds 100 bytes")


class CaseStatus(unittest.TestCase):
    def test_status_table(self):
        cases = [
            ({"status": "passed"}, [], "passed"),
            ({"status": "skipped"}, [], "skipped"),
            ({"status": "skipped"}, ["skipped"], "skipped"),
            ({"status": "passed"}, ["skipped"], "skipped"),
            ({"status": "failed"}, [], "failed"),
            ({"status": "passed"}, ["failure"], "failed"),
            ({"status": "passed"}, ["error"], "failed"),
            ({"status": "panicked"}, [], "failed"),
            ({"status": "unheard-of"}, [], "failed"),
            ({}, [], "passed"),
            ({}, ["skipped"], "skipped"),
            ({}, ["failure"], "failed"),
            ({"status": "skipped"}, ["failure"], "failed"),
        ]
        for attrs, children, want in cases:
            with self.subTest(attrs=attrs, children=children):
                element = ET.Element("testcase", attrs)
                for child in children:
                    ET.SubElement(element, child)
                self.assertEqual(checker.case_status(element), want)


class CommandLine(CheckerCase):
    def test_an_existing_record_is_never_replaced(self):
        out = self.directory / "coverage.json"
        out.write_text("kept", encoding="utf-8")
        run = self.run_check(Report().write(self.directory), out=out)
        self.assertEqual(run.exit_code, 47)
        self.assertIn("[NVKR-E-IMMUTABLE-VIOLATION]", run.stderr)
        self.assertEqual(out.read_text(encoding="utf-8"), "kept")

    def test_a_symlink_at_the_output_is_not_followed(self):
        target = self.directory / "target"
        target.write_text("kept", encoding="utf-8")
        out = self.directory / "coverage.json"
        out.symlink_to(target)
        run = self.run_check(Report().write(self.directory), out=out)
        self.assertEqual(run.exit_code, 47)
        self.assertEqual(target.read_text(encoding="utf-8"), "kept")

    def test_usage_errors_are_coded_and_exit_2(self):
        real = Report().write(self.directory)
        cases = [
            ("unknown stage", ["--dry", str(FIXTURE), "--real", str(real), "--stage", "rollout", "--out", str(self.directory / "x")]),
            ("missing stage", ["--dry", str(FIXTURE), "--real", str(real), "--out", str(self.directory / "x")]),
            ("missing out", ["--dry", str(FIXTURE), "--real", str(real), "--stage", "full"]),
            ("unknown flag", ["--dry", str(FIXTURE), "--real", str(real), "--stage", "full", "--out", str(self.directory / "x"), "--force"]),
        ]
        for name, argv in cases:
            with self.subTest(name):
                stderr = io.StringIO()
                with contextlib.redirect_stderr(stderr), self.assertRaises(SystemExit) as raised:
                    checker.main(argv)
                self.assertEqual(raised.exception.code, 2)
                self.assertIn("[NVKR-E-USAGE]", stderr.getvalue())

    def test_the_stages_are_the_harnesss(self):
        self.assertEqual(checker.STAGES, ("provision", "bootstrap", "deployment", "full"))


if __name__ == "__main__":
    unittest.main()
