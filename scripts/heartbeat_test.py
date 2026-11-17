# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Check the shell wrapper's output, status and owned timer cleanup."""
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest


class ShellHeartbeat(unittest.TestCase):
    def wait_for(self, predicate, description):
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if predicate():
                return
            time.sleep(0.02)
        self.fail(f"timed out waiting for {description}")

    def test_command_lifecycle(self):
        wrapper = Path(__file__).resolve().with_name("with-heartbeat.sh")
        for name, status, tick, interrupt in [
            ("tick and success", 0, True, ""),
            ("command failure", 42, False, ""),
            ("interrupted command", 143, False, "child"),
            ("terminal interruption", -signal.SIGINT, False, "group"),
        ]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as tmp:
                work = Path(tmp)
                timer_pids, child_pid = work / "timers", work / "child"
                release, output = work / "release", work / "output"
                # Record ownership and the requested interval, but shorten the test timer.
                timer = work / "sleep"
                timer.write_text('#!/bin/sh\nprintf "%s %s %s\\n" "$$" "$PPID" "$1" >> "$TIMER_PIDS"\nexec /bin/sleep 0.2\n')
                timer.chmod(0o755)
                env = {**os.environ, "PATH": str(work) + os.pathsep + os.environ["PATH"],
                       "TIMER_PIDS": str(timer_pids), "CHILD_PID": str(child_pid),
                       "RELEASE": str(release), "CHILD_STATUS": str(status)}
                # Announce readiness after installing native signal behavior. A shell
                # loop spawning sleep can defer SIGINT across foreground-child races.
                command = """
import os
from pathlib import Path
import signal
import time

signal.signal(signal.SIGINT, signal.SIG_DFL)
Path(os.environ["CHILD_PID"]).write_text(str(os.getpid()))
while not Path(os.environ["RELEASE"]).exists():
    time.sleep(0.02)
raise SystemExit(int(os.environ["CHILD_STATUS"]))
"""
                with output.open("w") as log:
                    process = subprocess.Popen(
                        ["bash", str(wrapper), "build", sys.executable, "-c", command],
                        env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True,
                    )
                    try:
                        self.wait_for(lambda: child_pid.exists() and child_pid.stat().st_size > 0
                                      and timer_pids.exists() and timer_pids.stat().st_size > 0,
                                      "command and timer startup")
                        if tick:
                            self.wait_for(lambda: "[working] build" in output.read_text()
                                          and len(timer_pids.read_text().splitlines()) >= 2,
                                          "heartbeat tick and next timer")
                        if interrupt == "group":
                            os.killpg(process.pid, signal.SIGINT)
                        elif interrupt == "child":
                            os.kill(int(child_pid.read_text()), signal.SIGTERM)
                        else:
                            release.touch()
                        self.assertEqual(process.wait(timeout=5), status, output.read_text())
                        self.assertIn("[start] build", output.read_text())
                        timers = [line.split() for line in timer_pids.read_text().splitlines()]
                        self.assertTrue(all(len(timer) == 3 and timer[2] == "10" for timer in timers), timers)
                        remaining_pids = set()

                        def timers_reaped():
                            remaining_pids.clear()
                            for pid in {int(value) for timer in timers for value in timer[:2]}:
                                try:
                                    os.kill(pid, 0)
                                except ProcessLookupError:
                                    continue
                                remaining_pids.add(pid)
                            return not remaining_pids

                        # Group interruption can leave an exited timer briefly visible
                        # until the OS reaps it. Require disappearance within a bound.
                        try:
                            self.wait_for(timers_reaped, "timer processes to be reaped")
                        except self.failureException as error:
                            self.fail(f"{error}; remaining timer/owner PIDs: {sorted(remaining_pids)}; "
                                      f"recorded PID/PPID/interval: {timers}")
                    finally:
                        # Also own cleanup if an assertion or the wrapper fails.
                        try:
                            os.killpg(process.pid, signal.SIGKILL)
                        except (ProcessLookupError, PermissionError):
                            pass
                        process.wait(timeout=5)


if __name__ == "__main__":
    unittest.main()
