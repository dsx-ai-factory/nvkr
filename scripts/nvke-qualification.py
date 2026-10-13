#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Prepare the existing NVKE Go qualification suite after NVKR publication.

JSON files here are operator mappings and reporting descriptions, not receipts.
Source and publication authentication/extraction stay in NVKR's existing owners.
This local adapter stages and compiles only. Live capacity/access admission and
generated-job activation remain explicit integration gates; it allocates nothing.
"""

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys


def read_json(path):
    if path.stat().st_size > 16 * 1024 * 1024:
        raise ValueError("input document exceeds the 16 MiB adapter bound")
    return json.loads(path.read_text(encoding="utf-8"))


def digest_file(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError("staged input must be a regular file")
    with path.open("rb") as stream:
        return "sha256:" + hashlib.file_digest(stream, "sha256").hexdigest()


def checked_run(run, repository, run_id, attempt):
    """Correlate the diagnostic Run; authenticated acceptance is delegated later."""
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("repository must be an explicit GitHub owner/repository")
    for value in (run_id, attempt):
        if not re.fullmatch(r"[1-9][0-9]*", value):
            raise ValueError("Run and attempt must be positive decimal strings")
    if run.get("schemaVersion") != "nvkr.dev/run/v4":
        raise ValueError("qualification requires the current Run schema")
    if run.get("attempt") != f"{run_id}-{attempt}":
        raise ValueError("Run record belongs to another invocation")
    if run.get("status") != "complete" or run.get("failure"):
        raise ValueError("qualification needs a complete producing Run")
    expected, observed = run.get("expected", []), run.get("observed", [])
    if not expected or len(expected) != len(observed):
        raise ValueError("Run is missing required output observations")
    named = {item["name"]: item for item in observed}
    if len(named) != len(observed) or len({item["name"] for item in expected}) != len(expected):
        raise ValueError("Run has duplicated outputs")
    for item in expected:
        actual = named.get(item["name"], {})
        for key in ("kind", "buildDigest", "components"):
            if actual.get(key, []) != item.get(key, []):
                raise ValueError("Run has missing or substituted outputs")
    identities = {key: run[key] for key in (
        "definitionDigest", "environmentDigest", "planDigest", "contentSubject",
    )}
    if any(not re.fullmatch(r"sha256:[0-9a-f]{64}", value) for value in identities.values()):
        raise ValueError("Run omits exact input/content identities")
    return named, identities


def command_json(command, cwd=None, timeout=1800):
    completed = subprocess.run(command, cwd=cwd, check=True, capture_output=True,
                               text=True, timeout=timeout)
    return json.loads(completed.stdout)


def command(command, cwd=None, timeout=1800):
    subprocess.run(command, cwd=cwd, check=True, timeout=timeout)


def compile_source(source):
    # Compilation needs tooling/cache settings, never fetch/signing/Forge tokens.
    allowed = {"PATH", "HOME", "TMPDIR", "GOTMPDIR", "GOPATH", "GOCACHE", "GOMODCACHE",
               "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "CGO_ENABLED", "GOFLAGS"}
    environment = {key: value for key, value in os.environ.items() if key in allowed}
    subprocess.run(["go", "test", "-run", "^$", "-count=1", "."],
                   cwd=Path(source) / "controller/test/e2e", check=True,
                   timeout=600, env=environment)


def checked_mapping(mapping):
    """Admit only the existing suite's core artifact roles, before filesystem work."""
    config, bindings = mapping["suite_config"], mapping["bindings"]
    lifecycle = config.get("lifecycle", {})
    if any(lifecycle.get(key) for key in ("skipDelete", "preserveOnFailure", "reuseCluster")):
        raise ValueError("nightly inputs must not preserve or reuse hardware")
    if not re.fullmatch(r"[0-9a-f]{40}", config["site"].get("deployPathRef", "")):
        raise ValueError("deployment assets require an explicit pinned source commit")
    charts = {"controller", "postgresql", "temporal", "spire", "spire-crds", "nktp"}
    if set(bindings.get("charts", {})) != charts:
        raise ValueError("each installed chart needs one candidate or explicit fixture binding")
    if set(bindings.get("images", {})) != {"controller", "capi-provisioned", "profiles"}:
        raise ValueError("each staged site image needs one candidate or explicit fixture binding")
    for binding in [bindings["cli"], *bindings["charts"].values(), *bindings["images"].values()]:
        if ("output" in binding) == ("fixture" in binding):
            raise ValueError("each role needs exactly one output or explicit fixture")
    if "fixture" in bindings["cli"]:
        raise ValueError("the CLI must come from the producing Run")
    for binding in bindings["images"].values():
        if "fixture" in binding:
            repository, digest = binding.get("repository"), binding.get("digest")
            if (not isinstance(repository, str) or not repository.strip()
                    or not isinstance(digest, str)
                    or not re.fullmatch(r"sha256:[0-9a-f]{64}", digest)):
                raise ValueError("image fixtures need an explicit repository and native sha256 digest")
    relative = Path(bindings["cli"]["install_path"].lstrip("/"))
    if relative.is_absolute() or ".." in relative.parts or str(relative) == ".":
        raise ValueError("CLI install path must stay inside the extracted bundle")


def suite_command(source, config, versions, capabilities, report_dir):
    return ["go", "test", "-v", "-count=1", "-timeout", "220m", ".", "-args",
            "-ginkgo.label-filter=site-controller", "-ginkgo.timeout=150m",
            "-ginkgo.fail-on-empty", "-ginkgo.no-color",
            "-ginkgo.junit-report=" + str(report_dir / "qualify-e2e.junit.xml"),
            "-ginkgo.json-report=" + str(report_dir / "qualify-e2e.report.json"),
            "-e2e.config=" + str(config), "-e2e.environment=forge",
            "-e2e.site-stage=full", "-e2e.keep-site=false",
            "-e2e.versions=" + versions, "-e2e.capabilities=" + capabilities]


def stage(args, run, mapping, invoke_json=command_json, invoke=command, progress=None):
    """Authenticate exact origin and stage through existing materialization commands."""
    named, identities = checked_run(run, args.repository, args.run_id, args.run_attempt)
    checked_mapping(mapping)
    source = invoke_json([str(args.nvkr), "factory", "inspect-source", "--plan", str(args.plan),
                          "--plan-digest", identities["planDigest"], "--node", args.source_node])
    if progress is not None:
        progress.update(harness_commit=source["commit"], source=source, materialization_phase="source-inspected")
    config = copy.deepcopy(mapping["suite_config"])
    bindings = mapping["bindings"]
    deploy_fixture = mapping.get("deploy_fixture", {})
    deploy_commit = config["site"]["deployPathRef"]
    if deploy_commit != source["commit"] and (
        deploy_fixture.get("commit") != deploy_commit or not deploy_fixture.get("identity")
    ):
        raise ValueError("deployment source differs from the harness without an explicit fixture identity")
    invocation = f"https://github.com/{args.repository}/actions/runs/{args.run_id}/attempts/{args.run_attempt}"
    layout = args.out / "publication"
    verified = invoke_json([str(args.nvkr), "release", "verify", "--reference", args.reference,
                           "--lane", "pipeline-attested", "--scope", "artifact-publication",
                           "--identity", args.identity, "--issuer", args.issuer,
                           "--cosign", str(args.cosign), "--cosign-digest", args.cosign_digest,
                           "--minimum-disk-evidence", args.minimum_disk_evidence,
                           "--definition", str(args.definition), "--environment", str(args.environment),
                           "--plan", str(args.plan), "--export-layout", str(layout),
                           "--expected-invocation", invocation])
    exact = verified["exactInputs"]
    if verified["release"]["subject"] != args.reference or exact["releaseSubject"] != args.reference:
        raise ValueError("verifier returned another publication reference")
    if any(exact.get(key) != value for key, value in identities.items()):
        raise ValueError("verified publication differs from the producing Run")
    authenticated = {item["name"]: item for item in verified["release"]["artifacts"]}
    if set(authenticated) != set(named):
        raise ValueError("verified publication differs from complete Run membership")
    for name, item in named.items():
        if any(authenticated[name].get(key) != item.get(key) for key in ("digest", "kind", "buildDigest")):
            raise ValueError("verified publication substitutes a producing Run output")
    if progress is not None:
        progress["materialization_phase"] = "publication-authenticated"
    staged = {"deploy_assets": {"commit": deploy_commit,
                               "fixture": deploy_fixture.get("identity", "authenticated harness source")}}
    if progress is not None:
        progress["artifacts"] = staged

    def selected(binding):
        name = binding["output"]
        if name not in authenticated:
            raise ValueError("input binding names an output absent from the complete publication")
        return name, authenticated[name]

    def fixture(binding, role):
        value = binding["fixture"]
        if not value.get("identity"):
            raise ValueError("fixtures need an explicit bounded source/material identity")
        path = Path(value["path"]).resolve(strict=True)
        if digest_file(path) != value["sha256"]:
            raise ValueError("fixture payload differs from its explicit byte pin")
        staged[role] = {"fixture": value["identity"], "payload_digest": value["sha256"]}
        return path

    cli = bindings["cli"]
    cli_name, cli_output = selected(cli)
    if cli_output["kind"] != "executable":
        raise ValueError("CLI binding is not an executable output")
    binaries = args.out / "binaries"
    invoke([str(args.nvkr), "publish", "extract", "--layout", str(layout),
            "--bundle", cli_name, "--out", str(binaries)])
    relative = Path(cli["install_path"].lstrip("/"))
    if relative.is_absolute() or ".." in relative.parts:
        raise ValueError("CLI install path must stay inside the extracted bundle")
    executable = (binaries / relative).resolve(strict=True)
    if not executable.is_relative_to(binaries.resolve()):
        raise ValueError("CLI install path escaped the materialized bundle")
    config.setdefault("nvkectl", {})["binary"] = str(executable)
    staged["cli"] = {"output": cli_name, "subject": cli_output["digest"],
                     "payload_digest": digest_file(executable)}
    images = config["site"].setdefault("images", {})
    for role in ("controller", "capi-provisioned", "profiles"):
        binding = bindings["images"][role]
        if "fixture" in binding:
            archive = fixture(binding, role)
            images[role] = {"repository": binding["repository"], "digest": binding["digest"], "archive": str(archive)}
        else:
            name, output = selected(binding)
            if output["kind"] != "oci":
                raise ValueError("image binding is not an OCI output")
            archive = args.out / (role + ".tar")
            native = output["digest"]
            if output.get("imageIndex"):
                children = [child for child in output["imageIndex"]["children"]
                            if child["platform"] == "linux/" + args.arch]
                if len(children) != 1:
                    raise ValueError("image output does not contain exactly one selected native platform")
                native = children[0]["manifestDigest"]
            reference = output["reference"].split("@", 1)[0] + "@" + native
            image = invoke_json([str(args.nvkr), "publish", "image-export", "--layout", str(layout),
                                 "--object", name, "--reference", reference,
                                 "--platform", "linux/" + args.arch, "--out", str(archive)])
            if image["manifestDigest"] != native or image["reference"] != reference:
                raise ValueError("image export substituted the selected native subject")
            images[role] = {"repository": output["reference"].split("@", 1)[0],
                            "digest": image["manifestDigest"], "archive": str(archive)}
            staged[role] = {"output": name, "subject": output["digest"],
                            "payload_digest": image["archiveDigest"]}
    overrides = {}
    for item, binding in sorted(bindings["charts"].items()):
        if "fixture" in binding:
            path = fixture(binding, "chart:" + item)
        else:
            name, output = selected(binding)
            if output["kind"] != "helm":
                raise ValueError("chart binding is not a Helm output")
            path = args.out / ("chart-" + item + ".tgz")
            invoke([str(args.nvkr), "publish", "get", "--layout", str(layout),
                    "--object", name, "--out", str(path)])
            staged["chart:" + item] = {"output": name, "subject": output["digest"],
                                        "payload_digest": digest_file(path)}
        overrides[item] = str(path)
    config["site"]["chartOverrides"] = overrides
    config_path = args.out / "e2e-config.json"
    config_path.write_text(json.dumps(config, sort_keys=True, indent=2) + "\n", encoding="utf-8")
    return source, identities, staged, config_path


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--plan-only", action="store_true")
    mode.add_argument("--stage", action="store_true")
    parser.add_argument("--compile-only", action="store_true", help="compile TestE2E without executing tests, after staging")
    for name in ("run", "plan", "definition", "environment", "inputs", "out"):
        parser.add_argument("--" + name, type=Path, required=True)
    for name in ("repository", "run-id", "run-attempt", "reference", "source-node", "stream-name",
                 "gpu", "k8s", "arch", "ubuntu", "versions"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--nvkr", type=Path)
    parser.add_argument("--identity")
    parser.add_argument("--issuer")
    parser.add_argument("--cosign", type=Path)
    parser.add_argument("--cosign-digest")
    parser.add_argument("--minimum-disk-evidence", choices=("strict", "build-provenance-v1"))
    args = parser.parse_args(argv)
    report = {"test_outcome": "not-run", "cleanup_outcome": "not-run"}
    directory_created = False
    try:
        run, mapping = read_json(args.run), read_json(args.inputs)
        named, identities = checked_run(run, args.repository, args.run_id, args.run_attempt)
        report.update(repository=args.repository, run_id=args.run_id, run_attempt=args.run_attempt,
                      publication_ref=args.reference, input_identities=identities,
                      stream_name=args.stream_name, gpu=args.gpu, k8s=args.k8s,
                      harness_commit="not-inspected", artifacts={}, materialization_phase="not-started")
        checked_mapping(mapping)
        expected_stream = f"forge-{args.gpu}-{args.arch}-ubuntu-{args.ubuntu}-kube-{args.k8s}"
        if args.stream_name != expected_stream or args.arch not in ("amd64", "arm64"):
            raise ValueError("full stream identity disagrees with declared capabilities")
        if args.gpu not in ("gb200", "l40s") or args.ubuntu not in ("24.04", "26.04") or args.k8s != "1.36":
            raise ValueError("capabilities do not name a current maintained qualification stream")
        if args.gpu == "l40s" and args.arch != "amd64":
            raise ValueError("the maintained L40S stream uses amd64")
        args.out = args.out.absolute()
        if args.out.exists():
            raise ValueError("refusing to reuse a cycle output directory")
        if args.compile_only and not args.stage:
            raise ValueError("compile-only requires authenticated staging")
        if args.stage and not all((args.nvkr, args.identity, args.issuer, args.cosign,
                                   args.cosign_digest, args.minimum_disk_evidence)):
            raise ValueError("staging requires explicit verifier executable, signer and disk policy")
        args.out.mkdir(mode=0o700, parents=True)
        directory_created = True
        capabilities = f"provider=forge,gpu={args.gpu},arch={args.arch},ubuntu={args.ubuntu},kube={args.k8s}"
        source = {"node": args.source_node, "path": "<restored exact source carrier>", "commit": "not-inspected"}
        staged, config = {}, args.out / "e2e-config.json"
        if args.stage:
            source, identities, staged, config = stage(args, run, mapping, progress=report)
            if args.compile_only:
                compile_source(source["path"])
        report.update(repository=args.repository, run_id=args.run_id, run_attempt=args.run_attempt,
                      stream_name=args.stream_name, gpu=args.gpu, k8s=args.k8s,
                      harness_commit=source["commit"], publication_ref=args.reference,
                      input_identities=identities, artifacts=staged or named,
                      source=source, materialization_outcome="core-artifacts-staged" if args.stage else "not-run",
                      go_test_command=suite_command(source["path"], config, args.versions, capabilities, args.out),
                      go_test_directory=str(Path(source["path"]) / "controller/test/e2e"),
                      runtime_admission="pending live usable-capacity/access and owned-cleanup inventory",
                      live_command_admitted=False,
                      remaining_input_gates=["QCOW2/EFI HTTPS exact-byte read-back", "post-boot material compatibility",
                                             "reviewed fixtures and suite-source capability", "rendered installed dependency closure"],
                      qualification_authority="none")
        (args.out / "qualification.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(report, sort_keys=True, indent=2))
        return 0
    except Exception as exc:
        report["materialization_outcome"] = "failed"
        report["error_class"] = type(exc).__name__
        report["error_code"] = "NVKR-E-PREFLIGHT-FAILED"
        if directory_created:
            (args.out / "qualification-failure.json").write_text(json.dumps(report, sort_keys=True) + "\n", encoding="utf-8")
        print(f"nvke-qualification: [NVKR-E-PREFLIGHT-FAILED] preparation failed ({type(exc).__name__})", file=sys.stderr)
        return 17


if __name__ == "__main__":
    raise SystemExit(main())
