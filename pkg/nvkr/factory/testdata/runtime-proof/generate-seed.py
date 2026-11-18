#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Generate the BIOS guest; no assembler or downloaded guest is required."""
from pathlib import Path

# Real-mode x86: establish DS/SS/SP, wait ten seconds through BIOS int 15h,
# write this sector to LBA 8 using int 13h's extended write, then request
# QEMU's pc-machine ACPI shutdown. A failed write halts without shutdown so
# Packer times out. The delay gives Packer time to start its shutdown waiter.
code = bytes.fromhex(
    "fa 31c0 8ed8 8ed0 bc007c fb "
    "b99800 ba8096 b486 cd15 "  # CX:DX = 0x00989680 = 10,000,000 us
    "be807c b280 b80043 cd13 7207 "
    "b80020 ba0406 ef "
    "fa f4 ebfd"
)
seed = bytearray(65536)
seed[:len(code)] = code
# Disk address packet: one sector at 0000:7c00 -> LBA 8.
seed[0x80:0x90] = bytes.fromhex("1000 0100 007c 0000 0800000000000000")
seed[510:512] = bytes.fromhex("55aa")
Path(__file__).with_name("seed.raw").write_bytes(seed)
