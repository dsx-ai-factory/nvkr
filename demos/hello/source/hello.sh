#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

printf 'Hello, %s! This is hello @VERSION@, built by NVKR.\n' "${1:-team}"
