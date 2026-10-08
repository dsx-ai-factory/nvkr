# Owned local network guard, October 8, 2026

## Result and scope

Native ARM64 Colima replays `network-runtime-20261008-04` and
`network-runtime-20261008-05` exited 0. Replay 04 exercised normal setup and
restoration; replay 05 deliberately raised a joined consumer failure after
starting DHCP/DNS, then exercised restoration. Both used the exact code below.
No guest was created. Listening DHCP/DNS is not guest DHCP/DNS delivery,
BGP reachability, tenant provisioning or workload acceptance.

This is an internal downstream development primitive in
`scripts/nke-local-network.py`, not a standalone full-demo command, an NKD
networking API, a generated execution route or Release authority. The site-only
entrypoint does not yet call it. The final deploy-only tenant adapter still
needs to connect its lifetime to the simulator, guests, site and disk server.

## Exact code and environment

The worktree was based on PR #118 commit
`e8ffcde45c2cefafcefd682bc8c4cda9f1855cd5` plus this increment.
These are SHA-256 values of the actual exercised files:

| File | Exact SHA-256 |
| --- | --- |
| `scripts/nke-local-network.py` | `4c0d20f53520a31df8caadda5f7460e770f4985c7b32f6cc4e92d750f415266b` |
| `scripts/nke-local-management.py` | `d28c40c31db3d7950a051da47173b632256694fe68ec3157c168596c4638a741` |
| `scripts/nke_local_network_test.py` | `2d4a8c6aaef7a9d79b32d4ba46ac94efcc98cb0518a4dde0dd6d6dfb2c9e8c1e` |

The dedicated `nkd-demo` Colima profile was running Linux aarch64 with native
Docker 29.5.2, eight CPUs and 24 GiB configured RAM. Its native output volume
reported 77 GiB available. The physical Mac volume reported only 22 GiB
available; native virtual capacity does not establish fresh OS-bake capacity.
Docker was reached explicitly through `colima-nkd-demo`, not the unavailable
default Docker Desktop socket. No context default was changed.

Host networking tools are operator-trusted development prerequisites, not an
authenticated toolchain closure. This replay used iproute2 6.1.0, dnsmasq 2.90
and iptables 1.8.10. There was no successful full-disk smoke or new image bake.

## Setup and ownership

Admission rejected unsupported platforms, existing demo bridge/policy,
overlapping guest/VIP routes, occupied listeners, a nonempty libvirt session
and a running simulator. A kernel-released lock serialized the guard.

The guard created bridge `nkdvm0` with a creation-time explicit random locally
administered unicast MAC, and checked bridge kind, address and known ifindex.
Only a successful complete iproute2 JSON inventory can establish its absence;
a request failure is never treated as absence. The bridge carried gateway
`172.30.0.1/24` and metadata address `169.254.169.254/32`.

Twelve exact firewall rules carried one unique attempt comment. They admitted
DHCP/DNS, metadata, BGP and disk-serving ports, guest forwarding/NAT and the
Kind-to-tenant-VIP API path. These describe intended routes, not observed guest
traffic. Temporary sysctl changes retained their original values.

The initially root-owned mode-0755 QEMU bridge helper temporarily gained SUID
under an exact inode/byte binding and a newly installed `allow nkdvm0` policy.
The guard copied dnsmasq into its private directory, required current-user
ownership, private permissions and one hard link, then granted only that copy
the requested network capabilities. libcap's own verifier checked the grant.
The original host dnsmasq binary and unrelated libvirt/Colima networks were not
adopted, stopped or permission-modified.

The private foreground dnsmasq process ran and logged sockets bound to
`nkdvm0`, DHCP range `172.30.0.10`–`172.30.0.254`, and upstream resolver
`192.168.5.1`. Each replay stopped and joined it before restoring host controls.

## Independent restoration readback

After both final replays, separate native read-only commands observed:

- No `nkdvm0` bridge and no session domains.
- No `/etc/qemu/bridge.conf`; the exact policy was archived in private diagnostics.
- QEMU helper restored to root-owned mode 0755.
- No capabilities on the private dnsmasq copy.
- Zero firewall rules carrying a demo-owner comment.
- IPv4 forwarding 1 and multipath hash policy 0, matching their original values.

Private logs and disabled-policy archives remain under
`/mnt/lima-colima-nkd-demo/nkd-local/network-runtime-20261008-{04,05}`.
Accepted Runs and shared Docker/Kind/libvirt caches were not pruned.

## Failures, fixes and contract tests

Earlier replay 01 failed admission against libvirt's interface-scoped DHCP
listener; admission was adjusted to coexist without stopping that foreign
service. Replay 02 exercised an earlier guard, not the final code above.
Replay 03 discovered that bridge creation did not retain its requested alias.
Cleanup refused the ambiguous bridge. Its exact index/address, private
capability, policy inode and unique rules were independently matched and
restored by a bounded private recovery harness before any subsequent replay.
Creation-time MAC binding replaced the ineffective alias.

Independent review also corrected ambiguous capability-grant recovery,
hard-link privilege leakage, mandatory diagnostic writes during restoration,
and treating an inventory command error as bridge absence. Negative tests
cover those cases, foreign ownership, active/unjoined producers and platform
admission. Cleanup can capture bounded results in native Linux anonymous RAM
without requiring successful diagnostic writes.

All 42 management/site/network Python fixtures passed on native Linux.
macOS passed with two explicitly Linux-only skips. The Go scripts gate now
includes the network fixtures. `make check`, uncached `go test ./... -count=1`,
`make build`, targeted scripts race checks and `git diff --check` passed.
Independent review reported no remaining actionable findings in this delta.
These fixtures and the live host restoration
replays do not establish guest bootstrap, BGP routes or full tenant/workload E2E.
