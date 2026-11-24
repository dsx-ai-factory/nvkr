#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

if [[ $# != 1 || -z "$1" ]]; then
  echo '[NVKR-E-INVALID-CATALOG] expected one candidate checkout path' >&2
  exit 1
fi

# Inspect candidate data only; this resolver runs from the trusted checkout.
catalogs=()
for catalog in "$1/inputs/catalogs/node-images" "$1/inputs/catalogs/node-images/diagnostic"; do
  if [[ -f "${catalog}/registry.yaml" ]]; then
    catalogs+=("${catalog}")
  fi
done
case ${#catalogs[@]} in
  0)
    echo '[NVKR-E-INVALID-CATALOG] missing supported NVKE OS catalog registry in candidate checkout' >&2
    exit 1
    ;;
  1) printf '%s\n' "${catalogs[0]}" ;;
  *)
    echo '[NVKR-E-INVALID-CATALOG] ambiguous NVKE OS catalog layout in candidate checkout' >&2
    exit 1
    ;;
esac
