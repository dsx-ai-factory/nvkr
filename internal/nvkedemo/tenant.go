// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Explicit downstream policy, not an upstream completion guarantee. The pinned
// NVKE workflow waits for worker pools then control-plane nodes (20 minutes each)
// and may retry longer. Keep a finite wait and independent diagnostic/withdrawal
// headroom instead of truncating deletion with the former three-minute window.
const (
	tenantDeletionWaitTimeout = 45 * time.Minute
	tenantDeleteTimeout       = 2 * time.Minute
	tenantWithdrawalTimeout   = 2 * time.Minute
	tenantCleanupTimeout      = time.Hour
)

// tenantKubeconfigPublished observes publication only. NVKE owns kubeconfig
// extraction and validation; the Secret may exist with only its breakglass key
// while certificate issuance is still pending.
func tenantKubeconfigPublished(raw []byte) (bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}
	var secret struct {
		Data struct {
			Value []byte `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &secret); err != nil {
		// Keep private Secret contents out of diagnostics.
		return false, errors.New("invalid tenant admin kubeconfig Secret JSON")
	}
	return len(secret.Data.Value) > 0, nil
}

// TenantRequest is deployment intent consumed and validated by the pinned NVKE
// CLI. It is not an NVKR Definition or an authenticated provider identity.
type TenantRequest struct {
	Name          string              `json:"name"`
	Kubernetes    string              `json:"controlPlaneKubernetesVersion"`
	CNI           string              `json:"cniPlugin"`
	CNIVersion    string              `json:"cniVersion"`
	EtcdEncrypted bool                `json:"etcdEncrypted"`
	VIP           string              `json:"controlPlaneVIP,omitempty"`
	Teleport      bool                `json:"teleportEnabled"`
	Infra         *TenantClusterInfra `json:"infra,omitempty"`
	ControlPlane  ControlPlaneIntent  `json:"controlPlaneConfig"`
	NodePools     []NodePoolIntent    `json:"nodePools"`
	Network       *TenantNetwork      `json:"network,omitempty"`
}
type TenantNetwork struct {
	PodCIDR     string `json:"podCIDR"`
	ServiceCIDR string `json:"serviceCIDR"`
}

type TenantProvider struct {
	Org  string `json:"orgID"`
	Site string `json:"siteID"`
	VPC  string `json:"vpcID"`
}
type ControlPlaneIntent struct {
	Count          int                 `json:"desiredCount"`
	Reserved       string              `json:"systemReservedMemory"`
	Teleport       bool                `json:"teleportEnabled"`
	DisableMonitor bool                `json:"provisioningMonitorDisabled"`
	Infra          *TenantMachineInfra `json:"infra,omitempty"`
	NICo           struct {
		Profile  string   `json:"configProfile"`
		OS       string   `json:"osProfile,omitempty"`
		Release  string   `json:"release,omitempty"`
		Types    []string `json:"instanceTypePreference"`
		Prefixes []string `json:"vpcPrefixIDs"`
	} `json:"nico,omitzero"`
}
type NodePoolIntent struct {
	Name             string              `json:"name"`
	Prefix           string              `json:"nodeNamePrefix"`
	Count            int                 `json:"desiredNodeCount"`
	ControlPlane     bool                `json:"controlPlane"`
	SystemReserved   string              `json:"systemReservedMemory"`
	KubeReserved     string              `json:"kubeReservedMemory"`
	Teleport         bool                `json:"teleportEnabled"`
	DisableProvision bool                `json:"autoProvisionDisabled"`
	DisableAuto      bool                `json:"autoModeDisabled"`
	DisableMonitor   bool                `json:"provisioningMonitorDisabled"`
	Recycle          int                 `json:"recycleBudget"`
	Infra            *TenantMachineInfra `json:"infra,omitempty"`
}

type tenantRow struct {
	Name         string              `json:"name"`
	ID           string              `json:"id"`
	NICo         TenantProvider      `json:"nico"`
	Infra        *TenantClusterInfra `json:"infra,omitempty"`
	ControlPlane *ControlPlaneIntent `json:"controlPlaneConfig,omitempty"`
	Conditions   tenantConditions    `json:"conditions"`
}

// Retain the pinned API statuses used for progress and provisioning failure.
// Messages, transition times and unrelated response fields are not decoded.
type tenantConditions struct {
	Provisioning struct {
		Status TenantConditionStatus `json:"status"`
	} `json:"provisioning"`
	Provisioned struct {
		Status TenantConditionStatus `json:"status"`
	} `json:"provisioned"`
}

func selectedTenant(rows []tenantRow, name, id string) (*tenantRow, error) {
	selected, err := selectedTenantIdentity(rows, name, id)
	if err != nil || selected == nil {
		return selected, err
	}
	if selected.Infra != nil || selected.NICo != (TenantProvider{OrgID, SiteID, VPCID}) {
		return nil, errors.New("tenant identity/provider changed; refusing adoption or removal")
	}
	return selected, nil
}

func selectedTenantIdentity(rows []tenantRow, name, id string) (*tenantRow, error) {
	var selected *tenantRow
	for i := range rows {
		row := &rows[i]
		if row.Name != name {
			continue
		}
		if selected != nil {
			return nil, errors.New("ambiguous tenant name; refusing ownership")
		}
		selected = row
	}
	if selected == nil {
		return nil, nil
	}
	// NVKE owns ID interpretation. Treat its identifier as opaque; constrain it
	// only where it becomes a filename, and never invent a UUID parser.
	if !safeTenantIdentity(selected.ID) || id != "" && selected.ID != id {
		return nil, errors.New("tenant identity/provider changed; refusing adoption or removal")
	}
	return selected, nil
}

func safeTenantIdentity(id string) bool {
	return id != "" && !strings.ContainsAny(id, "/\\\x00\r\n\t ") && id != "." && id != ".."
}

type tenantNode struct {
	Metadata struct {
		Name   string            `json:"name"`
		UID    string            `json:"uid"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Unschedulable bool `json:"unschedulable"`
		Taints        []struct {
			Key    string `json:"key"`
			Effect string `json:"effect"`
		} `json:"taints"`
	} `json:"spec"`
	Status struct {
		Conditions []struct{ Type, Status string }
		NodeInfo   struct{ Architecture, KubeletVersion, BootID string }
	} `json:"status"`
}

func readyNodesForCountVersion(nodes []tenantNode, version string, expected int) (bool, error) {
	if expected != nativeTenantNodeCount(false) && expected != nativeTenantNodeCount(true) {
		return false, errors.New("unsupported tenant node count")
	}
	if !stableKubernetesVersion(version) {
		return false, errors.New("require the selected stable Kubernetes version")
	}
	if len(nodes) > expected {
		return false, errors.New("unexpected extra tenant node")
	}
	if len(nodes) != expected {
		return false, nil
	}
	control := 0
	available := true
	names := map[string]bool{}
	for _, node := range nodes {
		if node.Metadata.Name == "" || names[node.Metadata.Name] {
			return false, errors.New("require distinct named tenant nodes")
		}
		names[node.Metadata.Name] = true
		_, isControlPlane := node.Metadata.Labels["node-role.kubernetes.io/control-plane"]
		if isControlPlane {
			control++
		} else {
			// The workload targets workers. Node Ready may precede
			// source-owned post-join completion and removal of its provisioning
			// marker; wait without changing taints or adding Job tolerations.
			if node.Spec.Unschedulable {
				available = false
			}
			for _, taint := range node.Spec.Taints {
				if taint.Key == "nke.nvidia.com/provisioning" || taint.Effect == "NoSchedule" || taint.Effect == "NoExecute" {
					available = false
				}
			}
		}
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				ready = true
			}
		}
		if !ready {
			return false, nil
		}
		if node.Status.NodeInfo.Architecture != "arm64" || node.Status.NodeInfo.KubeletVersion != "v"+version {
			return false, fmt.Errorf("tenant node differs from required ARM64 Kubernetes%s", version)
		}
	}
	if control != 1 {
		return false, errors.New("require exactly one tenant control plane")
	}
	return available, nil
}
func pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type tenant struct {
	name, id   string
	cliPath    string
	session    ManagementSession
	intent     bool
	sequence   int
	cleaning   bool
	cleanup    tenantCleanupObservations
	nativeCAPI *nativeCAPITenantOwnership
}

func (t *tenant) capture(ctx context.Context, args []string, phase string, budget time.Duration) ([]byte, error) {
	t.sequence++
	runner := t.session.Runner
	// Commands retain their private numbered logs. Their exit status is not
	// provisioning success; report interpreted state from the owning wait loop.
	runner.Progress = nil
	if t.cleaning {
		return t.cleanup.capture(ctx, runner, args, phase, t.sequence, budget)
	}
	return runner.Capture(ctx, args, fmt.Sprintf("tenant-%03d-%s", t.sequence, phase), budget)
}
func (t *tenant) cli() string {
	if t.cliPath != "" {
		return t.cliPath
	}
	return filepath.Join(t.session.Work, "cli", installerCLIName)
}
func (t *tenant) inventory(ctx context.Context) ([]tenantRow, error) {
	raw, err := t.capture(ctx, []string{t.cli(), "get", "clusters", "-o", "json"}, "inventory", 30*time.Second)
	if err != nil {
		return nil, err
	}
	var rows []tenantRow
	err = json.Unmarshal(raw, &rows)
	return rows, err
}
func (t *tenant) row(ctx context.Context) (*tenantRow, error) {
	rows, err := t.inventory(ctx)
	if err != nil {
		return nil, err
	}
	if t.nativeCAPI != nil {
		return t.nativeCAPIRow(ctx, rows)
	}
	return selectedTenant(rows, t.name, t.id)
}

func (t *tenant) kubectl(config string, args ...string) []string {
	return append([]string{filepath.Join(t.session.Tools, "kubectl"), "--kubeconfig", config}, args...)
}

type workload struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Backoff  int `json:"backoffLimit"`
		Deadline int `json:"activeDeadlineSeconds"`
		Template struct {
			Spec struct {
				Restart    string `json:"restartPolicy"`
				Containers []struct {
					Name    string   `json:"name"`
					Image   string   `json:"image"`
					Command []string `json:"command"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func workloadJob(image string) workload {
	job := workload{APIVersion: "batch/v1", Kind: "Job"}
	job.Metadata.Name = "nvkr-smoke"
	job.Spec.Deadline = 180
	job.Spec.Template.Spec.Restart = "Never"
	job.Spec.Template.Spec.Containers = append(job.Spec.Template.Spec.Containers, struct {
		Name    string   `json:"name"`
		Image   string   `json:"image"`
		Command []string `json:"command"`
	}{"smoke", image, []string{"sh", "-ec", "test $(uname -m) = aarch64; nslookup kubernetes.default.svc.cluster.local; echo NVKE_LOCAL_WORKLOAD_PASS"}})
	return job
}

type domainEvidence struct {
	Name, UUID, BackingDigest string
	Disk                      json.RawMessage
}

func (t *tenant) restore(ctx context.Context) (result error) {
	t.cleaning = true
	defer func() {
		// Observation loss must not prevent owned deletion or hide its result.
		if err := t.cleanup.preserve(context.WithoutCancel(ctx), t.session.Work); err != nil && t.session.Runner.Progress != nil {
			fmt.Fprintln(t.session.Runner.Progress, "Some private tenant cleanup observations unavailable; owned cleanup result is unchanged")
		}
	}()
	if !t.intent {
		return nil
	}
	row, err := t.row(ctx)
	if err != nil {
		return err
	}
	if row == nil {
		return nil
	}
	t.id = row.ID
	deleteProgress := beginProgress(t.session.Runner.Progress, "tenant-delete", PhaseStarted, PhaseFinished, PhaseFailed)
	if t.nativeCAPI != nil {
		t.nativeCAPI.deleting = true
	}
	_, err = t.capture(ctx, []string{t.cli(), "delete", "cluster", t.name}, "delete", tenantDeleteTimeout)
	deleteProgress.finish(err)
	if err != nil {
		return err
	}
	waitProgress := beginProgress(t.session.Runner.Progress, "tenant-deletion-wait", PhaseStarted, PhaseFinished, PhaseFailed)
	defer func() { waitProgress.finish(result) }()
	return t.waitDeleted(ctx, tenantDeletionWaitTimeout)
}

func (t *tenant) waitDeleted(ctx context.Context, budget time.Duration) error {
	if budget <= 0 {
		return errors.New("positive tenant deletion wait budget required")
	}
	deadline, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for {
		row, err := t.row(deadline)
		if err != nil {
			return fmt.Errorf("NVKE tenant deletion did not finish: %w", err)
		}
		if row == nil {
			return nil
		}
		if err = pause(deadline, 5*time.Second); err != nil {
			return fmt.Errorf("NVKE tenant deletion did not finish: %w", err)
		}
	}
}
