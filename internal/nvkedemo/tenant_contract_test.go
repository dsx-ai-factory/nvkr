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
	"strings"
	"testing"
	"time"
)

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
			ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			if name == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer deadlineCancel()
			}
			err := waitTenantDNS(ctx, "owned-tenant-kubeconfig", filepath.Join(work, "kubectl"), selected.session.Runner.Progress, selected.capture)
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
			want := []string{"deployment/coredns"}
			if name == "ready" || name == "node-local failure" {
				want = append(want, "daemonset/node-local-dns")
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != len(want) {
				t.Fatalf("unexpected readiness commands: %q", raw)
			}
			for i, resource := range want {
				prefix := "--kubeconfig owned-tenant-kubeconfig -n kube-system rollout status " + resource + " --timeout="
				value, ok := strings.CutPrefix(lines[i], prefix)
				budget, err := time.ParseDuration(value)
				if !ok || err != nil || budget <= 0 || budget > time.Hour || name != "deadline" && budget < 59*time.Minute {
					t.Fatalf("rollout did not inherit remaining work budget: %q", lines[i])
				}
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
			ready, err := readyNodesForCountVersion(nodes, "1.34.2", 2)
			if ready != (name == "exact") || (err != nil) != expectError {
				t.Fatalf("ready=%v err=%v, want ready=%v error=%v", ready, err, name == "exact", expectError)
			}
		})
	}
}

func tenantVMFixture(t *testing.T) (Runner, string, string, string, []string) {
	t.Helper()
	runner, root, work, first := domainFixture(t)
	second := strings.Replace(first, "000000000001", "000000000002", 1)
	third := strings.Replace(first, "000000000001", "000000000003", 1)
	names := []string{first, second, third}
	for _, name := range names {
		id := strings.TrimPrefix(name, "nico-vm-")
		raw := fmt.Sprintf(`<domain type="kvm"><name>%s</name><uuid>%s</uuid><os><type arch="aarch64">hvm</type></os><devices><tpm/><disk><source file="%s"/></disk></devices></domain>`, name, id, filepath.Join(work, "var/instances", id, "disk.qcow2"))
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
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(contents))
	// A nonempty Runner.Work must not turn the second VM's repeated inspection
	// phase into an exclusive-log collision. The live observer uses volatile capture.
	runner.Work = privateTestDir(t)
	return runner, root, digest, work, names
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

func TestNativeLinuxVMEvidenceRejectsSubstitution(t *testing.T) {
	for _, tc := range []struct{ name, message string }{
		{"exact", ""}, {"one domain", "exactly 3"}, {"extra domain", "exactly 3"},
		{"foreign disk", "outside"}, {"missing TPM", "lacks its real"}, {"missing OS", "lacks its real"},
		{"wrong architecture", "lacks its real"}, {"wrong engine", "lacks its real"}, {"duplicate TPM", "lacks its real"},
		{"relative backing", "absolute"}, {"backing symlink", ""}, {"wrong digest", "backing differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, root, digest, work, names := tenantVMFixture(t)
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
				raw = stringsToBytesReplace(raw, `<type arch="aarch64">hvm</type>`, "")
			case "wrong architecture":
				raw = stringsToBytesReplace(raw, `arch="aarch64"`, `arch="x86_64"`)
			case "wrong engine":
				raw = stringsToBytesReplace(raw, `type="kvm"`, `type="qemu"`)
			case "duplicate TPM":
				raw = stringsToBytesReplace(raw, "<tpm/>", "<tpm/><tpm/>")
			case "relative backing":
				writeTenantBacking(t, root, "cache/accepted.qcow2")
			case "backing symlink":
				link := filepath.Join(work, "cache", "link")
				if err := os.Symlink(filepath.Join(work, "cache", "accepted.qcow2"), link); err != nil {
					t.Fatal(err)
				}
				writeTenantBacking(t, root, link)
			case "wrong digest":
				digest = "sha256:" + strings.Repeat("0", 64)
			}
			writeTenantFixture(t, xmlFile, raw, 0o600)
			observed, err := nativeLinuxVMEvidence(t.Context(), runner, work, digest, 3)
			if tc.name == "exact" {
				if err != nil || len(observed) != 3 {
					t.Fatalf("exact site and tenant observation rejected: %v %v", observed, err)
				}
				for _, domain := range observed {
					if domain.BackingDigest != digest {
						t.Fatal("native domain lost its selected backing bytes")
					}
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
