// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const operatorNodesFixture = `{"items":[{"metadata":{"name":"private-control-name","labels":{"node-role.kubernetes.io/control-plane":""}},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"architecture":"arm64","kubeletVersion":"v1.34.2"},"addresses":[{"address":"private-address"}]}},{"metadata":{"name":"private-worker-name","labels":{"node-role.kubernetes.io/worker":""}},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"architecture":"arm64","kubeletVersion":"v1.34.2"}}}]}`

func operatorStatusFixture(t *testing.T) (OperatorAccess, Runner) {
	t.Helper()
	session := operatorSessionFixture(t)
	script := `#!/bin/sh
set -eu
case "$*" in
  *--raw=/readyz*) printf '%s\n' "${READY_REPLY:-ok}" ;;
  *'get nodes -o json'*) printf '%s\n' "$NODES_JSON" ;;
  *) exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(session.Tools, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	session.Runner.Env = SetEnv(session.Runner.Env, map[string]string{"NODES_JSON": operatorNodesFixture})
	return OperatorAccess{kubectl: filepath.Join(session.Tools, "kubectl"), site: filepath.Join(session.Work, "kubeconfig"), tenant: filepath.Join(session.Work, "tenant-kubeconfig")}, session.Runner
}

func TestOperatorLiveStatusExecutesBothClustersAndDoesNotLeakPrivateResponses(t *testing.T) {
	access, runner := operatorStatusFixture(t)
	// Repeated calls must retain independent logs and must not consume old
	// Ready events or overwrite the first invocation's private diagnostics.
	for range 2 {
		var out bytes.Buffer
		if err := access.WriteLiveStatus(t.Context(), runner, &out); err != nil {
			t.Fatal(err)
		}
		for _, wanted := range []string{"Site / Kind management", "Tenant / QCOW2", "API: ok", "Nodes: 2/2 Ready", "control-plane", "worker", "arm64", "v1.34.2", "get nodes -o json"} {
			if !strings.Contains(out.String(), wanted) {
				t.Fatalf("missing actual cluster projection %q: %s", wanted, &out)
			}
		}
		for _, private := range []string{"private-control-name", "private-worker-name", "private-address", "secret-config-sentinel", access.site, access.tenant, "PASS"} {
			if strings.Contains(out.String(), private) {
				t.Fatal("public status leaked or overclaimed", private)
			}
		}
	}
	directories, err := filepath.Glob(filepath.Join(runner.Work, "operator-status-*"))
	if err != nil || len(directories) != 2 {
		t.Fatal("status logs not separated", directories, err)
	}
	for _, directory := range directories {
		for _, phase := range []string{"site-api", "site-nodes", "tenant-api", "tenant-nodes"} {
			info, err := os.Stat(filepath.Join(directory, phase+".log"))
			if err != nil || info.Mode().Perm() != 0o600 || info.Size() == 0 {
				t.Fatal("actual kubectl observation not retained privately", phase, err)
			}
		}
	}
}

func TestOperatorLiveStatusFailuresRemainVisibleAndCoded(t *testing.T) {
	for _, test := range []struct{ name, ready, nodes, message string }{
		{"API not ready", "private-readiness-detail", operatorNodesFixture, "API: readiness not confirmed"},
		{"invalid JSON", "ok", "private-invalid-json", "Nodes: not confirmed"},
		{"no nodes", "ok", `{"items":[]}`, "Nodes: not confirmed"},
		{"not Ready", "ok", strings.ReplaceAll(operatorNodesFixture, `"True"`, `"False"`), "Nodes: 0/2 Ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			access, runner := operatorStatusFixture(t)
			runner.Env = SetEnv(runner.Env, map[string]string{"READY_REPLY": test.ready, "NODES_JSON": test.nodes})
			var out bytes.Buffer
			err := access.WriteLiveStatus(t.Context(), runner, &out)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || !strings.Contains(out.String(), test.message) || strings.Contains(out.String()+err.Error(), "private-") {
				t.Fatal("failed observation lost evidence or leaked private data", &out, err)
			}
		})
	}
}

func TestOperatorNodeProjectionRejectsAmbiguousOrUnsafeFields(t *testing.T) {
	for _, mutation := range []string{"duplicate name", "missing name", "duplicate Ready", "invalid Ready", "architecture controls", "version controls", "oversize version", "missing version", "missing Ready"} {
		t.Run(mutation, func(t *testing.T) {
			var nodes struct{ Items []tenantNode }
			if err := json.Unmarshal([]byte(operatorNodesFixture), &nodes); err != nil {
				t.Fatal(err)
			}
			node := &nodes.Items[0]
			switch mutation {
			case "duplicate name":
				node.Metadata.Name = nodes.Items[1].Metadata.Name
			case "missing name":
				node.Metadata.Name = ""
			case "duplicate Ready":
				node.Status.Conditions = append(node.Status.Conditions, node.Status.Conditions[0])
			case "invalid Ready":
				node.Status.Conditions[0].Status = "yes"
			case "architecture controls":
				node.Status.NodeInfo.Architecture = "arm64\x1b[2J"
			case "version controls":
				node.Status.NodeInfo.KubeletVersion = "v1.34.2\nPASS"
			case "oversize version":
				node.Status.NodeInfo.KubeletVersion = strings.Repeat("x", 129)
			case "missing version":
				node.Status.NodeInfo.KubeletVersion = ""
			case "missing Ready":
				node.Status.Conditions = nil
			}
			rows, ready, valid := operatorNodeRows(nodes.Items)
			if mutation == "missing Ready" {
				if !valid || ready != 1 || !strings.Contains(strings.Join(rows, "\n"), "Unknown") {
					t.Fatal("missing condition guessed Ready", rows, ready, valid)
				}
			} else if valid || ready != 0 || rows != nil {
				t.Fatal("ambiguous projection admitted", rows, ready, valid)
			}
		})
	}
}

func TestOperatorLiveStatusRejectsInvalidContextAndOutput(t *testing.T) {
	access, runner := operatorStatusFixture(t)
	for _, test := range []struct {
		name string
		out  io.Writer
		code nvkrerr.Code
	}{{"nil writer", nil, nvkrerr.CodeUsage}, {"short writer", operatorShortWriter{}, nvkrerr.CodePreflightFailed}} {
		t.Run(test.name, func(t *testing.T) {
			err := access.WriteLiveStatus(t.Context(), runner, test.out)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatal(err)
			}
		})
	}
	var missingContext context.Context
	if err := access.WriteLiveStatus(missingContext, runner, io.Discard); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestOperatorLiveStatusPreservesCancellationWithoutPrivateErrorPayload(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "deadline"}[deadline], func(t *testing.T) {
			access, runner := operatorStatusFixture(t)
			if err := os.WriteFile(access.kubectl, []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
				want = context.DeadlineExceeded
			} else {
				timer := time.AfterFunc(100*time.Millisecond, cancel)
				defer timer.Stop()
			}
			defer cancel()
			var out bytes.Buffer
			err := access.WriteLiveStatus(ctx, runner, &out)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeTimeout || !errors.Is(err, want) || strings.Contains(err.Error(), access.kubectl) {
				t.Fatal("canceled observation lost classification or leaked locators", err)
			}
		})
	}
}

func TestOperatorLiveStatusBoundsPublicProjectionBeforeOutput(t *testing.T) {
	access, runner := operatorStatusFixture(t)
	var rows struct{ Items []tenantNode }
	if err := json.Unmarshal([]byte(operatorNodesFixture), &rows); err != nil {
		t.Fatal(err)
	}
	node := rows.Items[0]
	node.Status.NodeInfo.KubeletVersion = "v" + strings.Repeat("1", 127)
	rows.Items = nil
	for index := range 512 {
		node.Metadata.Name = fmt.Sprintf("private-node-%d", index)
		rows.Items = append(rows.Items, node)
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runner.Work, "private-node-response.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nset -eu\ncase \"$*\" in\n *--raw=/readyz*) printf 'ok\\n' ;;\n *'get nodes -o json'*) cat \"$NODES_FILE\" ;;\n *) exit 99 ;;\nesac\n"
	if err := os.WriteFile(access.kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"NODES_FILE": path})
	var out bytes.Buffer
	err = access.WriteLiveStatus(t.Context(), runner, &out)
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || !strings.Contains(err.Error(), "presentation limit") || out.Len() != 0 {
		t.Fatal("oversize result was emitted or refused at the wrong boundary", err)
	}
}
