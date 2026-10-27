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
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/qemutpm"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

// NativeGuestConfig selects a local disk and already extracted native tools.
// This diagnostic verifies guest boot, HTTP delivery and deletion, not Kubernetes.
type NativeGuestConfig struct {
	Image, Tools, Out, Accelerator string
	DemoExecutable                 string
	Timeout                        time.Duration
	Progress                       io.Writer
	// site is issued only by the native site workflow, never by guest flags.
	site *nativeSiteAttempt
}

type nativeInstance struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Interfaces []struct {
		IPAddresses []string `json:"ipAddresses"`
	} `json:"interfaces"`
}

func nativeSimulatorEnvironment(ctx context.Context, cfg NativeGuestConfig, network nativeBridgeConfig,
	image, seedPath, simdir string,
) (map[string]string, error) {
	readinessTimeout := cfg.Timeout
	if cfg.site != nil {
		var err error
		readinessTimeout, err = remainingWork(ctx)
		if err != nil {
			return nil, err
		}
	}
	return map[string]string{
		"NICO_SIM_LISTEN_ADDR": net.JoinHostPort(network.gateway.String(), "18080"), "NICO_SIM_DB_PATH": filepath.Join(simdir, "nico.db"),
		"NICO_SIM_SEED_PATH": seedPath, "NICO_SIM_FILES_DIR": filepath.Join(simdir, "files"),
		"NICO_VMS_VM_BACKEND": "darwin-qemu", "NICO_VMS_BRIDGE_CIDR": network.prefix.String(),
		"NICO_VMS_BASE_IMAGE": image, "NICO_VMS_QEMU_ACCELERATOR": cfg.Accelerator,
		"NICO_VMS_BGP_ENABLED": "false", "NICO_CONTAINERS_ENABLED": "false",
		"NICO_VMS_DIAG_SSH_ENABLED": "false", "NICO_VMS_CLEANUP_ORPHANED_VMS": "false",
		"NICO_VMS_CONSOLE_SSH_PORT":   "-1",
		"NICO_VMS_IMAGE_CACHE_DIR":    filepath.Join(simdir, "cache"),
		"NICO_VMS_RECONCILE_INTERVAL": "1s", "NICO_VMS_READINESS_PROBE_INTERVAL": "1s",
		"NICO_VMS_READINESS_TIMEOUT": readinessTimeout.String(),
	}, nil
}

// RunNativeGuest starts one 2 GiB guest through NICo using the local Go disk
// server. All instance deletion and child/server joining precedes return.
func RunNativeGuest(ctx context.Context, cfg NativeGuestConfig) (result error) {
	defer func() {
		if result != nil {
			result = nvkrerr.Wrap(nvkrerr.CodePreflightFailed, result, "native guest launch")
		}
	}()
	if err := requireContext(ctx); err != nil {
		return err
	}
	if err := admitNativeHost(runtime.GOOS+"/"+runtime.GOARCH, cfg.Accelerator); err != nil {
		return err
	}
	if err := admitNativeQEMUAdapter(runtime.GOOS+"/"+runtime.GOARCH, cfg.Accelerator, cfg.DemoExecutable, cfg.site != nil); err != nil {
		return err
	}
	if err := admitNativeSeedISO(ctx, runtime.GOOS, NativeEnvironment()); err != nil {
		return err
	}
	if err := admitNativeAddresses(ctx, NativeEnvironment()); err != nil {
		return err
	}
	ctx, failAddresses := context.WithCancelCause(ctx)
	defer failAddresses(nil)
	if cfg.site == nil && (cfg.Timeout < time.Minute || cfg.Timeout > 30*time.Minute) {
		return errors.New("guest timeout must be 1..30 minutes")
	}
	image, err := filepath.Abs(cfg.Image)
	if err != nil || cfg.Image == "" {
		return errors.New("require a local QCOW path")
	}
	digest, err := DigestFile(ctx, image)
	if err != nil {
		return err
	}
	tools, err := filepath.Abs(cfg.Tools)
	if err != nil || cfg.Tools == "" {
		return errors.New("require extracted native tools")
	}
	_, _, _, members, err := nativeToolSelection(runtime.GOOS + "/" + runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := verifyToolMembers(tools, members); err != nil {
		return err
	}
	if err := admitNativeVMNet(ctx, runtime.GOOS, tools); err != nil {
		return err
	}
	out, err := freshDestination(cfg.Out)
	if err != nil {
		return err
	}
	if err := os.Mkdir(out, 0o700); err != nil {
		return err
	}
	if cfg.site != nil {
		if err := prepareNativeSimulatorUI(ctx, cfg.site.assets.Installer, out); err != nil {
			return err
		}
	}
	runner := Runner{Work: out, Env: NativeEnvironment(), Progress: cfg.Progress}
	runner.Env = SetEnv(runner.Env, map[string]string{"PATH": tools + string(os.PathListSeparator) + environmentValue(runner.Env, "PATH")})
	var tpm *qemutpm.Adapter
	if runtime.GOOS == "darwin" && (cfg.site != nil || cfg.Accelerator == "tcg") {
		qemu, qemuErr := commandPath("qemu-system-aarch64", runner.Env)
		swtpm, tpmErr := commandPath("swtpm", runner.Env)
		if err := errors.Join(qemuErr, tpmErr); err != nil {
			return err
		}
		tpm, err = qemutpm.Prepare(ctx, qemutpm.Options{Executable: cfg.DemoExecutable, Directory: filepath.Join(out, "native-qemu"), Instances: filepath.Join(out, "simulator", "instances"), QEMU: qemu, SWTPM: swtpm, Environment: runner.Env, Accelerator: qemutpm.Accelerator(cfg.Accelerator)})
		if err != nil {
			return err
		}
		runner.Env = SetEnv(runner.Env, tpm.Environment())
	}
	bridge, err := startNativeBridge(ctx, runner, runtime.GOOS, tools)
	if err != nil {
		return err
	}
	defer func() {
		if holdErr := waitRetainedCleanup(ctx, result); holdErr != nil {
			result = errors.Join(result, holdErr)
			return
		}
		if runtimeRetention(ctx) != nil && (errors.Is(result, ErrUnjoined) || errors.Is(result, ErrRetainedControls)) {
			recordRetainedCleanup(ctx, result)
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if tpm != nil {
			if err := tpm.Close(cleanup); err != nil {
				recordRetainedCleanup(ctx, errors.Join(ErrUnjoined, err))
				result = errors.Join(result, ErrUnjoined, err)
				return
			}
		}
		cleanupErr := bridge.Stop(cleanup)
		recordRetainedCleanup(ctx, cleanupErr)
		result = errors.Join(result, cleanupErr)
	}()
	defer recoverRetainedPanic(ctx, &result)
	network := bridge.config
	if cfg.site != nil {
		cfg.site.bridge = network
	}
	raw, err := runner.Capture(ctx, []string{"qemu-img", "info", "--output=json", image}, "native-local-disk", time.Minute)
	if err != nil {
		return err
	}
	var info struct {
		Format      string `json:"format"`
		Backing     string `json:"backing-filename"`
		VirtualSize int64  `json:"virtual-size"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return err
	}
	if info.Format != "qcow2" || info.Backing != "" || info.VirtualSize <= 0 {
		return errors.New("require a standalone valid local QCOW2 disk")
	}
	if cfg.Progress != nil {
		fmt.Fprintf(cfg.Progress, "Local QCOW verified: %s\n", digest)
	}
	seedPath := filepath.Join(tools, "..", "installer", "controller", "nico-sim", "internal", "seed", "default-seed.yaml")
	if _, err := os.Stat(seedPath); err != nil {
		return fmt.Errorf("require the extracted source-owned NICo seed: %w", err)
	}
	if cfg.site != nil {
		seedPath, err = nativeSiteSeed(ctx, seedPath, out, cfg.site.resources)
		if err != nil {
			return err
		}
	}
	simdir := filepath.Join(out, "simulator")
	if err := os.Mkdir(simdir, 0700); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(simdir, "files"), 0700); err != nil {
		return err
	}
	if cfg.site != nil {
		cfg.site.diskDigest, cfg.site.simulatorWork = digest, simdir
		if err := cfg.site.share(ctx, filepath.Join(simdir, "files")); err != nil {
			return err
		}
	}
	// This private server starts from an empty database. Cleanup owns only its
	// instances; it never adopts a shared NICo server or scans unrelated VMs.
	nicoEnvironment, err := nativeSimulatorEnvironment(ctx, cfg, network, image, seedPath, simdir)
	if err != nil {
		return err
	}
	runner.Env = SetEnv(runner.Env, nicoEnvironment)
	if err := configureNativeAddresses(&runner, simdir); err != nil {
		return err
	}
	binary := filepath.Join(tools, "nico-sim")
	var guard *NetworkGuard
	if runtime.GOOS == "linux" {
		cfg.Tools = tools
		guard, binary, err = prepareNativeLinux(ctx, &runner, cfg, simdir)
		if err != nil {
			return err
		}
		if cfg.site != nil {
			cfg.site.guard = guard
		}
	}
	if guard != nil {
		defer func() {
			if holdErr := waitRetainedCleanup(ctx, result); holdErr != nil {
				result = errors.Join(result, holdErr)
				guard.releaseHandles()
				return
			}
			if errors.Is(result, ErrUnjoined) || errors.Is(result, ErrRetainedControls) {
				recordRetainedCleanup(ctx, result)
				guard.releaseHandles()
				return
			}
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Minute)
			defer cancel()
			if err := guard.StopDNS(cleanup); err != nil {
				recordRetainedCleanup(ctx, err)
				result = errors.Join(result, err)
				guard.releaseHandles()
				return
			}
			domainRunner := runner
			domainRunner.Work = ""
			if err := SweepOwnedDomains(cleanup, domainRunner, simdir); err != nil {
				recordRetainedCleanup(ctx, err)
				result = errors.Join(result, err)
				guard.releaseHandles()
				return
			}
			if runtimeRetention(ctx) != nil {
				// The explicit foreground cleanup restores this original journal
				// after the independent owner and its producers have exited.
				guard.releaseHandles()
				return
			}
			result = errors.Join(result, guard.Close(cleanup))
		}()
		defer recoverRetainedPanic(ctx, &result)
	}
	server, err := runner.Start(ctx, []string{binary}, "native-nico")
	if err != nil {
		return err
	}
	var disk *diskService
	var instanceID string
	sequence := 0
	api := "http://" + net.JoinHostPort(network.gateway.String(), "18080")
	call := func(ctx context.Context, phase string, args ...string) ([]byte, error) {
		sequence++
		args = append([]string{filepath.Join(tools, "nico-simctl")}, args...)
		args = append(args, "--base-url", api)
		// The source CLI owns a 30-second HTTP deadline. Leave it time to report
		// the transport error before the subprocess owner's outer deadline.
		return runner.Capture(ctx, args, "native-"+strconv.Itoa(sequence)+"-"+phase, 40*time.Second)
	}
	defer func() {
		if holdErr := waitRetainedCleanup(ctx, result); holdErr != nil {
			result = errors.Join(result, holdErr)
			return
		}
		if runtimeRetention(ctx) != nil && errors.Is(result, ErrRetainedControls) {
			recordRetainedCleanup(ctx, result)
			return
		}
		boundary := beginProgress(cfg.Progress, "owned-node-cleanup", PhaseStarted, PhaseFinished, PhaseFailed)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), tenantCleanupTimeout+5*time.Minute)
		defer cancel()
		var cleanupResult error
		defer func() {
			recordRetainedCleanup(ctx, cleanupResult)
			result = errors.Join(result, cleanupResult)
			boundary.finish(cleanupResult)
		}()
		if cfg.site != nil {
			// Preserve the failed site before Kind cleanup or NICo fallback
			// removes it, even when deployment never created a tenant owner.
			result = cfg.site.preserveFailure(cleanup, result)
			if errors.Is(result, ErrUnjoined) {
				cleanupResult = errors.Join(cleanupResult, cfg.site.handoff.Stop(cleanup))
			} else {
				cleanupResult = errors.Join(cleanupResult, cfg.site.close(cleanup))
			}
		}
		deleted := map[string]bool{}
		// A create response can be lost on cancellation. Enumerate the private
		// fresh database so a successful but unacknowledged create is deleted too.
		for !errors.Is(result, ErrUnjoined) && !errors.Is(cleanupResult, ErrUnjoined) {
			data, listErr := call(cleanup, "cleanup-inventory", "instance", "list")
			var rows []nativeInstance
			if listErr != nil {
				cleanupResult = errors.Join(cleanupResult, listErr)
				break
			}
			if err := json.Unmarshal(data, &rows); err != nil {
				cleanupResult = errors.Join(cleanupResult, err)
				break
			}
			if len(rows) == 0 {
				break
			}
			inventoryLimit := nativeGuestLimit(cfg.site)
			if cfg.site == nil {
				inventoryLimit = 3
			}
			if len(rows) > inventoryLimit {
				cleanupResult = errors.Join(cleanupResult, errors.New("private native fixture exceeds its requested guest count"))
				break
			}
			owned, rejected := nativeCleanupRows(rows, instanceID, nativeGuestLimit(cfg.site))
			cleanupResult = errors.Join(cleanupResult, rejected)
			for _, row := range owned {
				if deleted[row.ID] {
					continue
				}
				if safePhase(row.ID) {
					consoleRoot := "instances"
					if guard != nil {
						consoleRoot = "var/instances"
					}
					cleanupResult = errors.Join(cleanupResult, retainNativeInstanceFiles(cleanup, simdir, consoleRoot, out, row.ID))
				}
				_, deleteErr := call(cleanup, "delete", "instance", "delete", "--instance-id", row.ID)
				cleanupResult = errors.Join(cleanupResult, deleteErr)
				if deleteErr == nil {
					deleted[row.ID] = true
				}
			}
			// Finish owned deletions in this pass, then stop on foreign rows.
			// Their rejection cannot be repaired by polling the same inventory.
			if rejected != nil {
				break
			}
			if err := pause(cleanup, time.Second); err != nil {
				cleanupResult = errors.Join(cleanupResult, err)
				break
			}
		}
		cleanupResult = errors.Join(cleanupResult, server.Stop(cleanup))
		if disk != nil {
			cleanupResult = errors.Join(cleanupResult, disk.Stop(cleanup))
		}
	}()
	defer recoverRetainedPanic(ctx, &result)
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if exited, _ := server.Exited(); exited {
			return errors.New("native NICo exited; inspect private native-nico log")
		}
		var health struct{ Status string }
		if localJSONRequest(startup, api+"/healthz", 2*time.Second, &health) == nil && health.Status == "ok" {
			break
		}
		if err := pause(startup, 200*time.Millisecond); err != nil {
			return err
		}
	}
	addressLifetime := ctx
	if owner := runtimeRetention(ctx); owner != nil {
		addressLifetime = owner.lifetime
	}
	addresses, err := startNativeAddresses(addressLifetime, runner, simdir, network, nativeGuestLimit(cfg.site), failAddresses)
	if err != nil {
		return err
	}
	defer func() {
		if holdErr := waitRetainedCleanup(ctx, result); holdErr != nil {
			result = errors.Join(result, holdErr)
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanupErr := addresses.Stop(cleanup)
		recordRetainedCleanup(ctx, cleanupErr)
		result = errors.Join(result, cleanupErr)
	}()
	defer recoverRetainedPanic(ctx, &result)
	diskPort := "0"
	if guard != nil {
		diskPort = "18082"
	}
	disk, err = startDiskService(ctx, image, net.JoinHostPort(network.gateway.String(), diskPort))
	if err != nil {
		return err
	}
	if cfg.Progress != nil {
		fmt.Fprintf(cfg.Progress, "Demo-owned local disk HTTP server: %s\n", disk.url)
	}
	data := startupCloudConfig{}
	data.PhoneHome.URL = "http://" + net.JoinHostPort(network.gateway.String(), "7777") + "/latest/meta-data/phone_home"
	data.PhoneHome.Post, data.PhoneHome.Tries = "all", 10
	data.RunCmd = [][]string{{"curl", "--fail", "--silent", "--show-error", "--head", "--max-time", "15", disk.url}}
	data.WriteFiles = append(data.WriteFiles, startupFile{"/var/lib/nvke-native/package-inventory.sh", "0700", strings.TrimPrefix(nativePackageInventory, "\n")})
	data.RunCmd = append(data.RunCmd, []string{"bash", "/var/lib/nvke-native/package-inventory.sh"})
	encoded, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	userdata := filepath.Join(out, "guest-cloud-init.yaml")
	userBytes := append([]byte("#cloud-config\n"), encoded...)
	instanceName, instanceType := "nvke-native-guest", "a1b2c3d4-0005-4000-8000-000000000001"
	if cfg.site != nil {
		userBytes, err = cfg.site.startup(ctx, api, disk.url, out)
		if err != nil {
			return err
		}
		instanceName, instanceType = "nvke-native-site", "a1b2c3d4-0005-4000-8000-000000000004"
	}
	if err := os.WriteFile(userdata, userBytes, 0600); err != nil {
		return err
	}
	var boot *nativeSiteBoot
	if cfg.site != nil {
		network, err = cfg.site.selectBootNetwork(network, disk.url)
		if err != nil {
			return err
		}
		boot = cfg.site.boot
	}
	var guestProgress *progressBoundary
	if cfg.site != nil {
		guestProgress = beginProgress(cfg.Progress, "native-site-guest", PhaseStarted, PhaseFinished, PhaseFailed)
		defer func() { guestProgress.finish(result) }()
	}
	created, err := createNativeGuest(ctx, api, instanceName, instanceType, userBytes, boot)
	if err != nil {
		return err
	}
	var instance nativeInstance
	if err := json.Unmarshal(created, &instance); err != nil {
		return err
	}
	if instance.ID == "" || instance.Name != instanceName {
		return errors.New("native create response lacks exact instance identity")
	}
	instanceID = instance.ID
	budget, stop := context.WithCancel(ctx)
	if cfg.Timeout > 0 {
		stop()
		budget, stop = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer stop()
	for {
		raw, err := nativeReadinessObservation(budget, cfg.Progress, func(ctx context.Context) ([]byte, error) {
			if exited, err := server.Exited(); exited {
				return nil, errors.Join(errSimulatorExited, err)
			}
			return call(ctx, "readiness", "instance", "list")
		})
		if err != nil {
			return err
		}
		var rows []nativeInstance
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].ID != instanceID {
			return errors.New("native instance disappeared or extra instance observed")
		}
		row := rows[0]
		if row.Status == "Ready" {
			if _, err := nativePrimaryAddress(row, network); err != nil {
				return err
			}
			if cfg.site != nil {
				guestProgress.finish(nil)
				// Registered after every dependent cleanup defer: callback
				// panic/Goexit must publish unsafe join state before unwinding
				// any guest, service or network owner, without a return value.
				defer func() {
					if cfg.site.unjoinedWork != nil && !errors.Is(result, cfg.site.unjoinedWork) {
						result = errors.Join(result, cfg.site.unjoinedWork)
					}
				}()
				return cfg.site.ready(ctx, runner, row, tools, out, server)
			}
			if disk.requests.Load() == 0 {
				return errors.New("guest called back but never reached the demo-owned disk server")
			}
			if cfg.Progress != nil {
				fmt.Fprintf(cfg.Progress, "PASS: native guest completed cloud-init at %s; Kubernetes remains separate\n", row.Interfaces[0].IPAddresses[0])
			}
			return nil
		}
		if row.Status == "Error" {
			return errors.New("native guest failed; private NICo and guest console logs retained")
		}
		if err := pause(budget, 2*time.Second); err != nil {
			return err
		}
	}
}

// createNativeGuest uses the pinned NICo API's existing startup-data field.
// The source CLI only accepts inline startup data, which would expose the
// site's private handoff credentials in process arguments.
func createNativeGuest(ctx context.Context, api, name, instanceType string, userData []byte, boot *nativeSiteBoot) ([]byte, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if len(userData) == 0 || len(userData) > 1<<20 {
		return nil, errors.New("native guest startup data must be nonempty and at most 1 MiB")
	}
	if instanceType == "a1b2c3d4-0005-4000-8000-000000000004" && (boot == nil || boot.script == "" || boot.diskURL == "") {
		return nil, errors.New("full native site requires its source-selected MachineImage boot script")
	}
	// Site and tenant guests share NICo's VPC-prefix allocator. Using its
	// independent subnet allocator here could issue overlapping addresses.
	body := map[string]any{
		"name": name, "tenantId": "d282929f-49d5-4dec-8d27-21ceb30ade19",
		"instanceTypeId": instanceType, "vpcId": VPCID,
		"labels":           map[string]string{"nke.nvidia.com/node-id": name},
		"sshKeyGroupIds":   []string{},
		"interfaces":       []map[string]string{{"vpcPrefixId": TenantPrefixID}},
		"phoneHomeEnabled": true, "userData": string(userData),
	}
	if boot != nil {
		body["ipxeScript"] = boot.script
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	budget, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(budget, http.MethodPost, api+"/v2/org/"+OrgID+"/forge/instance", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer nico-simctl-token")
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("native instance create returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, diagnosticLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > diagnosticLimit {
		return nil, errors.New("native instance create response exceeds diagnostic limit")
	}
	return raw, nil
}

func nativePrimaryAddress(row nativeInstance, network nativeBridgeConfig) (string, error) {
	if !network.prefix.IsValid() || len(row.Interfaces) != 1 || (len(row.Interfaces[0].IPAddresses) != 2 && !(network.dhcp && len(row.Interfaces[0].IPAddresses) == 1)) {
		return "", errors.New("native VPC-prefix instance requires its two allocated addresses")
	}
	var previous netip.Addr
	for _, value := range row.Interfaces[0].IPAddresses {
		address, err := netip.ParseAddr(value)
		if err != nil || !address.Is4() || !network.prefix.Contains(address) || address.Compare(network.gateway) <= 0 ||
			address.Compare(network.last) > 0 || address == previous {
			return "", errors.New("native VPC-prefix address is invalid, duplicated or outside the owned bridge")
		}
		previous = address
	}
	return row.Interfaces[0].IPAddresses[0], nil
}

var errSimulatorExited = errors.New("native NICo exited during readiness; inspect the private native-nico log")

// A joined read-only CLI failure is not a guest state observation. Retry it
// within the original readiness budget, requiring a successful response before
// interpreting state. The caller's original deadline bounds the observation.
// A command's independent timeout may retry only after its owner confirms join.
// Caller cancellation/expiry, an exited simulator, unsafe ownership and other
// exit codes are never retried.
func nativeReadinessObservation(ctx context.Context, progress io.Writer, observe func(context.Context) ([]byte, error)) ([]byte, error) {
	if _, err := remainingWork(ctx); err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := observe(ctx)
		if canceled := ctx.Err(); canceled != nil {
			return nil, errors.Join(err, canceled)
		}
		if err == nil || errors.Is(err, errSimulatorExited) || errors.Is(err, ErrUnjoined) || errors.Is(err, context.Canceled) ||
			CommandExitCode(err) != 1 && !errors.Is(err, errCommandDeadline) {
			return raw, err
		}
		if progress != nil {
			fmt.Fprintf(progress, "Native readiness observation failed (attempt %d); retrying within the existing deadline; readiness remains unconfirmed.\n", attempt)
		}
		if waitErr := pause(ctx, 2*time.Second); waitErr != nil {
			return nil, errors.Join(err, waitErr)
		}
	}
}

// Package and network diagnostics are emitted only into the private guest
// console. They inspect the demo guest's local configuration and resolve a
// public Ubuntu archive name. No package or configuration is changed.
const nativePackageInventory = `
printf '\nNVKE_PACKAGE_INSPECTION_START\n'
grep '^VERSION_ID=' /etc/os-release
dpkg-query -W -f='${binary:Package}\t${Version}\n' 'nke*' 'nvke*' 'spire*' 'containerd*' 2>/dev/null || true
for root in /usr/lib/nvke/debs /usr/lib/nke/debs /opt/nke/debs; do
  if [[ -f "$root/Packages" ]]; then
    printf 'LOCAL_PACKAGE_INDEX=%s\n' "$root/Packages"
    awk '/^Package:/ { print }' "$root/Packages"
  fi
done
apt-cache pkgnames | sort | sed -n '/^nke/p; /^nvke/p; /^spire/p; /^containerd/p'
for file in /usr/local/lib/spire-agent/dsx-imds-node-attestor-agent /usr/lib/nvke/debs/spire-agent_1.14.1+tpm1.9.0-1_arm64.deb /usr/lib/nke/debs/spire-agent_1.14.1+tpm1.9.0-1_arm64.deb; do
  if [[ -f "$file" ]]; then sha256sum "$file"; fi
done
printf 'NVKE_PACKAGE_INSPECTION_END\n'
printf 'NVKE_NATIVE_NETWORK_INSPECTION_START\n'
for file in /etc/netplan/99-nico-native.yaml /var/lib/cloud/instance/sem/bootper.nico_native_network.instance; do
  if [[ -f "$file" ]]; then
    printf 'NATIVE_NETWORK_FILE=%s\n' "$file"
    cat "$file"
  fi
done
ip -4 -brief address
printf 'NVKE_NATIVE_DNS_INSPECTION_START\n'
if timeout 15s getent ahostsv4 ports.ubuntu.com; then
  printf 'NVKE_NATIVE_DNS_RESOLVED\n'
else
  printf 'NVKE_NATIVE_DNS_FAILED\n'
fi
printf 'NVKE_NATIVE_DNS_INSPECTION_END\n'
find /etc/netplan -maxdepth 1 -type f -name '*.yaml' -print
networkctl status --all --no-pager || true
for file in /run/systemd/network/*netplan*.network; do
  if [[ -f "$file" ]]; then
    printf 'NATIVE_NETWORK_BACKEND=%s\n' "$file"
    cat "$file"
  fi
done
journalctl -b -u systemd-networkd.service --no-pager -n 30
printf 'NVKE_NATIVE_NETWORK_INSPECTION_END\n'
`

func nativeGuestLimit(site *nativeSiteAttempt) int {
	if site == nil {
		return 1
	}
	return 1 + site.tenantNodeCount()
}

func nativeCleanupRows(rows []nativeInstance, instanceID string, limit int) ([]nativeInstance, error) {
	if limit != 1 && limit != 1+nativeTenantNodeCount(false) && limit != 1+nativeTenantNodeCount(true) || limit != 1 && len(rows) > limit {
		return nil, errors.New("private native cleanup inventory exceeds the requested topology")
	}
	var owned []nativeInstance
	var rejected bool
	seen := map[string]bool{}
	for _, row := range rows {
		if row.ID == "" || seen[row.ID] || limit == 1 && (row.Name != "nvke-native-guest" || instanceID != "" && row.ID != instanceID) {
			rejected = true
			continue
		}
		seen[row.ID] = true
		owned = append(owned, row)
	}
	if rejected {
		return owned, errors.New("unexpected instance in private guest fixture")
	}
	return owned, nil
}
