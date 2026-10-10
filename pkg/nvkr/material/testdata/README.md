# Python acquisition fixture

`python-acquisition.tar.gz.base64` encodes one Python 3.13.7 standard-library
`tarfile.USTAR_FORMAT` archive. Gzip/base64 keeps the 10-KiB record small in Git;
the test decodes it before invoking the real held-archive verifier. The payload
is synthetic, not a Debian package. Exact raw archive and manifest-blob SHA-256
values are asserted by `TestObservePythonTarfileCapture`.

Recreate the encoded fixture on standard output with the retained generator:

```sh
python3 pkg/nvkr/material/testdata/python_acquisition_fixture.py
```

This generator uses only Python's standard JSON, tar, gzip and hash libraries.
It mirrors the source recorder's format; it does not invoke its APT/dpkg metadata
matching or establish acquisition-origin evidence. The raw tar bytes and their
digests, not gzip-version-specific bytes, define the fixture's acceptance.
