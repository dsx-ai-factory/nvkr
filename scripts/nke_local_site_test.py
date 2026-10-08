# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Site adapter contracts, not tenant/workload acceptance."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch


SPEC = importlib.util.spec_from_file_location("site", Path(__file__).with_name("nke-local-site.py"))
SITE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SITE)


class SiteTest(unittest.TestCase):
    def test_bootstrap_ca_requires_ca_signing_usage_and_strict_validation(self):
        with patch.object(SITE.MGMT, "execute") as execute:
            SITE.bootstrap_ca(Path("private"), {"HOME": "private"})
            argv = execute.call_args_list[0].args[0]
            self.assertIn("basicConstraints=critical,CA:TRUE", argv)
            self.assertIn("keyUsage=critical,keyCertSign,cRLSign", argv)
            self.assertEqual(execute.call_args_list[1].args[0],
                             ["openssl", "verify", "-x509_strict", "-CAfile", "private/ca.crt", "private/ca.crt"])

    def test_deployed_images_bind_complete_ready_exact_subjects(self):
        for case in ("exact", "foreign digest", "not ready", "init failed", "duplicate pod", "missing container"):
            with self.subTest(case=case):
                refs = {key: "172.18.0.1:15501/" + key + "@sha256:" + "a" * 64 for key in ("cwe", "profiles", "provider")}
                def capture(_argv, phase, *_args):
                    key = phase.removeprefix("deployed-")
                    container = {"cwe": "nke-controller", "profiles": "materialize-profiles", "provider": "manager"}[key]
                    state = {"name": container, "ready": case != "not ready", "imageID": refs[key],
                             "state": {"terminated": {"exitCode": 1 if case == "init failed" else 0}}}
                    if case == "foreign digest":
                        state["imageID"] = "foreign"
                    if case == "missing container":
                        state["name"] = "foreign"
                    field = "initContainerStatuses" if key == "profiles" else "containerStatuses"
                    pods = [{"metadata": {"uid": key + "-uid"}, "status": {field: [state]}}]
                    if case == "duplicate pod":
                        pods *= 2
                    return json.dumps({"items": pods})
                with patch.object(SITE.MGMT, "capture", capture):
                    if case == "exact":
                        observed = SITE.deployed_images(Path("tools"), Path("private"), {"SITE_KUBECONFIG": "private/kube"}, refs)
                        self.assertEqual({key: row["imageID"] for key, row in observed.items()}, refs)
                    else:
                        with self.assertRaises(SITE.VM.SmokeError):
                            SITE.deployed_images(Path("tools"), Path("private"), {"SITE_KUBECONFIG": "private/kube"}, refs)

    def test_missing_openssl_fails_before_site_creation(self):
        with patch.object(SITE.MGMT, "admit"), patch.object(SITE.shutil, "which", return_value=None), \
             patch.object(SITE.MGMT, "probe") as probe:
            with self.assertRaisesRegex(SITE.VM.SmokeError, "OpenSSL"):
                SITE.admit(Mock())
            probe.assert_not_called()

    def test_missing_shell_or_api_client_fails_before_site_creation(self):
        for missing in ("bash", "curl"):
            with self.subTest(missing=missing), patch.object(SITE.MGMT, "admit"), \
                 patch.object(SITE.shutil, "which", side_effect=lambda name: None if name == missing else name), \
                 patch.object(SITE.MGMT, "probe") as probe:
                with self.assertRaisesRegex(SITE.VM.SmokeError, missing):
                    SITE.admit(Mock())
                probe.assert_not_called()

    def test_registry_diagnostics_preserve_unjoined_process(self):
        with patch.object(SITE.time, "monotonic", side_effect=[0, 31]), \
             patch.object(SITE.MGMT, "execute", side_effect=SITE.MGMT.UnjoinedProcess("not joined")):
            with self.assertRaises(SITE.MGMT.UnjoinedProcess):
                SITE.wait_registry("private", "172.18.0.1:15501", Path("private"), {})

    def test_layout_rejects_symlink_root_ancestor_and_member(self):
        for substitution in ("packages", "producer", "member"):
            with self.subTest(substitution=substitution), tempfile.TemporaryDirectory() as directory:
                base = Path(directory).resolve()
                root, external = base / "run", base / "external"
                root.mkdir()
                external.mkdir()
                observed = {"digest": "sha256:" + "a" * 64}
                layout = root / "packages/cwe"
                if substitution == "packages":
                    (external / "cwe").mkdir()
                    (root / "packages").symlink_to(external, target_is_directory=True)
                elif substitution == "producer":
                    layout.parent.mkdir()
                    layout.symlink_to(external, target_is_directory=True)
                else:
                    layout.mkdir(parents=True)
                    (external / "foreign").write_text("foreign")
                    (layout / "blob").symlink_to(external / "foreign")
                (layout / "index.json").write_text(json.dumps({"manifests": [{"digest": observed["digest"]}]}))
                with patch.object(SITE.VM, "complete_output", return_value=observed):
                    with self.assertRaisesRegex(SITE.VM.SmokeError, "regular"):
                        SITE.selection(root, "cwe", "oci")

    def test_kind_telemetry_uses_source_owned_fixture_and_no_output_mutation(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            output = work / "site-install"
            output.mkdir()
            values = 'metrics-server:\n  enabled: true\n  args: ["--kubelet-insecure-tls"]\n'
            with patch.object(SITE.MGMT, "capture", return_value=values) as capture:
                self.assertEqual(SITE.kind_nktp_values(Path("installer"), work, {"OUT_DIR": str(output)}), values)
                self.assertFalse((output / "nktp.yaml").exists())
                self.assertEqual(capture.call_args.args[0][-1], SITE.CHART.INSTALLER_COMMIT + ":nktp/hack/e2e-values.yaml")
                capture.return_value = "foreign fixture"
                with self.assertRaises(SITE.VM.SmokeError):
                    SITE.kind_nktp_values(Path("installer"), work, {"OUT_DIR": str(output)})

    def test_registry_startup_is_bounded_and_retains_failure_diagnostics(self):
        for case in ("ready", "exited", "budget expired"):
            with self.subTest(case=case):
                response = Mock(status=200)
                response.read.return_value = b"{}"
                context = Mock()
                context.__enter__ = Mock(return_value=response)
                context.__exit__ = Mock(return_value=False)
                opener = Mock()
                opener.open.return_value = context
                clock = [0, 0] if case == "ready" else [0, 0, 31] if case == "exited" else [0, 31]
                with patch.object(SITE.urllib.request, "build_opener", return_value=opener), \
                     patch.object(SITE.time, "monotonic", side_effect=clock), \
                     patch.object(SITE.MGMT, "inspect", return_value=json.dumps([{"State": {"Running": case != "exited"}}])), \
                     patch.object(SITE.MGMT, "execute") as execute:
                    if case == "ready":
                        SITE.wait_registry("private", "172.18.0.1:15501", Path("private"), {})
                        opener.open.assert_called_once_with("http://172.18.0.1:15501/v2/", timeout=2)
                        execute.assert_not_called()
                    else:
                        with self.assertRaisesRegex(SITE.VM.SmokeError, "startup budget"):
                            SITE.wait_registry("private", "172.18.0.1:15501", Path("private"), {})
                        self.assertEqual(execute.call_args.args[0], ["docker", "logs", "--tail", "100", "private"])

    def test_endpoint_admission(self):
        for value in ("http://172.18.0.1:18089", "http://127.0.0.1:18089", "http://0.0.0.0:18089",
                      "https://172.18.0.1:18089", "http://172.18.0.1:18080", "http://user:password@172.18.0.1:18089",
                      "http://172.18.0.1:18089/private", "http://example.com:18089", "http://8.8.8.8:18089"):
            with self.subTest(value=value):
                if value == "http://172.18.0.1:18089":
                    self.assertEqual(SITE.endpoint(value), value)
                else:
                    with self.assertRaises(SITE.VM.SmokeError):
                        SITE.endpoint(value)

    def test_layout_must_match_completed_run(self):
        for mismatch in (False, True):
            with self.subTest(mismatch=mismatch), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                observed = {"digest": "sha256:" + "a" * 64}
                layout = root / "packages/cwe"
                layout.mkdir(parents=True)
                (layout / "index.json").write_text(json.dumps({"manifests": [{"digest": "foreign" if mismatch else observed["digest"]}]}))
                with patch.object(SITE.VM, "complete_output", return_value=observed):
                    if mismatch:
                        with self.assertRaises(SITE.VM.SmokeError):
                            SITE.selection(root, "cwe", "oci")
                    else:
                        self.assertEqual(SITE.selection(root, "cwe", "oci"), (layout, observed))

    def test_registry_cleanup_rejects_foreign_resource_or_disk_mount(self):
        for case in ("exact", "foreign owner", "different image", "different name", "persistent volume"):
            with self.subTest(case=case):
                row = {"Id": "owned-id", "Name": "/private-registry", "Image": "accepted-image",
                       "Config": {"Labels": {"dev.nkd.demo.owner": "private-owner"}}, "Mounts": [{"Type": "tmpfs"}]}
                if case == "foreign owner":
                    row["Config"]["Labels"]["dev.nkd.demo.owner"] = "foreign"
                elif case == "different image":
                    row["Image"] = "foreign"
                elif case == "different name":
                    row["Name"] = "/foreign"
                elif case == "persistent volume":
                    row["Mounts"] = [{"Type": "volume"}]
                with patch.object(SITE.MGMT, "inspect", side_effect=["owned-id", json.dumps([row]), ""]), \
                     patch.object(SITE.MGMT, "execute") as execute:
                    if case == "exact":
                        SITE.registry_cleanup("private-registry", "private-owner", "accepted-image", Path("private"), {})
                        self.assertEqual(execute.call_args.args[0], ["docker", "rm", "--force", "owned-id"])
                    else:
                        with self.assertRaises(SITE.VM.SmokeError):
                            SITE.registry_cleanup("private-registry", "private-owner", "accepted-image", Path("private"), {})
                        execute.assert_not_called()


if __name__ == "__main__":
    unittest.main()
