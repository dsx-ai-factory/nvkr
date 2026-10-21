#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
umask 077

# The selected synchronous agent RPC does not propagate cancellation to its
# command runner. Bound the complete fixed operation at its execution owner.
if [[ ${1:-} != --bounded ]]; then
  exec /usr/bin/timeout --signal=TERM --kill-after=5s 110s /bin/bash "$0" --bounded "$@"
fi
shift
[[ $# == 7 ]]
address=$1 port=$2 domain=$3 old_sha=$4 new_sha=$5 renderer_sha=$6 ca_sha=$7
env_file=/etc/nke/spire.env
renderer=/usr/lib/nvke/spire-configure.sh
ca=/etc/nke/trust-bundle.pem
tpm=/usr/local/lib/spire-agent/tpm_attestor_agent

for tool in flock sha256sum stat mktemp timeout systemctl; do command -v "$tool" >/dev/null; done
[[ $(id -u) == 0 ]]
[[ -f "$env_file" && ! -L "$env_file" && $(stat -c '%u:%a' "$env_file") == 0:600 ]]
[[ -f "$renderer" && ! -L "$renderer" && -x "$renderer" ]]
[[ -f "$ca" && ! -L "$ca" && -f "$tpm" && ! -L "$tpm" && -x "$tpm" ]]
printf '%s  %s\n%s  %s\n' "$renderer_sha" "$renderer" "$ca_sha" "$ca" | sha256sum --check --status

# Requires/BindsTo/PartOf would let a SPIRE restart stop this RPC's own server.
# After/Wants, used by the selected package, do not propagate that stop.
for property in Requires BindsTo PartOf; do
  dependencies=" $(systemctl show nvke-agent.service --property="$property" --value) "
  [[ "$dependencies" != *' spire-agent.service '* && "$dependencies" != *' nvke-spire-configure.service '* ]]
done
systemctl is-active --quiet nvke-spire-configure.service
systemctl is-active --quiet nvke-agent.service
agent_pid=$(systemctl show nvke-agent.service --property=MainPID --value)
[[ "$agent_pid" =~ ^[1-9][0-9]*$ ]]
exec 9>/run/nvkr-native-spire-handoff.lock
flock -w 5 9
current_sha=$(sha256sum "$env_file"); current_sha=${current_sha%% *}
tpm_sha=$(sha256sum "$tpm"); tpm_sha=${tpm_sha%% *}
# Admit an initial transition or an explicit recovery after that transition.
# New env bytes alone do not prove the renderer or service restart succeeded.
[[ "$current_sha" == "$old_sha" || "$current_sha" == "$new_sha" ]]
work=$(mktemp -d /var/lib/nvke-native/spire-handoff.XXXXXXXX)
operation_log="$work/operation.log"
: > "$operation_log"
outcome=reapplied
if [[ "$current_sha" != "$new_sha" ]]; then
  cp --preserve=mode,ownership "$env_file" "$work/spire.env.before"
  printf 'SPIRE_SERVER_ADDRESS=%s\nSPIRE_SERVER_PORT=%s\nSPIRE_TRUST_DOMAIN=%s\n' "$address" "$port" "$domain" > "$work/spire.env.after"
  printf '%s  %s\n' "$new_sha" "$work/spire.env.after" | sha256sum --check --status
  # The env file and scratch live on the same source-owned guest filesystem.
  # mv is atomic; retain the original bytes privately for explicit recovery.
  [[ $(stat -c '%d' "$env_file") == $(stat -c '%d' "$work/spire.env.after") ]]
  mv -T "$work/spire.env.after" "$env_file"
  outcome=applied
fi
# Invoke the installed owner for every explicitly requested operation, including
# recovery after an earlier rename. Restarting its oneshot unit would propagate
# its stop through spire-agent's Requires relationship. Keep private renderer and
# systemd diagnostics outside the one JSON metadata receipt.
# A bounded client can fail while systemd still owns queued/running work. Mark
# either owner-call failure unknown, including a generic non-timeout exit1.
unknown_completion() {
  printf 'NVKR_SITE_IDENTITY_REMOTE_COMPLETION_UNKNOWN\n'
  exit 125
}
if ! timeout --signal=TERM --kill-after=2s 15s "$renderer" >> "$operation_log" 2>&1; then
  unknown_completion
fi
if ! timeout --signal=TERM --kill-after=2s 80s systemctl restart spire-agent.service >> "$operation_log" 2>&1; then
  unknown_completion
fi
systemctl is-active --quiet spire-agent.service
systemctl is-active --quiet nvke-agent.service
[[ $(systemctl show nvke-agent.service --property=MainPID --value) == "$agent_pid" ]]
printf '%s  %s\n%s  %s\n%s  %s\n' "$new_sha" "$env_file" "$ca_sha" "$ca" "$tpm_sha" "$tpm" | sha256sum --check --status
printf '{"outcome":"%s","environmentSHA256":"%s","nodeAgentPID":%s,"tpmPluginSHA256":"%s","privateLog":"%s"}\n' "$outcome" "$new_sha" "$agent_pid" "$tpm_sha" "$operation_log"
