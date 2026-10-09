# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Site adapter contracts, not tenant/workload acceptance."""

import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch


SPEC = importlib.util.spec_from_file_location("site", Path(__file__).with_name("nke-local-site.py"))
SITE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SITE)


class SiteTest(unittest.TestCase):
    def test_site_consume_emits_no_pass_before_failed_registry_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            cli = work / "cli/nkectl"
            cli.parent.mkdir()
            cli.write_text("fixture")
            cli.chmod(0o700)
            digest = "sha256:" + "a" * 64
            outputs = {key: (work / key, {"digest": digest}) for key in
                       [*SITE.CHART_OUTPUTS, *SITE.IMAGE_KEYS, "nkectl"]}
            def inspect(_argv, label, *_args):
                if label == "site node address":
                    return json.dumps([{"NetworkSettings": {"Networks": {"kind": {"IPAddress": "172.18.0.2"}}}}])
                return "" if label == "fresh private registry identity" else json.dumps({"digest": digest})
            with patch.object(SITE.MGMT, "owned_nodes", return_value=["owned"]), \
                 patch.object(SITE.MGMT, "inspect", side_effect=inspect), patch.object(SITE.MGMT, "execute"), \
                 patch.object(SITE.CHART, "prepare", return_value=work / "deploy"), \
                 patch.object(SITE, "prepare_runtime_values") as prepare, \
                 patch.object(SITE, "wait_registry"), patch.object(SITE, "bootstrap_ca"), \
                 patch.object(SITE, "kind_nktp_values", return_value="enabled: false"), \
                 patch.object(SITE, "deployed_images", return_value={}), patch.object(SITE.VM, "digest", return_value=digest), \
                 patch.object(SITE, "registry_cleanup", side_effect=SITE.VM.SmokeError("registry cleanup failed")) as cleanup, \
                 patch("sys.stdout", new_callable=io.StringIO) as output:
                with self.assertRaisesRegex(SITE.VM.SmokeError, "registry cleanup failed"):
                    SITE.consume(Mock(nkdctl="/unused", nke_checkout="/unused", nico_org="fixture"), outputs,
                                 "fixture", "http://172.18.0.1:18089", "172.18.0.1", "id", "owned", work, work,
                                 {"PATH": "/usr/bin"})
            cleanup.assert_called_once()
            prepare.assert_called_once()
            self.assertNotIn("PASS:", output.getvalue())

    def test_site_success_is_emitted_only_after_management_returns_with_cleanup(self):
        arguments = ["site"]
        for name in ("nke-checkout", "nkdctl", "tools-run", "site-run", "components-run", "profiles-run",
                     "provider-run", "nico-endpoint", "nico-token-file", "out"):
            arguments.extend(["--" + name, "/unused"])
        for failure in (False, True):
            with self.subTest(failure=failure), patch.object(SITE.sys, "argv", arguments), \
                 patch.object(SITE.signal, "signal"), patch.object(SITE, "admit", return_value=({}, "token", "url", "gateway", "id")), \
                 patch("sys.stdout", new_callable=io.StringIO) as output, patch("sys.stderr", new_callable=io.StringIO), \
                 patch.object(SITE.MGMT, "probe", side_effect=SITE.VM.SmokeError("cleanup failed") if failure else None):
                self.assertEqual(SITE.main(), 17 if failure else 0)
                self.assertEqual("PASS:" in output.getvalue(), not failure)

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
                with patch.object(SITE.MGMT, "capture", capture), patch.object(SITE, "deployed_runtime_images", return_value={}):
                    if case == "exact":
                        observed = SITE.deployed_images(Path("tools"), Path("private"), {"SITE_KUBECONFIG": "private/kube"}, refs)
                        self.assertEqual({key: row["imageID"] for key, row in observed.items()}, refs)
                    else:
                        with self.assertRaises(SITE.VM.SmokeError):
                            SITE.deployed_images(Path("tools"), Path("private"), {"SITE_KUBECONFIG": "private/kube"}, refs)

    def test_dependency_values_bind_native_images_without_overwriting_chart_keys(self):
        outputs = {key: (Path(key), {"digest": "sha256:" + "%064x" % (index + 1)})
                   for index, key in enumerate([*SITE.CHART_OUTPUTS, *SITE.IMAGE_KEYS])}
        original = outputs["postgresql"]
        values = SITE.runtime_values("172.18.0.1:15501", outputs)
        self.assertEqual(outputs["postgresql"], original)
        self.assertEqual(values["postgresql"]["image"]["digest"], outputs["runtime-postgresql"][1]["digest"])
        self.assertNotEqual(values["postgresql"]["image"]["digest"], original[1]["digest"])
        spire = values["spire"]
        images = {"spire-server": spire["spire-server"]["image"],
                  "spire-controller-manager": spire["spire-server"]["controllerManager"]["image"],
                  "spire-tpm-server": spire["spire-server"]["nodeAttestor"]["tpmDirect"]["image"],
                  "spire-bash": spire["spire-server"]["tests"]["bash"]["image"],
                  "spire-agent": spire["spire-agent"]["image"],
                  "spiffe-csi": spire["spiffe-csi-driver"]["image"],
                  "csi-node-driver-registrar": spire["spiffe-csi-driver"]["nodeDriverRegistrar"]["image"],
                  "spire-oidc": spire["spiffe-oidc-discovery-provider"]["image"]}
        for name, image in images.items():
            with self.subTest(name=name):
                self.assertEqual(image, {"registry": "172.18.0.1:15501", "repository": "nkd-local/" + name,
                                         "tag": outputs["runtime-" + name][1]["digest"]})
        self.assertEqual(spire["spiffe-oidc-discovery-provider"]["tests"]["bash"]["image"], images["spire-bash"])
        self.assertEqual(spire["spire-agent"]["socketAlternate"]["image"], images["spire-bash"])
        self.assertEqual(spire["spire-agent"]["fsGroupFix"]["image"], images["spire-bash"])
        for item, name in (("server", "temporal-server"), ("admintools", "temporal-admin-tools"), ("web", "temporal-ui")):
            with self.subTest(name=name):
                self.assertEqual(values["temporal"][item]["image"], {
                    "repository": "172.18.0.1:15501/nkd-local/" + name,
                    "tag": SITE.TEMPORAL_TAGS[name] + "@" + outputs["runtime-" + name][1]["digest"]})

    def test_copy_images_transports_every_named_manifest_and_rejects_changed_descriptor(self):
        for case in ("exact", "changed descriptor"):
            with self.subTest(case=case):
                digest = "sha256:" + "a" * 64
                outputs = {key: (Path("run/packages") / key, {"digest": digest}) for key in SITE.IMAGE_KEYS}
                with patch.object(SITE.MGMT, "execute") as execute, \
                     patch.object(SITE.MGMT, "inspect", return_value=json.dumps({"digest": digest if case == "exact" else "foreign"})):
                    if case == "exact":
                        refs = SITE.copy_images(Path("tools"), Path("private"), {}, "172.18.0.1:15501", "owned", outputs)
                        self.assertEqual(set(refs), set(SITE.IMAGE_KEYS))
                        self.assertEqual(execute.call_count, len(SITE.IMAGE_KEYS))
                        for key, call in zip(SITE.IMAGE_KEYS, execute.call_args_list):
                            self.assertEqual(call.args[0], ["tools/oras", "cp", "--concurrency", "1", "--from-oci-layout",
                                "run/packages/" + key + "@" + digest,
                                "172.18.0.1:15501/nkd-local/" + key.removeprefix("runtime-") + ":owned", "--to-plain-http"])
                            self.assertTrue(refs[key].endswith("@" + digest))
                    else:
                        with self.assertRaisesRegex(SITE.VM.SmokeError, "registry changed observed manifest"):
                            SITE.copy_images(Path("tools"), Path("private"), {}, "172.18.0.1:15501", "owned", outputs)
                        self.assertEqual(execute.call_count, 1)

    def test_registry_capacity_deduplicates_and_rejects_unsafe_or_excess_bytes(self):
        for case in ("exact", "oversized blob", "combined overflow", "symlink", "conflicting size"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                outputs = {}
                for index, key in enumerate(SITE.IMAGE_KEYS):
                    layout = root / key
                    blobs = layout / "blobs/sha256"
                    blobs.mkdir(parents=True)
                    (blobs / "shared").write_bytes(b"shared")
                    (blobs / str(index)).write_bytes(b"unique")
                    outputs[key] = (layout, {})
                blob = outputs["cwe"][0] / "blobs/sha256/shared"
                if case == "oversized blob":
                    with blob.open("wb") as held:
                        held.truncate(SITE.REGISTRY_BYTES + 1)
                elif case == "combined overflow":
                    for key in ("cwe", "profiles"):
                        with (outputs[key][0] / "blobs/sha256/large").open("wb") as held:
                            held.truncate(SITE.REGISTRY_BYTES // 2)
                elif case == "symlink":
                    (blob.parent / "link").symlink_to(blob)
                elif case == "conflicting size":
                    blob.write_bytes(b"foreign-size")
                if case == "exact":
                    self.assertEqual(SITE.registry_capacity(outputs), 64 * 1024**2 + 6 * (len(SITE.IMAGE_KEYS) + 2))
                else:
                    with self.assertRaises(SITE.VM.SmokeError):
                        SITE.registry_capacity(outputs)

    def test_prepared_values_append_before_requires_and_revalidate_every_field(self):
        for case in ("exact", "duplicate header", "foreign semantics", "already exists"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                deploy = root / "deploy"
                item = deploy / "spire"
                item.mkdir(parents=True)
                release = item / "release.yaml"
                original = "chart:\n  name: spire\nvalues:\n  - values.yaml\n  - overlay.yaml\n# preserved comment\nrequires:\n  - gate\n"
                release.write_text(original + ("values:\n  - foreign\n" if case == "duplicate header" else ""))
                if case == "already exists":
                    (item / "nkd-image-inputs.json").write_text("foreign")
                before = ["single", "spire", "0.30.1", "", "charts/spire.tgz", "spire", "spire-server", "values.yaml,overlay.yaml", "gate"]
                after = before[:]
                after[7] += ",nkd-image-inputs.json"
                if case == "foreign semantics":
                    after[6] = "foreign"
                with patch.object(SITE, "runtime_values", return_value={"spire": {"native": "sha256:fixture"}}), \
                     patch.object(SITE.CHART, "release_fields", side_effect=[before, after]) as reader:
                    if case == "exact":
                        SITE.prepare_runtime_values(root / "checkout", deploy, "host", {}, Mock())
                        self.assertEqual(release.read_text(), original.replace("requires:\n", "  - nkd-image-inputs.json\nrequires:\n"))
                        self.assertEqual(json.loads((item / "nkd-image-inputs.json").read_text()), {"native": "sha256:fixture"})
                        self.assertEqual(reader.call_count, 2)
                    elif case == "already exists":
                        with self.assertRaises(FileExistsError):
                            SITE.prepare_runtime_values(root / "checkout", deploy, "host", {}, Mock())
                        self.assertEqual((item / "nkd-image-inputs.json").read_text(), "foreign")
                        self.assertEqual(release.read_text(), original)
                    else:
                        with self.assertRaises(SITE.VM.SmokeError):
                            SITE.prepare_runtime_values(root / "checkout", deploy, "host", {}, Mock())

    def test_dependency_runtime_requires_exact_native_ids_and_ready_or_completed_states(self):
        for case in ("exact", "foreign spec", "foreign imageID", "not ready", "init failed", "job failed",
                     "missing image", "missing status", "duplicate status", "foreign repository replica",
                     "missing temporal role", "missing bash role", "unrelated same container name",
                     "expired schema pods", "foreign schema init"):
            with self.subTest(case=case):
                refs = {"runtime-" + name: "172.18.0.1:15501/nkd-local/" + name + "@sha256:" + "a" * 64
                        for name in SITE.RUNTIME_IMAGES}
                pods = []
                for name in SITE.RUNTIME_IMAGES:
                    key = "runtime-" + name
                    image = refs[key]
                    if name in SITE.TEMPORAL_TAGS:
                        repository, digest = image.split("@")
                        image = repository + ":" + SITE.TEMPORAL_TAGS[name] + "@" + digest
                    namespace, instance, application, roles = SITE.RUNTIME_ROLES[name]
                    if name == "temporal-admin-tools" and case != "expired schema pods":
                        roles = (*roles, *SITE.SCHEMA_ROLES)
                    for field, container, component in roles:
                        if (case == "missing temporal role" and container == "temporal-frontend") or (
                                case == "missing bash role" and container == "fsgroupfix"):
                            continue
                        init = field == "initContainers"
                        job = component == "database"
                        status_field = "initContainerStatuses" if init else "containerStatuses"
                        state = {"name": container, "imageID": refs[key], "ready": not init and not job,
                                 "state": {"terminated": {"exitCode": 0}} if init or job else {"running": {}}}
                        pod = {"metadata": {"uid": name + container, "namespace": namespace, "labels": {
                            "app.kubernetes.io/instance": instance, "app.kubernetes.io/name": application,
                            "app.kubernetes.io/component": component}}, "spec": {field: [{"name": container, "image": image}]},
                            "status": {status_field: [state], "phase": "Succeeded" if job else "Running"}}
                        if name == "postgresql":
                            if case == "foreign spec":
                                pod["spec"][field][0]["image"] = image.replace("a" * 64, "b" * 64)
                            elif case == "foreign imageID":
                                state["imageID"] = "foreign"
                            elif case == "not ready":
                                state["ready"] = False
                            elif case == "missing image":
                                continue
                            elif case == "missing status":
                                pod["status"][status_field] = []
                            elif case == "duplicate status":
                                pod["status"][status_field] *= 2
                            elif case in ("foreign repository replica", "unrelated same container name"):
                                replica = json.loads(json.dumps(pod))
                                replica["metadata"]["uid"] = "foreign-replica"
                                replica["spec"][field][0]["image"] = "foreign.example/postgresql:substitute"
                                if case == "unrelated same container name":
                                    replica["metadata"]["namespace"] = "unrelated"
                                pods.append(replica)
                        elif (init and name == "spire-tpm-server" and case == "init failed") or (
                                job and container == "done" and case == "job failed"):
                            state["state"]["terminated"]["exitCode"] = 1
                        elif case == "foreign schema init" and container == "setup-default-store":
                            pod["spec"][field][0]["image"] = "foreign.example/schema:substitute"
                        pods.append(pod)
                with patch.object(SITE.MGMT, "capture", return_value=json.dumps({"items": pods})):
                    if case in ("exact", "unrelated same container name", "expired schema pods"):
                        result = SITE.deployed_runtime_images(Path("tools"), Path("private"), {"SITE_KUBECONFIG": "private/kube"}, refs)
                        self.assertEqual(set(result), set(refs))
                        self.assertEqual({row["container"] for row in result["runtime-spire-bash"]["containers"]},
                                         {"ensure-alternate-names", "fsgroupfix"})
                        self.assertTrue(all(row["status"] == "observed" for row in result.values()))
                    else:
                        with self.assertRaises(SITE.VM.SmokeError):
                            SITE.deployed_runtime_images(Path("tools"), Path("private"), {"SITE_KUBECONFIG": "private/kube"}, refs)

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
