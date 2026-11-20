# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Contracts for the hardware E2E workflow's node image check.

The transport is replaced by a scripted opener, so these say what the check requires of an answer and
nothing about any server: the real fetch is exercised by running the script against a real URL, and
that run is evidence of that URL only.
"""

import contextlib
import hashlib
import http.server
import importlib.util
import io
import itertools
import json
import os
from pathlib import Path
import stat
import struct
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request

HERE = Path(__file__).parent
spec = importlib.util.spec_from_file_location("checker", HERE / "hardware-e2e-check-node-image.py")
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)

URL = "https://artifacts.example.test/jobs/7/artifacts/outputs/node-image.qcow2"
SLOT = "os-image-amd64"


def qcow2(version=3, backing=0, bits=16, size=12 << 30, crypt=0, magic=b"QFI\xfb", incompatible=0):
    head = struct.pack(">4sIQIIQI", magic, version, backing, 0, bits, size, crypt).ljust(checker.QCOW2_HEADER_BYTES, b"\0")
    if version == 3:
        # The feature fields: incompatible, compatible and autoclear bits, the refcount order, the header length.
        head += struct.pack(">QQQII", incompatible, 0, 0, 4, checker.QCOW2_V3_HEADER_BYTES)
    return head


IMAGE = qcow2() + b"synthetic payload " * 64
DIGEST = hashlib.sha256(IMAGE).hexdigest()


class Response:
    def __init__(self, status=200, headers=None, chunks=(), fail_after=None):
        self.status = status
        self.headers = dict(headers or {})
        self.chunks = list(chunks)
        self.fail_after = fail_after
        self.closed = False

    def getcode(self):
        return self.status

    def read(self, _size):
        if self.fail_after is not None and not self.chunks:
            raise ConnectionResetError(self.fail_after)
        return self.chunks.pop(0) if self.chunks else b""

    def read1(self, size):
        return self.read(size)

    def close(self):
        self.closed = True


class Opener:
    """Hands out scripted answers in order and remembers what it was asked."""

    def __init__(self, *answers):
        self.answers = list(answers)
        self.calls = []

    def __call__(self, url, headers, timeout):
        self.calls.append((url, dict(headers), timeout))
        answer = self.answers.pop(0)
        if isinstance(answer, BaseException):
            raise answer
        return answer


def probe(status=200, accept="bytes"):
    return Response(status, {"Accept-Ranges": accept} if accept else {}, [b"Q"])


def body(image=IMAGE, length=None, headers=None, split=100, **kwargs):
    declared = len(image) if length is None else length
    merged = {"Content-Length": str(declared), **(headers or {})}
    return Response(200, merged, [image[:split], image[split:]], **kwargs)


def good():
    return Opener(probe(), body())


class CheckTest(unittest.TestCase):
    def check(self, opener, **kwargs):
        return checker.check(SLOT, kwargs.pop("url", URL), kwargs.pop("sha256", DIGEST), opener, **kwargs)

    def assertFails(self, exit_code, opener, message="", **kwargs):
        with self.assertRaises(checker.Failure) as raised:
            self.check(opener, **kwargs)
        self.assertEqual(raised.exception.exit_code, exit_code, str(raised.exception))
        self.assertIn(message, str(raised.exception))
        return raised.exception

    def test_accepts_the_named_bytes_and_records_what_it_saw(self):
        record = self.check(good())
        self.assertEqual(
            record,
            {
                "schema": "nvkr.dev/hardware-e2e-node-image-check/v1",
                "slot": SLOT,
                "method": "http-get",
                "observedByThisRun": True,
                "url": URL,
                "sha256": DIGEST,
                "bytes": len(IMAGE),
                "rangeProbe": "ignored",
                "rangeSupported": False,
                "acceptRanges": "bytes",
                "qcow2": {"version": 3, "virtualSizeBytes": 12 << 30, "clusterSizeBytes": 65536},
            },
        )

    def test_records_whether_the_server_honours_range_without_requiring_it(self):
        honoured = self.check(Opener(probe(206), body()))
        self.assertEqual((honoured["rangeProbe"], honoured["rangeSupported"]), ("honoured", True))
        ignored = self.check(Opener(probe(200, accept=""), body()))
        self.assertEqual((ignored["rangeProbe"], ignored["rangeSupported"], ignored["acceptRanges"]), ("ignored", False, ""))

    def test_a_server_that_rejects_the_range_probe_is_still_checked_by_the_full_get(self):
        # A server may refuse a Range request and serve the whole object; the full GET decides.
        for code, reason in ((403, "Forbidden"), (405, "Method Not Allowed"), (416, "Range Not Satisfiable"), (500, "Server Error")):
            with self.subTest(code=code):
                opener = Opener(urllib.error.HTTPError(URL, code, reason, {}, None), body())
                record = self.check(opener)
                self.assertEqual((record["rangeProbe"], record["rangeSupported"], record["acceptRanges"]), ("unavailable", False, ""))
                self.assertEqual((record["bytes"], record["sha256"]), (len(IMAGE), DIGEST))
                self.assertEqual([call[0] for call in opener.calls], [URL, URL])

    def test_an_unusable_probe_answer_is_unavailable_and_the_full_get_still_runs(self):
        for answer in (probe(status=500), probe(status=404), urllib.error.URLError("reset"), TimeoutError("timed out")):
            with self.subTest(answer=repr(answer)):
                record = self.check(Opener(answer, body()))
                self.assertEqual(record["rangeProbe"], "unavailable")

    def test_a_rejected_probe_never_lets_a_bad_full_get_pass(self):
        rejected = urllib.error.HTTPError(URL, 416, "Range Not Satisfiable", {}, None)
        self.assertFails(22, Opener(rejected, body(image=qcow2() + b"other " * 64)), "not the")
        self.assertFails(22, Opener(rejected, urllib.error.HTTPError(URL, 403, "Forbidden", {}, None)), "HTTP 403")
        self.assertFails(13, Opener(rejected, body(image=qcow2(crypt=2) + b"x" * 200)), "encrypted", sha256=DIGEST)

    def test_fetches_the_exact_url_anonymously_with_no_other_headers(self):
        opener = good()
        self.check(opener)
        self.assertEqual([call[0] for call in opener.calls], [URL, URL])
        probe_headers, full_headers = opener.calls[0][1], opener.calls[1][1]
        self.assertEqual(probe_headers["Range"], "bytes=0-0")
        self.assertNotIn("Range", full_headers)
        for headers in (probe_headers, full_headers):
            self.assertEqual(headers["Accept-Encoding"], "identity")
            self.assertFalse({"Authorization", "Cookie", "PRIVATE-TOKEN", "JOB-TOKEN"} & set(headers), headers)

    def test_closes_every_response_it_opens(self):
        for answers in ((probe(), body()), (probe(), body(length=1)), (probe(), body(image=IMAGE[:50] + b"x" * 0))):
            opener = Opener(*answers)
            with contextlib.suppress(checker.Failure):
                self.check(opener)
            self.assertTrue(all(answer.closed for answer in answers), answers)

    def test_refuses_a_url_the_lane_does_not_support_before_any_request(self):
        for url in (
            "http://artifacts.example.test/node.qcow2",
            "https://user@artifacts.example.test/node.qcow2",
            "https://artifacts.example.test/node.qcow2?sig=synthetic",
            "https://artifacts.example.test/node.qcow2#part",
            "https://artifacts.example.test/node.raw",
            "https://artifacts.example.test/",
            "https://artifacts.example.test",
            "ftp://artifacts.example.test/node.qcow2",
            "https://artifacts.example.test/a b.qcow2",
            "",
        ):
            with self.subTest(url=url):
                opener = good()
                self.assertFails(13, opener, "plain https URL", url=url)
                self.assertEqual(opener.calls, [])

    def test_a_http_error_is_a_failure_naming_its_status(self):
        for code, reason in ((401, "Unauthorized"), (403, "Forbidden"), (404, "Not Found"), (500, "Server Error")):
            error = urllib.error.HTTPError(URL, code, reason, {}, None)
            with self.subTest(code=code, at="probe and body"):
                self.assertFails(22, Opener(error, error), f"HTTP {code}")
            with self.subTest(code=code, at="body"):
                self.assertFails(22, Opener(probe(), error), f"HTTP {code}")

    def test_a_redirect_is_reported_and_not_followed(self):
        redirect = urllib.error.HTTPError(URL, 302, "Found", {"Location": "https://elsewhere.example.test/x.qcow2"}, None)
        opener = Opener(redirect, redirect)
        self.assertFails(22, opener, "redirect")
        self.assertEqual([call[0] for call in opener.calls], [URL, URL])

    def test_a_transport_failure_is_a_failure(self):
        self.assertFails(22, Opener(urllib.error.URLError("no route"), urllib.error.URLError("no route")), "could not be fetched")
        self.assertFails(22, Opener(probe(), TimeoutError("timed out")), "could not be fetched")
        dropped = Response(200, {"Content-Length": str(len(IMAGE))}, [IMAGE[:100]], fail_after="reset by peer")
        self.assertFails(22, Opener(probe(), dropped), "failed after 100 of")

    def test_a_non_success_answer_that_is_not_an_exception_is_a_failure(self):
        self.assertFails(22, Opener(probe(), Response(500, {"Content-Length": "5"}, [b"12345"])), "HTTP 500")
        self.assertFails(22, Opener(probe(), Response(206, {"Content-Length": "5"}, [b"12345"])), "not 200")

    def test_the_body_must_declare_and_match_its_length(self):
        cases = {
            "no length": (body(headers={"Content-Length": ""}), "Content-Length"),
            "not a number": (body(headers={"Content-Length": "many"}), "Content-Length"),
            "zero": (body(headers={"Content-Length": "0"}), "Content-Length"),
            "short": (body(length=len(IMAGE) + 1), "sent %d of its declared" % len(IMAGE)),
            "long": (body(length=len(IMAGE) - 1), "more than its declared"),
        }
        for name, (response, message) in cases.items():
            with self.subTest(name):
                self.assertFails(22, Opener(probe(), response), message)

    def test_a_body_over_the_size_limit_is_refused_before_it_is_read(self):
        response = body()
        self.assertFails(22, Opener(probe(), response), "byte limit", max_bytes=len(IMAGE) - 1)
        self.assertEqual(len(response.chunks), 2)

    def test_a_content_encoding_means_the_wire_bytes_are_not_the_image(self):
        self.assertFails(22, Opener(probe(), body(headers={"Content-Encoding": "gzip"})), "Content-Encoding gzip")
        self.assertEqual(self.check(Opener(probe(), body(headers={"Content-Encoding": "identity"}))), self.check(good()))

    def test_a_digest_that_differs_names_both(self):
        other = hashlib.sha256(b"another image").hexdigest()
        failure = self.assertFails(22, good(), "not the", sha256=other)
        self.assertIn(DIGEST, str(failure))
        self.assertIn(other, str(failure))

    def test_a_slow_body_is_cut_off(self):
        clock = itertools.count(0, 1000).__next__
        self.assertFails(22, good(), "still streaming", max_seconds=10, clock=clock)

    def test_an_image_the_imager_cannot_use_is_unsupported(self):
        cases = {
            "wrong magic": (qcow2(magic=b"QFI\x00"), "magic"),
            "version 1": (qcow2(version=1), "version 1"),
            "version 4": (qcow2(version=4), "version 4"),
            "backing file": (qcow2(backing=4096), "backing file"),
            "qcow2-layer encryption": (qcow2(crypt=2), "encrypted"),
            "absurd cluster size": (qcow2(bits=3), "cluster size"),
            "no virtual size": (qcow2(size=0), "no virtual size"),
            "version 3 with an external data file": (qcow2(incompatible=1 << 2), "external data file"),
            "version 3 with an external data file and other bits": (qcow2(incompatible=(1 << 2) | 1), "external data file"),
        }
        for name, (header, message) in cases.items():
            image = header + b"x" * 200
            with self.subTest(name):
                opener = Opener(probe(), body(image=image))
                self.assertFails(13, opener, message, sha256=hashlib.sha256(image).hexdigest())

    def test_an_image_too_small_for_a_header_is_unsupported(self):
        image = b"QFI\xfb" + b"\0" * 10
        self.assertFails(13, Opener(probe(), body(image=image, split=4)), "too small",
                         sha256=hashlib.sha256(image).hexdigest())

    def test_a_header_split_across_chunks_is_still_read(self):
        # 80 is past the version 2 header and short of the version 3 one: the check waits for the rest.
        for split in (10, checker.QCOW2_HEADER_BYTES, 80, checker.QCOW2_V3_HEADER_BYTES - 1):
            with self.subTest(split=split):
                record = self.check(Opener(probe(), body(split=split)))
                self.assertEqual(record["qcow2"]["version"], 3)

    def test_a_version_3_header_with_its_feature_fields_is_what_is_read(self):
        # The external-data-file flag sits after the 72-byte base header, so a base-header parse would pass it.
        image = qcow2(incompatible=1 << 2) + b"x" * 200
        self.assertFails(13, Opener(probe(), body(image=image, split=80)), "external data file", sha256=hashlib.sha256(image).hexdigest())

    def test_other_incompatible_feature_bits_do_not_decide_this_check(self):
        image = qcow2(incompatible=1) + b"x" * 200
        record = self.check(Opener(probe(), body(image=image)), sha256=hashlib.sha256(image).hexdigest())
        self.assertEqual(record["qcow2"]["version"], 3)

    def test_a_version_2_image_is_read_from_its_72_byte_header(self):
        image = qcow2(version=2) + b"x" * 200
        self.assertEqual(len(qcow2(version=2)), checker.QCOW2_HEADER_BYTES)
        record = self.check(Opener(probe(), body(image=image)), sha256=hashlib.sha256(image).hexdigest())
        self.assertEqual(record["qcow2"]["version"], 2)

    def test_a_version_3_header_cut_short_is_too_small(self):
        image = qcow2()[:90]
        self.assertFails(13, Opener(probe(), body(image=image, split=90)), "version 3 qcow2 header",
                         sha256=hashlib.sha256(image).hexdigest())

    def test_an_unsupported_image_is_refused_before_the_rest_is_streamed(self):
        response = body(image=qcow2(crypt=2) + b"x" * 200, split=checker.QCOW2_V3_HEADER_BYTES)
        self.assertFails(13, Opener(probe(), response), "encrypted", sha256=DIGEST)
        self.assertEqual(len(response.chunks), 1)

    def test_every_read_is_bounded_by_the_time_left(self):
        class Sock:
            def __init__(self):
                self.timeouts = []

            def settimeout(self, seconds):
                self.timeouts.append(seconds)

        class Raw:
            def __init__(self, sock):
                self._sock = sock

        class Fp:
            def __init__(self, sock):
                self.raw = Raw(sock)

        sock = Sock()
        response = body()
        response.fp = Fp(sock)
        ticks = itertools.count(0, 2).__next__
        self.check(Opener(probe(), response), timeout=60, max_seconds=100, clock=ticks)
        self.assertTrue(sock.timeouts)
        # The inactivity limit while there is time, then what is left of the deadline.
        self.assertEqual(sock.timeouts[0], min(60, 100 - 2))
        self.assertEqual(sock.timeouts, sorted(sock.timeouts, reverse=True))
        self.assertTrue(all(0 < seconds <= 60 for seconds in sock.timeouts))


class Handler(http.server.BaseHTTPRequestHandler):
    """A loopback server that behaves like the object stores this lane has to cope with."""

    requests = []

    def log_message(self, *_):
        pass

    def respond(self, send_body):
        type(self).requests.append((self.command, self.path, self.headers.get("Authorization")))
        if self.path == "/redirect.qcow2":
            self.send_response(302)
            self.send_header("Location", "/image.qcow2")
            self.end_headers()
        elif self.path == "/head-only.qcow2" and self.command == "GET":
            self.send_response(403)  # HEAD answers 200 for the same path, as GitLab artifact downloads do
            self.end_headers()
        elif self.path == "/trickle.qcow2" and self.command == "GET":
            # One byte at a time, each inside any socket timeout, for far longer than the check will wait.
            self.send_response(200)
            self.send_header("Content-Length", str(1 << 20))
            self.end_headers()
            try:
                for _ in range(100):
                    self.wfile.write(b"\0")
                    self.wfile.flush()
                    time.sleep(0.2)
            except OSError:
                pass  # the check gave up and closed the connection, which is the point
        elif self.path in ("/image.qcow2", "/head-only.qcow2"):
            self.send_response(200)
            self.send_header("Content-Length", str(len(IMAGE)))
            self.end_headers()
            if send_body:
                self.wfile.write(IMAGE)
        else:
            self.send_response(404)
            self.end_headers()

    def do_GET(self):  # noqa: N802 (http.server's naming)
        self.respond(True)

    def do_HEAD(self):  # noqa: N802
        self.respond(False)


class RealTransportTest(unittest.TestCase):
    """The same code paths over a real socket. This says nothing about https or about any real server."""

    def setUp(self):
        Handler.requests = []
        try:
            self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        except PermissionError as error:
            self.skipTest(f"cannot listen on loopback here: {error}")
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.stop)
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}"

    def stop(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=10)

    def test_streams_the_body_and_checks_it(self):
        size, header = checker.stream(
            checker.open_url, f"{self.base}/image.qcow2", DIGEST, 5, checker.DEFAULT_MAX_BYTES, 60, time.monotonic
        )
        self.assertEqual((size, header["version"]), (len(IMAGE), 3))
        self.assertEqual([(m, p, a) for m, p, a in Handler.requests], [("GET", "/image.qcow2", None)])

    def test_a_redirect_is_an_error_and_its_target_is_never_requested(self):
        with self.assertRaises(checker.Failure) as raised:
            checker.stream(
                checker.open_url, f"{self.base}/redirect.qcow2", DIGEST, 5, checker.DEFAULT_MAX_BYTES, 60, time.monotonic
            )
        self.assertEqual(raised.exception.exit_code, 22)
        self.assertIn("HTTP 302", str(raised.exception))
        self.assertEqual([path for _, path, _ in Handler.requests], ["/redirect.qcow2"])

    def test_a_url_that_answers_head_but_refuses_get_fails(self):
        url = f"{self.base}/head-only.qcow2"
        head = urllib.request.Request(url, method="HEAD")
        # No proxy: a proxy set in the environment would answer for a loopback address.
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(head, timeout=5) as response:  # noqa: S310 (loopback test server)
            self.assertEqual(response.status, 200)
        with self.assertRaises(checker.Failure) as raised:
            checker.stream(checker.open_url, url, DIGEST, 5, checker.DEFAULT_MAX_BYTES, 60, time.monotonic)
        self.assertIn("HTTP 403", str(raised.exception))

    def test_a_missing_object_fails_at_the_full_get_not_the_probe(self):
        self.assertEqual(checker.probe_range(checker.open_url, f"{self.base}/gone.qcow2", 5), ("unavailable", ""))
        with self.assertRaises(checker.Failure) as raised:
            checker.stream(checker.open_url, f"{self.base}/gone.qcow2", DIGEST, 5, checker.DEFAULT_MAX_BYTES, 60, time.monotonic)
        self.assertIn("HTTP 404", str(raised.exception))

    def test_the_probe_records_that_this_server_ignores_range(self):
        self.assertEqual(checker.probe_range(checker.open_url, f"{self.base}/image.qcow2", 5), ("ignored", ""))

    def test_a_server_that_trickles_bytes_is_cut_off_at_the_deadline_not_the_socket_timeout(self):
        # Each byte arrives inside the 5 second socket timeout, so only a deadline that interrupts the read ends this.
        started = time.monotonic()
        with self.assertRaises(checker.Failure) as raised:
            checker.stream(checker.open_url, f"{self.base}/trickle.qcow2", DIGEST, 5, checker.DEFAULT_MAX_BYTES, 1, time.monotonic)
        self.assertEqual(raised.exception.exit_code, 22)
        self.assertIn("still streaming after 1s", str(raised.exception))
        self.assertLess(time.monotonic() - started, 4)


class WriteNewTest(unittest.TestCase):
    """The pre-check in main is a courtesy; the exclusive create is what refuses a race."""

    def test_refuses_an_existing_file_a_symlink_and_a_missing_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            existing = root / "existing.json"
            existing.write_text("keep\n")
            link = root / "link.json"
            os.symlink(root / "elsewhere", link)
            for path in (existing, link, root / "missing" / "out.json"):
                with self.subTest(path=path.name), self.assertRaises(checker.Failure) as raised:
                    checker.write_new(path, {"a": 1})
                self.assertEqual(raised.exception.exit_code, 47)
            self.assertEqual(existing.read_text(), "keep\n")
            self.assertFalse((root / "elsewhere").exists())


class CommandTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.out = Path(self.directory.name) / "check.json"

    def run_main(self, opener, url=URL, sha256=DIGEST, extra=()):
        stdout, stderr = io.StringIO(), io.StringIO()
        argv = ["--slot", SLOT, "--url", url, "--sha256", sha256, "--out", str(self.out), *extra]
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            code = checker.main(argv, opener=opener)
        return code, stdout.getvalue(), stderr.getvalue()

    def test_writes_one_private_record_and_says_so(self):
        code, stdout, _ = self.run_main(good())
        self.assertEqual(code, 0)
        self.assertIn(f"node image ok: {SLOT}, {len(IMAGE)} bytes", stdout)
        self.assertEqual(stat.S_IMODE(self.out.stat().st_mode), 0o600)
        record = json.loads(self.out.read_text())
        self.assertEqual(
            sorted(record),
            ["acceptRanges", "bytes", "method", "observedByThisRun", "qcow2", "rangeProbe", "rangeSupported", "schema", "sha256", "slot", "url"],
        )

    def test_a_failure_writes_nothing_and_carries_its_code(self):
        cases = (
            (Opener(urllib.error.HTTPError(URL, 403, "Forbidden", {}, None), urllib.error.HTTPError(URL, 403, "Forbidden", {}, None)),
             URL, 22, "NVKR-E-VERIFY-FAILED"),
            (good(), "http://artifacts.example.test/node.qcow2", 13, "NVKR-E-UNSUPPORTED"),
            (Opener(probe(), body(image=qcow2(crypt=2) + b"x" * 200)), URL, 13, "NVKR-E-UNSUPPORTED"),
        )
        for opener, url, exit_code, code in cases:
            with self.subTest(url=url, code=code):
                got, _, stderr = self.run_main(opener, url=url)
                self.assertEqual(got, exit_code)
                self.assertIn(f"[{code}]", stderr)
                self.assertFalse(self.out.exists())

    def test_never_replaces_an_existing_output_and_asks_nothing_first(self):
        self.out.write_text("keep\n")
        opener = good()
        code, _, stderr = self.run_main(opener)
        self.assertEqual((code, self.out.read_text()), (47, "keep\n"))
        self.assertIn("[NVKR-E-IMMUTABLE-VIOLATION]", stderr)
        self.assertEqual(opener.calls, [])

    def test_does_not_write_through_a_symlink(self):
        target = Path(self.directory.name) / "elsewhere"
        os.symlink(target, self.out)
        opener = good()
        self.assertEqual(self.run_main(opener)[0], 47)
        self.assertFalse(target.exists())
        self.assertEqual(opener.calls, [])

    def test_usage_errors_exit_2_and_name_the_usage_code(self):
        cases = (
            ["--slot", "os-image-ppc64le", "--url", URL, "--sha256", DIGEST, "--out", "x"],
            ["--slot", SLOT, "--url", URL, "--sha256", DIGEST.upper(), "--out", "x"],
            ["--slot", SLOT, "--url", URL, "--sha256", DIGEST[:-1], "--out", "x"],
            ["--slot", SLOT, "--url", URL, "--sha256", DIGEST],
            ["--slot", SLOT, "--url", URL, "--sha256", DIGEST, "--out", "x", "--timeout", "0"],
            ["--slot", SLOT, "--url", URL, "--sha256", DIGEST, "--out", "x", "--max-seconds", "-1"],
            ["--slot", SLOT, "--out", "x"],
            ["--slot", SLOT, "--url", URL, "--out", "x"],
            ["--slot", SLOT, "--sha256", DIGEST, "--out", "x"],
            ["--slot", SLOT, "--substitutions", "s.json", "--url", URL, "--out", "x"],
            ["--slot", SLOT, "--substitutions", "s.json", "--sha256", DIGEST, "--out", "x"],
            [],
        )
        for argv in cases:
            with self.subTest(argv=argv):
                stderr = io.StringIO()
                with contextlib.redirect_stderr(stderr), self.assertRaises(SystemExit) as raised:
                    checker.main(argv, opener=good())
                self.assertEqual(raised.exception.code, 2)
                self.assertIn("[NVKR-E-USAGE]", stderr.getvalue())


RUN = "https://github.com/dsx-ai-factory/nvkr/actions/runs/123456789/attempts/1"
MISSING = object()
SOURCE = {"kind": "nvkr-release", "reference": "oci://ghcr.io/example/release@sha256:" + "b" * 64, "version": "v0.24.0-beta.12"}


def readback(**changes):
    value = {"method": "s3-readback", "run": RUN, "bytes": len(IMAGE), "qcow2Version": 3, "virtualSizeBytes": 12 << 30}
    value.update(changes)
    return {key: item for key, item in value.items() if item is not MISSING}


def replacement(verification, slot=SLOT, url=URL, sha256=DIGEST):
    entry = {"slot": slot, "from": "https://nvcr.example.test/old.qcow2", "to": url, "sha256": sha256, "source": SOURCE}
    if verification is not MISSING:
        entry["verification"] = verification
    return entry


def substitutions(*replaced, **top):
    record = {"schema": "nvkr.dev/hardware-e2e-substitutions/v1", "artifactSource": "nvkr-outputs", "replaced": list(replaced)}
    record.update(top)
    return record


class VerificationTest(unittest.TestCase):
    def test_accepts_the_two_methods_and_returns_them_unchanged(self):
        for value in ({"method": "http-get"}, readback(), readback(run="https://github.com/o/r/actions/runs/9"), readback(qcow2Version=2)):
            with self.subTest(value=value):
                self.assertEqual(checker.parse_verification(value), value)

    def test_refuses_what_the_lane_does_not_understand(self):
        cases = {
            "not an object": (None, "must be an object"),
            "a string": ("http-get", "must be an object"),
            "a list": ([{"method": "http-get"}], "must be an object"),
            "no method": ({}, "verification method must be"),
            "an unknown method": ({"method": "head-object"}, "verification method must be"),
            "a method in another case": ({"method": "HTTP-GET"}, "verification method must be"),
            "http-get with a field": ({"method": "http-get", "run": RUN}, "takes no other field"),
            "read-back without run": (readback(run=MISSING), "needs exactly"),
            "read-back without bytes": (readback(bytes=MISSING), "needs exactly"),
            "read-back without version": (readback(qcow2Version=MISSING), "needs exactly"),
            "read-back without virtual size": (readback(virtualSizeBytes=MISSING), "needs exactly"),
            "read-back with a field": (readback(extra=1), "needs exactly"),
            "a run over http": (readback(run=RUN.replace("https", "http")), "GitHub Actions run"),
            "a run on another host": (readback(run=RUN.replace("github.com", "gitlab.example.test")), "GitHub Actions run"),
            "a run that is a repository": (readback(run="https://github.com/dsx-ai-factory/nvkr"), "GitHub Actions run"),
            "run zero": (readback(run="https://github.com/o/r/actions/runs/0"), "GitHub Actions run"),
            "attempt zero": (readback(run="https://github.com/o/r/actions/runs/5/attempts/0"), "GitHub Actions run"),
            "a run with a query": (readback(run=RUN + "?x=1"), "GitHub Actions run"),
            "a run that is a number": (readback(run=123), "GitHub Actions run"),
            "a run with text before it": (readback(run="see " + RUN), "GitHub Actions run"),
            "zero bytes": (readback(bytes=0), "positive integers"),
            "negative bytes": (readback(bytes=-1), "positive integers"),
            "true bytes": (readback(bytes=True), "positive integers"),
            "fractional bytes": (readback(bytes=1.5), "positive integers"),
            "text bytes": (readback(bytes="1"), "positive integers"),
            "zero virtual size": (readback(virtualSizeBytes=0), "positive integers"),
            "true virtual size": (readback(virtualSizeBytes=True), "positive integers"),
            "version one": (readback(qcow2Version=1), "qcow2Version"),
            "version four": (readback(qcow2Version=4), "qcow2Version"),
            "true version": (readback(qcow2Version=True), "qcow2Version"),
            "text version": (readback(qcow2Version="3"), "qcow2Version"),
            "float version": (readback(qcow2Version=3.0), "qcow2Version"),
        }
        for name, (value, message) in cases.items():
            with self.subTest(name), self.assertRaises(checker.Failure) as raised:
                checker.parse_verification(value)
            self.assertEqual((raised.exception.code, raised.exception.exit_code), ("NVKR-E-UNSUPPORTED", 13), name)
            self.assertIn(message, str(raised.exception), name)


class RecordedTest(unittest.TestCase):
    def test_describes_what_the_publication_reported_and_says_this_run_did_not_see_it(self):
        self.assertEqual(
            checker.recorded(SLOT, URL, DIGEST, readback()),
            {
                "schema": "nvkr.dev/hardware-e2e-node-image-check/v1",
                "slot": SLOT,
                "method": "s3-readback",
                "observedByThisRun": False,
                "run": RUN,
                "url": URL,
                "sha256": DIGEST,
                "bytes": len(IMAGE),
                "qcow2": {"version": 3, "virtualSizeBytes": 12 << 30},
            },
        )

    def test_refuses_a_location_the_lane_does_not_support(self):
        for url in ("http://artifacts.example.test/a.qcow2", "https://artifacts.example.test/a.raw", "https://user@artifacts.example.test/a.qcow2",
                    "https://artifacts.example.test/a.qcow2?token=1", "oci://artifacts.example.test/a@sha256:" + "a" * 64):
            with self.subTest(url=url), self.assertRaises(checker.Failure) as raised:
                checker.recorded(SLOT, url, DIGEST, readback())
            self.assertEqual(raised.exception.exit_code, 13)


class NamedInTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name) / "substitutions.json"

    def write(self, record):
        self.path.write_text(record if isinstance(record, str) else json.dumps(record))

    def test_returns_what_the_record_names_for_the_slot_asked(self):
        arm = "https://artifacts.example.test/arm.qcow2"
        self.write(substitutions(replacement({"method": "http-get"}, slot="os-image-arm64", url=arm, sha256="c" * 64),
                                 replacement(readback())))
        self.assertEqual(checker.named_in(self.path, SLOT), (URL, DIGEST, readback()))
        self.assertEqual(checker.named_in(self.path, "os-image-arm64"), (arm, "c" * 64, {"method": "http-get"}))

    def test_refuses_a_record_it_cannot_rely_on(self):
        cases = {
            "not JSON": ("{", "not readable JSON"),
            "a list": ("[]", "is not a nvkr.dev/hardware-e2e-substitutions/v1 record"),
            "another schema": (substitutions(replacement(readback()), schema="nvkr.dev/other/v1"), "is not a nvkr.dev/hardware-e2e-substitutions/v1 record"),
            "a build from the subject": (substitutions(replacement(readback()), artifactSource="build-subject"), "only a run of NVKR outputs"),
            "no replacements field": ({"schema": "nvkr.dev/hardware-e2e-substitutions/v1", "artifactSource": "nvkr-outputs"}, "names 0 replacements"),
            "replacements that are not a list": (substitutions(replacement(readback()), replaced={"slot": SLOT}), "names 0 replacements"),
            "no replacement for the slot": (substitutions(replacement(readback(), slot="os-image-arm64")), "names 0 replacements"),
            "a replacement that is text": (substitutions("junk", replacement(readback())), "not an object"),
            "a replacement that is null": (substitutions(None, replacement(readback())), "not an object"),
            "a replacement that is a list": (substitutions([replacement(readback())], replacement(readback())), "not an object"),
            "two replacements for the slot": (substitutions(replacement(readback()), replacement(readback())), "names 2 replacements"),
            "no URL": (substitutions({"slot": SLOT, "sha256": DIGEST, "verification": readback()}), "names no URL"),
            "a URL that is not text": (substitutions(replacement(readback(), url=5)), "names no URL"),
            "no digest": (substitutions({"slot": SLOT, "to": URL, "verification": readback()}), "names no URL"),
            "an upper-case digest": (substitutions(replacement(readback(), sha256=DIGEST.upper())), "names no URL"),
            "no verification": (substitutions(replacement(MISSING)), "must be an object"),
            "a verification it does not know": (substitutions(replacement({"method": "head-object"})), "verification method must be"),
        }
        for name, (record, message) in cases.items():
            self.write(record)
            with self.subTest(name), self.assertRaises(checker.Failure) as raised:
                checker.named_in(self.path, SLOT)
            self.assertEqual((raised.exception.code, raised.exception.exit_code), ("NVKR-E-UNSUPPORTED", 13), name)
            self.assertIn(message, str(raised.exception), name)

    def test_refuses_a_record_that_is_not_there(self):
        with self.assertRaises(checker.Failure) as raised:
            checker.named_in(Path(self.directory.name) / "absent.json", SLOT)
        self.assertEqual(raised.exception.exit_code, 13)
        self.assertIn("not readable JSON", str(raised.exception))


class SubstitutionsCommandTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.out = Path(self.directory.name) / "check.json"
        self.path = Path(self.directory.name) / "substitutions.json"

    def run_main(self, opener, record, slot=SLOT):
        if record is not None:
            self.path.write_text(json.dumps(record))
        stdout, stderr = io.StringIO(), io.StringIO()
        argv = ["--slot", slot, "--substitutions", str(self.path), "--out", str(self.out)]
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            code = checker.main(argv, opener=opener)
        return code, stdout.getvalue(), stderr.getvalue()

    def test_a_read_back_image_is_recorded_and_never_fetched(self):
        opener = Opener()
        code, stdout, _ = self.run_main(opener, substitutions(replacement(readback())))
        self.assertEqual(code, 0)
        self.assertEqual(opener.calls, [])
        self.assertIn(f"node image recorded, not fetched: {SLOT}, {len(IMAGE)} bytes", stdout)
        self.assertIn(f"read back by {RUN}", stdout)
        self.assertEqual(stat.S_IMODE(self.out.stat().st_mode), 0o600)
        self.assertEqual(json.loads(self.out.read_text()), checker.recorded(SLOT, URL, DIGEST, readback()))

    def test_an_http_get_image_is_fetched_from_the_url_the_record_names(self):
        opener = good()
        code, stdout, _ = self.run_main(opener, substitutions(replacement({"method": "http-get"})))
        self.assertEqual(code, 0)
        self.assertEqual([call[0] for call in opener.calls], [URL, URL])
        self.assertIn(f"node image ok: {SLOT}", stdout)
        record = json.loads(self.out.read_text())
        self.assertEqual((record["method"], record["observedByThisRun"], record["url"], record["sha256"]), ("http-get", True, URL, DIGEST))

    def test_each_slot_follows_its_own_method(self):
        arm = "os-image-arm64"
        record = substitutions(replacement({"method": "http-get"}), replacement(readback(), slot=arm, sha256="c" * 64))
        opener = Opener()
        code, _, _ = self.run_main(opener, record, slot=arm)
        self.assertEqual((code, opener.calls), (0, []))
        written = json.loads(self.out.read_text())
        self.assertEqual((written["slot"], written["method"], written["sha256"]), (arm, "s3-readback", "c" * 64))

    def test_an_http_get_that_serves_other_bytes_fails_and_writes_nothing(self):
        other = qcow2() + b"other payload " * 64
        opener = Opener(probe(), body(image=other))
        code, _, stderr = self.run_main(opener, substitutions(replacement({"method": "http-get"})))
        self.assertEqual(code, 22)
        self.assertIn("[NVKR-E-VERIFY-FAILED]", stderr)
        self.assertFalse(self.out.exists())

    def test_a_record_it_cannot_rely_on_fails_before_any_request_and_writes_nothing(self):
        cases = (
            substitutions(replacement(MISSING)),
            substitutions(replacement({"method": "head-object"})),
            substitutions(replacement(readback(bytes=0))),
            substitutions(replacement(readback()), artifactSource="build-subject"),
            substitutions(replacement(readback(), slot="os-image-arm64")),
            substitutions(replacement(readback(), url="http://artifacts.example.test/a.qcow2")),
        )
        for record in cases:
            with self.subTest(record=record):
                opener = Opener()
                code, _, stderr = self.run_main(opener, record)
                self.assertEqual(code, 13)
                self.assertIn("[NVKR-E-UNSUPPORTED]", stderr)
                self.assertEqual(opener.calls, [])
                self.assertFalse(self.out.exists())

    def test_never_replaces_an_existing_output_and_reads_nothing_first(self):
        self.out.write_text("keep\n")
        opener = Opener()
        code, _, stderr = self.run_main(opener, None)  # the substitution record does not exist
        self.assertEqual((code, self.out.read_text(), opener.calls), (47, "keep\n", []))
        self.assertIn("[NVKR-E-IMMUTABLE-VIOLATION]", stderr)


if __name__ == "__main__":
    unittest.main()
