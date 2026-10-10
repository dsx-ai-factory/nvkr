#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Shared plumbing for the self-guided demo scripts. This file is sourced; it does
# not run a journey by itself.

demo_die() {
  printf 'demo: %s\n' "$*" >&2
  exit 2
}

demo_section() {
  printf '\n\033[1;36m%s\033[0m\n' "$1"
}

demo_need_command() {
  command -v "$1" >/dev/null 2>&1 || demo_die "missing required command: $1"
}

# Lifecycle attempt names allow lowercase characters. ISO-style uppercase T/Z
# separators would reject a default invocation before the Run can be prepared.
demo_default_attempt() {
  printf '%s-%s-%s\n' "$1" "$(date -u +%Y%m%dt%H%M%Sz)" "$$"
}

# Select an injected binary for hermetic tests, or keep the repository binary fresh
# for a human running a demo directly after editing the Go sources.
demo_prepare_nvkr() {
  NVKR=${NVKR_BIN:-bin/nvkr}
  if [ -n "${NVKR_BIN:-}" ]; then
    [ -x "$NVKR" ] || demo_die "NVKR_BIN is not executable: $NVKR"
    return
  fi

  local need_build=0 build_input
  if [ ! -x "$NVKR" ]; then
    need_build=1
  elif find client cmd internal pkg -type f -newer "$NVKR" -print -quit | grep -q .; then
    need_build=1
  else
    for build_input in go.mod go.sum Makefile .go-version; do
      [ ! "$build_input" -nt "$NVKR" ] || need_build=1
    done
  fi
  if [ "$need_build" -eq 1 ]; then
    demo_section "Build nvkr (the existing binary is missing or stale)"
    demo_need_command make
    make build
  fi
}

demo_absolute_path() {
  case "$1" in
    /*) printf '%s\n' "$1" ;;
    *) printf '%s/%s\n' "$PWD" "$1" ;;
  esac
}

# Admit a repository-relative workspace below .nvkr before any directory is
# created. Reject lexical escapes and existing symlink ancestors so a demo cannot
# redirect its owned workspace outside the repository.
demo_require_nvkr_child_path() {
  local work=${1:-} part current=.nvkr
  case "$work" in
    .nvkr/?*) ;;
    *) demo_die "--work must be a repository-relative child of .nvkr" ;;
  esac
  [ ! -L .nvkr ] || demo_die ".nvkr must not be a symlink"
  IFS=/ read -r -a parts <<< "$work"
  for part in "${parts[@]:1}"; do
    case "$part" in
      ''|.|..) demo_die "--work contains an unsafe path component: $work" ;;
    esac
    current="$current/$part"
    [ ! -L "$current" ] || demo_die "--work must not traverse a symlink: $work"
  done
}

# Prepare a clean workspace without turning --work into an arbitrary recursive
# deletion primitive. A demo marks its directory on first use and may reset only a
# directory bearing its exact owner marker. The optional third argument is the
# absolute legacy default path; it lets us adopt pre-marker default workspaces once
# without trusting an arbitrary caller-selected directory.
demo_prepare_work() {
  local work=${1:-} owner=${2:-} legacy_default=${3:-}
  local marker expected actual marker_owner entry

  [ -n "$work" ] || demo_die "work directory must not be empty"
  [ -n "$owner" ] || demo_die "demo workspace owner must not be empty"
  case "$work" in
    /|"$PWD") demo_die "refusing unsafe demo work directory: $work" ;;
  esac
  [ ! -L "$work" ] || demo_die "work directory must not be a symlink: $work"

  marker="$work/.nvkr-demo-work-v1"
  expected="nvkr-demo-work/v1
owner=$owner"

  if [ ! -e "$work" ]; then
    mkdir -p -- "$work"
  elif [ ! -d "$work" ]; then
    demo_die "work path is not a directory: $work"
  fi

  if [ -e "$marker" ] || [ -L "$marker" ]; then
    [ -f "$marker" ] && [ ! -L "$marker" ] ||
      demo_die "demo workspace marker is not a regular file: $marker"
    actual=$(<"$marker")
    if [ "$actual" != "$expected" ]; then
      marker_owner=$(printf '%s\n' "$actual" | sed -n 's/^owner=//p')
      [ -n "$marker_owner" ] || marker_owner=unknown
      demo_die "work directory belongs to demo '$marker_owner', not '$owner': $work"
    fi
  elif [ -n "$legacy_default" ] && [ "$work" = "$legacy_default" ]; then
    printf 'demo: adopting legacy workspace for safe reruns: %s\n' "$work"
  elif [ -z "$(find "$work" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
    : # An empty caller-created directory contains nothing that cleanup can destroy.
  else
    demo_die "work directory is not an owned demo workspace: $work"
  fi

  # The exact root and owner were validated above. Globs are expanded one direct
  # child at a time; symlink children are unlinked rather than followed.
  for entry in "$work"/* "$work"/.[!.]* "$work"/..?*; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    [ "$entry" = "$marker" ] && continue
    rm -rf -- "$entry"
  done
  (umask 077; printf '%s\n' "$expected" > "$marker")
}

demo_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    demo_need_command shasum
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}
