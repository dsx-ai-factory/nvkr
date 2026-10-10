// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// OperatorStatus contains only the owner's validated public cluster projection.
// It describes a timestamped observation, not retention or readiness authority.
type OperatorStatus struct{ text string }

// A current diagnostic observation is not the already-completed tenant
// acceptance result. Only this owner-issued marker may be ignored by its
// post-acceptance presenter; standalone status still returns a coded failure.
var errOperatorHealthUnconfirmed = errors.New("current diagnostic health unconfirmed")

// Text returns the formatted public observation, never raw Kubernetes responses.
func (status OperatorStatus) Text() string { return status.text }

// OperatorStatusWriter lets a presenter preserve the result separately from
// filtered subprocess logs. Ordinary writers still receive the same text.
type OperatorStatusWriter interface {
	WriteOperatorStatus(OperatorStatus) error
}

// WriteLiveStatus runs the selected session's actual kubectl, rather than
// projecting old Ready events or interpreting process existence as health.
// Only a small, validated node projection is public. Names, addresses, raw
// responses, kubeconfigs and command diagnostics stay in private local logs.
// This observation neither grants retention nor changes acceptance criteria.
func (access OperatorAccess) WriteLiveStatus(ctx context.Context, runner Runner, out io.Writer) error {
	if ctx == nil || out == nil {
		return nvkrerr.New(nvkrerr.CodeUsage, "live cluster status requires a context and output writer")
	}
	if err := requireContext(ctx); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "live cluster status canceled before inspection")
	}
	if err := access.inspectFiles(); err != nil {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "live cluster status requires the inspected private session access files")
	}
	if !filepath.IsAbs(runner.Work) || runner.Env == nil {
		return nvkrerr.New(nvkrerr.CodeUsage, "live cluster status requires private diagnostic storage and an explicit command environment")
	}
	directory, info, err := securepath.OpenDirectory(runner.Work)
	if err != nil {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "private cluster diagnostic directory unavailable")
	}
	if err := directory.Close(); err != nil || !currentOwner(info) || info.Mode().Perm() != 0o700 {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "cluster diagnostic directory must be private and owned")
	}
	// Each observation keeps its own no-replace command logs. Repeated status
	// calls must not collide with the original deployment's diagnostic files.
	logs, err := os.MkdirTemp(runner.Work, "operator-status-")
	if err != nil {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "could not retain private cluster status diagnostics")
	}
	runner.Work, runner.Progress, runner.output = logs, nil, nil
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var text strings.Builder
	fmt.Fprintf(&text, "\nLive Kubernetes checks · %s\nActual bundled kubectl; read-only observations, not a Release or a continuous health guarantee.\n", time.Now().UTC().Format(time.RFC3339))
	failed, unjoined := false, false
	var cancellation error
	for _, cluster := range []struct{ name, config string }{{"Site / Kind management", access.site}, {"Tenant / QCOW2", access.tenant}} {
		fmt.Fprintf(&text, "\n%s\n$ kubectl --kubeconfig <selected private config> get --raw=/readyz\n", cluster.name)
		raw, probeErr := runner.Capture(bounded, []string{access.kubectl, "--kubeconfig", cluster.config, "--request-timeout=10s", "get", "--raw=/readyz"}, clusterPhase(cluster.config == access.site, "api"), 15*time.Second)
		if probeErr != nil || strings.TrimSpace(string(raw)) != "ok" {
			cancellation = errors.Join(cancellation, operatorCancellation(probeErr), operatorCancellation(bounded.Err()))
			text.WriteString("API: readiness not confirmed. Private diagnostics retained.\n")
			failed, unjoined = true, unjoined || errors.Is(probeErr, ErrUnjoined)
			if errors.Is(probeErr, ErrUnjoined) || bounded.Err() != nil {
				break
			}
			continue
		}
		text.WriteString("API: ok\n$ kubectl --kubeconfig <selected private config> get nodes -o json\n")
		response, nodeErr := captureJSON[struct{ Items []tenantNode }](bounded, runner, []string{access.kubectl, "--kubeconfig", cluster.config, "--request-timeout=10s", "get", "nodes", "-o", "json"}, clusterPhase(cluster.config == access.site, "nodes"))
		rows, ready, valid := operatorNodeRows(response.Items)
		if nodeErr != nil || !valid {
			cancellation = errors.Join(cancellation, operatorCancellation(nodeErr), operatorCancellation(bounded.Err()))
			text.WriteString("Nodes: not confirmed. Private diagnostics retained.\n")
			failed, unjoined = true, unjoined || errors.Is(nodeErr, ErrUnjoined)
			if errors.Is(nodeErr, ErrUnjoined) || bounded.Err() != nil {
				break
			}
			continue
		}
		fmt.Fprintf(&text, "Nodes: %d/%d Ready\n", ready, len(rows))
		columns := tabwriter.NewWriter(&text, 0, 4, 2, ' ', 0)
		fmt.Fprintln(columns, "ROLE\tREADY\tARCH\tKUBERNETES")
		for _, row := range rows {
			fmt.Fprintln(columns, row)
		}
		_ = columns.Flush() // strings.Builder cannot fail.
		failed = failed || ready != len(rows)
	}
	if text.Len() > 64<<10 {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "public cluster observation exceeds its presentation limit; private diagnostics retained")
	}
	status := OperatorStatus{text: text.String()}
	if err := writeOperatorStatus(out, status); err != nil {
		return err
	}
	if failed {
		var err error = nvkrerr.Wrap(nvkrerr.CodePreflightFailed, errOperatorHealthUnconfirmed, "current cluster health was not confirmed; retained environment was not changed")
		if cancellation != nil {
			err = nvkrerr.Wrap(nvkrerr.CodeTimeout, errors.Join(cancellation, errOperatorHealthUnconfirmed), "current cluster observation was canceled or expired; retained environment was not changed")
		}
		if unjoined {
			return errors.Join(err, ErrUnjoined)
		}
		return err
	}
	return nil
}

func writeOperatorStatus(out io.Writer, status OperatorStatus) error {
	if presenter, ok := out.(OperatorStatusWriter); ok {
		if err := presenter.WriteOperatorStatus(status); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "present local demo cluster observation")
		}
		return nil
	}
	return writeOperatorText(out, status.Text())
}

// Carry only the standard cancellation cause across the public boundary,
// never arbitrary private subprocess errors or response text.
func operatorCancellation(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func clusterPhase(site bool, operation string) string {
	if site {
		return "site-" + operation
	}
	return "tenant-" + operation
}

func operatorNodeRows(nodes []tenantNode) ([]string, int, bool) {
	if len(nodes) == 0 {
		return nil, 0, false
	}
	rows, names, ready := make([]string, 0, len(nodes)), make(map[string]bool), 0
	for _, node := range nodes {
		if node.Metadata.Name == "" || names[node.Metadata.Name] {
			return nil, 0, false
		}
		names[node.Metadata.Name] = true
		info := node.Status.NodeInfo
		if !operatorArgument(info.Architecture) || !operatorArgument(info.KubeletVersion) || len(info.Architecture) > 128 || len(info.KubeletVersion) > 128 {
			return nil, 0, false
		}
		role, state, conditions := "unlabelled", "Unknown", 0
		if _, ok := node.Metadata.Labels["node-role.kubernetes.io/worker"]; ok {
			role = "worker"
		}
		if _, ok := node.Metadata.Labels["node-role.kubernetes.io/control-plane"]; ok {
			role = "control-plane"
		}
		for _, condition := range node.Status.Conditions {
			if condition.Type != "Ready" {
				continue
			}
			conditions++
			switch condition.Status {
			case "True":
				state = "True"
			case "False":
				state = "False"
			case "Unknown":
			default:
				return nil, 0, false
			}
		}
		if conditions > 1 {
			return nil, 0, false
		}
		if state == "True" {
			ready++
		}
		rows = append(rows, strings.Join([]string{role, state, info.Architecture, info.KubeletVersion}, "\t"))
	}
	slices.Sort(rows)
	return rows, ready, true
}

func writeOperatorText(out io.Writer, text string) error {
	written, err := io.WriteString(out, text)
	if err == nil && written != len(text) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "write local demo operator output")
	}
	return nil
}
