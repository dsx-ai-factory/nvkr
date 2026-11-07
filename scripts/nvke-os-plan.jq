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

# Relocate authored references and attach factory-compiled, predecessor-bound overlays.
def local_toolchain_definition($definition_dir; $repository_prefix; $scopes):
  def relocate: if startswith("/") then error("local demo catalog references must be repository-relative")
    else $repository_prefix + $definition_dir + "/" + . end;
  def locate: .base |= relocate | if .overlays then .overlays |= map(relocate) else . end;
  .catalog |= locate |
  .catalog as $default |
  .outputs |= map(.name as $name |
    if .catalog then .catalog |= locate else .catalog = $default end |
    .catalog.overlays = ((.catalog.overlays // []) +
      [$scopes | to_entries[] | select(.value.outputs | index($name)) |
        "./toolchain-attempt" + (if .key == 0 then "" else "-" + (.key | tostring) end) + ".json"]));

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
