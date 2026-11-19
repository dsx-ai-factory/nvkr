// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const retainedControlLimit = 64 << 10

// This record selects an endpoint. It cannot reconstruct live ownership or
// authorize a signal, a root operation, artifact verification or a Release.
// The live server and connected kernel peer must authenticate every request.
type retainedControlDescription struct {
	Version        int    `json:"version"`
	Work           string `json:"work"`
	Selector       string `json:"selector"`
	Socket         string `json:"socket"`
	Token          string `json:"token"`
	PID            int    `json:"pid"`
	PeerGeneration string `json:"peerGeneration,omitempty"`
}

type retainedAccessDescription struct {
	Kubectl, Site, Tenant string
	UIService             string
	UIPort                int
	UIHTTPObserved        bool
	SiteQCOW              bool
}

type retainedControlSnapshot struct {
	Version          int                       `json:"version"`
	Work             string                    `json:"work"`
	Selector         string                    `json:"selector"`
	State            retainedRuntimeState      `json:"state"`
	CheckedAt        time.Time                 `json:"checkedAt,omitempty"`
	Access           retainedAccessDescription `json:"access"`
	Detached         bool                      `json:"detached"`
	Joined           bool                      `json:"joined"`
	Progress         []retainedProgressFrame   `json:"progress,omitempty"`
	NetworkStarted   bool                      `json:"networkStarted"`
	TeardownReturned bool                      `json:"teardownReturned"`
	CleanupFailed    bool                      `json:"cleanupFailed"`
	OwnerExitSafe    bool                      `json:"ownerExitSafe"`
	FailureCode      nvkrerr.Code              `json:"failureCode,omitempty"`
}

// This private live control owns only the downstream service process. Root
// restoration remains in a fresh foreground command using the network owner.
// Neither a JSON request nor process existence is a joined-cleanup receipt.
type retainedControl struct {
	mu                  sync.Mutex
	owner               *retainedRuntime
	description         retainedControlDescription
	server              *http.Server
	listener            *net.UnixListener
	done                chan struct{}
	serveErr            error
	handlers            sync.WaitGroup
	handlersDone        chan struct{}
	closing             bool
	finish              chan struct{}
	start               chan struct{}
	started             bool
	progress            *retainedProgress
	detached            bool
	joined              bool
	finishing           bool
	detach              func() error
	endpointCleanup     func() error
	endpointCleanupOnce sync.Once
	endpointCleanupErr  error
}

type retainedRequesterKey struct{}

func newRetainedControl(ctx context.Context, owner *retainedRuntime, work string, detach func() error) (*retainedControl, error) {
	return newRetainedControlWithProgress(ctx, owner, work, work, detach, nil)
}

func newRetainedControlWithProgress(ctx context.Context, owner *retainedRuntime, work, selector string, detach func() error, progress *retainedProgress) (*retainedControl, error) {
	if ctx == nil || !owner.issued() || !filepath.IsAbs(work) || filepath.Clean(work) != work || !operatorArgument(work) || detach == nil {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "retained control requires its live owner and exact workspace")
	}
	if err := checkRetainedPeerCapability(ctx); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(selector) || filepath.Clean(selector) != selector || !operatorArgument(selector) {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "retained endpoint requires its exact original session selector")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "allocate private retained session")
	}
	token := hex.EncodeToString(random[:])
	generation, err := retainedPeerGeneration(ctx)
	if err != nil {
		return nil, err
	}
	listener, endpointCleanup, err := listenRetainedEndpoint(ctx, token)
	if err != nil {
		return nil, err
	}
	control := &retainedControl{
		owner: owner, listener: listener, done: make(chan struct{}), finish: make(chan struct{}), start: make(chan struct{}), detach: detach, progress: progress, endpointCleanup: endpointCleanup,
		description: retainedControlDescription{Version: 1, Work: work, Selector: selector, Socket: listener.Addr().String(), Token: token, PID: os.Getpid(), PeerGeneration: generation},
	}
	control.server = &http.Server{
		Handler: http.HandlerFunc(control.handle), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Second,
		MaxHeaderBytes: 4096,
		ConnContext: func(ctx context.Context, connection net.Conn) context.Context {
			unixConnection, ok := connection.(*net.UnixConn)
			allowed := ok && retainedRequesterUID(unixConnection) == uint32(os.Getuid())
			return context.WithValue(ctx, retainedRequesterKey{}, allowed)
		},
	}
	control.server.SetKeepAlivesEnabled(false)
	go func() {
		err := control.server.Serve(listener)
		control.mu.Lock()
		control.serveErr = err
		control.mu.Unlock()
		close(control.done)
	}()
	return control, nil
}

func (control *retainedControl) snapshot(ctx context.Context) (retainedControlSnapshot, error) {
	if err := requireContext(ctx); err != nil {
		return retainedControlSnapshot{}, err
	}
	control.owner.mu.Lock()
	state, networkStarted := control.owner.snapshot, control.owner.networkStarted
	returned, cleanupErr, failureCode := control.owner.teardownReturned, control.owner.cleanupErr, control.owner.failureCode
	control.owner.mu.Unlock()
	switch state.state {
	case retainedStarting, retainedPartial, retainedReady, retainedCleanupRequested:
	default:
		return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained owner state is unavailable")
	}
	control.mu.Lock()
	detached, joined := control.detached, control.joined
	control.mu.Unlock()
	access := state.access
	var frames []retainedProgressFrame
	if control.progress != nil {
		frames = control.progress.frames()
	}
	return retainedControlSnapshot{Version: 1, Work: control.description.Work, Selector: control.description.Selector, State: state.state, CheckedAt: state.checkedAt,
		Access: retainedAccessDescription{access.kubectl, access.site, access.tenant, access.uiService, access.uiPort, access.uiHTTPObserved, access.siteQCOW}, Detached: detached, Joined: joined, Progress: frames, NetworkStarted: networkStarted,
		TeardownReturned: returned, CleanupFailed: returned && cleanupErr != nil, OwnerExitSafe: returned && !errors.Is(cleanupErr, ErrUnjoined) && !errors.Is(cleanupErr, ErrRetainedControls), FailureCode: failureCode}, nil
}

func (control *retainedControl) handle(writer http.ResponseWriter, request *http.Request) {
	control.mu.Lock()
	if control.closing {
		control.mu.Unlock()
		http.Error(writer, "retained control is closing", http.StatusServiceUnavailable)
		return
	}
	control.handlers.Add(1)
	control.mu.Unlock()
	defer control.handlers.Done()
	allowed, _ := request.Context().Value(retainedRequesterKey{}).(bool)
	if !allowed || subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte("Bearer "+control.description.Token)) != 1 ||
		request.Header.Get("X-NVKE-Retained-Work") != control.description.Work || request.Header.Get("X-NVKE-Retained-Session") != control.description.Selector || request.URL.RawQuery != "" || request.ContentLength != 0 {
		http.Error(writer, "retained session authentication failed", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	var err error
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/state":
	case request.Method == http.MethodPost && request.URL.Path == "/start":
		err = control.startOwner(ctx)
	case request.Method == http.MethodPost && request.URL.Path == "/abort":
		// Stop work, not its services or dependent controls. The owning stack
		// reaches held-partial and remains accessible until explicit cleanup.
		control.owner.cancelOperations()
	case request.Method == http.MethodPost && request.URL.Path == "/detach":
		err = control.detachOwner(ctx)
	case request.Method == http.MethodPost && request.URL.Path == "/cleanup":
		err = control.owner.requestCleanup(ctx)
	case request.Method == http.MethodPost && request.URL.Path == "/finish":
		control.owner.mu.Lock()
		returned := control.owner.teardownReturned && control.owner.snapshot.state == retainedCleanupRequested
		failedExitSafe := returned && control.owner.cleanupErr != nil && !errors.Is(control.owner.cleanupErr, ErrUnjoined) && !errors.Is(control.owner.cleanupErr, ErrRetainedControls)
		control.mu.Lock()
		if !control.joined && !failedExitSafe {
			err = nvkrerr.New(nvkrerr.CodePreflightFailed, "live services have not joined; owner must remain")
		} else if !control.finishing {
			control.finishing = true
			close(control.finish)
		}
		control.mu.Unlock()
		control.owner.mu.Unlock()
	default:
		http.Error(writer, "retained control operation unavailable", http.StatusNotFound)
		return
	}
	if err != nil {
		// Private callback/root/runtime messages never cross this boundary.
		http.Error(writer, "retained owner could not admit this operation", http.StatusConflict)
		return
	}
	snapshot, err := control.snapshot(ctx)
	if err != nil {
		http.Error(writer, "retained state unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(snapshot)
}

// The launcher must authenticate the endpoint before service work can start.
// Closing a viewer is not a start or cleanup request. Admission serializes with
// cleanup so a request made before startup cannot subsequently deploy resources.
func (control *retainedControl) startOwner(ctx context.Context) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	control.owner.mu.Lock()
	defer control.owner.mu.Unlock()
	if control.owner.snapshot.state != retainedStarting {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "retained startup is no longer admitted")
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.closing || control.start == nil {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "retained startup owner is unavailable")
	}
	if !control.started {
		control.started = true
		close(control.start)
	}
	return nil
}

func (control *retainedControl) detachOwner(ctx context.Context) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	control.owner.mu.Lock()
	defer control.owner.mu.Unlock()
	state := control.owner.snapshot.state
	control.mu.Lock()
	if control.closing || state != retainedReady && state != retainedPartial {
		control.mu.Unlock()
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "closing retained owner cannot acknowledge detachment")
	}
	if control.detached {
		control.mu.Unlock()
		return nil
	}
	control.mu.Unlock()
	// Held-state admission and the native transition serialize with explicit
	// cleanup under owner.mu. Never hold the control/handler lock over a callback.
	if err := control.detach(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "retained owner detachment failed")
	}
	control.mu.Lock()
	control.detached = true
	control.mu.Unlock()
	return nil
}

// recordJoined is called by the runtime owner only after all actual service
// Stop-and-join obligations finish. No wire operation can supply this fact.
func (control *retainedControl) recordJoined(ctx context.Context, cleanupErr error) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	cleanupErr = errors.Join(cleanupErr, control.owner.joinedCleanup())
	if cleanupErr != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, errors.Join(cleanupErr, ErrRetainedControls), "retained runtime has not supplied a joined cleanup")
	}
	control.mu.Lock()
	control.joined = true
	control.mu.Unlock()
	return nil
}

func (control *retainedControl) close(ctx context.Context) error {
	if ctx == nil {
		return nvkrerr.New(nvkrerr.CodeUsage, "retained control join requires a bounded context")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return nvkrerr.New(nvkrerr.CodeUsage, "retained control join requires a deadline")
	}
	control.mu.Lock()
	control.closing = true
	if control.handlersDone == nil {
		control.handlersDone = make(chan struct{})
		go func() { control.handlers.Wait(); close(control.handlersDone) }()
	}
	handlersDone := control.handlersDone
	control.mu.Unlock()
	err := control.server.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, control.server.Close())
	}
	select {
	case <-control.done:
		control.mu.Lock()
		serveErr := control.serveErr
		control.mu.Unlock()
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		select {
		case <-handlersDone:
			control.endpointCleanupOnce.Do(func() {
				if control.endpointCleanup != nil {
					control.endpointCleanupErr = control.endpointCleanup()
				}
			})
			return errors.Join(err, serveErr, control.endpointCleanupErr)
		case <-ctx.Done():
			return errors.Join(ErrUnjoined, err, serveErr, ctx.Err())
		}
	case <-ctx.Done():
		return errors.Join(ErrUnjoined, err, ctx.Err())
	}
}

// A connected client is kernel-bound to this actual endpoint process. The
// descriptor passed by the caller remains a description, not adoption authority.
type retainedConnection struct {
	mu         sync.Mutex
	requested  bool
	connection *net.UnixConn
	peer       *SupervisorPeer
	described  retainedControlDescription
}

func connectRetained(ctx context.Context, description retainedControlDescription) (*retainedConnection, error) {
	if ctx == nil || description.Version != 1 || !filepath.IsAbs(description.Work) || filepath.Clean(description.Work) != description.Work ||
		!operatorArgument(description.Work) || !filepath.IsAbs(description.Selector) || filepath.Clean(description.Selector) != description.Selector || !operatorArgument(description.Selector) || description.PID <= 0 || len(description.Token) != 64 || !validRetainedEndpoint(description.Socket, description.Token) {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "retained connection requires an exact session description")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "retained connection requires a deadline")
	}
	if err := checkRetainedPeerCapability(ctx); err != nil {
		return nil, err
	}
	dialed, err := (&net.Dialer{}).DialContext(ctx, "unix", description.Socket)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nvkrerr.Wrap(nvkrerr.CodeTimeout, ctx.Err(), "retained connection canceled")
		}
		return nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "selected retained owner is unavailable; no resource adoption attempted")
	}
	connection := dialed.(*net.UnixConn)
	peer, err := observeRetainedPeer(ctx, connection, description.PID, description.PeerGeneration)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	return &retainedConnection{connection: connection, peer: peer, described: description}, nil
}

func (client *retainedConnection) request(ctx context.Context, method, action string) (retainedControlSnapshot, error) {
	var result retainedControlSnapshot
	if client == nil || client.connection == nil || client.peer == nil || ctx == nil {
		return result, nvkrerr.New(nvkrerr.CodeUsage, "retained request requires its connected live owner")
	}
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return result, nvkrerr.New(nvkrerr.CodeUsage, "retained request requires a deadline")
	}
	if err := ctx.Err(); err != nil {
		return result, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained request canceled")
	}
	client.mu.Lock()
	if client.requested {
		client.mu.Unlock()
		return result, nvkrerr.New(nvkrerr.CodeUsage, "retained connected requests are single-use; reconnect to the same owner for observation")
	}
	client.requested = true
	client.mu.Unlock()
	if err := client.connection.SetDeadline(deadline); err != nil {
		return result, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "bound retained request")
	}
	// net/http owns HTTP syntax and framing. Each issued connection carries one
	// request, with no redirects, proxies, ambient target or arbitrary argv.
	request, err := http.NewRequestWithContext(ctx, method, "http://retained.local"+action, nil)
	if err != nil {
		return result, nvkrerr.Wrap(nvkrerr.CodeUsage, err, "retained operation request")
	}
	request.Header.Set("Authorization", "Bearer "+client.described.Token)
	request.Header.Set("X-NVKE-Retained-Work", client.described.Work)
	request.Header.Set("X-NVKE-Retained-Session", client.described.Selector)
	request.Close = true
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(context.Context, string, string) (net.Conn, error) { return client.connection, nil }}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return result, nvkrerr.Wrap(nvkrerr.CodeTimeout, ctx.Err(), "retained operation acknowledgment expired; state remains unknown")
		}
		return result, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained operation was not acknowledged; do not infer cleanup or restart")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained owner refused the requested operation")
	}
	result, err = decodeRetainedResponse(ctx, response.Body, client.described.Work)
	if err == nil && result.Selector != client.described.Selector {
		return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "live owner acknowledgment differs from its original session selector")
	}
	return result, err
}

// The standard JSON owner decodes a bounded descriptive projection. This
// decoder cannot issue a live control or restore any execution/root authority.
func decodeRetainedResponse(ctx context.Context, body io.Reader, work string) (retainedControlSnapshot, error) {
	var result retainedControlSnapshot
	if ctx == nil || body == nil {
		return result, nvkrerr.New(nvkrerr.CodeUsage, "retained response requires a context and bounded body")
	}
	if err := ctx.Err(); err != nil {
		return result, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained response canceled")
	}
	raw, err := io.ReadAll(io.LimitReader(body, retainedControlLimit+1))
	if err != nil || len(raw) > retainedControlLimit {
		return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained response exceeds its bounded envelope")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained response could not be decoded")
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF || result.Version != 1 || result.Work != work {
		return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained response differs from the selected session")
	}
	switch result.State {
	case retainedStarting, retainedPartial, retainedReady, retainedCleanupRequested:
	default:
		return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained response has an unknown owner state")
	}
	if result.FailureCode != "" && !slices.Contains(nvkrerr.AllCodes, result.FailureCode) {
		return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained response has an unknown failure code")
	}
	if result.CleanupFailed && (!result.TeardownReturned || result.Joined) || result.OwnerExitSafe && !result.TeardownReturned {
		return retainedControlSnapshot{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained response has an inconsistent teardown outcome")
	}
	return result, nil
}

func (client *retainedConnection) close() error {
	if client == nil {
		return nil
	}
	connectionErr := client.connection.Close()
	if errors.Is(connectionErr, net.ErrClosed) {
		connectionErr = nil
	}
	return errors.Join(connectionErr, client.peer.Close())
}
