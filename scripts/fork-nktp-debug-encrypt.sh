#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# TEMPORARY FORK-ONLY DIAGNOSTICS. Revert this entire change before review.
# No command output, plaintext or key material may reach the Actions log.
exec >/dev/null 2>&1
set -euo pipefail
umask 077
[[ ${GITHUB_REPOSITORY:-} == njhensley/nvkr ]]
[[ ${GITHUB_REF:-} == refs/heads/fix/nvkr-nktp-chart-source ]]
[[ ${GITHUB_EVENT_NAME:-} == workflow_dispatch && ${NVKR_HOST_DEPLOY_FAILED:-} == 1 ]]
[[ ${RUNNER_TEMP:-} == /* && ${GITHUB_WORKSPACE:-} == /* ]]
plain="$RUNNER_TEMP/nktp-deploy-debug-plain"
encrypted="$RUNNER_TEMP/nktp-deploy-debug-encrypted"
tarball="$RUNNER_TEMP/nktp-deploy-debug.tar"
key="$RUNNER_TEMP/nktp-deploy-debug.key"
password="$RUNNER_TEMP/nktp-deploy-debug.hex"
[[ -d $plain && ! -L $plain && -f $plain/host-nvke-deploy.log ]]
for path in "$encrypted" "$tarball" "$key" "$password"; do
  [[ ! -e $path && ! -L $path ]]
done

# shellcheck disable=SC2329
cleanup() {
  local status=$?
  trap - EXIT
  find "$plain" -type f -exec shred -u -- {} +
  shred -u -- "$tarball" "$key" "$password"
  rmdir -- "$plain"
  exit "$status"
}
trap cleanup EXIT
mkdir -m 700 "$encrypted"
tar -C "$plain" -cf "$tarball" .
openssl rand 32 > "$key"
# Encode the random key for enc's line-oriented password reader. RSA wraps the
# original 32 bytes; the recipient repeats this hex conversion after unwrapping.
od -An -v -tx1 "$key" | tr -d ' \n' > "$password"
openssl enc -aes-256-cbc -salt -pbkdf2 -iter 200000 -md sha256 \
  -pass "file:$password" -in "$tarball" -out "$encrypted/deploy.tar.enc"
openssl pkeyutl -encrypt -pubin -inkey "$GITHUB_WORKSPACE/.github/nktp-debug-pubkey.pem" \
  -pkeyopt rsa_padding_mode:oaep -pkeyopt rsa_oaep_md:sha256 -pkeyopt rsa_mgf1_md:sha256 \
  -in "$key" -out "$encrypted/key.enc"
[[ -s $encrypted/deploy.tar.enc && $(wc -c < "$encrypted/key.enc") -eq 512 ]]
