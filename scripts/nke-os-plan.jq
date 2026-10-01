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

# Detect alternate spellings of this repository before enforcing the one exact
# transport URL served by the private-source handoff. This is an early diagnostic
# guard, not a Git URL parser or authenticated repository identity. Catalog and
# Git still own source admission and byte verification.
def nke_source_closure($pin):
  def nke_alias:
    ascii_downcase | split("://") | last | split("@") | last |
    split(":") | join("/") | rtrimstr("/") | rtrimstr(".git") |
    . == "github.com/dsx-ai-factory/nke";
  [.builds[].executionGraph.nodes[] |
    select(.kind == "fetch" and (.repo | nke_alias))] as $sources |
  ($sources | length) > 0 and
  all($sources[]; .repo == "https://github.com/dsx-ai-factory/nke.git" and
    .commit == $pin.commit and .treeHash == $pin.treeHash);
