# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

def disk_root:
  [.builds[0].executionGraph.nodes[] |
    select(.kind == "build" and .builderType == "vm-image") | .component] |
  if length == 1 then .[0] else error("expected exactly one disk root") end;

def toolchain_overlay($arch; $from; $to; $digest; $components):
  {apiVersion:"nkd.dev/catalog-overlay/v3", kind:"CatalogOverlay",
   name:("nke-os-local-toolchain-" + $arch), requiresCatalogDigest:$digest,
   toolchainSubstitutions:[{arch:$arch, from:$from, to:$to, components:$components}]};

# This launcher projection follows an already admitted Plan. Catalog loading
# authenticates the exact predecessor and substitution; this is not a validator
# or permission to rewrite unrelated producer toolchains.
def simulator_toolchain_scope:
  [.builds[] | select(.name == "nicosim-tools-arm64")] |
  if length != 1 then error("expected one simulator tools build") else .[0] end |
  . as $build |
  [.components[] as $name | .catalogSnapshot.Components[$name] |
    if .fromSource != true or (.toolchainImages.arm64 | type) != "string" then
      error("simulator component lacks a source toolchain")
    else {name:$name, pin:.toolchainImages.arm64} end] as $selected |
  [$selected[].pin] | unique |
  if length != 1 then error("expected one simulator source toolchain") else .[0] end |
  {pin:., catalogDigest:$build.catalogDigest, components:([$selected[].name] | sort)};

# Only the two source-built site charts share the local Helm wrapper. The
# ordinary catalog overlay authenticates every selected predecessor digest.
def site_helm_toolchain_scope:
  [.builds[] | select(.name == "controller-chart" or .name == "nktp")] as $builds |
  if ($builds | length) != 2 then error("expected two site chart builds") else . end |
  ([$builds[].catalogDigest] | unique |
    if length != 1 then error("site charts must share one catalog") else .[0] end) as $digest |
  [$builds[] | . as $build | .components[] as $name |
    $build.catalogSnapshot.Components[$name] |
    if .fromSource != true or (.toolchainImages.arm64 | type) != "string" then
      error("site chart lacks a source toolchain")
    else {name:$name, pin:.toolchainImages.arm64} end] as $selected |
  [$selected[].pin] | unique |
  if length != 1 then error("expected one site Helm toolchain") else .[0] end |
  {pin:., catalogDigest:$digest, components:([$selected[].name] | sort)};

# Conservatively recognize the NKE project path before enforcing the one exact
# transport URL served by the private-source handoff. This lexical diagnostic
# also rejects that project path on other hosts; it does not parse or authenticate
# Git URLs. Catalog and Git still own source admission and byte verification.
def nke_source_closure($pin):
  def nke_alias:
    ascii_downcase | split("?")[0] | split("#")[0] |
    split(":") | join("/") | split("/") | map(select(length > 0)) |
    .[-2:] | . == ["dsx-ai-factory", "nke"] or . == ["dsx-ai-factory", "nke.git"];
  [.builds[].executionGraph.nodes[] |
    select(.kind == "fetch" and (.repo | nke_alias))] as $sources |
  ($sources | length) > 0 and
  all($sources[]; .repo == "https://github.com/dsx-ai-factory/nke.git" and
    .commit == $pin.commit and .treeHash == $pin.treeHash);
