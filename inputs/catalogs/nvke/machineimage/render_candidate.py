# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Offline catalog experiment; descriptions are not authenticated receipts."""

import argparse
import base64
import hashlib
import json
from pathlib import Path
import re
import string
import subprocess
import sys
from urllib.parse import urlsplit

import yaml
from jsonschema import Draft4Validator, FormatChecker


HERE = Path(__file__).resolve().parent
CRD_PATH = "management-engine/config/crd/bases/internal.nvke.nvidia.com_machineimages.yaml"
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
NAME = re.compile(r"[a-z0-9](?:[-a-z0-9]*[a-z0-9])?\Z")
LIMIT = 4 * 1024 * 1024


class CandidateError(ValueError):
    """A fixed diagnostic that contains no caller-provided values."""


def require(condition, message):
    if not condition:
        raise CandidateError(message)


def read(path):
    with Path(path).open("rb") as stream:
        raw = stream.read(LIMIT + 1)
    require(len(raw) <= LIMIT, "input exceeds 4 MiB")
    return raw.decode("utf-8")


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate JSON key")
        result[key] = value
    return result


def read_json(path):
    return json.loads(read(path), object_pairs_hook=unique_object)


def load_crd(source, pin=None):
    """Read the committed blob, never a dirty working-tree CRD or network URL."""
    catalog_pin = yaml.safe_load(read(HERE.parent / "pins.lock.yaml"))["pins"]["nvke-controller"]["commit"]
    require(isinstance(catalog_pin, str) and re.fullmatch(r"[0-9a-f]{40}", catalog_pin), "catalog NVKE source must be commit-pinned")
    require(pin is None or pin == catalog_pin, "NVKE commit differs from the catalog controller pin")
    result = subprocess.run(
        ["git", "--no-replace-objects", "-C", str(source), "cat-file", "blob", f"{catalog_pin}^{{commit}}:{CRD_PATH}"],
        capture_output=True, check=True, timeout=30,
    )
    require(len(result.stdout) <= LIMIT, "CRD exceeds 4 MiB")
    crd = yaml.safe_load(result.stdout)
    require(crd["kind"] == "CustomResourceDefinition", "source document is not a CRD")
    require(crd["spec"]["group"] == "internal.nvke.nvidia.com" and
            crd["spec"]["names"]["kind"] == "MachineImage", "unexpected source CRD")
    versions = [v for v in crd["spec"]["versions"] if v["name"] == "v1" and v["served"]]
    require(len(versions) == 1, "source must serve exactly one v1 schema")
    schema = versions[0]["schema"]["openAPIV3Schema"]
    pending = [schema]
    while pending:
        node = pending.pop()
        if isinstance(node, dict):
            require("$ref" not in node, "CRD schema references are unsupported; validation must remain offline")
            pending.extend(node.values())
        elif isinstance(node, list):
            pending.extend(node)
    Draft4Validator.check_schema(schema)
    return Draft4Validator(schema, format_checker=FormatChecker())


def literal_url(value):
    require(isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9._~:/\[\]-]+", value), "unsafe URL literal")
    url = urlsplit(value)
    require(url.scheme == "https" and url.hostname and url.path.startswith("/"), "URL must be absolute HTTPS")
    require(not url.username and not url.password and not url.query and not url.fragment, "URL contains credentials or suffix")
    require(all(p not in ("", ".", "..") for p in url.path.split("/")[1:]), "ambiguous URL path")
    authority = f"[{url.hostname}]" if ":" in url.hostname else url.hostname
    if url.port is not None:
        require(1 <= url.port <= 65535, "invalid URL port")
        authority += f":{url.port}"
    require(url.netloc == authority, "ambiguous URL authority")
    return value


def key(record):
    return record["output"], record["platform"]


def selected_assets(publication):
    selection = publication["selection"]
    require(selection["schemaVersion"] == "nvkr.dev/boot-selection/v1", "unknown selection schema")
    expected = {}
    for kind, entries in (("disk-image", selection["disks"]), ("executable", selection["imagers"])):
        for item in entries:
            require(key(item) not in expected, "duplicate selection")
            expected[key(item)] = (kind, item)
    assets = {}
    for asset in publication["assets"]:
        binding = asset["binding"]
        pair = key(binding)
        require(pair in expected and pair not in assets, "extra or duplicate asset")
        kind, selected = expected[pair]
        require(binding["kind"] == kind and all(binding.get(k) == v for k, v in selected.items()), "asset differs from selection")
        require(pair[1] in ("linux/amd64", "linux/arm64"), "unsupported platform")
        require(NAME.fullmatch(pair[0]) and len(pair[0]) <= 128, "invalid output name")
        require(DIGEST.fullmatch(binding["sha256"]) and type(binding["size"]) is int and binding["size"] > 0, "invalid raw asset binding")
        literal_url(asset["url"])
        assets[pair] = asset
    require(assets.keys() == expected.keys(), "missing selected asset")
    return assets


def render(publications, metadata, validator, template):
    """Render all pairs in memory; validation failure releases no partial set."""
    require(0 < len(publications) <= 32, "expected 1..32 destination descriptions")
    meta = {}
    for item in metadata:
        require(set(item) == {"output", "platform", "family", "version", "variant", "buildDate"}, "unexpected metadata fields")
        require(key(item) not in meta, "duplicate disk metadata")
        meta[key(item)] = item
    used, destinations, names, documents = set(), set(), set(), {}
    for publication in publications:
        destination = publication["destination"]["name"]
        require(isinstance(destination, str) and NAME.fullmatch(destination) and len(destination) <= 63, "invalid destination")
        require(destination not in destinations, "duplicate destination")
        destinations.add(destination)
        assets = selected_assets(publication)
        disks = publication["selection"]["disks"]
        imagers = publication["selection"]["imagers"]
        require(disks and len(disks) <= 256, "expected 1..256 disks")
        require({d["platform"] for d in disks} == {i["platform"] for i in imagers}, "unused or missing imager platform")
        for disk in disks:
            require(len(documents) < 256, "candidate set exceeds 256 pairs")
            matches = [i for i in imagers if i["platform"] == disk["platform"]]
            require(len(matches) == 1, "expected one imager per architecture")
            require(matches[0]["member"] and matches[0]["profile"], "imager member/profile missing")
            image, imager = assets[key(disk)], assets[key(matches[0])]
            require(image["url"] != imager["url"], "disk and imager URLs must differ")
            values = meta[key(disk)]
            require(all(isinstance(v, str) for v in values.values()), "metadata must contain strings")
            used.add(key(disk))
            arch = disk["platform"].removeprefix("linux/")
            args = {k: json.dumps(values[k]) for k in ("family", "version", "variant", "buildDate")}
            args.update(architecture=json.dumps(arch), output=json.dumps(disk["output"]), destination=json.dumps(destination),
                        imager_sha=json.dumps(imager["binding"]["sha256"]), disk_url=image["url"], imager_url=imager["url"],
                        disk_sha=image["binding"]["sha256"].removeprefix("sha256:"),
                        console="console=tty0 console=ttyS1,115200" if arch == "amd64" else "console=tty0 console=ttyAMA0,115200",
                        kernel="pci=realloc=off reboot=pci loglevel=1 iommu=pt intel_iommu=on" if arch == "amd64" else "loglevel=1 iommu.passthrough=1")
            document = yaml.safe_load(string.Template(template).substitute(args))
            if not values["variant"]:
                document["spec"].pop("variant", None)
            require(document["apiVersion"] == "internal.nvke.nvidia.com/v1" and document["kind"] == "MachineImage", "template has wrong type")
            script = document["spec"]["nico"]["ipxeScript"]
            require(script.startswith("#!ipxe\n") and script.count("{{") == script.count("}}") and
                    len(script.splitlines()) > 1, "invalid iPXE template")
            # Exact byte fingerprint for a candidate name, not a Build/Release identity.
            raw = yaml.safe_dump(document, sort_keys=True, width=float("inf")).encode()
            name = "mi-" + base64.b32encode(hashlib.sha256(raw).digest()).decode().lower().rstrip("=")
            document["metadata"]["name"] = name
            validator.validate(document)
            pair = (*key(disk), destination)
            require(name not in names, "candidate name collision")
            names.add(name)
            documents[pair] = document
    require(used == meta.keys(), "unused disk metadata")
    return yaml.safe_dump_all([documents[k] for k in sorted(documents)], sort_keys=True, width=float("inf"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--nvke-source", required=True, type=Path, help="local NVKE Git object repository; no fetch is performed")
    parser.add_argument("--nvke-commit", help="optional assertion; must equal pins.lock.yaml's nvke-controller commit")
    parser.add_argument("--publication", action="append", required=True, type=Path, help="generic PreparedBootDescription/SelectedBootDescription JSON, one per destination")
    parser.add_argument("--metadata", required=True, type=Path, help="proposed disk metadata JSON array; not authenticated")
    args = parser.parse_args()
    try:
        validator = load_crd(args.nvke_source, args.nvke_commit)
        output = render([read_json(p) for p in args.publication], read_json(args.metadata), validator,
                        read(HERE / "machineimage.yaml.tmpl"))
    except CandidateError as exc:
        print(f"verify-failed: {exc}", file=sys.stderr)
        return 1
    except (ValueError, KeyError, TypeError, AttributeError, OSError, subprocess.SubprocessError, yaml.YAMLError) as exc:
        print(f"verify-failed: catalog candidate rejected ({type(exc).__name__})", file=sys.stderr)
        return 1
    # jsonschema failures are handled separately to avoid printing input values.
    sys.stdout.write(output)
    return 0


if __name__ == "__main__":
    from jsonschema.exceptions import ValidationError, SchemaError
    try:
        sys.exit(main())
    except (ValidationError, SchemaError):
        print("verify-failed: candidate does not satisfy source CRD schema", file=sys.stderr)
        sys.exit(1)
