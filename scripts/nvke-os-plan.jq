# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

def disk_root:
  [.builds[0].executionGraph.nodes[] |
    select(.kind == "build" and .builderType == "vm-image") | .component] |
  if length == 1 then .[0] else error("expected exactly one disk root") end;

def toolchain_overlay($arch; $from; $to; $digest; $components):
  {apiVersion:"nvkr.dev/catalog-overlay/v3", kind:"CatalogOverlay",
   name:("nvke-os-local-toolchain-" + $arch), requiresCatalogDigest:$digest,
   toolchainSubstitutions:[{arch:$arch, from:$from, to:$to, components:$components}]};

def disk_toolchain_scope:
  [.builds[] | select(any(.executionGraph.nodes[]; .builderType == "vm-image"))] |
  if length != 1 then error("expected one disk build") else .[0] end |
  . as $disk |
  [.executionGraph.nodes[] | select(.kind == "build" and .builderType == "vm-image") | .component] |
  if length != 1 then error("expected one disk root") else .[0] end |
  $disk.catalogSnapshot.Components[.].toolchainImages.arm64 as $pin |
  {pin:$pin, catalogDigest:$disk.catalogDigest, components:
    ([$disk.components[] as $name | $disk.catalogSnapshot.Components[$name] |
      select(.fromSource == true and .toolchainImages.arm64 == $pin) | $name] | sort)};

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

# Only the two source-built site charts select the shared local toolchain. The
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

# The full Definition uses one measured image for three catalog-scoped producer
# groups. Keep each authenticated predecessor separate rather than applying the
# disk catalog's substitution to unrelated catalogs.
def local_toolchain_scopes($lane):
  . as $plan |
  (if $lane == "all" then [disk_toolchain_scope, simulator_toolchain_scope, site_helm_toolchain_scope]
   elif $lane == "os" then [disk_toolchain_scope]
   elif $lane == "simulator" then [simulator_toolchain_scope]
   elif $lane == "site" then [site_helm_toolchain_scope]
   else error("unsupported local toolchain selection") end) |
  if ([.[].catalogDigest] | unique | length) != length then
    error("local toolchain groups must have distinct catalogs")
  else map(. as $scope | . + {outputs:
    ([$plan.builds[] | select(.catalogDigest == $scope.catalogDigest) | .name] | sort)}) end;

# Conservatively recognize the NVKE project path before enforcing the one exact
# transport URL served by the private-source handoff. This lexical diagnostic
# also rejects that project path on other hosts; it does not parse or authenticate
# Git URLs. Catalog and Git still own source admission and byte verification.
def nvke_source_closure($pin):
  def nvke_alias:
    ascii_downcase | split("?")[0] | split("#")[0] |
    split(":") | join("/") | split("/") | map(select(length > 0)) |
    .[-2:] | . == ["dsx-ai-factory", "nvke"] or . == ["dsx-ai-factory", "nvke.git"];
  [.builds[].executionGraph.nodes[] |
    select(.kind == "fetch" and (.repo | nvke_alias))] as $sources |
  ($sources | length) > 0 and
  all($sources[]; .repo == "https://github.com/dsx-ai-factory/nvke.git" and
    .commit == $pin.commit and .treeHash == $pin.treeHash);
