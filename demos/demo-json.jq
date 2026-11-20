# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Render untrusted release strings as ASCII JSON contents, including C1 and bidi
# controls. Escaping only JSON's C0 controls is insufficient for terminal output.
def hex4:
  [(. / 4096 | floor), ((. / 256 | floor) % 16), ((. / 16 | floor) % 16), (. % 16)] |
  map("0123456789abcdef"[.:.+1]) | join("");
def safe:
  @json | .[1:-1] | explode | map(
    if . < 127 then [.] | implode
    elif . <= 65535 then "\\u" + hex4
    else (. - 65536) as $n |
      "\\u" + (55296 + ($n / 1024 | floor) | hex4) +
      "\\u" + (56320 + ($n % 1024) | hex4)
    end) | join("");
