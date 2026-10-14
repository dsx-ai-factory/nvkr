#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Dispatch the committed development workflow from a clean, pushed branch."""

import argparse
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
from urllib.parse import quote


WORKFLOW = "nvkr-dev.yml"
COMMAND_TIMEOUT_SECONDS = 30


class PipelineDevError(Exception):
    """A developer-facing failure; this adapter issues no NVKR authority."""

    def __init__(self, code, message, status):
        super().__init__(f"{code}: {message}")
        self.status = status


def dispatch(repo, *, check=False, runner=subprocess.run, output=None, root=None):
    """Check the exact local/remote branch and optionally request one dispatch."""
    output = output or sys.stdout
    root = Path(root) if root is not None else Path(__file__).resolve().parents[1]
    if (not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+", repo)
            or repo.rsplit("/", 1)[1] in {".", ".."}):
        raise PipelineDevError("NVKR-E-USAGE", "--repo must be an explicit OWNER/REPO on github.com", 2)

    env = dict(os.environ, GH_HOST="github.com", GH_PROMPT_DISABLED="1", GIT_TERMINAL_PROMPT="0")

    def command(argv, failure, *, mutates=False):
        try:
            result = runner(argv, cwd=root, env=env, stdin=subprocess.DEVNULL,
                            capture_output=True, text=True, check=False,
                            timeout=COMMAND_TIMEOUT_SECONDS)
        except subprocess.TimeoutExpired as error:
            detail = ("Dispatch outcome is unknown; inspect the listed runs before retrying."
                      if mutates else "No workflow was dispatched.")
            raise PipelineDevError("NVKR-E-TIMEOUT", f"{failure} timed out. {detail}", 31) from error
        except OSError as error:
            raise PipelineDevError("NVKR-E-USAGE", f"Cannot execute {argv[0]}; install it and retry.", 2) from error
        if result.returncode != 0:
            raise PipelineDevError("NVKR-E-FETCH-FAILED", failure, 20)
        return result.stdout.strip()

    def local_branch():
        if command(["git", "status", "--porcelain=v1", "--untracked-files=all"], "Cannot inspect the Git working tree."):
            raise PipelineDevError("NVKR-E-USAGE", "Commit or set aside all tracked and untracked changes before dispatch.", 2)
        branch = command(["git", "symbolic-ref", "--quiet", "--short", "HEAD"],
                         "A named branch is required; check out a branch instead of detached HEAD.")
        if not branch or any(ord(char) < 33 or ord(char) == 127 for char in branch):
            raise PipelineDevError("NVKR-E-USAGE", "Git returned an invalid branch name.", 2)
        commit = command(["git", "rev-parse", "--verify", "HEAD^{commit}"], "Cannot resolve the local commit.")
        if not re.fullmatch(r"[0-9a-f]{40}", commit):
            raise PipelineDevError("NVKR-E-USAGE", "HEAD must resolve to one exact GitHub commit SHA.", 2)
        return branch, commit

    def api(path, failure):
        raw = command(["gh", "api", "--hostname", "github.com", path], failure)
        try:
            value = json.loads(raw)
        except json.JSONDecodeError as error:
            raise PipelineDevError("NVKR-E-FETCH-FAILED", "GitHub returned malformed JSON; no dispatch was requested.", 20) from error
        if not isinstance(value, dict):
            raise PipelineDevError("NVKR-E-FETCH-FAILED", "GitHub returned an ambiguous response; no dispatch was requested.", 20)
        return value

    branch, commit = local_branch()
    workflow = api(f"repos/{repo}/actions/workflows/{WORKFLOW}",
                   f"Cannot find {WORKFLOW} in {repo}. Land it on the default branch first, "
                   "then check `gh auth status` and repository access.")
    if workflow.get("path") != f".github/workflows/{WORKFLOW}" or workflow.get("state") != "active":
        raise PipelineDevError("NVKR-E-USAGE", f"{WORKFLOW} must be registered and active on {repo}'s default branch.", 2)
    remote = api(f"repos/{repo}/git/ref/heads/{quote(branch, safe='')}",
                 f"Cannot read branch {branch!r} in {repo}. Push the branch to this repository and check `gh auth status`.")
    target = remote.get("object")
    if (remote.get("ref") != "refs/heads/" + branch or not isinstance(target, dict)
            or target.get("type") != "commit" or target.get("sha") != commit):
        raise PipelineDevError("NVKR-E-USAGE", f"Remote branch {branch!r} does not match local HEAD {commit}; push or reconcile it first.", 2)
    if local_branch() != (branch, commit):
        raise PipelineDevError("NVKR-E-USAGE", "The local branch changed during preflight; inspect it and retry.", 2)

    status = ["gh", "run", "list", "--workflow", WORKFLOW, "--branch", branch, "--commit", commit,
              "--repo", repo, "--limit", "5", "--json", "databaseId,headSha,status,conclusion,url"]
    print(f"Ready: {repo} branch {branch!r} at {commit}", file=output)
    if check:
        print("Preflight passed; no workflow dispatched.", file=output)
        return
    print("Inspect matching runs: " + shlex.join(status), file=output, flush=True)
    command(["gh", "workflow", "run", WORKFLOW, "--ref", branch, "--repo", repo],
            "Workflow dispatch failed; check Actions permissions and inspect matching runs before retrying.", mutates=True)
    print("Dispatch request accepted. Verify the run's headSha and completion in Actions; branch refs can move after preflight.", file=output)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, epilog="Run make pipeline-dev-check before committing and pushing the generated bundle.")
    parser.add_argument("--repo", required=True, help="Explicit GitHub repository, OWNER/REPO")
    parser.add_argument("--check", action="store_true", help="Check local/remote prerequisites without dispatching")
    args = parser.parse_args(argv)
    try:
        dispatch(args.repo, check=args.check)
    except PipelineDevError as error:
        print(error, file=sys.stderr)
        return error.status
    return 0


if __name__ == "__main__":
    sys.exit(main())
