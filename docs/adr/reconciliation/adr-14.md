## Support by owner

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 14 Delivery | Executable/APT/Helm formats, byte-preserving acquired charts, image-bound source charts, standard Helm OCI manifests without custom artifact typing, RAW/ISO measurement, Linux held-qemu envelope verification, local held-QCOW2 retention and streamed plain/public-bootstrap FDE OCI disk wrappers | Independent full-payload signed M2 disk read-back, real strict receipt-to-package/finalization and unmodified ORAS binary interoperability remain unexercised. Stock Helm 4.2.3 pull/show/template passed locally against the compatibility registry in `TestHelmStandardPublication` with `NVKR_TEST_HELM_OCI=1`, preserving archive bytes and the rendered image digest; this is not live GitLab, hosted signing or install acceptance. Private-passphrase FDE and strict FDE remain unavailable; S3 copy transport has local contracts, without live provider or boot acceptance |
