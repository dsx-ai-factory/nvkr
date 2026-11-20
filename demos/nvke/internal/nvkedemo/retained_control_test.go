// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRetainedControlUnsupportedPlatformAdmitsNoEndpoint(t *testing.T) {
	if err := checkRetainedPeerCapability(t.Context()); err == nil {
		return // Positive endpoint/process fixtures cover admitted platforms.
	} else {
		var coded *nvkrerr.Error
		if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
			t.Fatal("unexpected platform admission failure", err)
		}
	}
	owner, _, _ := retainedTestOwner(t)
	control, err := newRetainedControl(t.Context(), owner, privateTestDir(t), detachRetainedProcess)
	var coded *nvkrerr.Error
	if control != nil || !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
		t.Fatal("unsupported host constructed retained control", err)
	}
}

func TestRetainedFailedTeardownExitNeverClaimsJoinedCleanup(t *testing.T) {
	for _, test := range []struct {
		name           string
		failure        error
		returned, safe bool
	}{
		{"still running", errors.New("private-resource-failure"), false, false},
		{"returned resource failure", errors.New("private-resource-failure"), true, true},
		{"unjoined producer", errors.Join(ErrUnjoined, errors.New("private-resource-failure")), true, false},
		{"unknown ownership", ErrRetainedControls, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner, ctx, _ := retainedTestOwner(t)
			if err := owner.requestCleanup(t.Context()); err != nil {
				t.Fatal(err)
			}
			recordRetainedCleanup(ctx, test.failure)
			if test.returned {
				owner.recordTeardownReturn(test.failure)
			}
			control := &retainedControl{owner: owner, finish: make(chan struct{}), description: retainedControlDescription{Work: "/exact", Token: strings.Repeat("a", 64)}}
			request := httptest.NewRequest(http.MethodPost, "/finish", nil)
			request = request.WithContext(context.WithValue(request.Context(), retainedRequesterKey{}, true))
			request.Header.Set("Authorization", "Bearer "+control.description.Token)
			request.Header.Set("X-NVKE-Retained-Work", control.description.Work)
			response := httptest.NewRecorder()
			control.handle(response, request)
			if (response.Code == http.StatusOK) != test.safe || control.joined {
				t.Fatal("failed exit invented join or bypassed ownership", response.Code, control.joined)
			}
			snapshot, err := control.snapshot(t.Context())
			if err != nil || snapshot.TeardownReturned != test.returned || snapshot.CleanupFailed != test.returned || snapshot.OwnerExitSafe != test.safe {
				t.Fatal("terminal outcome lost", snapshot, err)
			}
			if strings.Contains(response.Body.String(), "private-resource-failure") {
				t.Fatal("private teardown cause crossed control")
			}
			select {
			case <-control.finish:
				if !test.safe {
					t.Fatal("unsafe owner released")
				}
			default:
				if test.safe {
					t.Fatal("returned failed owner cannot finish")
				}
			}
			err = retainedTeardownFailure("/exact")
			if !errors.Is(err, ErrRetainedControls) || nvkrerr.Retryable(err) || !strings.Contains(err.Error(), "owner-return.log") || !strings.Contains(err.Error(), "owner-error.log") {
				t.Fatal("failed teardown was retryable or missing diagnostics", err)
			}
		})
	}
}

func TestRetainedControlRejectsUnissuedAndUnboundedDescriptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		wire retainedControlDescription
	}{
		{"nil context", nil, retainedControlDescription{}},
		{"zero description", ctx, retainedControlDescription{}},
		{"unbounded request", t.Context(), retainedControlDescription{Version: 1, Work: "/work", Socket: "@nvke-retained-" + strings.Repeat("a", 32), Token: strings.Repeat("a", 64), PID: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if client, err := connectRetained(test.ctx, test.wire); client != nil || err == nil {
				t.Fatal("description issued a live connection")
			}
		})
	}
	control := &retainedControl{}
	for _, test := range []struct {
		name string
		ctx  context.Context
	}{{"nil", nil}, {"unbounded", t.Context()}} {
		t.Run(test.name, func(t *testing.T) {
			if err := control.close(test.ctx); err == nil {
				t.Fatal("unadmitted close context reached a server")
			}
		})
	}
	var client *retainedConnection
	if _, err := client.request(ctx, http.MethodGet, "/state"); err == nil {
		t.Fatal("nil connected client supplied a live state")
	}
	if err := client.close(); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedCleanupCannotRaceHeldDetachAdmission(t *testing.T) {
	entered, release, detached := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var cleanupDuringDetach atomic.Bool
	owner, err := newRetainedRuntime(context.WithoutCancel(t.Context()), func() {
		select {
		case <-detached:
		default:
			cleanupDuringDetach.Store(true)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.markReady(t.Context(), OperatorAccess{kubectl: "/fixture", site: "/fixture-site", tenant: "/fixture-tenant"}); err != nil {
		t.Fatal(err)
	}
	control := &retainedControl{owner: owner, detach: func() error {
		close(entered)
		<-release
		close(detached)
		return nil
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	detachDone, cleanupDone := make(chan error, 1), make(chan error, 1)
	go func() { detachDone <- control.detachOwner(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("detach callback was not entered")
	}
	go func() { cleanupDone <- owner.requestCleanup(ctx) }()
	// Exercise concurrent request admission while the native transition is held.
	// The callback records whether cleanup was released before its completion.
	select {
	case <-cleanupDone:
		close(release)
		<-detachDone
		t.Fatal("explicit cleanup overtook a held native detach transition")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-detachDone; err != nil {
		t.Fatal(err)
	}
	if err := <-cleanupDone; err != nil || cleanupDuringDetach.Load() {
		t.Fatal("cleanup crossed detachment ownership", err)
	}
}

func TestRetainedControlClosePreservesUnjoinedAdmittedHandler(t *testing.T) {
	owner, _, _ := retainedTestOwner(t)
	if err := owner.markReady(t.Context(), OperatorAccess{kubectl: "/fixture", site: "/fixture-site", tenant: "/fixture-tenant"}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	control := &retainedControl{
		owner: owner, server: &http.Server{}, done: make(chan struct{}),
		description: retainedControlDescription{Work: "/exact", Token: strings.Repeat("a", 64)},
		detach:      func() error { close(entered); <-release; return nil },
	}
	// Reproduce the exact dangerous condition: Serve has returned, but an
	// already-admitted mutation handler still owns work. No listener/UID proof
	// is mocked as live runtime authority by this in-memory join contract.
	close(control.done)
	request := httptest.NewRequest(http.MethodPost, "/detach", nil)
	request = request.WithContext(context.WithValue(request.Context(), retainedRequesterKey{}, true))
	request.Header.Set("Authorization", "Bearer "+control.description.Token)
	request.Header.Set("X-NVKE-Retained-Work", control.description.Work)
	handlerDone := make(chan struct{})
	go func() { control.handle(httptest.NewRecorder(), request); close(handlerDone) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("admitted fixture handler did not enter")
	}
	short, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	err := control.close(short)
	stop()
	if !errors.Is(err, ErrUnjoined) {
		close(release)
		<-handlerDone
		t.Fatal("Serve exit hid the unjoined active handler", err)
	}
	close(release)
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("released handler did not join")
	}
	joined, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := control.close(joined); err != nil {
		t.Fatal("eventual handler join was not observed", err)
	}
	if err := control.recordJoined(joined, nil); err == nil {
		t.Fatal("handler join invented service cleanup")
	}
}

func TestRetainedResponseRejectsOverflowEvenAfterValidJSON(t *testing.T) {
	valid, err := json.Marshal(retainedControlSnapshot{Version: 1, Work: "/exact", State: retainedReady})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, raw string
		bad       bool
	}{
		{"valid projection", string(valid), false},
		{"bounded trailing whitespace", string(valid) + "\n ", false},
		{"overflow after valid JSON", string(valid) + strings.Repeat(" ", retainedControlLimit), true},
		{"second document", string(valid) + "{}", true},
		{"unknown field", `{"version":1,"work":"/exact","state":"retained-ready","invented":true}`, true},
		{"different workspace", `{"version":1,"work":"/different","state":"retained-ready"}`, true},
		{"unknown state", `{"version":1,"work":"/exact","state":"invented"}`, true},
		{"unknown error code", `{"version":1,"work":"/exact","state":"held-partial","failureCode":"private-provider-text"}`, true},
		{"failure before returned teardown", `{"version":1,"work":"/exact","state":"cleanup-requested","cleanupFailed":true}`, true},
		{"joined failed teardown", `{"version":1,"work":"/exact","state":"cleanup-requested","cleanupFailed":true,"teardownReturned":true,"joined":true}`, true},
		{"exit before returned teardown", `{"version":1,"work":"/exact","state":"cleanup-requested","ownerExitSafe":true}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeRetainedResponse(t.Context(), strings.NewReader(test.raw), "/exact")
			if (err != nil) != test.bad || test.bad && !reflect.DeepEqual(got, retainedControlSnapshot{}) {
				t.Fatalf("response bound/shape lost: %+v %v", got, err)
			}
		})
	}
}

// Authentication negatives use an in-memory HTTP handler only. They do not
// claim that a mocked requester supplies native peer or readiness authority.
func TestRetainedRequestAuthenticationPrecedesEveryMutation(t *testing.T) {
	for _, mutation := range []string{"no kernel requester", "wrong secret", "wrong workspace", "wrong selector", "body", "query"} {
		t.Run(mutation, func(t *testing.T) {
			var canceled atomic.Int32
			owner, err := newRetainedRuntime(context.WithoutCancel(t.Context()), func() { canceled.Add(1) })
			if err != nil {
				t.Fatal(err)
			}
			control := &retainedControl{owner: owner, description: retainedControlDescription{Work: "/exact", Token: strings.Repeat("a", 64)}}
			request := httptest.NewRequest(http.MethodPost, "/cleanup", nil)
			request = request.WithContext(context.WithValue(request.Context(), retainedRequesterKey{}, true))
			request.Header.Set("Authorization", "Bearer "+control.description.Token)
			request.Header.Set("X-NVKE-Retained-Work", control.description.Work)
			request.Header.Set("X-NVKE-Retained-Session", control.description.Selector)
			switch mutation {
			case "no kernel requester":
				request = request.WithContext(t.Context())
			case "wrong secret":
				request.Header.Set("Authorization", "Bearer forged")
			case "wrong workspace":
				request.Header.Set("X-NVKE-Retained-Work", "/different")
			case "wrong selector":
				request.Header.Set("X-NVKE-Retained-Session", "/different-selector")
			case "body":
				request.ContentLength = 1
			case "query":
				request.URL.RawQuery = "joined=true"
			}
			response := httptest.NewRecorder()
			control.handle(response, request)
			if response.Code != http.StatusForbidden || canceled.Load() != 0 || strings.Contains(response.Body.String(), control.description.Token) {
				t.Fatal("unauthenticated request mutated or leaked the exact session")
			}
		})
	}
}
