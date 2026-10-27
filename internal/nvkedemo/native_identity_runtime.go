// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/siteidentity"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"go.yaml.in/yaml/v3"
)

type nativePodConfigMap struct {
	Name  string
	Items []struct{ Key, Path string }
}
type nativePodEnvironment struct {
	Name, Value string
	ValueFrom   *struct{ FieldRef *struct{ FieldPath string } }
}
type nativeIdentityConfig struct {
	Spire struct {
		ClassName       string `yaml:"className"`
		ServerNamespace string `yaml:"serverNamespace"`
		TPMMap          string `yaml:"tpmPubhashesConfigMapName"`
		ServerPort      string `yaml:"serverPort"`
	} `yaml:"spire"`
	NodeAgent struct {
		ExternalMTLS bool `yaml:"externalMTLSEnabled"`
		TLSJWT       struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"tlsJWT"`
	} `yaml:"nodeAgent"`
}

// Identity observations extend existing Pod projections privately; the shared
// deployment projections and their callers keep their original contract.
type nativeIdentityMount struct {
	profileMount
	SubPath, SubPathExpr string
}
type nativeIdentityVolume struct {
	profileVolume
	ConfigMap *nativePodConfigMap
	CSI       *struct {
		Driver   string
		ReadOnly bool
	}
}
type nativeIdentityContainer struct {
	podContainer
	VolumeMounts []nativeIdentityMount
	Env          []nativePodEnvironment
}
type nativeIdentityContainerState struct {
	podContainerState
	ContainerID string
}
type nativeIdentityPodDocument struct {
	Metadata podMetadata
	Spec     struct {
		Containers                   []nativeIdentityContainer
		Volumes                      []nativeIdentityVolume
		ServiceAccountName, NodeName string
	}
	Status struct {
		ContainerStatuses []nativeIdentityContainerState
		HostIP            string
	}
}
type nativeIdentityPodList struct{ Items []nativeIdentityPodDocument }
type nativeIdentityPod struct {
	pod       nativeIdentityPodDocument
	container nativeIdentityContainer
	state     nativeIdentityContainerState
}
type nativeIdentityRuntime struct {
	controller, server                          nativeIdentityPod
	config                                      nativeIdentityConfig
	configName, configKey, configSHA, configUID string
	bootID, agentPort, serverPort               string
}
type nativeIdentityCommands struct {
	runner              Runner
	kubectl, kubeconfig string
	sequence            int
}

func (k *nativeIdentityCommands) capture(ctx context.Context, phase string, timeout time.Duration, input *os.File, args ...string) ([]byte, error) {
	k.sequence++
	runner := k.runner
	runner.input = input
	return runner.Capture(ctx, append([]string{k.kubectl, "--kubeconfig", k.kubeconfig, "--request-timeout=" + strconv.Itoa(int(timeout.Seconds())+5) + "s"}, args...), fmt.Sprintf("site-identity-%03d-%s", k.sequence, phase), timeout)
}
func (k *nativeIdentityCommands) get(ctx context.Context, out any, args ...string) error {
	raw, err := k.capture(ctx, "observe", 15*time.Second, nil, append([]string{"get"}, append(args, "-o", "json")...)...)
	if err != nil {
		return err
	}
	return nativeIdentityJSON(raw, out)
}
func (k *nativeIdentityCommands) exec(ctx context.Context, pod nativeIdentityPod, phase string, timeout time.Duration, input *os.File, args ...string) ([]byte, error) {
	prefix := []string{"exec"}
	if input != nil {
		prefix = append(prefix, "-i")
	}
	prefix = append(prefix, "-n", pod.pod.Metadata.Namespace, pod.pod.Metadata.Name, "-c", pod.container.Name, "--")
	return k.capture(ctx, phase, timeout, input, append(prefix, args...)...)
}
func nativeIdentityEnvironment(container nativeIdentityContainer, name string) (nativePodEnvironment, error) {
	var found *nativePodEnvironment
	for _, env := range container.Env {
		if env.Name == name {
			if found != nil {
				return nativePodEnvironment{}, errors.New("ambiguous selected controller environment")
			}
			value := env
			found = &value
		}
	}
	if found == nil {
		return nativePodEnvironment{}, errors.New("selected controller omits required environment: " + name)
	}
	return *found, nil
}
func nativeIdentitySelectedPod(rows nativeIdentityPodList, reference string) (nativeIdentityPod, error) {
	var selected *nativeIdentityPod
	for _, pod := range rows.Items {
		for _, container := range pod.Spec.Containers {
			for _, state := range pod.Status.ContainerStatuses {
				if state.Name != container.Name || state.ImageID != reference {
					continue
				}
				if selected != nil || pod.Metadata.Name == "" || pod.Metadata.Namespace == "" || pod.Metadata.UID == "" || !state.Ready || state.State.Running == nil || state.ContainerID == "" {
					return nativeIdentityPod{}, errors.New("selected identity container is unavailable or ambiguous")
				}
				value := nativeIdentityPod{pod: pod, container: container, state: state}
				selected = &value
			}
		}
	}
	if selected == nil {
		return nativeIdentityPod{}, errors.New("selected identity image has no exact ready container")
	}
	return *selected, nil
}
func nativeIdentityMountedMap(pod nativeIdentityPod, file string, directory bool) (string, string, error) {
	var mapName, key string
	for _, mount := range pod.container.VolumeMounts {
		if mount.SubPath != "" || mount.SubPathExpr != "" || !mount.ReadOnly {
			continue
		}
		if (directory && mount.MountPath != file) || (!directory && !strings.HasPrefix(file, mount.MountPath+"/")) {
			continue
		}
		for _, volume := range pod.pod.Spec.Volumes {
			if volume.Name != mount.Name || volume.ConfigMap == nil || volume.ConfigMap.Name == "" {
				continue
			}
			if mapName != "" {
				return "", "", errors.New("ambiguous selected identity ConfigMap mount")
			}
			mapName = volume.ConfigMap.Name
			if !directory {
				relative := strings.TrimPrefix(file, mount.MountPath+"/")
				key = relative
				if len(volume.ConfigMap.Items) != 0 {
					key = ""
					for _, item := range volume.ConfigMap.Items {
						if item.Path == relative {
							if key != "" {
								return "", "", errors.New("ambiguous identity config projection")
							}
							key = item.Key
						}
					}
				}
			}
		}
	}
	if mapName == "" || (!directory && (key == "" || path.Clean(file) != file)) {
		return "", "", errors.New("selected identity ConfigMap is not mounted read-only at its source path")
	}
	return mapName, key, nil
}
func nativeIdentityCSI(pod nativeIdentityPod) error {
	if pod.pod.Spec.ServiceAccountName == "" {
		return errors.New("selected controller has no workload service account")
	}
	matches := 0
	for _, mount := range pod.container.VolumeMounts {
		if mount.MountPath != "/spiffe-workload-api" || !mount.ReadOnly || mount.SubPath != "" || mount.SubPathExpr != "" {
			continue
		}
		for _, volume := range pod.pod.Spec.Volumes {
			if volume.Name == mount.Name && volume.CSI != nil && volume.CSI.Driver == "csi.spiffe.io" && volume.CSI.ReadOnly {
				matches++
			}
		}
	}
	if matches != 1 {
		return errors.New("selected controller lacks its exact CSI identity context")
	}
	return nil
}
func nativeIdentityPort(raw string) (string, error) {
	n, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != raw {
		return "", errors.New("selected identity port is not canonical")
	}
	return raw, nil
}
func nativeIdentityConfigProjection(raw string) (nativeIdentityConfig, error) {
	var cfg nativeIdentityConfig
	if len(raw) == 0 || len(raw) > 64<<10 {
		return cfg, errors.New("source controller configuration exceeds its bound")
	}
	// This is only a projection through the existing YAML implementation. NVKE's
	// deployed config owner validates its full schema; this cannot replace it.
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, err
	}
	if cfg.Spire.ClassName == "" || cfg.Spire.ServerNamespace == "" || cfg.Spire.TPMMap == "" || cfg.NodeAgent.ExternalMTLS || cfg.NodeAgent.TLSJWT.Enabled {
		return cfg, errors.New("selected controller does not expose the supported SPIFFE-only identity profile")
	}
	_, err := nativeIdentityPort(cfg.Spire.ServerPort)
	return cfg, err
}
func (k *nativeIdentityCommands) runtime(ctx context.Context, assets NativeAssets, spec nativeStaticEntrySpec, ip string) (nativeIdentityRuntime, error) {
	var runtime nativeIdentityRuntime
	var pods nativeIdentityPodList
	if err := k.get(ctx, &pods, "pods", "--all-namespaces"); err != nil {
		return runtime, err
	}
	controllerRef, err := nativeImageDigestReference(assets.Images["controller"])
	if err != nil {
		return runtime, err
	}
	serverRef, err := nativeImageDigestReference(assets.Images["runtime-spire-server"])
	if err != nil {
		return runtime, err
	}
	if runtime.controller, err = nativeIdentitySelectedPod(pods, controllerRef); err != nil {
		return runtime, err
	}
	if runtime.server, err = nativeIdentitySelectedPod(pods, serverRef); err != nil {
		return runtime, err
	}
	if err := nativeIdentityCSI(runtime.controller); err != nil {
		return runtime, err
	}
	configEnv, err := nativeIdentityEnvironment(runtime.controller.container, "CONTROLLER_CONFIG_PATH")
	if err != nil || configEnv.ValueFrom != nil {
		return runtime, errors.New("selected controller lacks its source config path")
	}
	runtime.configName, runtime.configKey, err = nativeIdentityMountedMap(runtime.controller, configEnv.Value, false)
	if err != nil {
		return runtime, err
	}
	var cm struct {
		Metadata struct{ UID string }
		Data     map[string]string
	}
	if err := k.get(ctx, &cm, "configmap", "-n", runtime.controller.pod.Metadata.Namespace, runtime.configName); err != nil {
		return runtime, err
	}
	configRaw := cm.Data[runtime.configKey]
	runtime.configUID = cm.Metadata.UID
	runtime.configSHA = plainSHA([]byte(configRaw))
	if runtime.configUID == "" {
		return runtime, errors.New("selected controller config has no object identity")
	}
	if runtime.config, err = nativeIdentityConfigProjection(configRaw); err != nil {
		return runtime, err
	}
	if runtime.config.Spire.ClassName != spec.ClassName || runtime.config.Spire.ServerNamespace != runtime.server.pod.Metadata.Namespace {
		return runtime, errors.New("permanent server installation differs from source host policy")
	}
	mapName, _, err := nativeIdentityMountedMap(runtime.server, "/run/spire/data/tpm-direct/hashes", true)
	if err != nil || mapName != runtime.config.Spire.TPMMap {
		return runtime, errors.New("permanent TPM allowlist is not the selected server's source-owned mount")
	}
	port, err := nativeIdentityEnvironment(runtime.controller.container, "NODE_AGENT_PORT")
	if err != nil || port.ValueFrom != nil {
		return runtime, errors.New("selected controller lacks its observed node-agent port")
	}
	if runtime.agentPort, err = nativeIdentityPort(port.Value); err != nil {
		return runtime, err
	}
	address, err := nativeIdentityEnvironment(runtime.controller.container, "SPIRE_SERVER_ADDRESS")
	if err != nil {
		return runtime, err
	}
	if address.ValueFrom != nil {
		if address.ValueFrom.FieldRef == nil || address.ValueFrom.FieldRef.FieldPath != "status.hostIP" || runtime.controller.pod.Status.HostIP != ip {
			return runtime, errors.New("selected SPIRE address is not the observed site host")
		}
	} else if address.Value != ip {
		return runtime, errors.New("selected SPIRE address differs from the observed site host")
	}
	var services struct {
		Items []struct {
			Metadata struct{ Namespace string }
			Spec     struct {
				Type     string
				Selector map[string]string
				Ports    []struct {
					Protocol string
					NodePort int
				}
			}
		}
	}
	if err := k.get(ctx, &services, "services", "-n", runtime.server.pod.Metadata.Namespace); err != nil {
		return runtime, err
	}
	matches := 0
	for _, service := range services.Items {
		if service.Metadata.Namespace != runtime.server.pod.Metadata.Namespace || service.Spec.Type != "NodePort" || len(service.Spec.Selector) == 0 {
			continue
		}
		selected := true
		for key, value := range service.Spec.Selector {
			if runtime.server.pod.Metadata.Labels[key] != value {
				selected = false
			}
		}
		if !selected {
			continue
		}
		for _, port := range service.Spec.Ports {
			if (port.Protocol == "TCP" || port.Protocol == "") && strconv.Itoa(port.NodePort) == runtime.config.Spire.ServerPort {
				matches++
				runtime.serverPort = strconv.Itoa(port.NodePort)
			}
		}
	}
	if matches != 1 {
		return runtime, errors.New("selected permanent SPIRE port has no exact server Service binding")
	}
	var nodes struct {
		Items []struct {
			Metadata struct{ Name string }
			Status   struct {
				NodeInfo  struct{ BootID string }
				Addresses []struct{ Type, Address string }
			}
		}
	}
	if err := k.get(ctx, &nodes, "nodes"); err != nil {
		return runtime, err
	}
	if len(nodes.Items) != 1 || nodes.Items[0].Metadata.Name != runtime.controller.pod.Spec.NodeName || nodes.Items[0].Status.NodeInfo.BootID == "" {
		return runtime, errors.New("site host boot identity is absent or ambiguous")
	}
	addressMatches := 0
	for _, address := range nodes.Items[0].Status.Addresses {
		if address.Type == "InternalIP" && address.Address == ip {
			addressMatches++
		}
	}
	if addressMatches != 1 {
		return runtime, errors.New("site node differs from its observed native address")
	}
	runtime.bootID = nodes.Items[0].Status.NodeInfo.BootID
	return runtime, nil
}
func (k *nativeIdentityCommands) recheck(ctx context.Context, selected nativeIdentityRuntime, assets NativeAssets, spec nativeStaticEntrySpec, ip string) error {
	current, err := k.runtime(ctx, assets, spec, ip)
	if err != nil {
		return err
	}
	if current.controller.pod.Metadata.UID != selected.controller.pod.Metadata.UID || current.server.pod.Metadata.UID != selected.server.pod.Metadata.UID || current.controller.state.ContainerID != selected.controller.state.ContainerID || current.server.state.ContainerID != selected.server.state.ContainerID || current.configUID != selected.configUID || current.configSHA != selected.configSHA || current.bootID != selected.bootID || !reflect.DeepEqual(current.config, selected.config) {
		return errors.New("admitted identity context changed during handoff")
	}
	return nil
}
func (k *nativeIdentityCommands) observe(ctx context.Context, runtime nativeIdentityRuntime, spec nativeStaticEntrySpec) (nativeIdentityObservation, error) {
	var observed nativeIdentityObservation
	var entries struct{ Items []nativeStaticEntry }
	if err := k.get(ctx, &entries, "clusterstaticentries.spire.spiffe.io"); err != nil {
		return observed, err
	}
	observed.entries = entries.Items
	var cm struct{ Data map[string]string }
	mapRaw, err := k.capture(ctx, "observe-allowlist", 15*time.Second, nil, "get", "configmap", "-n", runtime.config.Spire.ServerNamespace, runtime.config.Spire.TPMMap, "--ignore-not-found", "-o", "json")
	if err != nil {
		return observed, err
	}
	if len(strings.TrimSpace(string(mapRaw))) != 0 {
		if err := nativeIdentityJSON(mapRaw, &cm); err != nil {
			return observed, err
		}
	}
	observed.allowlist = cm.Data
	for _, item := range []struct {
		args []string
		out  any
	}{{[]string{"agent", "list", "-output", "json"}, &observed.agents}, {[]string{"entry", "show", "-parentID", spec.ParentID, "-spiffeID", spec.SPIFFEID, "-output", "json"}, &observed.registrations}} {
		raw, err := k.exec(ctx, runtime.server, "spire-observe", 15*time.Second, nil, append([]string{"/opt/spire/bin/spire-server"}, item.args...)...)
		if err != nil {
			return observed, err
		}
		if err := nativeIdentityJSON(raw, item.out); err != nil {
			return observed, err
		}
	}
	return observed, nil
}

func nativeIdentityHeldInput(ctx context.Context, root, name, expected string, limit int64) (*os.File, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	file, info, err := openRunFile(root, name)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) { _ = file.Close(); return nil, err }
	if !info.Mode().IsRegular() || !currentOwner(info) || info.Mode().Perm()&0o022 != 0 || info.Size() <= 0 || info.Size() > limit {
		return fail(errors.New("identity input requires a bounded owned held regular file"))
	}
	hash := sha256.New()
	if _, err := transportarchive.CopyExact(ctx, hash, file, info.Size()); err != nil {
		return fail(err)
	}
	after, err := file.Stat()
	if err != nil || !securepath.SameMetadata(info, after) || hex.EncodeToString(hash.Sum(nil)) != strings.TrimPrefix(expected, "sha256:") {
		return fail(errors.New("identity input differs from its admitted bytes"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return file, nil
}

func nativeIdentityRequestFile(ctx context.Context, work string, request siteidentity.Request) (*os.File, error) {
	raw, err := canonical.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := siteidentity.DecodeRequest(ctx, raw); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(work, "site-identity-request-")
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func nativeIdentityHelperRequest(spec nativeStaticEntrySpec, runtime nativeIdentityRuntime, ip string) (siteidentity.Request, error) {
	peer, err := spiffeid.FromString(spec.SPIFFEID)
	if err != nil {
		return siteidentity.Request{}, err
	}
	caller, err := spiffeid.FromPath(peer.TrustDomain(), "/nvke-service/"+runtime.controller.pod.Spec.ServiceAccountName)
	if err != nil {
		return siteidentity.Request{}, err
	}
	port, err := strconv.ParseUint(runtime.serverPort, 10, 16)
	if err != nil {
		return siteidentity.Request{}, err
	}
	request := siteidentity.Request{SchemaVersion: siteidentity.SchemaVersion, Operation: "handoff", Endpoint: net.JoinHostPort(ip, runtime.agentPort), ExpectedPeer: spec.SPIFFEID, ExpectedCaller: caller.String(), WorkloadAPI: "unix:///spiffe-workload-api/spire-agent.sock", TrustDomain: peer.TrustDomain().Name(), ServerAddress: ip, ServerPort: uint16(port), BudgetSeconds: 240}
	return request, nil
}

func (s *nativeSiteAttempt) completeNativeSiteIdentity(ctx context.Context, runner Runner, ip, out string) (receipt *nativeSiteIdentityReceipt, resultErr error) {
	progress := beginProgress(runner.Progress, "native-site-identity-handoff", PhaseStarted, PhaseFinished, PhaseFailed)
	defer func() {
		if errors.Is(resultErr, ErrUnjoined) && s.unjoinedWork == nil {
			s.unjoinedWork = resultErr
		}
		progress.finish(resultErr)
	}()
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if len(s.spireEnvironment) == 0 {
		return nil, errors.New("site host identity requires the original authored environment bytes")
	}
	if s.assets.identity == nil {
		return nil, errors.New("site host identity requires the admitted tool/source member receipt")
	}
	private, err := nativeObservationRunner(ctx, runner, "site-identity-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			raw := []byte(resultErr.Error())
			raw = append(raw[:min(len(raw), 1<<20)], '\n')
			resultErr = errors.Join(resultErr, privateWrite(filepath.Join(private.Work, "failure.log"), raw))
		}
	}()
	k := nativeIdentityCommands{runner: private, kubectl: filepath.Join(s.assets.HostTools, "kubectl"), kubeconfig: filepath.Join(out, "site.kubeconfig")}
	raw, err := readRunFile(ctx, out, "site-prepared.json", 16<<10)
	if err != nil {
		return nil, err
	}
	var prepared struct {
		TPM string `json:"tpmEkThumbprint"`
	}
	if err := nativeIdentityJSON(raw, &prepared); err != nil {
		return nil, err
	}
	sourceRaw, err := readRunFile(ctx, out, "clusterstaticentries.yaml", 1<<20)
	if err != nil {
		return nil, err
	}
	sourceSHA := plainSHA(sourceRaw)
	original, err := nativeIdentityHeldInput(ctx, out, "clusterstaticentries.yaml", sourceSHA, 1<<20)
	if err != nil {
		return nil, err
	}
	defer original.Close()
	source, spec, err := nativeSourceStaticEntry(sourceRaw, prepared.TPM)
	if err != nil {
		return nil, err
	}
	runtime, err := k.runtime(ctx, s.assets, spec, ip)
	if err != nil {
		return nil, err
	}
	before, err := k.observe(ctx, runtime, spec)
	if err != nil {
		return nil, err
	}
	if err := nativeIdentityBaseline(source, spec, before); err != nil {
		return nil, err
	}
	request, err := nativeIdentityHelperRequest(spec, runtime, ip)
	if err != nil {
		return nil, err
	}
	requestBytes, err := canonical.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := siteidentity.DecodeRequest(ctx, requestBytes); err != nil {
		return nil, err
	}
	helper, err := nativeIdentityHeldInput(ctx, s.assets.SiteTools, nativeIdentityHelper, s.assets.identity.helperSHA, 128<<20)
	if err != nil {
		return nil, err
	}
	defer helper.Close()
	if err := k.recheck(ctx, runtime, s.assets, spec, ip); err != nil {
		return nil, err
	}
	// Fixed file staging only. No caller supplies a command or shell contents.
	staged, err := k.exec(ctx, runtime.controller, "stage-helper", 45*time.Second, helper, "/bin/sh", "-c", `umask 077; d=$(mktemp -d /tmp/nvkr-site-identity.XXXXXXXX) || exit; cat > "$d/helper" && chmod 0700 "$d/helper" && sha256sum "$d/helper"`)
	helperPath, err := nativeIdentityStagedHelper(staged, s.assets.identity.helperSHA, err)
	if err != nil {
		return nil, err
	}
	if err := k.recheck(ctx, runtime, s.assets, spec, ip); err != nil {
		return nil, err
	}
	probeRequest := request
	probeRequest.Operation = "probe"
	probeRequest.BudgetSeconds = 30
	probeInput, err := nativeIdentityRequestFile(ctx, private.Work, probeRequest)
	if err != nil {
		return nil, err
	}
	probeRaw, probeErr := k.exec(ctx, runtime.controller, "probe-host", 45*time.Second, probeInput, helperPath)
	closeErr := probeInput.Close()
	if err := errors.Join(probeErr, closeErr); err != nil {
		return nil, err
	}
	var probe siteidentity.Result
	if err := canonical.UnmarshalExact(bytesTrimOneNewline(probeRaw), &probe); err != nil {
		return nil, err
	}
	if probe.SchemaVersion != siteidentity.SchemaVersion || probe.Operation != "probe" || probe.Completion != siteidentity.NotStarted || probe.FailureCode != "" || probe.CallerID != request.ExpectedCaller || probe.Before.PeerID != spec.SPIFFEID || probe.Before.BootID != runtime.bootID || probe.Status != nil || probe.Reconfiguration != "" || probe.After != nil {
		return nil, errors.New("admitted identity context cannot authenticate the original same-boot host")
	}
	if err := k.recheck(ctx, runtime, s.assets, spec, ip); err != nil {
		return nil, err
	}
	if _, err := original.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// First create can require cold API discovery and OpenAPI schema loading.
	// Keep this one mutation within the caller's work deadline, without retry.
	policyBudget, err := remainingWork(ctx)
	if err != nil {
		return nil, err
	}
	createdRaw, err := k.capture(ctx, "import-source-policy", policyBudget, original, "create", "-f", "-", "-o", "json")
	if err != nil {
		return nil, err
	}
	createdUID, err := nativeIdentityCreatedEntry(createdRaw, source, prepared.TPM)
	if err != nil {
		return nil, err
	}
	// Only observations are retried. Source policy is created once; there is no
	// automatic overwrite, guest retry, rollback or partial-state adoption.
	policyCtx, stopPolicy := context.WithTimeout(ctx, 2*time.Minute)
	var policyErr error
	for policyCtx.Err() == nil {
		if err := k.recheck(policyCtx, runtime, s.assets, spec, ip); err != nil {
			stopPolicy()
			return nil, err
		}
		observed, err := k.observe(policyCtx, runtime, spec)
		if err == nil {
			_, _, err = nativeIdentityPolicy(source, spec, prepared.TPM, createdUID, observed)
		}
		if err == nil {
			policyErr = nil
			break
		}
		policyErr = err
		if errors.Is(err, ErrUnjoined) {
			stopPolicy()
			return nil, err
		}
		if err := pause(policyCtx, 2*time.Second); err != nil {
			policyErr = errors.Join(policyErr, err)
			break
		}
	}
	policyErr = errors.Join(policyErr, policyCtx.Err())
	stopPolicy()
	if policyErr != nil {
		return nil, policyErr
	}
	check, err := k.exec(ctx, runtime.controller, "verify-helper", 15*time.Second, nil, "sha256sum", helperPath)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(strings.Fields(string(check)), []string{strings.TrimPrefix(s.assets.identity.helperSHA, "sha256:"), helperPath}) {
		return nil, errors.New("staged identity helper changed before dispatch")
	}
	if err := k.recheck(ctx, runtime, s.assets, spec, ip); err != nil {
		return nil, err
	}
	requestInput, err := nativeIdentityRequestFile(ctx, private.Work, request)
	if err != nil {
		return nil, err
	}
	defer requestInput.Close()
	dispatchedAt := time.Now()
	result, err := s.captureNativeIdentityMutation(ctx, time.Duration(request.BudgetSeconds+30)*time.Second, func(critical context.Context) ([]byte, error) {
		return k.exec(critical, runtime.controller, "handoff-host", time.Duration(request.BudgetSeconds+30)*time.Second, requestInput, helperPath)
	})
	if err != nil {
		return nil, err
	}
	if result.CallerID != request.ExpectedCaller {
		return nil, errors.New("handoff caller differs from the admitted controller")
	}
	if result.Status == nil || result.Status.ServerAddress != request.ServerAddress || result.Status.ServerPort != uint32(request.ServerPort) {
		return nil, errors.New("source-owned SPIRE status differs from its selected permanent endpoint")
	}
	proofCtx, stopProof := context.WithTimeout(ctx, time.Minute)
	defer stopProof()
	var proofErr error
	for proofCtx.Err() == nil {
		if err := k.recheck(proofCtx, runtime, s.assets, spec, ip); err != nil {
			return nil, err
		}
		after, err := k.observe(proofCtx, runtime, spec)
		var cseUID, registrationID, agentSerial string
		if err == nil {
			cseUID, registrationID, err = nativeIdentityPolicy(source, spec, prepared.TPM, createdUID, after)
		}
		if err == nil {
			agentSerial, err = nativeIdentityFreshTPM(spec, before, after, result, dispatchedAt, time.Now(), runtime.bootID)
		}
		if err == nil {
			receipt = &nativeSiteIdentityReceipt{sourceSHA: sourceSHA, helperSHA: s.assets.identity.helperSHA, cseUID: cseUID, registrationID: registrationID, agentSerial: agentSerial, bootID: runtime.bootID, observedAt: time.Now()}
			if err := privateJSON(filepath.Join(private.Work, "completed.json"), struct {
				SourceSHA, HelperSHA, CSEUID, RegistrationID, AgentSerial, BootID string
				ObservedAt                                                        time.Time
			}{receipt.sourceSHA, receipt.helperSHA, receipt.cseUID, receipt.registrationID, receipt.agentSerial, receipt.bootID, receipt.observedAt}); err != nil {
				return nil, err
			}
			return receipt, nil
		}
		proofErr = err
		if errors.Is(err, ErrUnjoined) {
			return nil, err
		}
		if err := pause(proofCtx, 2*time.Second); err != nil {
			return nil, errors.Join(proofErr, err)
		}
	}
	return nil, errors.Join(proofErr, proofCtx.Err())
}

// Staging cannot mutate SPIRE identity. An observed command exit or a joined
// hash mismatch permits normal cleanup; a missing exit cannot prove the remote
// exec stopped and must retain its dependent controller and site.
func nativeIdentityStagedHelper(raw []byte, digest string, commandErr error) (string, error) {
	if commandErr != nil {
		if CommandExitCode(commandErr) < 0 && !errors.Is(commandErr, ErrUnjoined) {
			return "", errors.Join(ErrUnjoined, commandErr)
		}
		return "", commandErr
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || fields[0] != strings.TrimPrefix(digest, "sha256:") || path.Base(fields[1]) != "helper" || path.Clean(fields[1]) != fields[1] || path.Dir(path.Dir(fields[1])) != "/tmp" || !strings.HasPrefix(path.Base(path.Dir(fields[1])), "nvkr-site-identity.") {
		return "", errors.New("remote helper staging returned no exact artifact receipt")
	}
	return fields[1], nil
}
func bytesTrimOneNewline(raw []byte) []byte { return []byte(strings.TrimSuffix(string(raw), "\n")) }
