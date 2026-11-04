## External test output and local diagnostic boundary

`testresults` implements bounded JUnit/CTRF descriptions and deterministic serialization in lane
**none (descriptive)**. `validation` dispatches explicit named checks; `conformance` implements the
`kube-conformance` / `hydrophone` diagnostic adapter. Lifecycle authenticates selected product/tools
publications and exports held executable bytes; catalog, buildgraph, acquisition and delivery retain
production ownership. Original reports, product/tool source provenance and before/after observations
are retained. Normal harness exit status is a fact with an unknown cause, never a passing verdict.
Local/disposable acknowledgements are operator declarations, not proof of locality or exclusive ownership.

Contract and process-fixture tests cover guards, capture, cleanup and dispatch. They do not establish
successful external execution of the corrected published-source route. Report parsing, original JUnit
validity, continuous installed-byte proof, AI/user adapters, Definition applicability detection,
qualification signing and promotion from these outputs remain unimplemented.

Explicit complete local diagnostic Runs are now captured through a lifecycle-owned capability,
including original Plan/compiler inputs and held payloads. Product and tools may share one capture;
local reports omit hosted qualification inputs. Delivery retains distinct named native image output and
evidence bindings when their exact manifest bytes are shared. Conflicting aliases, repeated logical names
and duplicate direct descriptors remain rejected; physical closure accounting uses distinct digests.
Round-trip and rejection contracts cover this case; they do not prove a full native conformance run.

The committed native conformance demo inputs target Mac/Linux ARM64 and use
matching suite/tool outputs from the same fresh artifact Run.

The ordinary local demo also admits Linux AMD64 KVM. Default AMD64 input selection
uses the shared catalogs with architecture-specific acquired binaries and disk
firmware. Host/site tools are selected in the artifact Run and reused without a
second build. Default AMD64 and Mac preparation share the existing local
toolchain bootstrap and catalog-scoped substitutions; only Mac binds installed
native Packer dependencies. The consumer binds each disk, image and executable bundle to its
replayed Plan platform; bootstrap, imports, identity inputs and readiness use the
same architecture. AMD64 default conformance inputs are not supplied; shared selectors
now use the selected architecture. Local qualification retains its separate ARM64 lane.
Contract tests and compiled input validation are distinct from a real AMD64 KVM
build/deployment result, which is not yet recorded.

The shared native startup imports the ordinary named OVN image under its exact native digest and
projects matching chart values after package installation and before agent startup;
the OS package's independently built image is not treated as the named output.
The shared site startup imports each selected image through containerd's local manifest importer, preserving its repository alias and checking that exact reference through CRI before bootstrap. CRI owns the managed label: pre-setting it skips the config-ID alias required by image-volume mounts. Runtime evidence is limited to the identified Runs in [Local ARM64 demo presentation](local-arm64-demo-presentation.md#local-arm64-demo-presentation).
The native adapter projects Kubernetes version from the selected NVKE installer's shipped site-spec template, rechecking those verified payload bytes at execution boundaries. Site packages, bootstrap, tenant requests, readiness and optional conformance share that source-selected version; current authored conformance inputs select 1.36.3.
Tenant waiting now refreshes the exact owned NVKE tenant and worker topology through the existing
consumer owner before each kubeconfig/node observation. It publishes only the existing typed conditions
and refuses NVKE's exact `Provisioning=False` / `Provisioned=False` failure predicate; missing or Unknown
conditions do not prove failure. API progress advances only after a successful decoded node query, and
controller Provisioned does not replace the separate node, cloud-init, DNS or workload gates. Owned
subprocess fixtures cover condition transitions, transient reads, replaced identity, stage ordering and
retained resources. These contracts do not establish fresh runtime acceptance of this change.
Native tenants use NVKE's generic CAPI NicoCluster/NicoMachineTemplate input with source-generated join-token bootstrap and retained mTLS. NVKR supplies package installation and exact local image imports as pre-kubeadm commands; NVKE owns credentials and its final agent restart. Because commit-string agent versions can bypass the controller's semver-gated cloud-init check, the adapter independently observes each fresh Ready node's actual host cloud-init status without wait mode through a temporary Pod with a recursively read-only host mount. The shared conformance invocation selects an attempt-owned scratch directory on its output filesystem, retaining the factory's disk-backing/headroom checks. Recorded runtime evidence and its consumer/artifact revisions appear in [Local ARM64 demo presentation](local-arm64-demo-presentation.md#local-arm64-demo-presentation). The old native Release-template rewriting and compiled template-hash lists remain removed.
The site-create request carries the source-selected MachineImage boot script,
rendered with the existing NVKE disk-policy template and current local disk URL.
Its admitted iPXE selection also supplies DHCP address mode to both site readiness
and bootstrap address checks; standalone guests retain the static two-address contract.
Owned-subnet, reserved-address and duplicate-address rejection remain shared.
These source and shell contracts do not prove fresh runtime import, DHCP callback
identity or conformance acceptance. Strict manifest observations remain required.

Linux retains sealed executable descriptors. Mac local diagnostics hold authenticated Darwin
bytes and execute private Mach-O snapshots, checked before/after each command
and removed after joined process cleanup. This detects replacement between
commands, without Linux's sealed-descriptor guarantee against hostile same-user
writers. Published validation, signing and qualification admission remain Linux-only. Focused owner,
adapter and dispatch tests establish those contracts; the full native demo plus conformance remains
externally unverified. Existing validation-vector v3 and qualification-evidence v2 are unchanged.
[ADR-16 §6](../0016-release-gates-and-digest-first-selection.md#6-passive-external-qualification-handoff--deferred-producer-protocol)
owns diagnostic execution and passive external qualification; [ADR-18 §3](../0018-pipeline-ir-and-ci-adapter-emission.md#3-helper-owned-isolation-and-narrowly-admitted-native-arc-disks)
owns the host isolation posture. The [operator reference](../../external-test-output-contract.md)
owns prerequisites, selection and capture limits. These proposed contracts establish no organizational ratification.
