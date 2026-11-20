# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Only lines beginning with an object are Go events; malformed events fail closed.
def escaped:
  @html | gsub("&apos;"; "&#x27;") | gsub("\\|"; "&#124;") | gsub("[\n\r]"; " ");
def seconds:
  (. * 1000 | round) as $millis |
  (($millis / 1000 | floor) | tostring) + "." +
  ("00" + (($millis % 1000) | tostring))[-3:];
def table($title; $events):
  "### " + $title, "", "| Seconds | Result | Name |", "| ---: | --- | --- |",
  ($events | sort_by(-(.Elapsed // 0), .Package, (.Test // "")) | .[:20][] |
    (.Package + (if has("Test") then " / " + .Test else "" end) | escaped) as $name |
    "| \((.Elapsed // 0) | seconds) | \(.Action) | <code>\($name)</code> |"), "";
[inputs | select(startswith("{")) | fromjson |
  select(.Action == "pass" or .Action == "fail" or .Action == "skip") |
  select(has("Test") or has("Package")) |
  if (.Package | type == "string") and
    ((has("Test") | not) or (.Test | type == "string")) and
    ((has("Elapsed") | not) or (.Elapsed | type == "number"))
  then . else error("invalid terminal test event") end] as $events |
[$events[] | select(has("Test") | not) | select(has("Package"))] as $packages |
[$events[] | select(has("Test"))] as $tests |
if $packages | length == 0 then error("no completed package results; inspect the retained test log")
else
  "## Go test timings", "",
  "Package durations overlap. Parent tests include their subtests; do not sum these rows.", "",
  table("Slowest packages"; $packages), table("Slowest tests and subtests"; $tests),
  "Completed packages: \($packages | length); failed packages: \([$packages[] | select(.Action == "fail")] | length)."
end
