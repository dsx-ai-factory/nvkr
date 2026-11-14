# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Optional offline workflow diagnostics; no release or merge authority."""

import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import tarfile
import tempfile


VERSION = "1.32.0"
PINS = {
    ("Linux", "x86_64"): (
        "x86_64-unknown-linux-gnu",
        "bb110cc765eca9cd7efb2e284a956be5974c60027b0970f16397d86208a9957b",
    ),
    ("Darwin", "arm64"): (
        "aarch64-apple-darwin",
        "6efed18c4539a663fd3c851a2be9533fd8456c4e646d9416206da059154a0d3c",
    ),
}


def collect(root):
    listed = subprocess.run(
        ["git", "-C", str(root), "ls-files", "-z", "--cached", "--others",
         "--exclude-standard", "--", ".github"],
        check=True, capture_output=True, timeout=15,
    ).stdout.decode().split("\0")
    inputs = sorted({name for name in listed if name and (
        (Path(name).parent.as_posix() == ".github/workflows"
         and Path(name).suffix in (".yml", ".yaml"))
        or Path(name).name in ("action.yml", "action.yaml")
    )})
    if not inputs:
        raise ValueError("no workflow/action inputs collected")
    for name in inputs:
        path = root / name
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"missing or non-regular input: {name}")
        path.read_bytes()  # Missing/unreadable indexed inputs must not silently disappear.
    return inputs


def install(work):
    target, checksum = PINS[(platform.system(), platform.machine())]
    archive = work / "zizmor.tar.gz"
    with (work / "download.txt").open("wb") as log:
        subprocess.run(
            ["curl", "--fail", "--silent", "--show-error", "--location",
             "--connect-timeout", "10", "--max-time", "60",
             f"https://github.com/zizmorcore/zizmor/releases/download/v{VERSION}/zizmor-{target}.tar.gz",
             "--output", str(archive)],
            check=True, stdout=log, stderr=log, timeout=70,
        )
    if hashlib.sha256(archive.read_bytes()).hexdigest() != checksum:
        raise ValueError("zizmor archive checksum mismatch; executable not extracted")
    with tarfile.open(archive, "r:gz") as bundle:
        member = bundle.getmember("zizmor")
        if not member.isfile():
            raise ValueError("zizmor archive member is not a regular file")
        binary = work / "zizmor"
        with bundle.extractfile(member) as source:
            binary.write_bytes(source.read())
        binary.chmod(0o700)
    return binary


def summarize(raw):
    findings = json.loads(raw)
    if not isinstance(findings, list):
        raise ValueError("expected zizmor JSON v1 findings array")
    lines = []
    for finding in findings:
        # Require the fields used by this renderer; malformed output is incomplete.
        ident, desc = finding["ident"], finding["desc"]
        severity = finding["determinations"]["severity"]
        ignored = finding["ignored"]
        if not all(isinstance(v, str) for v in (ident, desc, severity)) or type(ignored) is not bool:
            raise ValueError("invalid finding fields")
        lines.append(f"{severity} {ident}: {desc}" + (" [ignored]" if ignored else ""))
        for loc in finding["locations"]:
            name = loc["symbolic"]["key"]["Local"]["verbatim_path"]
            line = loc["concrete"]["location"]["start_point"]["row"] + 1
            lines.append(f"  {name}:{line}: {loc['symbolic']['annotation']}")
    return len(findings), "\n".join(lines)


def report(root, output):
    output.mkdir(parents=True, exist_ok=True)
    attempt = Path(tempfile.mkdtemp(prefix="attempt-", dir=output))
    # Retain tool bytes separately from the uploaded diagnostics. No shared executable cache.
    work = Path(tempfile.mkdtemp(prefix="nvkr-zizmor-"))
    status = "INCOMPLETE"
    detail = "collection did not finish"
    (attempt / "summary.txt").write_text(f"zizmor {VERSION}: INCOMPLETE\n")
    try:
        inputs = collect(root)
        (attempt / "inputs.json").write_text(json.dumps(inputs, indent=2) + "\n")
        binary = install(work)
        with (attempt / "findings.json").open("wb") as stdout, (attempt / "stderr.txt").open("wb") as stderr:
            result = subprocess.run(
                [str(binary), "--offline", "--strict-collection", "--no-progress",
                 "--color", "never", "--format", "json-v1", "--no-exit-codes", *inputs],
                cwd=root, stdout=stdout, stderr=stderr, timeout=120,
                env=dict(os.environ, RUST_LOG="info"),  # Do not hide incomplete-audit warnings.
            )
        if result.returncode != 0:
            raise ValueError(f"zizmor tool/collection failure (exit {result.returncode})")
        count, text = summarize((attempt / "findings.json").read_text())
        (attempt / "findings.txt").write_text(text + "\n")
        if re.search(r"^[ \t]*(?:WARN|ERROR)[ \t]+",
                     (attempt / "stderr.txt").read_text(), re.MULTILINE):
            raise ValueError("zizmor emitted audit warnings/errors; see stderr.txt and partial findings")
        status = "FINDINGS" if count else "NO FINDINGS"
        detail = f"{count} findings (including ignored entries); {len(inputs)} inputs"
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, tarfile.TarError) as error:
        detail = str(error)
    if (work / "download.txt").exists():
        (attempt / "download.txt").write_bytes((work / "download.txt").read_bytes())
    summary = (f"zizmor {VERSION}: {status}\n{detail}\n"
               "Report-only; offline audits only. Online checks omitted.\n"
               f"Tool/download scratch: {work}\n")
    (attempt / "summary.txt").write_text(summary)
    print(f"zizmor {VERSION}: {status}; diagnostics: {attempt}")
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as stream:
            stream.write(f"zizmor {VERSION}: **{status}** (report-only, offline). See zizmor-report artifact.\n")
    return int(status == "INCOMPLETE")


if __name__ == "__main__":
    repository = Path(__file__).resolve().parents[1]
    sys.exit(report(repository, Path(os.environ.get("ZIZMOR_REPORT_DIR", repository / ".nvkr/zizmor-report")).resolve()))
