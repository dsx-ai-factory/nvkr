"""Print a synthetic Python USTAR compatibility fixture; no filesystem writes."""
import base64
import gzip
import hashlib
import io
import json
import tarfile

payload = b"Python tarfile compatibility fixture; not a Debian package"
digest = hashlib.sha256(payload).hexdigest()
manifest = {"schemaVersion": "nvkr.dev/package-acquisitions/v1", "architecture": "arm64",
            "scope": "apt-pre-install-pkgs", "packages": [{"name": "fixture", "version": "1:2.0-1",
            "architecture": "all", "digest": "sha256:" + digest, "size": len(payload),
            "path": "packages/" + digest + ".deb",
            "repositoryUris": ["https://archive.example.invalid/pool/fixture.deb"]}]}
raw = json.dumps(manifest, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
output = io.BytesIO()
with tarfile.open(fileobj=output, mode="w", format=tarfile.USTAR_FORMAT) as archive:
    for name, content in (("manifest.json", raw), (manifest["packages"][0]["path"], payload)):
        info = tarfile.TarInfo(name)
        info.mode, info.size = 0o444, len(content)
        archive.addfile(info, io.BytesIO(content))
print(base64.b64encode(gzip.compress(output.getvalue(), mtime=0)).decode())
