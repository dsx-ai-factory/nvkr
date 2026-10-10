// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
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
	name := filepath.Base(path)
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
		if request.URL.Path != "/"+name || request.URL.RawQuery != "" || (request.Method != http.MethodGet && request.Method != http.MethodHead) {
			http.NotFound(writer, request)
			return
		}
		http.ServeContent(writer, request, name, info.ModTime(), io.NewSectionReader(file, 0, info.Size()))
	})
	service.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, WriteTimeout: 180 * time.Second, BaseContext: func(net.Listener) context.Context { return serveContext }}
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
	budget, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(budget, http.MethodGet, url, nil)
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
	input, _, err := securepath.OpenRegular(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	buffer := make([]byte, 1<<20)
	for {
		if err = ctx.Err(); err != nil {
			break
		}
		var n int
		n, err = input.Read(buffer)
		if n > 0 {
			written, writeErr := output.Write(buffer[:n])
			if writeErr != nil {
				err = writeErr
				break
			}
			if written != n {
				err = io.ErrShortWrite
				break
			}
		}
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			break
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

type demoAdmission struct {
	materials         *SiteMaterials
	simulator, image  lifecycle.Artifact
	disk, layout, out string
	env               []string
	binding           map[string]string
}

func admitDemo(ctx context.Context, cfg Config) (demoAdmission, error) {
	var admitted demoAdmission
	if cfg.Timeout < MinTenantTimeoutSeconds*time.Second || cfg.Timeout > MaxTenantTimeoutSeconds*time.Second {
		return admitted, fmt.Errorf("tenant budget must be %d..%d seconds", MinTenantTimeoutSeconds, MaxTenantTimeoutSeconds)
	}
	diskOutput, err := selectedDiskOutput(cfg.Network)
	if err != nil {
		return admitted, err
	}
	if err := admitSimulatorEndpoint(ctx, Runner{Env: NativeEnvironment(), Progress: cfg.Progress}); err != nil {
		return admitted, err
	}
	materials, err := AdmitSite(ctx, cfg)
	if err != nil {
		return admitted, err
	}
	env := NativeEnvironment()
	for _, tool := range []string{"qemu-img", "qemu-io", "qemu-system-aarch64", "cloud-localds", "swtpm_setup", "certtool", "debugfs"} {
		if _, err = commandPath(tool, env); err != nil {
			return admitted, err
		}
	}
	simulatorRoot, err := resolveExisting(cfg.Runs.Simulator)
	if err != nil {
		return admitted, err
	}
	osRoot, err := resolveExisting(cfg.Runs.OS)
	if err != nil {
		return admitted, err
	}
	out, err := freshDestination(cfg.Out)
	if err != nil {
		return admitted, err
	}
	for _, root := range []string{simulatorRoot, osRoot} {
		if insidePath(out, root) {
			return admitted, errors.New("demo output must be outside accepted attempts")
		}
	}
	simulator, err := CompleteOutput(ctx, simulatorRoot, SimulatorBundle, "executable")
	if err != nil {
		return admitted, err
	}
	image, err := CompleteOutput(ctx, osRoot, diskOutput, "disk-image")
	if err != nil {
		return admitted, err
	}
	if image.Disk == nil || image.Disk.Format != "qcow2" || image.Disk.Encryption != "none" || image.Disk.Outcome != "succeeded" || image.Disk.PayloadDigest != image.Digest || image.Disk.Size <= 0 {
		return admitted, errors.New("require successful observed plain QCOW2")
	}
	disk := filepath.Join(osRoot, "outputs", diskOutput+".qcow2")
	if err = verifyDiskBytes(ctx, disk, image); err != nil {
		return admitted, err
	}
	layout := filepath.Join(simulatorRoot, "packages", SimulatorBundle)
	if err = diagnosticIndexMatches(layout, simulator.Digest); err != nil {
		return admitted, err
	}
	parent, _, err := securepath.OpenDirectory(filepath.Dir(out))
	if err != nil {
		return admitted, err
	}
	free, err := securepath.AvailableDiskBackedBytes(ctx, parent)
	_ = parent.Close()
	if err != nil {
		return admitted, err
	}
	if free < 30<<30 || free-(30<<30) < uint64(image.Disk.Size) {
		return admitted, errors.New("need disk-copy bytes plus 30 GiB free for two-VM diagnostic")
	}
	runner := Runner{Env: env, Progress: cfg.Progress}
	binding, err := admitLibvirtEnvironment(ctx, runner)
	if err != nil {
		return admitted, err
	}
	if err = admitSimulatorPort(ctx, materials.Gateway); err != nil {
		return admitted, err
	}
	return demoAdmission{materials, simulator, image, disk, layout, out, env, binding}, nil
}

// Run deploys completed attempts through nvkr extraction and source-owned NVKE
// interfaces. Success is a local diagnostic, not Release authorization. Every
// daemon is joined before removing its shared networking controls.
func Run(ctx context.Context, cfg Config) (result error) {
	boundary := beginProgress(cfg.Progress, "local-demo", DeploymentStarted, DeploymentFinished, DeploymentFailed)
	defer func() { boundary.finish(result) }()
	mode, err := normalizeNetwork(cfg.Network)
	if err != nil {
		return err
	}
	cfg.Network = mode
	err = run(ctx, cfg)
	if err == nil {
		if cfg.Progress != nil && runtimeRetention(ctx) == nil {
			fmt.Fprintln(cfg.Progress, demoSuccessSummary(mode))
		}
		return nil
	}
	return localDemoError(err)
}

func localDemoError(err error) error {
	if err == nil {
		return nil
	}
	code := nvkrerr.CodePreflightFailed
	var coded *nvkrerr.Error
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &coded) && coded.Code == nvkrerr.CodeTimeout) {
		code = nvkrerr.CodeTimeout
	}
	return nvkrerr.Wrap(code, err, "local NVKE demo")
}

// This is emitted only after the deployment owner has joined cleanup. Naming
// the selected network prevents an alternate diagnostic from claiming the
// default NVIDIA OVN path was exercised.
func demoSuccessSummary(mode string) string {
	selection := "NVIDIA OVN"
	switch mode {
	case "upstream-ovn":
		selection = "upstream OVN diagnostic; NVIDIA OVN not validated"
	case "kind-calico":
		selection = "Kind/Calico diagnostic; NVIDIA OVN not deployed"
	}
	return "PASS (" + selection + "): real ARM64 QCOW2 tenant, two ready nodes, workload/DNS, exact artifacts and owned cleanup.\nUnqualified local diagnostic, not a signed Release or complete source-built distribution."
}

func run(ctx context.Context, cfg Config) (result error) {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if err := networkPlatform(); err != nil {
		return err
	}
	probeExecutable, err := admitNetworkProbeExecutable(ctx, cfg.DemoExecutable)
	if err != nil {
		return err
	}
	admitted, err := admitDemo(ctx, cfg)
	if err != nil {
		return err
	}
	// Each subprocess executes from its private workspace, so relative caller
	// locators must be resolved before any workspace is created.
	cfg.NVKR, err = resolveExisting(cfg.NVKR)
	if err != nil {
		return err
	}
	if err = os.Mkdir(admitted.out, 0o700); err != nil {
		return err
	}
	runner := Runner{Work: admitted.out, Env: SetEnv(admitted.env, admitted.binding), Progress: cfg.Progress}
	guard, err := NewNetworkGuard(ctx, runner, admitted.materials.KindCIDR, probeExecutable)
	if err != nil {
		return err
	}
	noteRetainedNetworkStart(ctx) // Original journal now owns a restore obligation.
	var simulator *OwnedProcess
	var httpService *diskService
	restore := true
	domainRunner := runner
	domainRunner.Work = ""
	domainRunner.Env = SetEnv(admitted.env, admitted.binding)
	privateDisk := ""
	defer func() {
		if holdErr := waitRetainedCleanup(ctx, result); holdErr != nil {
			result = errors.Join(result, holdErr)
			guard.releaseHandles()
			return
		}
		if errors.Is(result, ErrUnjoined) || errors.Is(result, ErrRetainedControls) {
			restore = false
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), retainedRuntimeCleanupTimeout)
		defer cancel()
		var cleanupErr error
		if runtimeRetention(ctx) != nil {
			if !restore {
				cleanupErr = errors.Join(ErrRetainedControls, result)
			} else {
				cleanupErr = cleanupRetainedRuntime(cleanup, simulator, httpService, guard, admitted.out, domainRunner)
			}
		} else {
			cleanupErr = cleanupRuntime(cleanup, simulator, httpService, guard, admitted.out, domainRunner, restore)
		}
		recordRetainedCleanup(ctx, cleanupErr)
		result = errors.Join(result, cleanupErr)
		guard.releaseHandles()
		if result == nil {
			result = errors.Join(verifyDiskBytes(cleanup, admitted.disk, admitted.image), verifyDiskBytes(cleanup, privateDisk, admitted.image))
			recordRetainedCleanup(ctx, result)
		}
	}()
	defer recoverRetainedPanic(ctx, &result)
	simulatorWork := filepath.Join(admitted.out, "simulator")
	if err = os.Mkdir(simulatorWork, 0o700); err != nil {
		return err
	}
	publication := filepath.Join(admitted.out, "sim-publication")
	if err = runner.Run(ctx, []string{cfg.NVKR, "publish", "root", "--bundle", SimulatorBundle + "=" + admitted.layout, "--out", publication}, "verify-simulator", 120*time.Second); err != nil {
		return err
	}
	bundle := filepath.Join(simulatorWork, "bundle")
	if err = runner.Run(ctx, []string{cfg.NVKR, "publish", "extract", "--layout", publication, "--bundle", SimulatorBundle, "--out", bundle}, "extract-simulator", 120*time.Second); err != nil {
		return err
	}
	if err = verifyToolMembers(bundle, []string{"nico-sim", "nico-sim-runtime-assets", "nico-simctl"}); err != nil {
		// Runtime assets are an archive, not an executable. Admit exact membership
		// separately without weakening executable member checks.
		rows, readErr := os.ReadDir(bundle)
		if readErr != nil {
			return readErr
		}
		var names []string
		for _, row := range rows {
			info, statErr := os.Lstat(filepath.Join(bundle, row.Name()))
			if statErr != nil {
				return statErr
			}
			if !info.Mode().IsRegular() {
				return errors.New("simulator bundle contains nonregular member")
			}
			names = append(names, row.Name())
		}
		if !slices.Equal(names, []string{"nico-sim", "nico-sim-runtime-assets", "nico-simctl"}) {
			return errors.New("require exactly three accepted simulator members")
		}
	}
	if err = SafeExtractAssets(ctx, filepath.Join(bundle, "nico-sim-runtime-assets"), simulatorWork); err != nil {
		return err
	}
	seed, err := projectSeed(ctx, simulatorWork)
	if err != nil {
		return err
	}
	binary := filepath.Join(admitted.out, "nico-sim")
	if err = copyPrivate(ctx, filepath.Join(bundle, "nico-sim"), binary, 0o700); err != nil {
		return err
	}
	serve := filepath.Join(admitted.out, "disk-http")
	if err = os.Mkdir(serve, 0o700); err != nil {
		return err
	}
	privateDisk = filepath.Join(serve, admitted.image.Name+".qcow2")
	if err = copyPrivate(ctx, admitted.disk, privateDisk, 0o444); err != nil {
		return err
	}
	if err = verifyDiskBytes(ctx, privateDisk, admitted.image); err != nil {
		return err
	}
	info, err := captureJSON[struct {
		Format  string
		Backing string `json:"backing-filename"`
		Size    int64  `json:"virtual-size"`
	}](ctx, runner, []string{"qemu-img", "info", "--output=json", privateDisk}, "inspect-base-disk")
	if err != nil {
		return err
	}
	if info.Format != "qcow2" || info.Backing != "" || info.Size <= 0 || info.Size > 60<<30 {
		return errors.New("require standalone QCOW2 fitting selected tenant instance")
	}
	if err = guard.Start(ctx); err != nil {
		return err
	}
	if err = guard.AllowKindSimulator(ctx, admitted.materials.Gateway); err != nil {
		return err
	}
	if err = guard.Grant(ctx, binary, "cap_net_admin=ep"); err != nil {
		return err
	}
	httpService, err = startDiskService(ctx, privateDisk, net.JoinHostPort(GuestGateway, "18080"))
	if err != nil {
		return err
	}
	if err = verifyHTTPDisk(ctx, httpService.url, admitted.image); err != nil {
		return err
	}
	var random [24]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(random[:])
	if err = privateWrite(filepath.Join(admitted.out, "nico-api-header"), []byte("Authorization: Bearer "+token+"\n")); err != nil {
		return err
	}
	binding, err := admitLibvirtEnvironment(ctx, runner)
	if err != nil {
		return err
	}
	if !maps.Equal(binding, admitted.binding) {
		return errors.New("libvirt path binding changed during setup")
	}
	serverEnv, err := simulatorEnvironment(ctx, simulatorWork, privateDisk, admitted.out, admitted.materials.Gateway, binding)
	if err != nil {
		return err
	}
	serverRunner := runner
	serverRunner.Work = simulatorWork
	serverRunner.Env = serverEnv
	guard.BGPIntent = true
	if err = guard.Checkpoint(ctx); err != nil {
		return err
	}
	simulator, err = serverRunner.Start(ctx, []string{binary}, "simulator")
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("http://%s:%d", admitted.materials.Gateway, simulatorAPIPort)
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	for {
		if exited, _ := simulator.Exited(); exited {
			cancel()
			return errors.New("real VM-backed NICoSim startup failed; private log retained")
		}
		var health struct{ Status string }
		err = localJSONRequest(startup, endpoint+"/healthz", 2*time.Second, &health)
		if err == nil && health.Status == "ok" {
			break
		}
		if err = pause(startup, 200*time.Millisecond); err != nil {
			cancel()
			return fmt.Errorf("simulator startup: %w", err)
		}
	}
	cancel()
	if err = privateJSON(filepath.Join(admitted.out, "demo-inputs.json"), struct {
		Scope                             string
		Network                           string
		Simulator, Disk                   lifecycle.Artifact
		HTTPReadback, WorkloadAcquisition string
		Seed                              seedProjection
		Outputs                           map[string]SelectedOutput
	}{"unqualified-local-demo", cfg.Network, admitted.simulator, admitted.image, admitted.image.Digest, WorkloadImage, seed, admitted.materials.Outputs}); err != nil {
		return err
	}
	cfg.Out = filepath.Join(admitted.out, "site")
	if err = WithManagement(ctx, cfg, func(ctx context.Context, session ManagementSession) error {
		return ConsumeSite(ctx, cfg, admitted.materials, SiteConnection{token, endpoint, OrgID}, session, func(ctx context.Context, session ManagementSession) error {
			return consumeTenant(ctx, cfg, guard, session, simulator, admitted.image, domainRunner, endpoint)
		})
	}); err != nil {
		return err
	}
	// The deferred cleanup and final rehash must finish before Run emits PASS.
	return nil
}

type stopper interface{ Stop(context.Context) error }

func joinRuntime(ctx context.Context, producers []stopper, stopDNS func(context.Context) error) error {
	var failures []error
	for _, producer := range producers {
		if producer != nil {
			if err := producer.Stop(ctx); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if err := stopDNS(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
func cleanupRuntime(ctx context.Context, server *OwnedProcess, httpService *diskService, guard *NetworkGuard, work string, domains Runner, restore bool) error {
	var producers []stopper
	if server != nil {
		producers = append(producers, server)
	}
	if httpService != nil {
		producers = append(producers, httpService)
	}
	if err := joinRuntime(ctx, producers, guard.StopDNS); err != nil {
		return fmt.Errorf("daemon cleanup failed; host controls retained: %w", err)
	}
	if !restore {
		return nil
	}
	if server != nil {
		if err := SweepOwnedDomains(ctx, domains, filepath.Join(work, "simulator")); err != nil {
			return err
		}
	}
	inventory, err := DomainInventory(ctx, domains)
	if err != nil {
		return err
	}
	if len(inventory) != 0 {
		return errors.New("unknown/remaining domain; network retained")
	}
	if guard.BGPIntent {
		if err = restoreBGP(ctx, guard); err != nil {
			return err
		}
		guard.BGPIntent = false
		guard.cleanupCheckpoint(ctx)
	}
	return guard.Close(ctx)
}

// The retained owner joins its actual producers and exact owned domains, but
// deliberately leaves recorded privileged host controls for foreground Restore.
// This is not full E2E cleanup and must never emit the full-cleanup PASS banner.
func cleanupRetainedRuntime(ctx context.Context, server *OwnedProcess, httpService *diskService, guard *NetworkGuard, work string, domains Runner) error {
	owner := runtimeRetention(ctx)
	if !owner.issued() || guard == nil {
		return nvkrerr.New(nvkrerr.CodeUsage, "retained service teardown requires its live owner and original network guard")
	}
	owner.mu.Lock()
	requested := owner.snapshot.state == retainedCleanupRequested
	owner.mu.Unlock()
	if !requested {
		return errors.Join(ErrRetainedControls, nvkrerr.New(nvkrerr.CodePreflightFailed, "service teardown requires explicit cleanup"))
	}
	var producers []stopper
	if server != nil {
		producers = append(producers, server)
	}
	if httpService != nil {
		producers = append(producers, httpService)
	}
	if err := joinRuntime(ctx, producers, guard.StopDNS); err != nil {
		return errors.Join(ErrRetainedControls, err)
	}
	if server != nil {
		if err := SweepOwnedDomains(ctx, domains, filepath.Join(work, "simulator")); err != nil {
			return errors.Join(ErrRetainedControls, err)
		}
	}
	inventory, err := DomainInventory(ctx, domains)
	if err != nil {
		return errors.Join(ErrRetainedControls, err)
	}
	if len(inventory) != 0 {
		return errors.Join(ErrRetainedControls, nvkrerr.New(nvkrerr.CodePreflightFailed, "remaining domains prevent retained owner completion"))
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
// It does not delete tenants, VMs or management resources and cannot import a
// legacy Python journal. Restore old controls before upgrading the helper.
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
