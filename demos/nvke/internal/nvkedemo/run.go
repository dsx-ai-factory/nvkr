// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

type diskService struct {
	server   *http.Server
	done     chan error
	file     *os.File
	url      string
	cancel   context.CancelFunc
	handlers sync.WaitGroup
	mu       sync.Mutex
	closing  bool
	requests atomic.Int64
}

const retainedRuntimeCleanupTimeout = 8 * time.Minute
const retainedRestoreTimeout = 8 * time.Minute
const retainedRegistryCleanupTimeout = 2 * time.Minute
const retainedManagementCleanupTimeout = 3 * time.Minute

// RetainedCleanupTimeout covers natural tenant deletion, joined producer
// teardown, foreground network restoration and control acknowledgment. It is
// a wait budget, not permission to skip an owner or claim cleanup on expiry.
const RetainedCleanupTimeout = tenantCleanupTimeout + retainedRegistryCleanupTimeout + retainedManagementCleanupTimeout + retainedRuntimeCleanupTimeout + retainedRestoreTimeout + time.Minute

func startDiskService(ctx context.Context, path, address string) (*diskService, error) {
	return startDiskServiceWithNightlyAlias(ctx, path, address, "")
}

// The nightly suite verifies its fixed URL while the upstream-OVN profile uses
// its named output URL. Both routes read the same held file. No other alias or
// filesystem route is admitted, and the ordinary service exposes no alias.
func startDiskServiceWithNightlyAlias(ctx context.Context, path, address, alias string) (*diskService, error) {
	name := filepath.Base(path)
	if alias != "" && (name != DiskOutputName+".qcow2" || alias != "nvke-node-image-upstream-ovn-arm64.qcow2") {
		return nil, nvkrerr.New(nvkrerr.CodeEngineCapability, "disk service does not support this candidate URL alias")
	}
	file, info, err := securepath.OpenRegular(path)
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 {
		_ = file.Close()
		return nil, errors.New("empty served disk")
	}
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "tcp4", address)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	service := &diskService{done: make(chan error, 1), file: file, url: "http://" + listener.Addr().String() + "/" + name}
	serviceLifetime := ctx
	if owner := runtimeRetention(ctx); owner != nil {
		serviceLifetime = owner.lifetime
	}
	serveContext, cancel := context.WithCancel(serviceLifetime)
	service.cancel = cancel
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		service.mu.Lock()
		if service.closing {
			service.mu.Unlock()
			http.Error(writer, "service stopping", http.StatusServiceUnavailable)
			return
		}
		service.handlers.Add(1)
		service.mu.Unlock()
		defer service.handlers.Done()
		selectedPath := request.URL.Path == "/"+name || alias != "" && request.URL.Path == "/"+alias
		if !selectedPath || request.URL.RawQuery != "" || (request.Method != http.MethodGet && request.Method != http.MethodHead) {
			http.NotFound(writer, request)
			return
		}
		service.requests.Add(1)
		http.ServeContent(writer, request, name, info.ModTime(), io.NewSectionReader(file, 0, info.Size()))
	})
	// Payload transfers live for the owned service context. A whole-body write
	// deadline would reject a valid large disk on a slower host or connection.
	service.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return serveContext }}
	service.server.SetKeepAlivesEnabled(false)
	go func() { service.done <- service.server.Serve(listener) }()
	return service, nil
}
func (s *diskService) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.cancel()
	shutdown, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := s.server.Shutdown(shutdown)
	cancel()
	if err != nil {
		closeErr := s.server.Close()
		// Bounded graceful shutdown may escalate to closing every connection.
		// A joined fallback is successful; a failed close/join never is.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			err = closeErr
		} else {
			err = errors.Join(err, closeErr)
		}
	}
	select {
	case serveErr := <-s.done:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		joined := make(chan error, 1)
		go func() { s.handlers.Wait(); joined <- s.file.Close() }()
		select {
		case closeErr := <-joined:
			return errors.Join(err, serveErr, closeErr)
		case <-ctx.Done():
			return errors.Join(ErrUnjoined, ctx.Err(), err, serveErr)
		}
	case <-ctx.Done():
		return errors.Join(ErrUnjoined, ctx.Err(), err)
	}
}
func verifyHTTPDisk(ctx context.Context, url string, image lifecycle.Artifact) error {
	if image.Disk == nil || image.Disk.Size <= 0 {
		return errors.New("missing exact disk description")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("disk readback did not return exact200 response")
	}
	sum := sha256.New()
	size, err := io.Copy(sum, io.LimitReader(response.Body, image.Disk.Size+1))
	if err != nil {
		return err
	}
	if size != image.Disk.Size || "sha256:"+hex.EncodeToString(sum.Sum(nil)) != image.Digest {
		return errors.New("HTTP disk readback differs from completed Run")
	}
	return nil
}
func copyPrivate(ctx context.Context, source, destination string, mode os.FileMode) error {
	input, info, err := securepath.OpenRegular(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = transportarchive.CopyExact(ctx, output, input, info.Size())
	// Keep error mapping with the existing callers, as the private copy did
	// before delegating transfer to the transport owner.
	var copied *nvkrerr.Error
	if errors.As(err, &copied) && copied.Err != nil {
		err = copied.Err
	}
	if err == nil {
		after, statErr := input.Stat()
		if statErr != nil || !securepath.SameMetadata(info, after) {
			err = errors.Join(statErr, errors.New("private copy source changed while reading"))
		}
	}
	if err == nil {
		err = output.Chmod(mode)
	}
	return errors.Join(err, output.Close())
}
func verifyDiskBytes(ctx context.Context, path string, image lifecycle.Artifact) error {
	file, info, err := securepath.OpenRegular(path)
	if err != nil {
		return err
	}
	_ = file.Close()
	if image.Disk == nil || info.Size() != image.Disk.Size {
		return errors.New("disk size differs from completed Run")
	}
	digest, err := DigestFile(ctx, path)
	if err != nil {
		return err
	}
	if digest != image.Digest {
		return errors.New("disk bytes differ from completed Run")
	}
	return nil
}

func allowedKeys(row map[string]json.RawMessage, keys ...string) bool {
	for key := range row {
		if !slices.Contains(keys, key) {
			return false
		}
	}
	return true
}
func rawString(row map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(row[key], &value)
	return value
}
func routeDestination(row map[string]json.RawMessage) (string, error) {
	raw, present := row["dst"]
	if !present {
		return "default", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", errors.New("invalid native route destination")
	}
	return value, nil
}
func routePrefix(destination string) (netip.Prefix, error) {
	if destination == "default" || destination == "" {
		return netip.Prefix{}, nil
	}
	prefix, err := netip.ParsePrefix(destination)
	if err == nil {
		return prefix.Masked(), nil
	}
	address, err := netip.ParseAddr(destination)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(address, address.BitLen()), nil
}
func routeFlags(row map[string]json.RawMessage) bool {
	var flags []string
	if raw, ok := row["flags"]; ok {
		if err := json.Unmarshal(raw, &flags); err != nil || strings.TrimSpace(string(raw)) == "null" {
			return false
		}
	}
	return len(flags) == 0 || slices.Equal(flags, []string{"linkdown"})
}
func selectBGPRoutes(rows []map[string]json.RawMessage, bridgePresent bool) ([]map[string]json.RawMessage, error) {
	var result []map[string]json.RawMessage
	for _, row := range rows {
		dst, destinationErr := routeDestination(row)
		if destinationErr != nil {
			return nil, destinationErr
		}
		prefix, err := routePrefix(dst)
		if err != nil {
			return nil, err
		}
		if !prefix.IsValid() || !prefix.Overlaps(netip.MustParsePrefix(VIPCIDR)) {
			continue
		}
		if dst == VIPCIDR && rawString(row, "type") == "blackhole" {
			var flags []string
			flagErr := error(nil)
			if raw, ok := row["flags"]; ok {
				flagErr = json.Unmarshal(raw, &flags)
				if strings.TrimSpace(string(raw)) == "null" {
					flagErr = errors.New("null route flags")
				}
			}
			if !allowedKeys(row, "dst", "type", "flags") || flagErr != nil || len(flags) != 0 {
				return nil, errors.New("covering BGP route changed; refusing removal")
			}
		} else if prefix == netip.MustParsePrefix(TenantVIP+"/32") {
			if !bridgePresent || !allowedKeys(row, "dst", "gateway", "dev", "flags", "nexthops") || !routeFlags(row) {
				return nil, errors.New("VIP route bridge/shape changed")
			}
			hops := []map[string]json.RawMessage{row}
			if raw, ok := row["nexthops"]; ok {
				if err = json.Unmarshal(raw, &hops); err != nil || len(hops) == 0 {
					return nil, errors.New("invalid VIP next hops")
				}
			}
			for _, hop := range hops {
				weight := 1
				if raw, ok := hop["weight"]; ok {
					if err = json.Unmarshal(raw, &weight); err != nil {
						return nil, err
					}
				}
				gateway, parseErr := netip.ParseAddr(rawString(hop, "gateway"))
				keys := []string{"dst", "gateway", "dev", "flags", "weight"}
				if reflect.DeepEqual(hop, row) {
					keys = append(keys, "nexthops")
				}
				if !allowedKeys(hop, keys...) || !routeFlags(hop) || weight != 1 || rawString(hop, "dev") != BridgeName || parseErr != nil || !netip.MustParsePrefix(GuestCIDR).Contains(gateway) {
					return nil, errors.New("VIP next-hop route changed; refusing removal")
				}
			}
		} else {
			return nil, errors.New("unknown overlapping BGP route; refusing removal")
		}
		result = append(result, row)
	}
	return result, nil
}
func restoreBGP(ctx context.Context, guard *NetworkGuard) error {
	guard.SetCleaning(true)
	if err := guard.RequireIdle(ctx); err != nil {
		return err
	}
	var rows []map[string]json.RawMessage
	if err := guard.inventory(ctx, []string{"ip", "-j", "route", "show", "table", "main"}, &rows); err != nil {
		return err
	}
	bridge, err := guard.BridgeBinding(ctx)
	if err != nil {
		return err
	}
	selected, err := selectBGPRoutes(rows, bridge != "")
	if err != nil {
		return err
	}
	for _, row := range selected {
		var current []map[string]json.RawMessage
		if err = guard.inventory(ctx, []string{"ip", "-j", "route", "show", "table", "main"}, &current); err != nil {
			return err
		}
		if !slices.ContainsFunc(current, func(candidate map[string]json.RawMessage) bool { return reflect.DeepEqual(row, candidate) }) {
			return errors.New("BGP route changed during restoration")
		}
		args := []string{"ip", "route", "delete"}
		if rawString(row, "type") == "blackhole" {
			args = append(args, "blackhole")
		}
		args = append(args, rawString(row, "dst"), "table", "main")
		if _, err = guard.Command(ctx, args, true); err != nil {
			return err
		}
	}
	var final []map[string]json.RawMessage
	if err = guard.inventory(ctx, []string{"ip", "-j", "route", "show", "table", "main"}, &final); err != nil {
		return err
	}
	for _, row := range final {
		destination, parseErr := routeDestination(row)
		if parseErr != nil {
			return parseErr
		}
		prefix, parseErr := routePrefix(destination)
		if parseErr != nil {
			return parseErr
		}
		if prefix.IsValid() && prefix.Overlaps(netip.MustParsePrefix(VIPCIDR)) {
			return errors.New("BGP routes remain")
		}
	}
	return nil
}

// Restore removes only recorded network controls after all producers are idle.
// It does not delete tenants, VMs or management resources.
func Restore(ctx context.Context, work string, progress io.Writer) error {
	runner := Runner{Work: work, Env: NativeEnvironment(), Progress: progress}
	binding, err := admitLibvirtEnvironment(ctx, runner)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "local network recovery")
	}
	runner.Env = SetEnv(runner.Env, binding)
	guard, err := RecoverNetworkGuard(ctx, runner)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "local network recovery")
	}
	defer guard.releaseHandles()
	if err = restoreNetwork(ctx, guard); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "local network recovery")
	}
	return nil
}
func restoreNetwork(ctx context.Context, guard *NetworkGuard) error {
	if guard.finished {
		return nil
	}
	err := guard.RequireIdle(ctx)
	if err == nil && guard.BGPIntent {
		err = restoreBGP(ctx, guard)
		if err == nil {
			guard.BGPIntent = false
			guard.cleanupCheckpoint(ctx)
		}
	}
	if err == nil {
		err = guard.Close(ctx)
	}
	return err
}
