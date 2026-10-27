// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const walkthroughTimeout = 3 * time.Minute

// These projections deliberately omit node addresses, pod names, credentials,
// provider messages and raw logs. Kubernetes owns response decoding through
// encoding/json; this presentation is not a readiness or qualification receipt.
type walkthroughMetadata struct {
	Name, Namespace string
}

type walkthroughPod struct {
	Metadata walkthroughMetadata
	Status   struct {
		Phase      string
		Containers []struct {
			Ready        bool
			RestartCount int
		} `json:"containerStatuses"`
	}
}

// walkthroughPodRows reuses the bounded Kubernetes pod projection for recorded
// observations. Raw identities and addresses remain private.
func walkthroughPodRows(raw []byte) (string, error) {
	var pods struct{ Items []walkthroughPod }
	if err := json.Unmarshal(raw, &pods); err != nil || len(pods.Items) == 0 || len(pods.Items) > 128 {
		return "", walkthroughInvalid()
	}
	slices.SortFunc(pods.Items, func(a, b walkthroughPod) int {
		return strings.Compare(a.Metadata.Namespace+"/"+a.Metadata.Name, b.Metadata.Namespace+"/"+b.Metadata.Name)
	})
	var text strings.Builder
	text.WriteString("NAMESPACE  POD  PHASE  READY  RESTARTS\n")
	for index, pod := range pods.Items {
		if !walkthroughName(pod.Metadata.Namespace) || !slices.Contains([]string{"Pending", "Running", "Succeeded", "Failed", "Unknown"}, pod.Status.Phase) || len(pod.Status.Containers) > 32 {
			return "", walkthroughInvalid()
		}
		ready, restarts := 0, 0
		for _, container := range pod.Status.Containers {
			if container.RestartCount < 0 {
				return "", walkthroughInvalid()
			}
			if container.Ready {
				ready++
			}
			restarts += container.RestartCount
		}
		fmt.Fprintf(&text, "%s  pod-%03d  %s  %d/%d  %d\n", pod.Metadata.Namespace, index+1, pod.Status.Phase, ready, len(pod.Status.Containers), restarts)
	}
	return text.String(), nil
}

func writeWalkthroughPages(ctx context.Context, out io.Writer, text string) error {
	if len(text) > 64<<10 {
		return walkthroughInvalid()
	}
	delay := time.Duration(0)
	if pager, ok := out.(OperatorStatusPager); ok {
		delay = min(3*time.Second, max(time.Duration(0), pager.OperatorStatusPageDuration()))
	}
	if ctx.Err() != nil {
		return errors.Join(writeOperatorStatus(out, OperatorStatus{text: text, walkthrough: true}), nvkrerr.Wrap(nvkrerr.CodeTimeout, ctx.Err(), "walkthrough presentation canceled; owned cleanup still required"))
	}
	if delay == 0 {
		return writeOperatorStatus(out, OperatorStatus{text: text, walkthrough: true})
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	const pageRows = 12
	pages := (len(lines) + pageRows - 1) / pageRows
	cluster, command := "", ""
	for start := 0; start < len(lines); start += pageRows {
		var page strings.Builder
		fmt.Fprintf(&page, "Walkthrough page %d/%d · observed before cleanup\n", start/pageRows+1, pages)
		if cluster != "" {
			fmt.Fprintln(&page, cluster)
		}
		if command != "" {
			fmt.Fprintln(&page, command)
		}
		for _, line := range lines[start:min(start+pageRows, len(lines))] {
			if line == "Site / Kind management" || line == "Site / native QCOW2" || line == "Tenant / QCOW2" {
				cluster, command = line, ""
			}
			if strings.HasPrefix(line, "$ kubectl ") {
				command = line
			}
			fmt.Fprintln(&page, line)
		}
		if err := writeOperatorStatus(out, OperatorStatus{text: page.String(), walkthrough: true}); err != nil {
			return err
		}
		if err := pause(ctx, delay); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "walkthrough presentation canceled; owned cleanup still required")
		}
	}
	return nil
}

func walkthroughName(name string) bool { return operatorArgument(name) && len(name) <= 128 }

func walkthroughInvalid() error {
	return nvkrerr.New(nvkrerr.CodePreflightFailed, "walkthrough response did not confirm its bounded observation; private diagnostics retained")
}
