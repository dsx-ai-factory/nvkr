#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu
for argument do
    if [ "$argument" = --create ]; then
        # Stable traversal also chooses a stable payload member for hard links.
        # Preserve numeric package ownership without host user/group names.
        exec /usr/bin/tar --sort=name --numeric-owner \
            '--pax-option=exthdr.name=%d/PaxHeaders/%f' "$@"
    fi
done
exec /usr/bin/tar "$@"
