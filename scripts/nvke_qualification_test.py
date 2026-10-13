# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import contextlib
import copy
import importlib.util
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("nvke_qualification", Path(__file__).with_name("nvke-qualification.py"))
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)
DIGEST = "sha256:" + "a" * 64
COMMIT = "b" * 40


def run_record():
    output = {"name": "cli", "kind": "executable", "buildDigest": DIGEST,
              "digest": DIGEST, "components": ["cli"], "reference": "example.test/cli@" + DIGEST}
    image = {"name": "controller", "kind": "oci", "buildDigest": DIGEST,
             "digest": DIGEST, "components": ["controller"], "reference": "example.test/controller@" + DIGEST,
             "imageIndex": {"children": [{"platform": "linux/amd64", "manifestDigest": "sha256:" + "c" * 64}]}}
    return {"schemaVersion": "nvkr.dev/run/v4", "attempt": "123-2", "status": "complete", "expected": copy.deepcopy([output, image]), "observed": [output, image],
            **{key: DIGEST for key in ("definitionDigest", "environmentDigest", "planDigest", "contentSubject")}}


class QualificationDriverTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.fixture = self.root / "fixture"
        self.fixture.write_bytes(b"bounded fixture")
        pinned = {"identity": "reviewed fixture at explicit source", "path": str(self.fixture),
                  "sha256": driver.digest_file(self.fixture)}
        self.mapping = {
            "suite_config": {"site": {"deployPathRef": COMMIT}},
            "bindings": {
                "cli": {"output": "cli", "install_path": "/usr/local/bin/nvkectl"},
                "images": {"controller": {"output": "controller"}, **{
                    name: {"fixture": pinned, "repository": "example.test/" + name, "digest": DIGEST}
                    for name in ("capi-provisioned", "profiles")}},
                "charts": {item: {"fixture": pinned} for item in (
                    "controller", "postgresql", "temporal", "spire", "spire-crds", "nktp")},
            },
        }
        self.args = SimpleNamespace(
            repository="example/nvkr", run_id="123", run_attempt="2", nvkr=Path("nvkr"),
            plan=Path("plan.json"), source_node="exact-fetch", out=self.root / "cycle", arch="amd64",
            reference="example.test/release@" + DIGEST, identity="^builder$", issuer="issuer",
            cosign=Path("cosign"), cosign_digest=DIGEST, minimum_disk_evidence="build-provenance-v1",
            definition=Path("definition.yaml"), environment=Path("environment.yaml"))
        self.args.out.mkdir()

    def owners(self, commands):
        def invoke_json(argv):
            commands.append(argv)
            if argv[1:3] == ["factory", "inspect-source"]:
                return {"path": str(self.root), "commit": COMMIT}
            if argv[1:3] == ["release", "verify"]:
                return {"release": {"subject": self.args.reference, "artifacts": run_record()["observed"]},
                        "exactInputs": {"releaseSubject": self.args.reference, **{key: DIGEST for key in (
                            "definitionDigest", "environmentDigest", "planDigest", "contentSubject")}}}
            if argv[1:3] == ["publish", "image-export"]:
                reference = argv[argv.index("--reference") + 1]
                return {"reference": reference, "manifestDigest": reference.split("@")[1], "archiveDigest": DIGEST}
            raise AssertionError("unexpected command " + repr(argv))

        def invoke(argv):
            commands.append(argv)
            if argv[1:3] == ["publish", "extract"]:
                target = self.args.out / "binaries/usr/local/bin/nvkectl"
                target.parent.mkdir(parents=True)
                target.write_bytes(b"authenticated extracted CLI")
            else:
                raise AssertionError("unexpected command " + repr(argv))
        return invoke_json, invoke

    def test_exact_owner_calls_bind_origin_native_image_and_existing_suite_config(self):
        commands = []
        source, identities, staged, path = driver.stage(self.args, run_record(), self.mapping, *self.owners(commands))
        config = json.loads(path.read_text())
        verify = commands[1]
        self.assertEqual(verify[verify.index("--expected-invocation") + 1],
                         "https://github.com/example/nvkr/actions/runs/123/attempts/2")
        self.assertEqual(config["site"]["images"]["controller"]["digest"], "sha256:" + "c" * 64)
        self.assertNotIn("tag", config["site"]["images"]["controller"])
        self.assertEqual(config["site"]["chartOverrides"]["nktp"], str(self.fixture.resolve()))
        self.assertEqual(source["commit"], COMMIT)
        self.assertEqual(identities["planDigest"], DIGEST)
        self.assertIn("fixture", staged["profiles"])
        self.assertTrue(all("make" not in argv and "go" not in argv for argv in commands))

    def test_wrong_invocation_incomplete_duplicate_and_substituted_run_fail_before_owners(self):
        for mutation in ("attempt", "missing", "duplicate", "substituted"):
            with self.subTest(mutation=mutation):
                run = copy.deepcopy(run_record())
                if mutation == "attempt": run["attempt"] = "123-1"
                if mutation == "missing": run["observed"].pop()
                if mutation == "duplicate": run["observed"][1] = run["observed"][0]
                if mutation == "substituted": run["observed"][0]["buildDigest"] = "sha256:" + "d" * 64
                with self.assertRaises(ValueError):
                    driver.stage(self.args, run, self.mapping,
                                 lambda *_: self.fail("reached an owner"), lambda *_: self.fail("reached an owner"))

    def test_source_or_verifier_failure_never_stages_or_invokes_go(self):
        for failed_step in ("inspect-source", "verify"):
            commands = []
            invoke_json, invoke = self.owners(commands)
            def failing(argv):
                if argv[2] == failed_step: raise RuntimeError("owner refused")
                return invoke_json(argv)
            progress = {}
            with self.subTest(failed_step=failed_step), self.assertRaises(RuntimeError):
                driver.stage(self.args, run_record(), self.mapping, failing, invoke, progress)
            self.assertTrue(all(argv[1] != "publish" for argv in commands))
            if failed_step == "verify": self.assertEqual(progress["harness_commit"], COMMIT)

    def test_wrong_publication_or_member_cannot_reach_payload_extraction(self):
        for changed in ("reference", "member"):
            commands = []
            invoke_json, invoke = self.owners(commands)
            def substituted(argv):
                result = copy.deepcopy(invoke_json(argv))
                if argv[2] == "verify":
                    if changed == "reference": result["release"]["subject"] = "example.test/other@" + DIGEST
                    else: result["release"]["artifacts"][0]["digest"] = "sha256:" + "d" * 64
                return result
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                driver.stage(self.args, run_record(), self.mapping, substituted, invoke)
            self.assertTrue(all(argv[1] != "publish" for argv in commands))

    def test_retention_traversal_and_unrecorded_source_fixtures_are_rejected(self):
        for changed in ("retention", "chart-path", "cli-path", "deploy-source"):
            mapping = copy.deepcopy(self.mapping)
            if changed == "retention": mapping["suite_config"]["lifecycle"] = {"preserveOnFailure": True}
            if changed == "chart-path": mapping["bindings"]["charts"]["../../outside"] = mapping["bindings"]["charts"].pop("nktp")
            if changed == "cli-path": mapping["bindings"]["cli"]["install_path"] = "/../../outside"
            if changed == "deploy-source": mapping["suite_config"]["site"]["deployPathRef"] = "d" * 40
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                driver.stage(self.args, run_record(), mapping, *self.owners([]))

    def test_fixture_tampering_retains_partial_failure_identity(self):
        self.fixture.write_bytes(b"changed")
        progress = {}
        with self.assertRaises(ValueError):
            driver.stage(self.args, run_record(), self.mapping, *self.owners([]), progress=progress)
        self.assertEqual(progress["materialization_phase"], "publication-authenticated")
        self.assertIn("cli", progress["artifacts"])

    def cli_args(self, out, mode="--plan-only", compile_only=False):
        run_path, mapping_path = self.root / "run.json", self.root / "inputs.json"
        run_path.write_text(json.dumps(run_record()))
        mapping_path.write_text(json.dumps(self.mapping))
        argv = [mode, "--run", str(run_path), "--inputs", str(mapping_path), "--out", str(out)]
        fields = {"plan": "plan", "definition": "definition", "environment": "environment", "repository": "example/nvkr",
                  "run-id": "123", "run-attempt": "2", "reference": self.args.reference, "source-node": "exact-fetch",
                  "stream-name": "forge-gb200-amd64-ubuntu-26.04-kube-1.36", "gpu": "gb200", "k8s": "1.36",
                  "arch": "amd64", "ubuntu": "26.04", "versions": "controller=exact"}
        if mode == "--stage":
            fields.update(nvkr="nvkr", identity="^builder$", issuer="issuer", cosign="cosign",
                          **{"cosign-digest": DIGEST, "minimum-disk-evidence": "build-provenance-v1"})
        for key, value in fields.items(): argv += ["--" + key, value]
        if compile_only:
            argv.append("--compile-only")
        return argv

    def test_plan_only_needs_no_testgrid_inputs_and_invokes_no_processes(self):
        out = self.root / "prepared"
        argv = self.cli_args(out)
        with patch.object(driver.subprocess, "run", side_effect=AssertionError("network/process")), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(driver.main(argv), 0)
        report = json.loads(output.getvalue())
        self.assertEqual(json.loads((out / "qualification.json").read_text()), report)
        self.assertEqual([path.name for path in out.iterdir()], ["qualification.json"])
        self.assertEqual(report["test_outcome"], "not-run")
        self.assertEqual(report["cleanup_outcome"], "not-run")
        self.assertEqual(report["input_identities"]["planDigest"], DIGEST)
        self.assertFalse(report["live_command_admitted"])
        self.assertNotIn("testgrid_publication_command", report)
        self.assertNotIn("testgrid_destination", report)
        self.assertNotIn("publication_outcome", report)

    def test_staging_and_compile_only_need_no_testgrid_inputs(self):
        real_stage = driver.stage
        for compile_only in (False, True):
            with self.subTest(compile_only=compile_only):
                out = self.root / ("compiled" if compile_only else "staged")
                commands = []
                self.args.out = out
                owners = self.owners(commands)

                def staged(args, run, mapping, progress):
                    return real_stage(args, run, mapping, *owners, progress=progress)

                with patch.object(driver, "stage", side_effect=staged), \
                        patch.object(driver, "compile_source") as compile_probe, \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(driver.main(self.cli_args(out, "--stage", compile_only)), 0)
                if compile_only:
                    compile_probe.assert_called_once_with(str(self.root))
                else:
                    compile_probe.assert_not_called()
                report = json.loads((out / "qualification.json").read_text())
                self.assertEqual(report["harness_commit"], COMMIT)
                self.assertEqual(report["materialization_outcome"], "core-artifacts-staged")
                self.assertEqual(report["test_outcome"], "not-run")
                self.assertEqual(report["cleanup_outcome"], "not-run")
                self.assertFalse((out / "testgrid-metadata.json").exists())
                self.assertTrue(all(argv[1] in ("factory", "release", "publish") for argv in commands))

    def test_compile_only_rejects_unstaged_inputs_before_creating_output(self):
        out = self.root / "unstaged"
        with patch.object(driver.subprocess, "run", side_effect=AssertionError("network/process")), \
                contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(driver.main(self.cli_args(out, compile_only=True)), 17)
        self.assertFalse(out.exists())

    def test_invalid_image_fixture_fields_fail_before_owners_or_output_in_both_modes(self):
        original = copy.deepcopy(self.mapping)
        cases = [
            ("missing-repository", "repository", None),
            ("null-repository", "repository", None),
            ("empty-repository", "repository", ""),
            ("blank-repository", "repository", "  "),
            ("nonstring-repository", "repository", 12),
            ("missing-digest", "digest", None),
            ("null-digest", "digest", None),
            ("empty-digest", "digest", ""),
            ("malformed-digest", "digest", "sha256:xyz"),
            ("noncanonical-digest", "digest", "sha256:" + "A" * 64),
            ("nonstring-digest", "digest", 12),
        ]
        for role in ("capi-provisioned", "profiles"):
            for case, field, value in cases:
                self.mapping = copy.deepcopy(original)
                binding = self.mapping["bindings"]["images"][role]
                if case.startswith("missing-"):
                    binding.pop(field)
                else:
                    binding[field] = value
                with self.subTest(role=role, case=case), self.assertRaises(ValueError):
                    driver.stage(self.args, run_record(), self.mapping,
                                 lambda *_: self.fail("reached an owner"),
                                 lambda *_: self.fail("reached an owner"))
                for mode in ("--plan-only", "--stage"):
                    out = self.root / (role + "-" + case + mode)
                    with self.subTest(role=role, case=case, mode=mode), \
                            patch.object(driver.subprocess, "run", side_effect=AssertionError("network/process")), \
                            contextlib.redirect_stderr(io.StringIO()):
                        self.assertEqual(driver.main(self.cli_args(out, mode)), 17)
                    self.assertFalse(out.exists())

    def test_existing_output_is_not_modified_when_cycle_reuse_is_refused(self):
        argv = self.cli_args(self.args.out)
        with patch.object(driver, "command_json", side_effect=AssertionError("network/process")), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(driver.main(argv), 17)
        self.assertEqual(list(self.args.out.iterdir()), [])

    def test_existing_go_suite_command_retains_cleanup_headroom_and_no_keep_flags(self):
        argv = driver.suite_command("source", Path("config"), "controller=exact", "arch=amd64", Path("reports"))
        self.assertEqual(argv[:6], ["go", "test", "-v", "-count=1", "-timeout", "220m"])
        self.assertIn("-ginkgo.timeout=150m", argv)
        self.assertIn("-ginkgo.fail-on-empty", argv)
        self.assertIn("-e2e.keep-site=false", argv)

    def test_compile_probe_does_not_receive_source_signing_or_hardware_credentials(self):
        environment = {"PATH": "tools", "GOCACHE": "cache", "NVKR_PIN_SOURCE_TOKEN_0": "source-secret",
                       "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "signing-secret", "FORGE_API_KEY": "hardware-secret"}
        with patch.dict(driver.os.environ, environment, clear=True), patch.object(driver.subprocess, "run") as run:
            driver.compile_source("source")
        self.assertEqual(run.call_args.kwargs["env"], {"PATH": "tools", "GOCACHE": "cache"})
        self.assertEqual(run.call_args.args[0], ["go", "test", "-run", "^$", "-count=1", "."])


if __name__ == "__main__":
    unittest.main()
