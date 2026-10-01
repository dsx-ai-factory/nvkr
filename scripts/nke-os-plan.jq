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
