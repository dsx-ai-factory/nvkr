// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"golang.org/x/sys/unix"
)

func TestTenantSelectsMaintainedOVNChartStream(t *testing.T) {
	request := NewTenantRequest("owned")
	if request.CNI != "ovn-kubernetes" || request.CNIVersion != siteOVNVersion {
		t.Fatalf("tenant must select the packaged NVIDIA OVN chart stream: %+v", request)
	}
}

func TestTenantNetworkSelectionIsExplicitAndPreservesTopology(t *testing.T) {
	for _, test := range []struct {
		name, network, cni, version string
	}{
		{"default", "", "ovn-kubernetes", siteOVNVersion},
		{"maintained", "ovn", "ovn-kubernetes", siteOVNVersion},
		{"upstream diagnostic", "upstream-ovn", "ovn-kubernetes", upstreamSiteOVNVersion},
		{"temporary diagnostic", "kind-calico", "calico", "3.30.1"},
		{"unknown", "calico", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := newTenantRequest("owned", test.network)
			if test.cni == "" {
				assertConfigUsage(t, err)
				if !reflect.DeepEqual(request, TenantRequest{}) {
					t.Fatalf("unknown network must reject without request: %+v %v", request, err)
				}
				return
			}
			if err != nil || request.CNI != test.cni || request.CNIVersion != test.version {
				t.Fatalf("request selected unexpected CNI: %+v %v", request, err)
			}
			request.CNI, request.CNIVersion = "ovn-kubernetes", siteOVNVersion
			if !reflect.DeepEqual(request, NewTenantRequest("owned")) {
				t.Fatal("network selection changed unrelated tenant topology")
			}
		})
	}
}

func TestTenantRejectsUnknownNetworkBeforeInventory(t *testing.T) {
	work := privateTestDir(t)
	selected := tenant{name: "owned", network: "unknown", session: ManagementSession{Work: work, Runner: Runner{Work: work}}}
	assertConfigUsage(t, selected.run(t.Context(), 0))
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected network left output: %v %v", entries, err)
	}
}

func TestDemoNetworkDiagnosticScopeIsExplicit(t *testing.T) {
	for _, test := range []struct{ input, mode, label string }{
		{"", "ovn", "NVIDIA OVN"},
		{"ovn", "ovn", "NVIDIA OVN"},
		{"upstream-ovn", "upstream-ovn", "upstream OVN diagnostic; NVIDIA OVN not validated"},
		{"kind-calico", "kind-calico", "Kind/Calico diagnostic; NVIDIA OVN not deployed"},
	} {
		t.Run(test.mode+"/"+test.input, func(t *testing.T) {
			mode, err := normalizeNetwork(test.input)
			if err != nil || mode != test.mode {
				t.Fatalf("network normalization: %q %v", mode, err)
			}
			if summary := demoSuccessSummary(mode); !strings.HasPrefix(summary, "PASS ("+test.label+"):") || !strings.Contains(summary, "not a signed Release") {
				t.Fatalf("ambiguous diagnostic summary: %q", summary)
			}
			path := filepath.Join(privateTestDir(t), "tenant-result.json")
			if err := privateJSON(path, tenantResult{Scope: "unqualified-local-demo", Network: mode}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var result tenantResult
			if err := json.Unmarshal(raw, &result); err != nil || result.Network != test.mode || result.Scope != "unqualified-local-demo" {
				t.Fatalf("retained diagnostic lost network/scope: %+v %v", result, err)
			}
		})
	}
}

func TestTenantCalicoRolloutIsExplicitBoundedAndFailClosed(t *testing.T) {
	for _, test := range []struct{ name, network string }{
		{"default", ""}, {"ovn", "ovn"}, {"upstream-ovn", "upstream-ovn"}, {"ready", "kind-calico"},
		{"failed", "kind-calico"}, {"canceled", "kind-calico"},
		{"deadline", "kind-calico"}, {"unknown", "calico"},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := privateTestDir(t)
			calls := filepath.Join(work, "calls")
			writeTenantFixture(t, filepath.Join(work, "kubectl"), []byte(`#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CALLS"
case "$FAIL" in
  failed) exit 7 ;;
  deadline) exec sleep 60 ;;
esac
`), 0o700)
			selected := tenant{network: test.network, session: ManagementSession{Work: work, Tools: work, Runner: Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "CALLS=" + calls, "FAIL=" + test.name}}}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.name == "canceled" {
				cancel()
			}
			if test.name == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer deadlineCancel()
			}
			err := selected.waitCNI(ctx, "owned-tenant-kubeconfig")
			switch test.name {
			case "canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled CNI readiness: %v", err)
				}
			case "deadline":
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnjoined) {
					t.Fatalf("CNI readiness must honor remaining deadline and join child: %v", err)
				}
			case "failed":
				if CommandExitCode(err) != 7 {
					t.Fatalf("Calico rollout failure was hidden: %v", err)
				}
			case "unknown":
				assertConfigUsage(t, err)
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			raw, readErr := os.ReadFile(calls)
			if test.name == "deadline" && os.IsNotExist(readErr) {
				return // The parent may expire before command admission.
			}
			if test.name == "default" || test.name == "ovn" || test.name == "upstream-ovn" || test.name == "unknown" || test.name == "canceled" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("unselected or rejected CNI readiness started work: %v", readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := "--kubeconfig owned-tenant-kubeconfig -n calico-system rollout status daemonset/calico-node --timeout=240s\n"
			if string(raw) != want {
				t.Fatalf("unexpected CNI readiness commands: %q", raw)
			}
		})
	}
}

func TestTenantDNSRolloutIsBoundedAndFailClosed(t *testing.T) {
	for _, name := range []string{"ready", "coredns failure", "node-local failure", "canceled", "deadline"} {
		t.Run(name, func(t *testing.T) {
			work := privateTestDir(t)
			calls := filepath.Join(work, "calls")
			writeTenantFixture(t, filepath.Join(work, "kubectl"), []byte(`#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CALLS"
case "$FAIL:$*" in
  "coredns failure:"*deployment/coredns*) exit 7 ;;
  "node-local failure:"*daemonset/node-local-dns*) exit 7 ;;
  "deadline:"*) exec sleep 60 ;;
esac
`), 0o700)
			selected := tenant{session: ManagementSession{Work: work, Tools: work, Runner: Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "CALLS=" + calls, "FAIL=" + name}}}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			if name == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer deadlineCancel()
			}
			err := selected.waitDNS(ctx, "owned-tenant-kubeconfig")
			switch name {
			case "ready":
				if err != nil {
					t.Fatal(err)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled readiness: %v", err)
				}
			case "deadline":
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnjoined) {
					t.Fatalf("readiness must preserve deadline and join child: %v", err)
				}
			default:
				if CommandExitCode(err) != 7 {
					t.Fatalf("rollout failure was hidden: %v", err)
				}
			}
			raw, readErr := os.ReadFile(calls)
			if name == "deadline" && os.IsNotExist(readErr) {
				// A short remaining deadline may expire during process admission,
				// before the shell runs; it must still prevent subsequent work.
				return
			}
			if name == "canceled" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("canceled readiness started a command: %v", readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := "--kubeconfig owned-tenant-kubeconfig -n kube-system rollout status deployment/coredns --timeout=240s\n"
			if name == "ready" || name == "node-local failure" {
				want += "--kubeconfig owned-tenant-kubeconfig -n kube-system rollout status daemonset/node-local-dns --timeout=240s\n"
			}
			if string(raw) != want {
				t.Fatalf("unexpected readiness commands: %q", raw)
			}
		})
	}
}

func TestTenantSelectionNeverAdoptsSubstitution(t *testing.T) {
	for _, name := range []string{"exact", "absent", "unrelated", "duplicate", "changed ID", "foreign org", "foreign site", "foreign VPC", "empty ID", "path ID", "traversal ID", "whitespace ID", "NUL ID"} {
		t.Run(name, func(t *testing.T) {
			row := tenantRow{Name: "owned", ID: "opaque-owned-id", NICo: TenantProvider{OrgID, SiteID, VPCID}}
			rows := []tenantRow{row}
			id := row.ID
			switch name {
			case "absent":
				rows = nil
			case "unrelated":
				rows[0].Name = "other"
			case "duplicate":
				rows = append(rows, row)
			case "changed ID":
				rows[0].ID = "replacement"
			case "foreign org":
				rows[0].NICo.Org = "other"
			case "foreign site":
				rows[0].NICo.Site = "other"
			case "foreign VPC":
				rows[0].NICo.VPC = "other"
			case "empty ID":
				rows[0].ID, id = "", ""
			case "path ID":
				rows[0].ID, id = "../foreign", ""
			case "traversal ID":
				rows[0].ID, id = "..", ""
			case "whitespace ID":
				rows[0].ID, id = "foreign\n", ""
			case "NUL ID":
				rows[0].ID, id = "foreign\x00", ""
			}
			selected, err := selectedTenant(rows, "owned", id)
			switch name {
			case "exact":
				if err != nil || selected == nil || *selected != row {
					t.Fatalf("exact tenant rejected: %v %v", selected, err)
				}
			case "absent", "unrelated":
				if err != nil || selected != nil {
					t.Fatalf("unrelated tenant adopted: %v %v", selected, err)
				}
			default:
				if err == nil || selected != nil {
					t.Fatalf("substituted tenant adopted: %v %v", selected, err)
				}
			}
		})
	}
}

func readyTenantFixture() []tenantNode {
	nodes := make([]tenantNode, 2)
	for i := range nodes {
		nodes[i].Metadata.Name = fmt.Sprintf("owned-%d", i)
		nodes[i].Metadata.Labels = map[string]string{}
		nodes[i].Status.NodeInfo.Architecture = "arm64"
		nodes[i].Status.NodeInfo.KubeletVersion = "v1.34.2"
		nodes[i].Status.Conditions = append(nodes[i].Status.Conditions, struct{ Type, Status string }{"Ready", "True"})
	}
	nodes[0].Metadata.Labels["node-role.kubernetes.io/control-plane"] = ""
	return nodes
}

func TestTenantRequiresTwoDistinctReadyNativeNodes(t *testing.T) {
	for _, name := range []string{"exact", "none", "one", "extra", "empty name", "duplicate name", "not ready", "unknown ready", "wrong condition", "foreign arch", "foreign kubelet", "no control plane", "two control planes"} {
		t.Run(name, func(t *testing.T) {
			nodes := readyTenantFixture()
			expectError := true
			switch name {
			case "exact":
				expectError = false
			case "none":
				nodes, expectError = nil, false
			case "one":
				nodes, expectError = nodes[:1], false
			case "extra":
				nodes = append(nodes, nodes[0])
			case "empty name":
				nodes[1].Metadata.Name = ""
			case "duplicate name":
				nodes[1].Metadata.Name = nodes[0].Metadata.Name
			case "not ready":
				nodes[1].Status.Conditions[0].Status, expectError = "False", false
			case "unknown ready":
				nodes[1].Status.Conditions[0].Status, expectError = "Unknown", false
			case "wrong condition":
				nodes[1].Status.Conditions[0].Type, expectError = "Other", false
			case "foreign arch":
				nodes[1].Status.NodeInfo.Architecture = "amd64"
			case "foreign kubelet":
				nodes[1].Status.NodeInfo.KubeletVersion = "v1.34.3"
			case "no control plane":
				delete(nodes[0].Metadata.Labels, "node-role.kubernetes.io/control-plane")
			case "two control planes":
				nodes[1].Metadata.Labels["node-role.kubernetes.io/control-plane"] = ""
			}
			ready, err := readyNodes(nodes)
			if ready != (name == "exact") || (err != nil) != expectError {
				t.Fatalf("ready=%v err=%v, want ready=%v error=%v", ready, err, name == "exact", expectError)
			}
		})
	}
}

func tenantVMFixture(t *testing.T) (Runner, string, lifecycle.Artifact, string, []string) {
	return tenantVMFixtureAt(t, "")
}

func tenantVMFixtureAt(t *testing.T, simulatorWork string) (Runner, string, lifecycle.Artifact, string, []string) {
	t.Helper()
	runner, root, work, first := domainFixture(t)
	if simulatorWork != "" {
		work = simulatorWork
	}
	second := strings.Replace(first, "000000000001", "000000000002", 1)
	names := []string{first, second}
	for _, name := range names {
		id := strings.TrimPrefix(name, "nico-vm-")
		raw := fmt.Sprintf(`<domain><name>%s</name><uuid>%s</uuid><os><type>hvm</type></os><devices><tpm/><disk><source file="%s"/></disk></devices></domain>`, name, id, filepath.Join(work, "var/instances", id, "disk.qcow2"))
		writeTenantFixture(t, filepath.Join(root, name+".xml"), []byte(raw), 0o600)
		writeTenantFixture(t, filepath.Join(root, name+".uuid"), []byte(id), 0o600)
	}
	writeTenantFixture(t, filepath.Join(root, "inventory"), []byte(strings.Join(names, "\n")+"\n"), 0o600)
	if err := os.Mkdir(filepath.Join(work, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(work, "cache", "accepted.qcow2")
	contents := []byte("fixture QCOW2 bytes; this test never starts QEMU")
	writeTenantFixture(t, base, contents, 0o600)
	writeTenantBacking(t, root, base)
	writeTenantFixture(t, filepath.Join(root, "qemu-img"), []byte("#!/bin/sh\nset -eu\ntest \"$1\" = info\ncat \"$FIXTURE/backing.json\"\n"), 0o700)
	image := lifecycle.Artifact{Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(contents)), Disk: &delivery.DiskOutput{Size: int64(len(contents))}}
	// A nonempty Runner.Work must not turn the second VM's repeated inspection
	// phase into an exclusive-log collision. vmEvidence uses volatile capture.
	runner.Work = privateTestDir(t)
	return runner, root, image, work, names
}

func writeTenantFixture(t *testing.T, path string, raw []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
}

func writeTenantBacking(t *testing.T, root, backing string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"full-backing-filename": backing})
	if err != nil {
		t.Fatal(err)
	}
	writeTenantFixture(t, filepath.Join(root, "backing.json"), raw, 0o600)
}

func TestTenantVMEvidenceRejectsSubstitution(t *testing.T) {
	for _, tc := range []struct{ name, message string }{
		{"exact", ""}, {"one domain", "exactly two"}, {"extra domain", "exactly two"},
		{"foreign disk", "not owned"}, {"missing TPM", "lacks real"}, {"missing OS", "lacks real"},
		{"relative backing", "absolute"}, {"outside cache", "exact downloaded"}, {"backing symlink", "exact downloaded"},
		{"wrong size", "exact downloaded"}, {"wrong digest", "digest differs"}, {"missing disk description", "exact downloaded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, root, image, work, names := tenantVMFixture(t)
			xmlFile := filepath.Join(root, names[1]+".xml")
			raw, err := os.ReadFile(xmlFile)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "one domain":
				writeTenantFixture(t, filepath.Join(root, "inventory"), []byte(names[0]+"\n"), 0o600)
			case "extra domain":
				writeTenantFixture(t, filepath.Join(root, "inventory"), []byte(strings.Join(names, "\n")+"\nforeign\n"), 0o600)
			case "foreign disk":
				raw = stringsToBytesReplace(raw, work, "/foreign")
			case "missing TPM":
				raw = stringsToBytesReplace(raw, "<tpm/>", "")
			case "missing OS":
				raw = stringsToBytesReplace(raw, "<type>hvm</type>", "")
			case "relative backing":
				writeTenantBacking(t, root, "cache/accepted.qcow2")
			case "outside cache":
				base := filepath.Join(privateTestDir(t), "foreign.qcow2")
				accepted, readErr := os.ReadFile(filepath.Join(work, "cache", "accepted.qcow2"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				// Byte-identical outside data isolates the containment check;
				// a wrong size/digest must not mask its removal.
				writeTenantFixture(t, base, accepted, 0o600)
				writeTenantBacking(t, root, base)
			case "backing symlink":
				link := filepath.Join(work, "cache", "link")
				if err := os.Symlink(filepath.Join(work, "cache", "accepted.qcow2"), link); err != nil {
					t.Fatal(err)
				}
				linkInfo, statErr := os.Lstat(link)
				if statErr != nil {
					t.Fatal(statErr)
				}
				// Match lstat size so the independent no-symlink guard is
				// decisive even if its neighboring size check remains intact.
				image.Disk.Size = linkInfo.Size()
				writeTenantBacking(t, root, link)
			case "wrong size":
				image.Disk.Size++
			case "wrong digest":
				image.Digest = "sha256:" + strings.Repeat("0", 64)
			case "missing disk description":
				image.Disk = nil
			}
			writeTenantFixture(t, xmlFile, raw, 0o600)
			observed, err := vmEvidence(t.Context(), work, image, runner)
			if tc.name == "exact" {
				if err != nil || len(observed) != 2 || observed[0].BackingDigest != image.Digest || observed[1].BackingDigest != image.Digest {
					t.Fatalf("exact two-domain observation rejected: %v %v", observed, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.message) || observed != nil {
				t.Fatalf("wrong refusal for %s: observed=%v err=%v", tc.name, observed, err)
			}
			if _, err := os.Stat(filepath.Join(root, "events")); !os.IsNotExist(err) {
				t.Fatal("observation mutated a libvirt domain")
			}
		})
	}
}

func stringsToBytesReplace(raw []byte, old, replacement string) []byte {
	return []byte(strings.ReplaceAll(string(raw), old, replacement))
}

func TestTenantDiagnosticHeaderAdmission(t *testing.T) {
	for _, name := range []string{"exact", "empty", "too large", "public mode", "hardlink", "symlink", "directory", "FIFO", "foreign owner"} {
		t.Run(name, func(t *testing.T) {
			root := privateTestDir(t)
			path := filepath.Join(root, "header")
			writeTenantFixture(t, path, []byte("Authorization: Bearer fixture-only-token\n"), 0o600)
			switch name {
			case "empty":
				writeTenantFixture(t, path, nil, 0o600)
			case "too large":
				writeTenantFixture(t, path, make([]byte, 257), 0o600)
			case "public mode":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
			case "symlink", "directory", "FIFO":
				if err := os.Rename(path, filepath.Join(root, "held")); err != nil {
					t.Fatal(err)
				}
				var err error
				switch name {
				case "symlink":
					err = os.Symlink(filepath.Join(root, "held"), path)
				case "directory":
					err = os.Mkdir(path, 0o700)
				case "FIFO":
					err = unix.Mkfifo(path, 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "foreign owner":
				if err := os.Chown(path, os.Getuid()+1, -1); err != nil {
					if errors.Is(err, unix.EPERM) {
						t.Skip("foreign-UID fixture requires chown capability")
					}
					t.Fatal(err)
				}
			}
			err := privateHeader(path)
			if (err == nil) != (name == "exact") {
				t.Fatalf("header %s admission: %v", name, err)
			}
		})
	}
}
