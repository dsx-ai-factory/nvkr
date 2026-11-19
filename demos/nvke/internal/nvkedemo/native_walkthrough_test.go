// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const nativeWalkthroughPods = `{"items":[{"metadata":{"name":"private-pod-name","namespace":"kube-system","uid":"private-pod-id"},"status":{"phase":"Running","podIP":"private-pod-address","containerStatuses":[{"ready":true,"restartCount":2,"imageID":"private-image"}]}},{"metadata":{"name":"private-job-name","namespace":"default"},"status":{"phase":"Succeeded","containerStatuses":[{"restartCount":0}]}}]}`

func nativeWalkthroughFixture(t *testing.T) (nativeSiteAttempt, OperatorAccess, Runner) {
	t.Helper()
	access, runner := operatorStatusFixture(t)
	access.siteQCOW = true
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$FIXTURE/events"
case "$*" in
  *--raw=/readyz*) printf '%s\n' "${READY_REPLY:-ok}" ;;
  *'get nodes -o json'*) printf '%s\n' "$NODES_JSON" ;;
  *'get pods -A -o json'*)
    if test "${BLOCK_PODS:-}" = 1; then
      printf '%s\n' "$$" > "$FIXTURE/pod-pid"
      exec /bin/sleep 30
    fi
    printf '%s\n' "$PODS_JSON" ;;
  *) exit 91 ;;
esac
`
	writeTenantFixture(t, access.kubectl, []byte(script), 0700)
	cli := filepath.Join(runner.Work, "nvkectl")
	writeTenantFixture(t, cli, []byte("#!/bin/sh\ntest \"$*\" = 'get clusters -o json' || exit 92\nprintf 'private-api-response\\n'\n"), 0700)
	runner.Env = SetEnv(runner.Env, map[string]string{"FIXTURE": runner.Work, "PODS_JSON": nativeWalkthroughPods})
	return nativeSiteAttempt{assets: NativeAssets{CLI: cli}}, access, runner
}

func TestNativeWalkthroughSelectsBothClustersAndKeepsRawPayloadPrivate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			attempt, access, runner := nativeWalkthroughFixture(t)
			public := &walkthroughPageBuffer{}
			var rawSink bytes.Buffer
			runner.Progress, runner.output = public, &rawSink
			if err := attempt.presentStatus(t.Context(), runner, access, enabled); err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"private-", runner.Work, access.site, access.tenant} {
				if strings.Contains(public.String(), private) || strings.Contains(rawSink.String(), private) {
					t.Fatalf("private diagnostic escaped: %q", private)
				}
			}
			if strings.Count(public.String(), "API: ok") != 2 || strings.Count(public.String(), "Nodes: 2/2 Ready") != 2 || !strings.Contains(public.String(), "Site / native QCOW2") {
				t.Fatal("initial API/node observations were duplicated or lost", public.String())
			}
			events, err := os.ReadFile(filepath.Join(runner.Work, "events"))
			if err != nil {
				t.Fatal(err)
			}
			for _, config := range []string{access.site, access.tenant} {
				query := "--kubeconfig " + config + " --request-timeout=10s get pods -A -o json"
				want := 0
				if enabled {
					want = 1
				}
				if strings.Count(string(events), query) != want {
					t.Fatalf("wrong cluster/number of pod observations: %s", events)
				}
			}
			for _, mutation := range []string{" apply ", " create ", " delete ", " exec ", "rollout"} {
				if strings.Contains(string(events), mutation) {
					t.Fatal("walkthrough mutated or reran work", string(events))
				}
			}
			for _, page := range public.pages {
				if page.Walkthrough() != enabled {
					t.Fatal("native presentation lost its selected kind")
				}
			}
			if !enabled {
				if strings.Contains(public.String(), "get pods") {
					t.Fatal("default status changed")
				}
				return
			}
			for _, row := range []string{"default  pod-001  Succeeded  0/1  0", "kube-system  pod-002  Running  1/1  2"} {
				if strings.Count(public.String(), row) != 2 {
					t.Fatal("sanitized pod table lost rows", public.String())
				}
			}
			directories, err := filepath.Glob(filepath.Join(runner.Work, "operator-status-*"))
			if err != nil || len(directories) != 1 {
				t.Fatal("observation storage not owned once", directories, err)
			}
			for _, phase := range []string{"site-pods", "tenant-pods"} {
				path := filepath.Join(directories[0], phase+".log")
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("raw pod response not private", err)
				}
				raw, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(raw), "private-pod-name") {
					t.Fatal("actual raw response was not retained", err)
				}
			}
		})
	}
}

func TestNativeWalkthroughFailureIsSurfacedWithoutPublicPrivatePayload(t *testing.T) {
	for _, pods := range []string{"private-invalid-json", `{"items":[]}`, strings.ReplaceAll(nativeWalkthroughPods, "kube-system", `\u001b[31m`)} {
		attempt, access, runner := nativeWalkthroughFixture(t)
		runner.Env = SetEnv(runner.Env, map[string]string{"PODS_JSON": pods})
		var public bytes.Buffer
		runner.Progress = &public
		err := attempt.presentStatus(t.Context(), runner, access, true)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodePreflightFailed || !strings.Contains(public.String(), "Pods: observation not confirmed") || strings.Contains(public.String()+err.Error(), "private-") || strings.ContainsRune(public.String(), '\x1b') {
			t.Fatal("explicit walkthrough failure was hidden or leaked payload", err, public.String())
		}
	}
}

func TestNativeWalkthroughCancellationJoinsTheActivePodObservation(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(strconv.FormatBool(expired), func(t *testing.T) {
			attempt, access, runner := nativeWalkthroughFixture(t)
			runner.Env = SetEnv(runner.Env, map[string]string{"BLOCK_PODS": "1"})
			var public bytes.Buffer
			runner.Progress = &public
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if expired {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 2*time.Second)
				want = context.DeadlineExceeded
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- attempt.presentStatus(ctx, runner, access, true) }()
			pidPath := filepath.Join(runner.Work, "pod-pid")
			deadline := time.Now().Add(5 * time.Second)
			var pid int
			for time.Now().Before(deadline) {
				if raw, err := os.ReadFile(pidPath); err == nil {
					pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid == 0 {
				cancel()
				<-done
				t.Fatal("actual pod observation did not start")
			}
			if !expired {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) || errors.Is(err, ErrUnjoined) || strings.Contains(err.Error(), access.kubectl) {
					t.Fatal("cancellation cause/join/privacy lost", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("pod observation did not stop and join")
			}
			if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
				t.Fatal("pod observation process survived returned cancellation", err)
			}
			events, err := os.ReadFile(filepath.Join(runner.Work, "events"))
			if err != nil || strings.Count(string(events), "get pods -A -o json") != 1 || strings.Contains(string(events), "--kubeconfig "+access.tenant) {
				t.Fatal("canceled observation continued or retried", string(events), err)
			}
		})
	}
}

func TestNativeWalkthroughPagesMaximumAdmittedPodInventory(t *testing.T) {
	attempt, access, runner := nativeWalkthroughFixture(t)
	var response struct{ Items []walkthroughPod }
	if err := json.Unmarshal([]byte(nativeWalkthroughPods), &response); err != nil {
		t.Fatal(err)
	}
	pod := response.Items[0]
	response.Items = nil
	for index := range 128 {
		pod.Metadata.Name = fmt.Sprintf("private-pod-%03d", index)
		response.Items = append(response.Items, pod)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"PODS_JSON": string(raw)})
	public := &walkthroughPageBuffer{}
	runner.Progress = public
	if err := attempt.presentStatus(t.Context(), runner, access, true); err != nil {
		t.Fatal(err)
	}
	// The controlled pager uses milliseconds. The interactive presenter may use
	// three seconds per page; its complete inventory needs the walkthrough
	// allowance rather than the ordinary 45-second status budget.
	if len(public.pages) <= 15 || strings.Count(public.String(), "  Running  1/1  2") != 256 || strings.Count(public.String(), "pod-128") != 2 {
		t.Fatal("admitted inventory was truncated", len(public.pages))
	}
	for _, page := range public.pages {
		if !page.Walkthrough() || strings.Count(page.Text(), "\n") > 15 {
			t.Fatal("paging bounds or owner marker lost", page.Text())
		}
	}
}
