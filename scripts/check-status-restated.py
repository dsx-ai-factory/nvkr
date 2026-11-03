#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Fail when a diff restates validation or evidence status outside the reconciliation ledger.

Status lives only in docs/adr/reconciliation.md rows and dated docs/evidence/ records; every
other document links the row instead of restating it. This checks Markdown lines added since the
merge base of BASE and HEAD (or the working tree, including untracked files, when HEAD is omitted)
against a fixed list of status phrases taken from fixed review findings. Other phrasings pass; it
cannot judge whether a sentence is true, and it ignores text already in the tree. Mark a deliberate
exception on the matched line with <!-- status-ok: reason -->.
"""

import argparse
import codecs
import functools
from pathlib import Path
import re
import subprocess
import sys

LEDGER = "docs/adr/reconciliation.md"
OWNERS = f"{LEDGER}#support-by-owner"
EVIDENCE = "docs/evidence/"
MARKDOWN = ("*.md", "*.mdx")
REVISION_HINT = "fetch BASE and HEAD with full history, or set STATUS_BASE/STATUS_HEAD"

# Each phrase claims or denies recorded validation. "Unqualified" names a lane
# (static-unqualified) and a bare "unverified" describes what a verifier skips, so only a
# validation subject makes "unverified" count. "Externally exercised" also names the evidence
# category the ledger owns, so only its predicate form counts.
_UN = r"un(?:recorded|proven|exercised|validated|tested)"
_DONE = r"(?:recorded|exercised|proven|validated|qualified|verified)"
_RECORD = r"(?:hosted |external |acceptance |development )*(?:run|execution|evidence|result|observation)s?"
PHRASES = [re.compile(p.replace(" ", r"\s+"), re.IGNORECASE) for p in (
    rf"\b(?:remains?|remained|is|are) (?:still )?{_UN}\b",
    r"\b(?:execution|acceptance|qualification|cycle|deployment|build|run|publication)s? (?:remains?|is|are)"
    r" (?:still )?unverified\b",
    rf"\brequires? (?:a |an )?recorded {_RECORD}\b",
    r"\b(?:awaits?|needs?|pending|until) (?:a |an )?recorded (?:hosted |external )?(?:run|execution)",
    r"\bno (?:hosted |external )?(?:run|evidence|execution) (?:has been |is )?recorded\b",
    rf"\b(?:has|have) not (?:yet )?been {_DONE}\b",
    rf"\bnot yet {_DONE}\b",
    r"\b(?:hosted|external|production) (?:publication|signing|acceptance|execution|qualification|verification)"
    rf" (?:was|were|has been|have been|is|are) (?:not )?(?:yet )?{_DONE}\b",
    r"\b(?:is|are|was|were|been|not) (?:yet )?(?:externally|operationally)"
    r" (?:exercised|accepted|validated|proven)\b",
)]
ESCAPE = re.compile(r"<!--\s*status-ok\b(.*?)-->")
REASON = re.compile(r"^\s*:\s*\S")
OWNER_ROW = re.compile(r"^\| (\d\d)(?:[–-](\d\d))? ([^|]+?) \|")
ADR_PATH = re.compile(r"^docs/adr/(\d{4})-")
ADR_REF = re.compile(r"\bADR-(\d{1,2})\b")


def git(repo, *args, hint=None):
    """Return git stdout; a failed command is a usage error, never a silent pass."""
    run = subprocess.run(["git", "-C", repo, *args], capture_output=True, text=True,
                         encoding="utf-8", errors="replace", check=False)
    if run.returncode != 0:
        detail = f" ({hint})" if hint else ""
        raise SystemExit(f"check-status-restated: git {' '.join(args)}: {run.stderr.strip()}{detail}")
    return run.stdout


def header_path(line):
    """Return the repository path named by a "+++ b/..." header, or None for /dev/null."""
    name = line[4:].removesuffix("\t")  # git appends a tab to names containing spaces
    if name.startswith('"'):
        name = codecs.escape_decode(name[1:-1].encode("utf-8"))[0].decode("utf-8", "replace")
    return None if name == "/dev/null" else name.removeprefix("b/")


def added_lines(diff):
    """Return {path: {new-side line numbers}} for the added lines of a -U0 diff."""
    added, path, nxt, in_header = {}, None, 0, False
    for line in diff.splitlines():
        if line.startswith("diff --git "):
            path, in_header = None, True
        elif in_header and line.startswith("+++ "):
            path = header_path(line)
        elif line.startswith("@@"):
            in_header = False
            nxt = int(re.match(r"@@ -\S+ \+(\d+)", line).group(1))
        elif not in_header and line.startswith("+") and path:
            added.setdefault(path, set()).add(nxt)
            nxt += 1
    return added


def untracked_lines(repo):
    """Return {path: None} for untracked, non-ignored Markdown files, whose every line is added."""
    listed = git(repo, "-c", "core.quotePath=false", "ls-files", "-z", "--others", "--exclude-standard",
                 "--", *MARKDOWN)
    return dict.fromkeys(filter(None, listed.split("\0")))


def paragraphs(text, added):
    """Yield blank-line-delimited paragraphs that hold an added line, as [(number, text, added)].

    Matching whole paragraphs lets a phrase wrap from unchanged lines onto an added one without
    crossing into the next paragraph; added=None marks every line added.
    """
    run = []
    for number, line in enumerate([*text.splitlines(), ""], 1):
        if line.strip():
            run.append((number, line, added is None or number in added))
        elif run:
            if any(is_added for _, _, is_added in run):
                yield run
            run = []


def has_reason(line):
    """Report whether the line carries a status-ok escape with a non-empty reason."""
    return any(REASON.match(m.group(1)) for m in ESCAPE.finditer(line))


def scan(run):
    """Return (line, message, context) findings for one run; phrases may wrap across its lines."""
    text, starts = "", []
    for _, line, _ in run:
        starts.append(len(text))
        text += line + " "

    def line_of(offset):
        return max(i for i, s in enumerate(starts) if s <= offset)

    findings, seen = [], set()
    for number, line, added in run:
        if added and any(not REASON.match(m.group(1)) for m in ESCAPE.finditer(line)):
            findings.append((number, "status-ok escape needs a reason: <!-- status-ok: why -->", ""))
    for phrase in PHRASES:
        for m in phrase.finditer(text):
            lines = range(line_of(m.start()), line_of(m.end() - 1) + 1)
            added = [run[i][0] for i in lines if run[i][2]]
            if not added or added[0] in seen or any(has_reason(run[i][1]) for i in lines):
                continue
            seen.add(added[0])
            # The matched lines decide the ADR row first, then the rest of the paragraph.
            span = " ".join(run[i][1] for i in lines) + " " + text
            findings.append((added[0], f'status restated ("{" ".join(m.group().split())}")', span))
    return findings


@functools.cache
def ledger(repo, head):
    """Return the ledger lines at head, or in the working tree when head is None."""
    if head is None:
        return Path(repo, LEDGER).read_text(encoding="utf-8").splitlines()
    return git(repo, "show", f"{head}:{LEDGER}").splitlines()


def ledger_row(repo, head, path, text):
    """Name the ledger row to link: the Support-by-owner row of the ADR the line belongs to."""
    adr = ADR_PATH.match(path) or ADR_REF.search(text)
    if not adr:
        return f"its {LEDGER} row"
    number = int(adr.group(1))
    for row in ledger(repo, head):
        m = OWNER_ROW.match(row)
        if m and int(m.group(1)) <= number <= int(m.group(2) or m.group(1)):
            return f'{OWNERS} row "{m.group(1)} {m.group(3)}"'
    return f"its {LEDGER} row"


def check(repo, base, head=None):
    """Return (merge base, formatted findings) for Markdown lines added since the merge base."""
    since = git(repo, "merge-base", base, head or "HEAD", hint=REVISION_HINT).strip()
    diff = git(repo, "-c", "core.quotePath=false", "diff", "--no-color", "--no-ext-diff", "-U0", "-M",
               "--src-prefix=a/", "--dst-prefix=b/", "--diff-filter=AMR", since, *([head] if head else []),
               "--", *MARKDOWN, hint=REVISION_HINT)
    changed = added_lines(diff) | ({} if head else untracked_lines(repo))
    out = set()
    for path, added in changed.items():
        if path == LEDGER or path.startswith(EVIDENCE):
            continue
        if head:
            text = git(repo, "show", f"{head}:{path}")
        else:
            text = Path(repo, path).read_text(encoding="utf-8", errors="replace")
        for run in paragraphs(text, added):
            for number, message, span in scan(run):
                if span:
                    message += f"; link {ledger_row(repo, head, path, span)} instead"
                out.add((path, number, message))
    return since, [f"{path}:{number}: {message}" for path, number, message in sorted(out)]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo", default=".", help="repository checkout (default: .)")
    parser.add_argument("base", help="base revision; the diff starts at its merge base with head")
    parser.add_argument("head", nargs="?", help="head revision (default: the working tree and untracked files)")
    args = parser.parse_args(argv)
    args.head = args.head or None  # make passes an empty STATUS_HEAD for the working tree
    since, findings = check(args.repo, args.base, args.head)
    for finding in findings:
        print(finding)
    print(f"check-status-restated: {len(findings)} finding(s) in lines added since {since[:12]}, "
          f"the merge base of {args.base} and {args.head or 'the working tree'}", file=sys.stderr)
    if findings:
        print("Status lives in the reconciliation ledger: link its row, or add "
              "<!-- status-ok: reason --> to the line.", file=sys.stderr)
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
