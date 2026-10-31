// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Only an owned, nonblocking presenter may publish bootstrap observations.
// Plain writers retain diagnostics privately rather than delaying cleanup.
type nativeBootstrapObserver interface {
	observeNativeBootstrap(string)
}

func reportNativeBootstrapObservation(runner Runner, text string) {
	if observer, ok := runner.Progress.(nativeBootstrapObserver); ok && len(text) > 0 && len(text) <= 32<<10 {
		observer.observeNativeBootstrap(text)
	}
}

// NVKE owns the JSON log contract (--json-logs). Only closed phase/outcome
// values and these static messages become observations. Arbitrary fields,
// error text and the sitespec printed on stdout remain private.
func nativeBootstrapLogSummary(raw []byte) string {
	var result strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for emitted := 0; emitted < 64 && scanner.Scan(); {
		var event struct {
			Message   string `json:"msg"`
			Operation string `json:"operation"`
			Phase     string `json:"phase"`
			Outcome   string `json:"outcome"`
			Status    string `json:"status"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Operation != "bootstrap_site" {
			continue
		}
		switch event.Message {
		case "cluster state":
			switch event.Status {
			case "Deleting", "UpdateFailed", "Updating", "Upgrading", "Recycling", "Provisioning", "Provisioned", "ProvisioningFailed", "Unknown":
				fmt.Fprintf(&result, "Bootstrap cluster state observation: %s\n", event.Status)
			default:
				continue
			}
		case "phase finished":
			switch event.Phase {
			case "preflight", "bootstrap_cluster", "engine_deploy", "site_cluster", "finish":
			default:
				continue
			}
			if event.Outcome != "succeeded" && event.Outcome != "failed" {
				continue
			}
			fmt.Fprintf(&result, "Bootstrap phase observation: %s %s\n", event.Phase, event.Outcome)
		case "creating bootstrap kind cluster", "bootstrap kind cluster ready", "installing postgres", "installing temporal", "installing SPIRE CRDs", "installing SPIRE", "installing the controller", "controller deployment healthy", "waiting for the cluster to finish provisioning", "cluster provisioned", "all control plane nodes joined":
			fmt.Fprintf(&result, "Bootstrap stage observation: %s\n", event.Message)
		default:
			continue
		}
		emitted++
	}
	return result.String()
}

func runNativeBootstrap(ctx context.Context, runner Runner, args []string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if len(args) == 0 {
		return configUsage("native bootstrap requires its selected CLI")
	}
	command := append([]string{args[0], "--json-logs"}, args[1:]...)
	var stderr bytes.Buffer
	runner.output, runner.privateCaptureStdout = &stderr, true
	_, err := runner.Capture(ctx, command, "host-nvke-bootstrap", 0)
	// An unresolved child may still own its output writer. Never inspect it
	// until the existing subprocess owner has confirmed the join.
	if !errors.Is(err, ErrUnjoined) {
		reportNativeBootstrapObservation(runner, nativeBootstrapLogSummary(stderr.Bytes()))
	}
	return err
}

// Read-only, bounded observations precede cleanup. Neither unavailable samples
// nor presentation errors replace the bootstrap failure; lost child joining
// must still retain dependent controls through the existing cleanup owner.
func (s *nativeSiteAttempt) observeBootstrapFailure(ctx context.Context, runner Runner, cause error) error {
	_, observes := runner.Progress.(nativeBootstrapObserver)
	if ctx == nil || cause == nil || errors.Is(cause, ErrUnjoined) || !observes {
		return cause
	}
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	private, err := nativeObservationRunner(bounded, runner, "native-bootstrap-observation-")
	if err != nil {
		return cause
	}
	if s.bootstrap != nil {
		for _, item := range []struct {
			name string
			args []string
		}{{"disk", []string{"df", "-Pm", "/"}}, {"memory", []string{"free", "-m"}}} {
			args := append([]string{s.bootstrap.limactl, "--log-level=error", "--tty=false", "shell", "--workdir=/", "bootstrap"}, item.args...)
			raw, err := private.Capture(bounded, args, "guest-"+item.name, 5*time.Second)
			if errors.Is(err, ErrUnjoined) {
				return errors.Join(cause, err)
			}
			if err == nil {
				reportNativeBootstrapObservation(runner, fmt.Sprintf("Bootstrap guest %s observation:\n%s\n", item.name, raw))
			}
		}
	}
	config, err := private.Capture(bounded, []string{filepath.Join(s.assets.HostTools, "kind"), "get", "kubeconfig", "--name", s.bootstrapName}, "bootstrap-config", 5*time.Second)
	if errors.Is(err, ErrUnjoined) {
		return errors.Join(cause, err)
	}
	path := filepath.Join(private.Work, "bootstrap.kubeconfig")
	if err != nil || privateWrite(path, config) != nil {
		return cause
	}
	// NVKE WaitReady already uses this exact proxy on the owned bootstrap
	// cluster. Read through its captured kubeconfig, not an unrelated host port.
	for _, collection := range []string{"clusters", "nodes"} {
		endpoint := "/api/v1/namespaces/nvke-controller/services/nvke-controller:http/proxy/api/" + collection
		raw, err := private.Capture(bounded, []string{filepath.Join(s.assets.HostTools, "kubectl"), "--kubeconfig", path, "--request-timeout=5s", "get", "--raw", endpoint}, "bootstrap-"+collection, 5*time.Second)
		if errors.Is(err, ErrUnjoined) {
			return errors.Join(cause, err)
		}
		if err == nil {
			reportNativeBootstrapObservation(runner, nativeBootstrapConditionSummary(collection, raw))
		}
	}
	raw, err := private.Capture(bounded, []string{filepath.Join(s.assets.HostTools, "kubectl"), "--kubeconfig", path, "--request-timeout=5s", "get", "pods", "--all-namespaces", "-o", "json"}, "bootstrap-pods", 5*time.Second)
	if errors.Is(err, ErrUnjoined) {
		return errors.Join(cause, err)
	}
	if err == nil {
		if rows, err := walkthroughPodRows(raw); err == nil {
			reportNativeBootstrapObservation(runner, fmt.Sprintf("Bootstrap pod observation (not readiness acceptance):\n%s", rows))
		}
	}
	return cause
}
