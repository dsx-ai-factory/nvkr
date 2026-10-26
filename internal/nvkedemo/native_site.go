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
	"maps"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

// NativeConfig selects one site and the requested native tenant topology.
// Cleanup false delegates lifetime to the retained deployment owner.
type NativeConfig struct {
	Resources               NativeResources
	TenantWorkers           int
	TenantTimeout           time.Duration
	Image, Out, Accelerator string
	Network, DemoExecutable string
	conformance             *nativeConformanceConfig
	Assets                  NativeAssetConfig
	Cleanup, Walkthrough    bool
	Progress                io.Writer
}

type nativeSiteAttempt struct {
	resources                 NativeResources
	tenantNodes               int
	capacity                  *nativeResourceAdmission
	use                       func(context.Context, *NativeSite) error
	unjoinedWork              error
	spireEnvironment          []byte
	tenantTimeout             time.Duration
	conformance               *nativeConformanceConfig
	assets                    NativeAssets
	handoff                   *nativeHandoff
	files                     string
	cleanup, walkthrough      bool
	sequence                  int
	bootstrapName             string
	bootstrapRunner           *Runner
	bootstrap                 *hostBootstrap
	network                   string
	api                       string
	bridge                    nativeBridgeConfig
	guard                     *NetworkGuard
	tenantOwner               *tenant
	diskDigest, simulatorWork string
	nodes                     []tenantNode
	workload                  json.RawMessage
	boot                      *nativeSiteBoot
}

// RunNative runs source-owned site bootstrap, deployment and tenant commands
// on the execution host, with one site and the selected tenant QCOW guests.
func RunNative(ctx context.Context, cfg NativeConfig) error {
	result := runNativeSite(ctx, cfg, runNativeDemo)
	if result == nil && cfg.Cleanup && cfg.Progress != nil {
		fmt.Fprintln(cfg.Progress, "PASS: native site and tenant checks, natural tenant deletion and joined owned cleanup completed.")
	}
	return result
}

func runNativeSite(ctx context.Context, cfg NativeConfig, use func(context.Context, *NativeSite) error) (result error) {
	defer func() {
		if result != nil {
			result = nvkrerr.Wrap(nvkrerr.CodePreflightFailed, result, "native NVKE demo")
		}
	}()
	if err := requireContext(ctx); err != nil {
		return err
	}
	if use == nil {
		return configUsage("native site requires a lifecycle callback")
	}
	tenantNodes, err := selectedNativeTenantNodes(cfg.TenantWorkers, cfg.conformance != nil)
	if err != nil {
		return err
	}
	if err := admitNativeHost(runtime.GOOS+"/"+runtime.GOARCH, cfg.Accelerator); err != nil {
		return err
	}
	if err := admitNativeQEMUAdapter(runtime.GOOS+"/"+runtime.GOARCH, cfg.Accelerator, cfg.DemoExecutable, true); err != nil {
		return err
	}
	if err := admitNativeSeedISO(ctx, runtime.GOOS, NativeEnvironment()); err != nil {
		return err
	}
	if cfg.conformance != nil && runtime.GOARCH != "arm64" {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "local conformance requires native ARM64")
	}
	if err := admitBootstrapHost(runtime.GOOS, NativeEnvironment()); err != nil {
		return err
	}
	if cfg.TenantTimeout != 0 && (cfg.TenantTimeout < MinTenantTimeoutSeconds*time.Second || cfg.TenantTimeout > MaxTenantTimeoutSeconds*time.Second) {
		return nvkrerr.New(nvkrerr.CodeUsage, "native tenant budget must be 300..7200 seconds")
	}
	mode, err := nativeDeploymentNetwork(cfg.Network)
	if err != nil {
		return err
	}
	if cfg.Image == "" || cfg.Assets.RuntimeRun == "" {
		return errors.New("require a local QCOW, explicit accelerator and completed runtime-image Run")
	}
	out, err := freshDestination(cfg.Out)
	if err != nil {
		return err
	}
	resources, err := selectedNativeResources(cfg.Resources)
	if err != nil {
		return err
	}
	capacity := &nativeResourceAdmission{resources: resources, tenantWorkers: cfg.TenantWorkers, conformance: cfg.conformance != nil}
	if err := capacity.check(ctx, filepath.Dir(out), false, cfg.Progress); err != nil {
		return err
	}
	if _, err := DigestFile(ctx, cfg.Image); err != nil {
		return err
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return err
	}
	cfg.Assets.Network = mode
	cfg.Assets.Out = filepath.Join(out, "assets")
	cfg.Assets.Progress = cfg.Progress
	preparation := beginProgress(cfg.Progress, "installer-preparation", PhaseStarted, PhaseFinished, PhaseFailed)
	assets, err := PrepareNativeAssets(ctx, cfg.Assets)
	preparation.finish(err)
	if err != nil {
		return err
	}
	reportProgress(cfg.Progress, ProgressEvent{Kind: NativeDeployment, ExpectedNodes: tenantNodes, Conformance: cfg.conformance != nil})
	attempt := &nativeSiteAttempt{use: use, assets: assets, cleanup: cfg.Cleanup, walkthrough: cfg.Walkthrough, network: mode, tenantTimeout: cfg.TenantTimeout, conformance: cfg.conformance, tenantNodes: tenantNodes, resources: resources, capacity: capacity}
	result = RunNativeGuest(ctx, NativeGuestConfig{
		Image: cfg.Image, Tools: assets.HostTools, Out: filepath.Join(out, "runtime"),
		Accelerator: cfg.Accelerator, Progress: cfg.Progress, site: attempt, DemoExecutable: cfg.DemoExecutable,
	})
	return result
}

func nativeSiteSeed(ctx context.Context, source, out string, resources NativeResources) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	resources, err := selectedNativeResources(resources)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	items, ok := doc["instance_types"].([]any)
	if !ok {
		return "", errors.New("source seed lacks instance types")
	}
	var site, tenant map[string]any
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			return "", errors.New("malformed instance type in source seed")
		}
		switch row["id"] {
		case "a1b2c3d4-0005-4000-8000-000000000004":
			if site != nil || row["name"] != "cpu6-mem32gb-disk60gb" || row["site_id"] != SiteID || row["gpu_count"] != 0 || row["cpu_count"] != 6 || row["memory_gib"] != 32 || row["disk_gib"] != 60 {
				return "", errors.New("unexpected source site instance type")
			}
			site = row
		case TenantInstanceType:
			if tenant != nil || row["name"] != "cpu4-mem6gb-disk60gb" || row["site_id"] != SiteID || row["gpu_count"] != 0 || row["cpu_count"] != 4 || row["memory_gib"] != 6 || row["disk_gib"] != 60 {
				return "", errors.New("unexpected source tenant instance type")
			}
			tenant = row
		}
	}
	if site == nil || tenant == nil {
		return "", errors.New("source seed omits selected site or tenant instance type")
	}
	// Change only runtime sizing after both selected source rows are admitted.
	// Source artifacts remain untouched, and tenant CNI requests remain intact.
	site["name"], site["memory_gib"] = fmt.Sprintf("native-site-cpu%d-mem%dgb-disk60gb", site["cpu_count"], resources.SiteMemoryGiB), resources.SiteMemoryGiB
	tenant["name"], tenant["cpu_count"], tenant["memory_gib"] = fmt.Sprintf("cpu%d-mem%dgb-disk60gb", tenantDemoVCPUs, resources.TenantMemoryGiB), tenantDemoVCPUs, resources.TenantMemoryGiB
	raw, err = yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	path := filepath.Join(out, "native-seed.yaml")
	return path, os.WriteFile(path, raw, 0600)
}

func (s *nativeSiteAttempt) share(ctx context.Context, files string) (result error) {
	if err := requireContext(ctx); err != nil {
		return err
	}
	s.files = files
	if err := bootstrapCA(ctx, ManagementSession{Work: filepath.Dir(files), Runner: Runner{Work: filepath.Dir(files), Env: NativeEnvironment()}}); err != nil {
		return err
	}
	// Both directories are freshly created below this attempt's private output
	// root. NICo and the later host consumers have not started reading them.
	source, sourceInfo, err := securepath.OpenDirectory(s.assets.Files)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, destinationInfo, err := securepath.OpenDirectory(files)
	if err != nil {
		return err
	}
	defer destination.Close()
	if sourceInfo.Mode().Perm() != 0o700 || !currentOwner(sourceInfo) || destinationInfo.Mode().Perm() != 0o700 || !currentOwner(destinationInfo) {
		return errors.New("native image sharing requires private owned directories")
	}
	defer func() { result = errors.Join(result, source.Sync(), destination.Sync()) }()
	for _, role := range slices.Sorted(maps.Keys(s.assets.Images)) {
		if role == "provisioned-provider" {
			continue
		}
		if err := requireContext(ctx); err != nil {
			return err
		}
		name := role + ".tar"
		image := s.assets.Images[role]
		if image.File != filepath.Join(s.assets.Files, name) {
			return errors.New("native image carrier differs from its private staging path")
		}
		carrier, info, err := securepath.OpenChild(source, name, false)
		if err != nil {
			return err
		}
		if err := carrier.Close(); err != nil {
			return err
		}
		if info.Mode().Perm() != 0o600 || !currentOwner(info) {
			return errors.New("native image sharing requires a private owned carrier")
		}
		if err := transportarchive.PublishAtNoReplace(int(source.Fd()), name, int(destination.Fd()), name); err != nil {
			return err
		}
		// Keep each named runtime path accurate even if a later move fails.
		// Files remains the staging root containing the host-only provider.
		image.File = filepath.Join(files, name)
		s.assets.Images[role] = image
	}
	inputs := s.sources()
	for _, name := range []string{"nerdctl", "spire-ca.crt"} {
		if err := copyPrivate(ctx, inputs[name], filepath.Join(files, name), 0600); err != nil {
			return err
		}
	}
	return nil
}

func (s *nativeSiteAttempt) sources() map[string]string {
	result := map[string]string{"nerdctl": filepath.Join(s.assets.SiteTools, "nerdctl"), "spire-ca.crt": filepath.Join(filepath.Dir(s.files), "ca.crt")}
	for role, image := range s.assets.Images {
		if role != "provisioned-provider" {
			result[role+".tar"] = image.File
		}
	}
	return result
}

func (s *nativeSiteAttempt) startup(ctx context.Context, api, diskURL, out string) ([]byte, error) {
	kubernetes, err := nativeKubernetesVersion(ctx, s.assets)
	if err != nil {
		return nil, err
	}
	port := "0"
	if s.guard != nil {
		port = "18081"
	}
	handoff, err := startNativeHandoffAt(ctx, s.bridge.gateway.String(), port, out)
	if err != nil {
		return nil, err
	}
	s.handoff = handoff
	version, _, _, err := selectedOVNOutputs(s.network)
	if err != nil {
		return nil, err
	}
	cfg := NativeSiteStartup{KubernetesVersion: kubernetes, OVNVersion: version, OVNImage: s.assets.Images["ovn"].Reference, Name: "nvke-native-site", Gateway: s.bridge.gateway.String(),
		ControllerImage: s.assets.Images["controller"].Reference, ProviderImage: s.assets.Images["provisioned-provider"].Reference,
		HandoffURL: handoff.URL, HandoffToken: handoff.Token, HandoffCertificate: handoff.Certificate,
		Deployment: &NativeSiteDeployment{NicoURL: api, ImageURL: diskURL, ProfilesImage: s.assets.Images["profiles"].Reference},
	}
	cfg.ImageImports = make(map[string]string)
	for role, image := range s.assets.Images {
		if role == "ovn" || role == "provisioned-provider" {
			continue
		}
		reference, err := nativeImageDigestReference(image)
		if err != nil {
			return nil, err
		}
		cfg.ImageImports[role] = reference
	}
	if err := s.prepareBoot(ctx, diskURL, api); err != nil {
		return nil, err
	}
	for name := range s.sources() {
		digest, err := DigestFile(ctx, filepath.Join(s.files, name))
		if err != nil {
			return nil, err
		}
		cfg.Assets = append(cfg.Assets, StartupAsset{Name: name, URL: api + "/files/" + name, Digest: digest})
	}
	s.spireEnvironment = nativeBootstrapSpireEnvironment(cfg)
	data, err := SiteStartupUserData(ctx, cfg)
	return []byte(data), err
}

func (s *nativeSiteAttempt) close(ctx context.Context) error {
	if errors.Is(s.unjoinedWork, ErrUnjoined) {
		return errors.Join(s.unjoinedWork, s.handoff.Stop(ctx))
	}
	cleanup := s.closeTenant(ctx)
	if errors.Is(cleanup, ErrUnjoined) {
		return errors.Join(cleanup, s.handoff.Stop(ctx))
	}
	cleanup = errors.Join(cleanup, s.stopBootstrap(ctx))
	return errors.Join(cleanup, s.handoff.Stop(ctx))
}

func (s *nativeSiteAttempt) closeTenant(ctx context.Context) error {
	if errors.Is(s.unjoinedWork, ErrUnjoined) {
		return s.unjoinedWork
	}
	if s.tenantOwner == nil {
		return nil
	}
	runner := s.tenantOwner.session.Runner
	if err := observeNativeAPI(ctx, runner, s.tenantOwner.cli()); errors.Is(err, ErrUnjoined) {
		return err
	}
	cleanup := s.tenantOwner.restore(ctx)
	if cleanup != nil && !errors.Is(cleanup, ErrUnjoined) {
		cleanup = retainNativeSiteFailure(ctx, runner, s.assets.HostTools, filepath.Join(s.tenantOwner.session.Work, "site.kubeconfig"), cleanup)
	}
	return cleanup
}

func (s *nativeSiteAttempt) stopBootstrap(ctx context.Context) error {
	if s.bootstrapRunner != nil {
		s.sequence++
		phase := fmt.Sprintf("native-site-%03d-host-bootstrap-cleanup", s.sequence)
		if err := s.bootstrapRunner.Run(ctx, []string{filepath.Join(s.assets.HostTools, "kind"), "delete", "cluster", "--name", s.bootstrapName}, phase, 2*time.Minute); err != nil {
			// The runtime still must stop and join even if Kind cleanup fails.
			stopErr := s.bootstrap.close(ctx)
			if stopErr == nil && s.bootstrap != nil && s.bootstrap.root == "" && s.bootstrap.process != nil {
				// Removing the private backend also removes its Kind containers.
				s.bootstrapRunner = nil
			}
			return errors.Join(err, stopErr)
		}
		s.bootstrapRunner = nil
	}
	return s.bootstrap.close(ctx)
}

func (s *nativeSiteAttempt) capture(ctx context.Context, runner Runner, phase string, args ...string) ([]byte, error) {
	s.sequence++
	return runner.Capture(ctx, args, fmt.Sprintf("native-site-%03d-%s", s.sequence, phase), 2*time.Minute)
}

// setup is the single source-owned site bootstrap/deployment path. It cannot
// create a tenant, publish native-result.json or claim full demo completion.
func (s *nativeSiteAttempt) setup(ctx context.Context, runner Runner, row nativeInstance, tools, out string, server *OwnedProcess) (configured Runner, result error) {
	complete := beginProgress(runner.Progress, "native-site-ready", PhaseStarted, PhaseFinished, PhaseFailed)
	defer func() { complete.finish(result) }()
	ip, err := nativePrimaryAddress(row, s.bridge)
	if err != nil {
		return Runner{}, err
	}
	stage := beginProgress(runner.Progress, "site-05-install-site-controller", PhaseStarted, PhaseFinished, PhaseFailed)
	prepared := beginProgress(runner.Progress, "native-site-preparation", PhaseStarted, PhaseFinished, PhaseFailed)
	err = waitNativeSiteHandoff(ctx, s.handoff, server)
	prepared.finish(err)
	if err == nil {
		err = s.bootstrapOnHost(ctx, runner, ip, tools, out)
	}
	stage.finish(err)
	if err != nil {
		return Runner{}, err
	}
	kubeconfig := filepath.Join(out, "site.kubeconfig")
	if err := admitNativeSiteContract(ctx, s.assets); err != nil {
		return Runner{}, err
	}
	if err := s.admitAPIClient(ctx, runner, tools, out, kubeconfig); err != nil {
		return Runner{}, err
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"XDG_CONFIG_HOME": filepath.Join(out, "config")})
	cli := s.assets.CLI
	if _, err := s.capture(ctx, runner, "configure", cli, "config", "set-site", "--name", "nvke-native-site", "--url", "http://"+ip+":30080", "--kubeconfig", kubeconfig); err != nil {
		return Runner{}, err
	}
	check := beginProgress(runner.Progress, "check-05-install-site-controller", PhaseStarted, PhaseFinished, PhaseFailed)
	_, err = s.capture(ctx, runner, "providers", cli, "get", "providers")
	check.finish(err)
	if err != nil {
		return Runner{}, err
	}

	return runner, nil
}

func (s *nativeSiteAttempt) ready(ctx context.Context, runner Runner, row nativeInstance, tools, out string, server *OwnedProcess) error {
	configured, err := s.setup(ctx, runner, row, tools, out, server)
	if err != nil {
		return err
	}
	runner = configured
	return withNativeSiteScope(ctx, s, runner, tools, out, s.use)
}

func runNativeDemo(ctx context.Context, site *NativeSite) error {
	scope := site.scope
	s, runner, tools, out := scope.attempt, scope.runner, scope.tools, scope.out
	kubeconfig := filepath.Join(out, "site.kubeconfig")
	tenant, err := site.createTenant(ctx, "native-demo-tenant", out)
	if err != nil {
		return err
	}
	access := OperatorAccess{kubectl: filepath.Join(tools, "kubectl"), site: kubeconfig, tenant: filepath.Join(out, "tenant.kubeconfig"), siteQCOW: true}
	if err := s.presentStatus(ctx, runner, access, s.walkthrough); err != nil {
		return err
	}
	if s.conformance != nil {
		if err := tenant.runConformance(ctx, out); err != nil {
			return err
		}
	}
	var domains []domainEvidence
	if s.guard != nil {
		domains, err = nativeLinuxVMEvidence(ctx, runner, s.simulatorWork, s.diskDigest, 1+s.tenantNodeCount())
		if err != nil {
			return err
		}
	}
	if err := privateJSON(filepath.Join(out, "native-result.json"), struct {
		Scope, Network, ImageDigest string
		Nodes                       []tenantNode
		Domains                     []domainEvidence
		Workload                    json.RawMessage
	}{"unqualified-local-demo", s.network, s.diskDigest, s.nodes, domains, s.workload}); err != nil {
		return err
	}
	if runner.Progress != nil {
		fmt.Fprintf(runner.Progress, "Tenant checks passed: %d Ready nodes and successful workload/DNS. Site kubeconfig: %s\n", s.tenantNodeCount(), kubeconfig)
	}
	if owner := runtimeRetention(ctx); owner != nil {
		if err := access.inspectFiles(); err != nil {
			return err
		}
		access, err = inspectOperatorAPIs(ctx, ManagementSession{Runner: runner}, access)
		if err != nil {
			return err
		}
		return owner.markReady(ctx, access)
	}
	return nil
}

// Health remains a timestamped read-only observation after tenant acceptance.
// An unhealthy response is displayed without tearing down the retained demo;
// output/admission failures, cancellation and unjoined children still propagate.
func presentNativeStatus(ctx context.Context, runner Runner, access OperatorAccess) error {
	if runner.Progress == nil {
		return nil
	}
	err := access.WriteLiveStatus(ctx, runner, runner.Progress)
	if errors.Is(err, errOperatorHealthUnconfirmed) && !errors.Is(err, ErrUnjoined) && ctx.Err() == nil {
		return nil
	}
	return err
}

// Observe the existing child owner without another goroutine or a second Wait.
// A lost runtime cannot be turned into a successful bootstrap callback.
func waitNativeSiteHandoff(ctx context.Context, handoff *nativeHandoff, server *OwnedProcess) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if exited, err := server.Exited(); exited {
		return errors.Join(errors.New("native NICo exited before site startup completed; inspect the private native-nico log"), err)
	}
	select {
	case <-server.done:
		_, err := server.Exited()
		return errors.Join(errors.New("native NICo exited before site startup completed; inspect the private native-nico log"), err)
	case err := <-handoff.result:
		if exited, exitErr := server.Exited(); exited {
			return errors.Join(err, exitErr, errors.New("native NICo exited during site startup"))
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *nativeSiteAttempt) prepareBoot(ctx context.Context, imageURL, api string) error {
	s.boot = nil
	if err := requireContext(ctx); err != nil {
		return err
	}
	path := filepath.Join(s.assets.Installer, "controller", "test", "fixtures", "releases", "nvke-nico-vms-24.04-arm64-fde-machineimage.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var image struct {
		Spec struct {
			NICo struct {
				Script string `yaml:"ipxeScript"`
			} `yaml:"nico"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(raw, &image); err != nil {
		return err
	}
	boot, err := projectNativeSiteBoot(ctx, image.Spec.NICo.Script, imageURL)
	if err != nil {
		return err
	}
	// Generic CAPI consumes this exact script in NicoMachineTemplate. It does
	// not consume TPM-only ClusterRelease/NodePoolRelease userdata templates.
	s.boot, s.api = boot, api
	return nil
}

// selectedNativeTenantNodes owns downstream topology selection independently of
// artifact selection. Conformance requires two schedulable workers.
func selectedNativeTenantNodes(workers int, conformance bool) (int, error) {
	if workers == 0 {
		workers = 1
		if conformance {
			workers = 2
		}
	}
	if workers != 1 && workers != 2 || conformance && workers != 2 {
		return 0, configUsage("tenantWorkers must be 1 or 2; conformance requires 2")
	}
	return 1 + workers, nil
}

// The existing bounded observers use the two supported default counts.
func nativeTenantNodeCount(conformance bool) int {
	nodes, _ := selectedNativeTenantNodes(0, conformance)
	return nodes
}

func (s *nativeSiteAttempt) tenantNodeCount() int {
	if s.tenantNodes != 0 {
		return s.tenantNodes
	}
	return nativeTenantNodeCount(s.conformance != nil)
}

// Native acceptance binds every requested Kubernetes node to a distinct VM boot.
// The legacy diagnostic owner has a separate observation contract.
func readyNativeTenantNodes(nodes []tenantNode, version string, expected int) (bool, error) {
	ready, err := readyNodesForCountVersion(nodes, version, expected)
	if err != nil || !ready {
		return ready, err
	}
	uids, boots := map[string]bool{}, map[string]bool{}
	for _, node := range nodes {
		uid, boot := node.Metadata.UID, node.Status.NodeInfo.BootID
		if !safeTenantIdentity(uid) || !safeTenantIdentity(boot) || uids[uid] || boots[boot] {
			return false, errors.New("require distinct native tenant node and boot identities")
		}
		uids[uid], boots[boot] = true, true
	}
	return true, nil
}

// NVKE owns request validation. Select its reviewed wire fields from the same
// source contract used for bootstrap and deployment on both native hosts.
func (s *nativeSiteAttempt) namedTenantRequest(ctx context.Context, name string) (TenantRequest, error) {
	mode, err := normalizeNetwork(s.network)
	if err != nil {
		return TenantRequest{}, err
	}
	version, err := nativeKubernetesVersion(ctx, s.assets)
	if err != nil {
		return TenantRequest{}, err
	}
	request := TenantRequest{
		Name: name, Kubernetes: version, CNI: "ovn-kubernetes", CNIVersion: siteOVNVersion,
		ControlPlane: ControlPlaneIntent{Count: 1, Reserved: "1Gi", DisableMonitor: true},
		NodePools: []NodePoolIntent{{Name: "worker", Prefix: "w", Count: s.tenantNodeCount() - 1,
			SystemReserved: "1Gi", KubeReserved: "1Gi", DisableAuto: true, DisableMonitor: true}},
	}
	switch mode {
	case "ovn":
	case "upstream-ovn":
		request.CNIVersion = upstreamSiteOVNVersion
	default:
		return TenantRequest{}, nvkrerr.New(nvkrerr.CodeEngineCapability, "native tenant requires an OVN network selection")
	}

	// The source default pod range contains the Mac vmnet underlay. Keep both
	// tenant ranges separate from that underlay and the site Kubernetes ranges.
	request.Network = &TenantNetwork{PodCIDR: "10.245.0.0/16", ServiceCIDR: "10.97.0.0/16"}
	if !s.bridge.prefix.IsValid() {
		return TenantRequest{}, nvkrerr.New(nvkrerr.CodeEngineCapability, "tenant requires an observed guest subnet")
	}
	for _, network := range []string{request.Network.PodCIDR, request.Network.ServiceCIDR} {
		if s.bridge.prefix.Overlaps(netip.MustParsePrefix(network)) {
			return TenantRequest{}, nvkrerr.New(nvkrerr.CodeEngineCapability, "tenant Kubernetes range %s overlaps guest subnet %s", network, s.bridge.prefix)
		}
	}
	timeout := s.tenantTimeout
	if timeout == 0 {
		timeout = 45 * time.Minute
	}
	preparation, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.capiTenantRequest(preparation, request)
}

func (s *nativeSiteAttempt) runTenantRequest(ctx context.Context, runner Runner, cli, tools, out, siteConfig string, request TenantRequest) error {
	ownership, err := newNativeCAPITenantOwnership(request)
	if err != nil {
		return err
	}
	if s.capacity != nil {
		// The site is already allocated. Re-observe only the additional tenant
		// demand and headroom before any create request, including site reuse.
		if err := s.capacity.check(ctx, out, true, runner.Progress); err != nil {
			return err
		}
	}
	path := filepath.Join(out, "tenant-request.json")
	if err := privateJSON(path, request); err != nil {
		return err
	}
	s.tenantOwner = &tenant{name: request.Name, cliPath: cli, session: ManagementSession{Work: out, Tools: tools, Runner: runner}, nativeCAPI: ownership}
	rows, err := s.tenantOwner.inventory(ctx)
	if err != nil {
		return err
	}
	if row, err := selectedTenantIdentity(rows, request.Name, ""); err != nil || row != nil {
		return errors.Join(err, errors.New("native tenant name already exists"))
	}
	s.tenantOwner.intent = true
	if _, err := s.tenantOwner.capture(ctx, []string{cli, "create", "cluster", "-f", path}, "tenant-create", 2*time.Minute); err != nil {
		return err
	}
	row, err := s.tenantOwner.row(ctx)
	if err != nil {
		return err
	}
	if row == nil {
		return errors.New("NVKE omitted the exact created native tenant")
	}
	s.tenantOwner.id = row.ID
	timeout := s.tenantTimeout
	if timeout == 0 {
		timeout = 45 * time.Minute
	}
	budget, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := budget.Deadline()
	progress := tenantProgress{writer: runner.Progress, started: time.Now(), deadline: deadline, expectedNodes: s.tenantNodeCount()}
	refresh := func(stage string, nodes []tenantNode) error {
		var fresh *tenantRow
		_, err := nativeReadinessObservation(budget, runner.Progress, func(ctx context.Context) ([]byte, error) {
			fresh = nil
			row, err := s.tenantOwner.row(ctx)
			if err == nil {
				fresh = row
			}
			return nil, err
		})
		if err != nil {
			return err
		}
		if fresh == nil {
			return errors.New("NVKE omitted the exact owned native tenant; refusing further readiness checks")
		}
		progress.conditions = fresh.Conditions
		progress.update(time.Now(), stage, nodes)
		// NVKE v0.22.0 clusterstate.IsProvisioningFailed uses exactly these
		// two statuses. Missing or Unknown conditions do not prove failure.
		if fresh.Conditions.Provisioning.Status == TenantConditionFalse && fresh.Conditions.Provisioned.Status == TenantConditionFalse {
			return errors.New("NVKE reported native tenant provisioning failed")
		}
		return nil
	}
	tenantConfig := filepath.Join(out, "tenant.kubeconfig")
	kubectl := filepath.Join(tools, "kubectl")
	observer := nativeTenantObserver{}
	for {
		if err := refresh("tenant-kubeconfig", nil); err != nil {
			return err
		}
		if err := observer.update(budget, s, runner, tools, siteConfig, time.Now()); err != nil {
			return err
		}
		raw, err := nativeReadinessObservation(budget, runner.Progress, func(ctx context.Context) ([]byte, error) {
			return s.tenantOwner.capture(ctx, []string{kubectl, "--kubeconfig", siteConfig, "-n", "nvke-system", "get", "secret", request.Name + "-admin-kubeconfig", "--ignore-not-found", "-o", "json"}, "tenant-config-secret", 2*time.Minute)
		})
		if err != nil {
			return err
		}
		published, err := tenantKubeconfigPublished(raw)
		if err != nil {
			return err
		}
		if published {
			if _, err := s.tenantOwner.capture(budget, []string{cli, "get", "kubeconfig", request.Name, "--output-file", tenantConfig}, "tenant-config", 2*time.Minute); err != nil {
				return err
			}
			break
		}
		if err := pause(budget, 10*time.Second); err != nil {
			return err
		}
	}
	var observed []tenantNode
	progress.update(time.Now(), "tenant-api", nil)
	for {
		// Controller conditions do not establish a successful tenant API query.
		if err := refresh(progress.previous.Name, observed); err != nil {
			return err
		}
		if err := observer.update(budget, s, runner, tools, siteConfig, time.Now()); err != nil {
			return err
		}
		raw, err := s.tenantOwner.capture(budget, []string{kubectl, "--kubeconfig", tenantConfig, "--request-timeout=10s", "get", "nodes", "-o", "json"}, "tenant-nodes", 2*time.Minute)
		if err == nil {
			var nodes struct{ Items []tenantNode }
			if err := json.Unmarshal(raw, &nodes); err != nil {
				return err
			}
			observed = nodes.Items
			ready, err := readyNativeTenantNodes(nodes.Items, request.Kubernetes, s.tenantNodeCount())
			if err != nil {
				return err
			}
			progress.update(time.Now(), "tenant-nodes", observed)
			if ready {
				break
			}
		} else if CommandExitCode(err) != 1 {
			return err
		}
		if err := pause(budget, 10*time.Second); err != nil {
			return err
		}
	}
	capture := s.tenantOwner.capture
	workloadImage, err := nativeImageDigestReference(s.assets.Images["workload"])
	if err != nil {
		return err
	}
	progress.update(time.Now(), "tenant-cloud-init", observed)
	if err := runNativeCloudInit(budget, out, tenantConfig, kubectl, request.Kubernetes, workloadImage, s.tenantNodeCount(), observed, capture); err != nil {
		return err
	}
	if err := waitTenantDNS(budget, tenantConfig, kubectl, runner.Progress, capture); err != nil {
		return err
	}
	progress.update(time.Now(), "tenant-workload", observed)
	s.nodes = observed
	s.workload, err = runTenantWorkload(budget, out, tenantConfig, kubectl, workloadImage, capture)
	return err
}

// The source-owned controller chart admits API ingress from cluster pods.
// Add the documented operator rule for this demo's observed gateway only.
func (s *nativeSiteAttempt) admitAPIClient(ctx context.Context, runner Runner, tools, out, kubeconfig string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	gateway := s.bridge.gateway
	if !gateway.Is4() || !gateway.IsPrivate() || !s.bridge.prefix.Contains(gateway) {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "API client admission requires the observed private IPv4 demo gateway")
	}
	raw, err := s.capture(ctx, runner, "api-client-controller", filepath.Join(tools, "kubectl"), "--kubeconfig", kubeconfig,
		"-n", "nke-controller", "get", "deployments", "-l", "app.kubernetes.io/instance=nke-controller", "-o", "json")
	if err != nil {
		return err
	}
	var observed struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Selector json.RawMessage `json:"selector"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &observed); err != nil {
		return err
	}
	if len(observed.Items) != 1 || observed.Items[0].Metadata.Namespace != "nke-controller" {
		return errors.New("API client admission requires exactly one controller Deployment in its namespace")
	}
	selector := observed.Items[0].Spec.Selector
	var selected struct {
		MatchLabels map[string]string `json:"matchLabels"`
	}
	if err := json.Unmarshal(selector, &selected); err != nil {
		return err
	}
	if selected.MatchLabels["app.kubernetes.io/instance"] != "nke-controller" {
		return errors.New("API client admission requires the exact controller release selector")
	}
	// Preserve the server's complete selector. Kubernetes, through kubectl apply,
	// owns LabelSelector validation; this consumer does not parse its grammar.
	policy := map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]string{"name": "nvke-demo-api-client", "namespace": "nke-controller"},
		"spec": map[string]any{"podSelector": selector, "policyTypes": []string{"Ingress"},
			"ingress": []any{map[string]any{"from": []any{map[string]any{"ipBlock": map[string]string{"cidr": netip.PrefixFrom(gateway, 32).String()}}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": 8080}}}}}}
	path := filepath.Join(out, "nvke-api-client.json")
	if err := privateJSON(path, policy); err != nil {
		return err
	}
	_, err = s.capture(ctx, runner, "api-client-admission", filepath.Join(tools, "kubectl"), "--kubeconfig", kubeconfig, "apply", "--server-side", "-f", path)
	return err
}

// This selection is issued only after the actual pinned MachineImage fixture is
// admitted. NVKE's source-owned template engine renders the disk policy; NICoSim
// still owns iPXE parsing and booting the selected local QCOW.
type nativeSiteBoot struct{ script, diskURL string }

// The admitted iPXE selection makes NICo record a confirmed DHCP lease instead
// of the standalone guest's static address pair. Both readiness consumers must
// use that instance address mode without changing the owned bridge's bounds.
func (s *nativeSiteAttempt) selectBootNetwork(network nativeBridgeConfig, diskURL string) (nativeBridgeConfig, error) {
	if s.boot == nil || s.boot.script == "" || diskURL == "" || s.boot.diskURL != diskURL {
		return nativeBridgeConfig{}, errors.New("native site create lacks its current projected MachineImage boot selection")
	}
	network.dhcp = true
	s.bridge = network
	return network, nil
}

func projectNativeSiteBoot(ctx context.Context, source, diskURL string) (*nativeSiteBoot, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if strings.Count(source, "http://127.0.0.1/nvke-node.qcow2") != 1 {
		return nil, errors.New("source MachineImage must expose one local QCOW URL slot")
	}
	location, err := url.Parse(diskURL)
	if err != nil || location.Scheme != "http" || location.Hostname() == "" || location.Port() == "" || location.User != nil || location.RawQuery != "" || location.Fragment != "" {
		return nil, errors.New("native site boot requires the selected local disk URL")
	}
	address, err := netip.ParseAddr(location.Hostname())
	if err != nil || !address.Is4() || !address.IsPrivate() {
		return nil, errors.New("native site boot requires the owned private disk endpoint")
	}
	projected := strings.Replace(source, "http://127.0.0.1/nvke-node.qcow2", diskURL, 1)
	parsed, err := template.New("source-machine-image").Option("missingkey=error").Parse(projected)
	if err != nil {
		return nil, err
	}
	var rendered bytes.Buffer
	if err := parsed.Execute(&rendered, struct{ DiskSelectionStrategy string }{"smallest"}); err != nil {
		return nil, err
	}
	return &nativeSiteBoot{rendered.String(), diskURL}, nil
}
