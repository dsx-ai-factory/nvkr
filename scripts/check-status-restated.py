#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Fail when a diff restates validation or evidence status outside the reconciliation ledger.

Status lives only in docs/adr/reconciliation.md rows and dated docs/evidence/ records; every
other document links the row instead of restating it. This checks Markdown lines added between
BASE and HEAD against status phrases taken from fixed review findings. It cannot judge whether
a sentence is true, and it ignores text already in the tree. Mark a deliberate exception on the
matched line with <!-- status-ok: reason -->.
"""

import argparse
import functools
import re
import subprocess
import sys

LEDGER = "docs/adr/reconciliation.md"
EXEMPT = (LEDGER, "docs/evidence/")
MARKDOWN = ("*.md", "*.mdx")

# Each phrase claims or denies recorded validation. "Unqualified" names a lane
# (static-unqualified) and "unverified" describes what a verifier skips, so neither counts.
_UN = r"un(?:recorded|proven|exercised|validated|tested)"
_DONE = r"(?:recorded|exercised|proven|validated|qualified|verified)"
_RECORD = r"(?:hosted |external |acceptance |development )*(?:run|execution|evidence|result|observation)s?"
PHRASES = [re.compile(p.replace(" ", r"\s+"), re.IGNORECASE) for p in (
    rf"\b(?:remains?|remained|is|are) (?:still )?{_UN}\b",
    rf"\brequires? (?:a |an )?recorded {_RECORD}\b",
    r"\b(?:awaits?|needs?|pending|until) (?:a |an )?recorded (?:run|hosted|execution|evidence)",
    r"\bno (?:hosted |external )?(?:run|evidence|execution) (?:has been |is )?recorded\b",
    rf"\b(?:has|have) not (?:yet )?been {_DONE}\b",
    rf"\bnot yet {_DONE}\b",
    r"\b(?:hosted|external|production) (?:publication|signing|acceptance|execution|qualification|verification)"
    rf" (?:was|were|has been|have been|is|are) (?:not )?(?:yet )?{_DONE}\b",
    r"\b(?:externally|operationally) (?:exercised|accepted|validated|proven)\b",
)]
ESCAPE = re.compile(r"<!--\s*status-ok\b(.*?)-->")
REASON = re.compile(r"^\s*:\s*\S")
OWNER_ROW = re.compile(r"^\| (\d\d)(?:[–-](\d\d))? ([^|]+?) \|")
ADR_PATH = re.compile(r"^docs/adr/(\d{4})-")
ADR_REF = re.compile(r"\bADR-(\d{1,2})\b")


def git(repo, *args):
    """Return git stdout; a failed command is a usage error, never a silent pass."""
    run = subprocess.run(["git", "-C", repo, *args], capture_output=True, text=True, check=False)
    if run.returncode != 0:
        raise SystemExit(f"check-status-restated: git {' '.join(args)}: {run.stderr.strip()}")
    return run.stdout


def added_runs(diff):
    """Yield (path, [(line_number, text)]) for the added lines of each -U0 hunk."""
    path, run, nxt, prev = None, [], 0, ""
    for line in diff.splitlines():
        # An added line may itself begin with "++ ", so a header needs its "--- " line.
        header, prev = prev.startswith("--- ") and line.startswith("+++ "), line
        if line.startswith(("diff --git ", "@@")) and run:
            yield path, run
            run = []
        if header:
            path = None if line == "+++ /dev/null" else line[6:]
        elif line.startswith("@@"):
            nxt = int(re.match(r"@@ -\S+ \+(\d+)", line).group(1))
        elif line.startswith("+") and path:
            run.append((nxt, line[1:]))
            nxt += 1
    if run:
        yield path, run


def has_reason(line):
    """Report whether the line carries a status-ok escape with a non-empty reason."""
    return any(REASON.match(m.group(1)) for m in ESCAPE.finditer(line))


def scan(run):
    """Return (line, message) findings for one run; phrases may wrap across its lines."""
    text, starts = "", []
    for _, line in run:
        starts.append(len(text))
        text += line + " "

    def line_of(offset):
        return max(i for i, s in enumerate(starts) if s <= offset)

    findings, seen = [], set()
    for number, line in run:
        if any(not REASON.match(m.group(1)) for m in ESCAPE.finditer(line)):
            findings.append((number, "status-ok escape needs a reason: <!-- status-ok: why -->"))
    for phrase in PHRASES:
        for m in phrase.finditer(text):
            first, last = line_of(m.start()), line_of(m.end() - 1)
            if first in seen:
                continue
            if any(has_reason(run[i][1]) for i in range(first, last + 1)):
                continue
            seen.add(first)
            findings.append((run[first][0], f'status restated ("{" ".join(m.group().split())}")'))
    return findings


@functools.cache
def ledger(repo, head):
    """Return the ledger lines at head."""
    return git(repo, "show", f"{head}:{LEDGER}").splitlines()


def ledger_row(repo, head, path, line):
    """Name the ledger row to link: the Support-by-owner row of the ADR the line belongs to."""
    adr = ADR_PATH.match(path) or ADR_REF.search(line)
    if not adr:
        return LEDGER
    number = int(adr.group(1))
    for i, row in enumerate(ledger(repo, head), 1):
        m = OWNER_ROW.match(row)
        if m and int(m.group(1)) <= number <= int(m.group(2) or m.group(1)):
            return f"{LEDGER}:{i} (Support by owner, {m.group(1)} {m.group(3)})"
    return LEDGER


def check(repo, base, head):
    """Return formatted findings for Markdown lines added in base...head."""
    diff = git(repo, "-c", "core.quotePath=false", "diff", "--no-color", "--no-ext-diff", "-U0", "-M",
               "--src-prefix=a/", "--dst-prefix=b/", "--diff-filter=AMR", f"{base}...{head}", "--", *MARKDOWN)
    out = set()
    for path, run in added_runs(diff):
        if path.startswith(EXEMPT):
            continue
        text = dict(run)
        for number, message in scan(run):
            if message.startswith("status restated"):
                message += f"; link {ledger_row(repo, head, path, text[number])} instead"
            out.add((path, number, message))
    return [f"{path}:{number}: {message}" for path, number, message in sorted(out)]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo", default=".", help="repository checkout (default: .)")
    parser.add_argument("base", help="base revision; the diff starts at its merge base with head")
    parser.add_argument("head", help="head revision")
    args = parser.parse_args(argv)
    findings = check(args.repo, args.base, args.head)
    for finding in findings:
        print(finding)
    print(f"check-status-restated: {len(findings)} finding(s)", file=sys.stderr)
    if findings:
        print("Status lives in the reconciliation ledger: link its row, or add "
              "<!-- status-ok: reason --> to the line.", file=sys.stderr)
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
