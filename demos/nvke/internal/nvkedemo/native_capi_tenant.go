// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"time"
)

// These are NVKE's generic CAPI request fields. NVKE validates the manifests,
// issues the SPIRE join token and renders bootstrap credentials and cloud-init.
type TenantClusterInfra struct {
	Cluster json.RawMessage `json:"cluster"`
}

type TenantMachineInfra struct {
	MachineTemplate json.RawMessage   `json:"machineTemplate"`
	PreCommands     []string          `json:"preKubeadmCommands,omitempty"`
	BootstrapFiles  []json.RawMessage `json:"bootstrapFiles,omitempty"`
}

func (s *nativeSiteAttempt) capiTenantRequest(ctx context.Context, request TenantRequest) (TenantRequest, error) {
	if s.boot == nil || s.boot.script == "" {
		return TenantRequest{}, errors.New("native tenant requires the selected local QCOW boot projection")
	}
	commands, err := s.capiTenantCommands(ctx, request)
	if err != nil {
		return TenantRequest{}, err
	}
	cluster, err := json.Marshal(map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1", "kind": "NicoCluster",
		"metadata": map[string]string{"name": request.Name}, "spec": map[string]string{"siteID": SiteID},
	})
	if err != nil {
		return TenantRequest{}, err
	}
	template := func(role string) (*TenantMachineInfra, error) {
		raw, err := json.Marshal(map[string]any{
			"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1", "kind": "NicoMachineTemplate",
			"metadata": map[string]string{"name": request.Name + "-" + role},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
				"vpcID": VPCID, "instanceTypeID": TenantInstanceType,
				"cloudInitInjectHostname": true, "ipxeScript": s.boot.script,
				"interfaces": []map[string]string{{"vpcPrefixID": TenantPrefixID}},
			}}},
		})
		return &TenantMachineInfra{MachineTemplate: raw, PreCommands: []string{commands}}, err
	}
	request.Infra = &TenantClusterInfra{Cluster: cluster}
	request.ControlPlane.Infra, err = template("control-plane")
	if err != nil {
		return TenantRequest{}, err
	}
	for i := range request.NodePools {
		request.NodePools[i].Infra, err = template(request.NodePools[i].Name)
		if err != nil {
			return TenantRequest{}, err
		}
	}
	return request, nil
}

func (s *nativeSiteAttempt) capiTenantCommands(ctx context.Context, request TenantRequest) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	if err := admitNativeResourceProfile(s.resources.Profile, request.CNIVersion); err != nil {
		return "", err
	}
	kubeletPreparation, err := nativeKubeletProfilePreparation(ctx, s.resources.Profile)
	if err != nil {
		return "", err
	}
	if request.CNI != "ovn-kubernetes" || request.Kubernetes == "" || request.CNIVersion == "" {
		return "", errors.New("native CAPI tenant requires selected Kubernetes and OVN packages")
	}
	endpoint, err := url.Parse(s.api)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != s.bridge.gateway.String() || endpoint.Port() == "" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", errors.New("native CAPI tenant requires the owned artifact endpoint on its observed gateway")
	}
	// Keep NVKE-generated /etc/spire-agent/agent.conf, its join token, trust
	// bundle and node-agent mTLS files. The image's instance-identity renderer
	// must not overwrite those files when SPIRE starts.
	script := "set -euo pipefail\n" +
		"install -d -m 0755 /run/systemd/system/nvke-agent.service.d\n" +
		"printf '%s\\n' '[Unit]' 'ConditionPathExists=/run/nvkr-native-tenant-preparation.ready' > /run/systemd/system/nvke-agent.service.d/20-nvkr-preparation.conf\n" +
		"test ! -e /run/nvkr-native-tenant-preparation.ready\nsystemctl daemon-reload\n" +
		"systemctl stop nvke-agent.service spire-agent.service\n" +
		"NVKE_AGENT_SKIP_START=1 DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-agent " + operatorQuote("nvke-kubeadm-distro-"+request.Kubernetes) + " spire-agent " + operatorQuote("nvke-cni-ovn-images-"+request.CNIVersion) + " " + operatorQuote("nvke-cni-ovn-charts-"+request.CNIVersion) + "\n" +
		kubeletPreparation + "test ! -e /etc/nke/spire.env\n" +
		"test -s /etc/spire-agent/agent.conf\n" +
		"test -s /etc/spire-agent/initial-trust-bundle.pem\n" +
		"install -d -m 0755 -o spire -g spire /tmp/spire-agent /tmp/spire-agent/public\n" +
		"sysctl --system\nsystemctl daemon-reload\nsystemctl start containerd.service\n"
	for _, role := range []string{"ovn", "workload"} {
		image, ok := s.assets.Images[role]
		if !ok {
			return "", fmt.Errorf("native CAPI tenant requires selected %s image", role)
		}
		digest, err := DigestFile(ctx, image.File)
		if err != nil {
			return "", err
		}
		reference, err := nativeImageDigestReference(image)
		if err != nil {
			return "", err
		}
		if role == "ovn" && reference != "localhost/nvke/ovn-kubernetes@"+image.ManifestDigest {
			return "", errors.New("OVN import alias differs from selected manifest")
		}
		arch := s.assets.arch
		preload, err := nativeImagePreloadScriptForArchitecture(nativeImagePreload{
			location: s.api + "/files/" + role + ".tar",
			digest:   digest, file: "/var/lib/nvke-demo-" + role + ".tar", reference: reference,
		}, arch)
		if err != nil {
			return "", err
		}
		script += preload + "\n"
	}
	values, err := nativeOVNValuesScriptForProfile(request.CNIVersion, s.assets.Images["ovn"].Reference, s.resources.Profile)
	if err != nil {
		return "", err
	}
	script += values + "\nsystemctl restart spire-agent.service\nprintf '%s\\n' ready > /run/nvkr-native-tenant-preparation.ready\n"
	// CAPI's source-owned renderer appends the node-agent restart after these
	// commands, even when a command fails. The runtime guard keeps that restart
	// from admitting a failed preparation. This is not a cloud-init completion
	// receipt: source-built agents with a commit-string version can bypass the
	// controller's semver-gated cloud-init check. Actual readiness, workload/DNS
	// and requested conformance checks remain separate runtime requirements.
	// This late drop-in also cannot prevent initial agent startup before runcmd;
	// runNativeCloudInit independently checks the real status on every requested Ready node.
	preparation, err := guestWorkCommand(ctx, script)
	if err != nil {
		return "", err
	}
	return "/bin/bash -ec " + operatorQuote(preparation), nil
}

// Native ownership includes the exact submitted infrastructure manifests and
// bootstrap commands. Server observations, not the request alone, authorize
// adopting the ID and later deleting this cluster.
type nativeCAPITenantOwnership struct {
	cluster      TenantClusterInfra
	controlPlane TenantMachineInfra
	worker       NodePoolIntent
	workerID     string
	deleting     bool
}

func newNativeCAPITenantOwnership(request TenantRequest) (*nativeCAPITenantOwnership, error) {
	if request.Infra == nil || request.ControlPlane.Infra == nil || request.ControlPlane.Count != 1 || len(request.NodePools) != 1 || request.NodePools[0].Infra == nil || request.NodePools[0].ControlPlane || (request.NodePools[0].Count != nativeTenantNodeCount(false)-1 && request.NodePools[0].Count != nativeTenantNodeCount(true)-1) ||
		!reflect.ValueOf(request.ControlPlane.NICo).IsZero() {
		return nil, errors.New("native tenant ownership requires its complete CAPI request")
	}
	// Copy the wire projection so later mutation of the caller's request cannot
	// change the provider configuration that must be observed from NVKE.
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var frozen TenantRequest
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	return &nativeCAPITenantOwnership{cluster: *frozen.Infra, controlPlane: *frozen.ControlPlane.Infra, worker: frozen.NodePools[0]}, nil
}

func sameInfraManifest(a, b json.RawMessage) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

func sameMachineInfra(a, b *TenantMachineInfra) bool {
	return a != nil && b != nil && sameInfraManifest(a.MachineTemplate, b.MachineTemplate) &&
		reflect.DeepEqual(a.PreCommands, b.PreCommands) && len(a.BootstrapFiles) == 0 && len(b.BootstrapFiles) == 0
}

func (t *tenant) nativeCAPIClusterRow(rows []tenantRow) (*tenantRow, error) {
	for _, row := range rows {
		if row.Name == "" || !safeTenantIdentity(row.ID) {
			return nil, errors.New("native cluster inventory received an invalid cluster identity")
		}
		if t.id != "" && row.ID == t.id && row.Name != t.name {
			return nil, errors.New("native tenant identity changed its name; refusing adoption or removal")
		}
	}
	row, err := selectedTenantIdentity(rows, t.name, t.id)
	if err != nil || row == nil {
		return row, err
	}
	ownership := t.nativeCAPI
	if row.NICo != (TenantProvider{}) || row.Infra == nil || row.ControlPlane == nil || row.ControlPlane.Count != 1 || !reflect.ValueOf(row.ControlPlane.NICo).IsZero() ||
		!sameInfraManifest(row.Infra.Cluster, ownership.cluster.Cluster) ||
		!sameMachineInfra(row.ControlPlane.Infra, &ownership.controlPlane) {
		return nil, errors.New("native tenant infrastructure changed; refusing adoption or removal")
	}
	return row, nil
}

func (t *tenant) nativeCAPIRow(ctx context.Context, rows []tenantRow) (*tenantRow, error) {
	row, err := t.nativeCAPIClusterRow(rows)
	if err != nil || row == nil {
		return row, err
	}
	ownership := t.nativeCAPI
	// JSON inventory avoids NVKE's separate name lookup. Select only the
	// already observed cluster ID; unrelated pools confer no ownership.
	raw, err := t.capture(ctx, []string{t.cli(), "get", "nodepools", "-o", "json"}, "native-pool-ownership", 30*time.Second)
	if err != nil {
		return t.nativeCAPIDeletionObserved(ctx, err)
	}
	var pools []struct {
		ID, Name, ClusterID string
		ControlPlane        bool
		Count               int `json:"desiredNodeCount"`
		Infra               *TenantMachineInfra
		NICo                json.RawMessage
	}
	if err := json.Unmarshal(raw, &pools); err != nil {
		return nil, err
	}
	if pools == nil {
		return nil, errors.New("native worker ownership requires a node pool inventory array")
	}
	ownedPools := pools[:0]
	seenPools := make(map[string]bool, len(pools))
	for _, pool := range pools {
		if !safeTenantIdentity(pool.ID) || !safeTenantIdentity(pool.ClusterID) || seenPools[pool.ID] {
			return nil, errors.New("native node pool inventory contains an invalid identity")
		}
		seenPools[pool.ID] = true
		if ownership.workerID != "" && pool.ID == ownership.workerID && pool.ClusterID != row.ID {
			return nil, errors.New("native tenant worker changed its cluster; refusing adoption or removal")
		}
		if pool.ClusterID == row.ID {
			ownedPools = append(ownedPools, pool)
		}
	}
	pools = ownedPools
	if len(pools) == 0 && ownership.deleting && ownership.workerID != "" {
		return row, nil
	}
	if len(pools) != 1 || pools[0].Name != ownership.worker.Name || pools[0].ClusterID != row.ID || pools[0].ControlPlane || (!ownership.deleting && pools[0].Count != ownership.worker.Count) ||
		!safeTenantIdentity(pools[0].ID) || ownership.workerID != "" && pools[0].ID != ownership.workerID ||
		len(pools[0].NICo) != 0 && string(pools[0].NICo) != "null" || !sameMachineInfra(pools[0].Infra, ownership.worker.Infra) {
		return nil, errors.New("native tenant worker infrastructure changed; refusing adoption or removal")
	}
	ownership.workerID = pools[0].ID
	return row, nil
}

// A pool observation can fail while owned deletion is in progress. Confirm
// cluster absence with one fresh inventory; a query failure alone is never
// deletion evidence. Unavailable reads may wait only within the deletion owner.
func (t *tenant) nativeCAPIDeletionObserved(ctx context.Context, queryErr error) (*tenantRow, error) {
	if !t.nativeCAPI.deleting || t.id == "" || ctx.Err() != nil || errors.Is(queryErr, ErrUnjoined) {
		return nil, queryErr
	}
	pending, canWait := t.nativeDeletionReadFailure(ctx, queryErr).(*nativeDeletionReadPending)
	rows, err := t.inventory(ctx)
	if err != nil {
		if _, unavailable := err.(*nativeDeletionReadPending); canWait && unavailable {
			return nil, &nativeDeletionReadPending{cause: errors.Join(queryErr, err)}
		}
		return nil, errors.Join(queryErr, err)
	}
	if ctx.Err() != nil {
		return nil, errors.Join(queryErr, ctx.Err())
	}
	row, err := t.nativeCAPIClusterRow(rows)
	if err != nil {
		return nil, errors.Join(queryErr, err)
	}
	if row != nil {
		if canWait {
			return nil, pending
		}
		return nil, queryErr
	}
	return nil, nil
}

func nativeTenantSecurityValues() map[string]any {
	return map[string]any{
		"controller": map[string]any{
			"supportedInfraProviders": "nico,infra", "nodeAgent": map[string]string{"spireAttestationMethod": "joinToken"},
			// Keep the source's root filesystem and security context unchanged.
			// Only the held, hash-checked identity helper uses this bounded scratch.
			"volumes":      []map[string]any{{"name": nativeIdentityStagingVolume, "emptyDir": map[string]string{"sizeLimit": nativeIdentityStagingLimit}}},
			"volumeMounts": []map[string]any{{"name": nativeIdentityStagingVolume, "mountPath": nativeIdentityStagingRoot}},
			// Match NVKE's site qualification values. Its SPIRE Service exposes
			// 443; the controller's 8081 default is the backing Pod port.
			// Helm replaces the env list, so retain the site node-agent port.
			"env": []map[string]string{
				{"name": "NODE_AGENT_PORT", "value": "15005"},
				{"name": "SPIRE_SERVER_API_ADDRESS", "value": "spire-server.spire-server.svc.cluster.local:443"},
			},
		},
		"spire": map[string]any{"spire-server": map[string]any{"nodeAttestor": map[string]any{"joinToken": map[string]bool{"enabled": true}}}},
	}
}
