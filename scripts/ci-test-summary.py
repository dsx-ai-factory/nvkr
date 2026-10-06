#!/usr/bin/env python3
"""Summarize go test -json events captured from make test (including make output)."""

import html
import json
import sys


def summarize(lines):
    packages, tests = [], []
    for line in lines:
        # Make's echoed commands and the final coverage total are not JSON.
        if not line.startswith("{"):
            continue
        event = json.loads(line)
        if event.get("Action") not in {"pass", "fail", "skip"}:
            continue
        if "Test" in event:
            tests.append(event)
        elif "Package" in event:
            packages.append(event)
    if not packages:
        raise ValueError("no completed package results; inspect the retained test log")

    result = ["## Go test timings", "",
              "Package durations overlap. Parent tests include their subtests; do not sum these rows.", ""]
    for title, events in [("Slowest packages", packages), ("Slowest tests and subtests", tests)]:
        result += ["### " + title, "", "| Seconds | Result | Name |", "| ---: | --- | --- |"]
        for event in sorted(events, key=lambda e: (-e.get("Elapsed", 0), e["Package"], e.get("Test", "")))[:20]:
            name = event["Package"] + (" / " + event["Test"] if "Test" in event else "")
            # Test names are repository-controlled: render as text, never HTML.
            name = html.escape(name).replace("|", "&#124;").replace("\n", " ").replace("\r", " ")
            result.append(f"| {event.get('Elapsed', 0):.3f} | {event['Action']} | <code>{name}</code> |")
        result.append("")
    failures = sum(event["Action"] == "fail" for event in packages)
    result.append(f"Completed packages: {len(packages)}; failed packages: {failures}.")
    return "\n".join(result) + "\n"


if __name__ == "__main__":
    with open(sys.argv[1], encoding="utf-8") as source:
        print(summarize(source), end="")
