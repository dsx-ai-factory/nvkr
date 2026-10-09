// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
)

// TenantRequest is deployment intent consumed and validated by the pinned NKE
// CLI. It is not an NKD Definition or an authenticated provider identity.
type TenantRequest struct {
	Name          string             `json:"name"`
	Kubernetes    string             `json:"controlPlaneKubernetesVersion"`
	CNI           string             `json:"cniPlugin"`
	CNIVersion    string             `json:"cniVersion"`
	EtcdEncrypted bool               `json:"etcdEncrypted"`
	VIP           string             `json:"controlPlaneVIP"`
	Teleport      bool               `json:"teleportEnabled"`
	NICo          TenantProvider     `json:"nico"`
	ControlPlane  ControlPlaneIntent `json:"controlPlaneConfig"`
	NodePools     []NodePoolIntent   `json:"nodePools"`
}
type TenantProvider struct {
	Org  string `json:"orgID"`
	Site string `json:"siteID"`
	VPC  string `json:"vpcID"`
}
type ControlPlaneIntent struct {
	Count          int    `json:"desiredCount"`
	Reserved       string `json:"systemReservedMemory"`
	Teleport       bool   `json:"teleportEnabled"`
	DisableMonitor bool   `json:"provisioningMonitorDisabled"`
	NICo           struct {
		Profile  string   `json:"configProfile"`
		OS       string   `json:"osProfile"`
		Types    []string `json:"instanceTypePreference"`
		Prefixes []string `json:"vpcPrefixIDs"`
	} `json:"nico"`
}
type NodePoolIntent struct {
	Name             string `json:"name"`
	Prefix           string `json:"nodeNamePrefix"`
	Count            int    `json:"desiredNodeCount"`
	ControlPlane     bool   `json:"controlPlane"`
	SystemReserved   string `json:"systemReservedMemory"`
	KubeReserved     string `json:"kubeReservedMemory"`
	Teleport         bool   `json:"teleportEnabled"`
	DisableProvision bool   `json:"autoProvisionDisabled"`
	DisableAuto      bool   `json:"autoModeDisabled"`
	DisableMonitor   bool   `json:"provisioningMonitorDisabled"`
	Recycle          int    `json:"recycleBudget"`
	NICo             struct {
		Profile  string   `json:"configProfile"`
		OS       string   `json:"osProfile"`
		Type     string   `json:"instanceTypeID"`
		Prefixes []string `json:"vpcPrefixIDs"`
	} `json:"nico"`
}

func NewTenantRequest(name string) TenantRequest {
	request := TenantRequest{Name: name, Kubernetes: "1.34.2", CNI: "ovn-kubernetes", CNIVersion: "1.4.0", VIP: TenantVIP, NICo: TenantProvider{OrgID, SiteID, VPCID}}
	request.ControlPlane.Count = 1
	request.ControlPlane.Reserved = "1Gi"
	request.ControlPlane.DisableMonitor = true
	request.ControlPlane.NICo.Profile = "nico-vms-cp"
	request.ControlPlane.NICo.OS = TenantProfile
	request.ControlPlane.NICo.Types = []string{TenantInstanceType}
	request.ControlPlane.NICo.Prefixes = []string{TenantPrefixID}
	pool := NodePoolIntent{Name: "worker", Prefix: "w", Count: 1, SystemReserved: "1Gi", KubeReserved: "1Gi", DisableAuto: true, DisableMonitor: true}
	pool.NICo.Profile = "nico-vms-worker"
	pool.NICo.OS = TenantProfile
	pool.NICo.Type = TenantInstanceType
	pool.NICo.Prefixes = []string{TenantPrefixID}
	request.NodePools = []NodePoolIntent{pool}
	return request
}

type tenantRow struct {
	Name string         `json:"name"`
	ID   string         `json:"id"`
	NICo TenantProvider `json:"nico"`
}

func selectedTenant(rows []tenantRow, name, id string) (*tenantRow, error) {
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
	// NKE owns ID interpretation. Treat its identifier as opaque; constrain it
	// only where it becomes a filename, and never invent a UUID parser.
	if selected.ID == "" || strings.ContainsAny(selected.ID, "/\\\x00\r\n\t ") || selected.ID == "." || selected.ID == ".." || selected.NICo != (TenantProvider{OrgID, SiteID, VPCID}) || id != "" && selected.ID != id {
		return nil, errors.New("tenant identity/provider changed; refusing adoption or removal")
	}
	return selected, nil
}

type tenantNode struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Status struct {
		Conditions []struct{ Type, Status string }
		NodeInfo   struct{ Architecture, KubeletVersion string }
	} `json:"status"`
}

func readyNodes(nodes []tenantNode) (bool, error) {
	if len(nodes) > 2 {
		return false, errors.New("unexpected extra tenant node")
	}
	if len(nodes) != 2 {
		return false, nil
	}
	control := 0
	names := map[string]bool{}
	for _, node := range nodes {
		if node.Metadata.Name == "" || names[node.Metadata.Name] {
			return false, errors.New("require two distinct named tenant nodes")
		}
		names[node.Metadata.Name] = true
		if _, ok := node.Metadata.Labels["node-role.kubernetes.io/control-plane"]; ok {
			control++
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
		if node.Status.NodeInfo.Architecture != "arm64" || node.Status.NodeInfo.KubeletVersion != "v1.34.2" {
			return false, errors.New("tenant node differs from required ARM64 Kubernetes1.34.2")
		}
	}
	if control != 1 {
		return false, errors.New("require exactly one tenant control plane")
	}
	return true, nil
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
	name, id      string
	session       ManagementSession
	simulator     *OwnedProcess
	image         lifecycle.Artifact
	domains       Runner
	diagnosticURL string
	intent        bool
	sequence      int
	cleaning      bool
}

func (t *tenant) capture(ctx context.Context, args []string, phase string, budget time.Duration) ([]byte, error) {
	t.sequence++
	runner := t.session.Runner
	if t.cleaning {
		runner.Work = ""
	}
	return runner.Capture(ctx, args, fmt.Sprintf("tenant-%03d-%s", t.sequence, phase), budget)
}
func (t *tenant) cli() string { return filepath.Join(t.session.Work, "cli", "nkectl") }
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
	return selectedTenant(rows, t.name, t.id)
}
func (t *tenant) alive(ctx context.Context) error {
	if exited, _ := t.simulator.Exited(); exited {
		return errors.New("NICoSim exited during tenant provisioning")
	}
	row, err := t.row(ctx)
	if err != nil {
		return err
	}
	if row == nil {
		return errors.New("created tenant disappeared")
	}
	return nil
}
func (t *tenant) kubectl(config string, args ...string) []string {
	return append([]string{filepath.Join(t.session.Tools, "kubectl"), "--kubeconfig", config}, args...)
}

func (t *tenant) run(ctx context.Context, budget time.Duration) error {
	rows, err := t.inventory(ctx)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return errors.New("require an empty freshly installed site before tenant creation")
	}
	request := filepath.Join(t.session.Work, "tenant-request.json")
	if err = privateJSON(request, NewTenantRequest(t.name)); err != nil {
		return err
	}
	t.intent = true
	if _, err = t.capture(ctx, []string{t.cli(), "create", "cluster", "-f", request}, "create", 120*time.Second); err != nil {
		return err
	}
	row, err := t.row(ctx)
	if err != nil {
		return err
	}
	if row == nil {
		return errors.New("NKE did not expose the exact created tenant")
	}
	t.id = row.ID
	if t.session.Runner.Progress != nil {
		fmt.Fprintln(t.session.Runner.Progress, "Tenant created; waiting for real QCOW2 control plane and worker")
	}
	provision, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	kubeconfig := filepath.Join(t.session.Work, "tenant-kubeconfig")
	for {
		if err = t.alive(provision); err != nil {
			return err
		}
		raw, readErr := t.capture(provision, t.kubectl(environmentValue(t.session.Runner.Env, "SITE_KUBECONFIG"), "-n", "nke-system", "get", "secret", t.name+"-admin-kubeconfig", "--ignore-not-found", "-o", "json"), "kubeconfig-secret", 30*time.Second)
		if readErr != nil {
			return readErr
		}
		var secret json.RawMessage
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &secret); err != nil {
				return err
			}
		}
		if len(secret) > 0 && string(secret) != "null" {
			if _, err = t.capture(provision, []string{t.cli(), "get", "kubeconfig", t.name, "--output-file", kubeconfig}, "kubeconfig", 30*time.Second); err != nil {
				return err
			}
			break
		}
		if err = pause(provision, 10*time.Second); err != nil {
			return fmt.Errorf("tenant provisioning budget: %w", err)
		}
	}
	raw, err := t.capture(provision, t.kubectl(kubeconfig, "config", "view", "-o", "json"), "tenant-config", 30*time.Second)
	if err != nil {
		return err
	}
	var config struct {
		Clusters []struct{ Cluster struct{ Server string } }
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		return err
	}
	if len(config.Clusters) != 1 || config.Clusters[0].Cluster.Server != "https://"+TenantVIP+":6443" {
		return errors.New("tenant kubeconfig does not select the admitted VM VIP")
	}
	var nodes struct{ Items []tenantNode }
	for {
		if err = t.alive(provision); err != nil {
			return err
		}
		raw, err = t.capture(provision, t.kubectl(kubeconfig, "--request-timeout=10s", "get", "nodes", "-o", "json"), "nodes", 30*time.Second)
		if err != nil {
			if CommandExitCode(err) != 1 {
				return err
			}
		} else {
			if err = json.Unmarshal(raw, &nodes); err != nil {
				return err
			}
			ready, checkErr := readyNodes(nodes.Items)
			if checkErr != nil {
				return checkErr
			}
			if ready {
				break
			}
		}
		if err = pause(provision, 10*time.Second); err != nil {
			return fmt.Errorf("two-node provisioning budget: %w", err)
		}
	}
	domains, err := vmEvidence(ctx, filepath.Join(filepath.Dir(t.session.Work), "simulator"), t.image, t.domains)
	if err != nil {
		return err
	}
	job := filepath.Join(t.session.Work, "workload.json")
	if err = privateJSON(job, workloadJob()); err != nil {
		return err
	}
	if _, err = t.capture(ctx, t.kubectl(kubeconfig, "apply", "-f", job), "workload-apply", 30*time.Second); err != nil {
		return err
	}
	if _, err = t.capture(ctx, t.kubectl(kubeconfig, "wait", "job/nkd-smoke", "--for=condition=Complete", "--timeout=210s"), "workload-complete", 240*time.Second); err != nil {
		return err
	}
	raw, err = t.capture(ctx, t.kubectl(kubeconfig, "logs", "job/nkd-smoke"), "workload-log", 30*time.Second)
	if err != nil {
		return err
	}
	if !slices.Contains(strings.Split(strings.TrimSpace(string(raw)), "\n"), "NKE_LOCAL_WORKLOAD_PASS") {
		return errors.New("workload did not report ARM64 execution and cluster DNS success")
	}
	raw, err = t.capture(ctx, t.kubectl(kubeconfig, "get", "pods", "-l", "job-name=nkd-smoke", "-o", "json"), "workload-pod", 30*time.Second)
	if err != nil {
		return err
	}
	var pods struct {
		Items []struct {
			Status struct {
				Containers []struct{ ImageID string } `json:"containerStatuses"`
			}
		}
	}
	if err = json.Unmarshal(raw, &pods); err != nil {
		return err
	}
	var ids []string
	for _, pod := range pods.Items {
		for _, container := range pod.Status.Containers {
			ids = append(ids, container.ImageID)
		}
	}
	if len(ids) != 1 || strings.Split(ids[0], "@")[len(strings.Split(ids[0], "@"))-1] != strings.Split(WorkloadImage, "@")[1] {
		return errors.New("workload image ID differs from pinned diagnostic acquisition")
	}
	return privateJSON(filepath.Join(t.session.Work, "tenant-result.json"), struct {
		Scope, Name, TenantID string
		Nodes                 []tenantNode
		Domains               []domainEvidence
		Workload              json.RawMessage
	}{"unqualified-local-demo", t.name, t.id, nodes.Items, domains, raw})
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

func workloadJob() workload {
	job := workload{APIVersion: "batch/v1", Kind: "Job"}
	job.Metadata.Name = "nkd-smoke"
	job.Spec.Deadline = 180
	job.Spec.Template.Spec.Restart = "Never"
	job.Spec.Template.Spec.Containers = append(job.Spec.Template.Spec.Containers, struct {
		Name    string   `json:"name"`
		Image   string   `json:"image"`
		Command []string `json:"command"`
	}{"smoke", WorkloadImage, []string{"sh", "-ec", "test $(uname -m) = aarch64; nslookup kubernetes.default.svc.cluster.local; echo NKE_LOCAL_WORKLOAD_PASS"}})
	return job
}

type domainEvidence struct {
	Name, UUID, BackingDigest string
	Disk                      json.RawMessage
}

func vmEvidence(ctx context.Context, work string, image lifecycle.Artifact, runner Runner) ([]domainEvidence, error) {
	runner.Work = ""
	names, err := DomainInventory(ctx, runner)
	if err != nil {
		return nil, err
	}
	if len(names) != 2 {
		return nil, errors.New("require exactly two real simulator-owned libvirt domains")
	}
	slices.Sort(names)
	var result []domainEvidence
	for _, name := range names {
		binding, readErr := OwnedDomain(ctx, runner, name, work)
		if readErr != nil {
			return nil, readErr
		}
		if binding == nil {
			return nil, errors.New("tenant domain is not owned by this simulator directory")
		}
		var domain struct {
			TPM []struct{} `xml:"devices>tpm"`
			OS  struct {
				Type string `xml:"type"`
			} `xml:"os"`
		}
		if err = xml.Unmarshal([]byte(binding.XML), &domain); err != nil {
			return nil, err
		}
		if len(domain.TPM) == 0 || domain.OS.Type == "" {
			return nil, errors.New("tenant domain lacks real guest/TPM configuration")
		}
		disk := filepath.Join(work, "var", "instances", strings.TrimPrefix(name, "nico-vm-"), "disk.qcow2")
		raw, readErr := runner.Capture(ctx, []string{"qemu-img", "info", "--force-share", "--output=json", disk}, "tenant-disk-inspection", 30*time.Second)
		if readErr != nil {
			return nil, readErr
		}
		var info struct {
			Backing string `json:"full-backing-filename"`
		}
		if err = json.Unmarshal(raw, &info); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(info.Backing) {
			return nil, errors.New("tenant backing is not an absolute observed path")
		}
		base, readErr := resolveExisting(info.Backing)
		if readErr != nil {
			return nil, readErr
		}
		original, readErr := os.Lstat(info.Backing)
		if readErr != nil {
			return nil, readErr
		}
		if original.Mode()&os.ModeSymlink != 0 || !insidePath(base, filepath.Join(work, "cache")) || image.Disk == nil || original.Size() != image.Disk.Size {
			return nil, errors.New("tenant overlay does not use exact downloaded NKD QCOW2")
		}
		digest, readErr := DigestFile(ctx, base)
		if readErr != nil {
			return nil, readErr
		}
		if digest != image.Digest {
			return nil, errors.New("tenant backing disk digest differs")
		}
		result = append(result, domainEvidence{name, binding.UUID, digest, raw})
	}
	return result, nil
}

func (t *tenant) preserve(ctx context.Context) error {
	if !t.intent {
		return nil
	}
	names, err := DomainInventory(ctx, t.domains)
	if err != nil {
		return err
	}
	if len(names) > 2 {
		return errors.New("diagnostic inventory exceeds two-VM contract")
	}
	simulator := filepath.Join(filepath.Dir(t.session.Work), "simulator")
	for _, name := range names {
		binding, readErr := OwnedDomain(ctx, t.domains, name, simulator)
		if readErr != nil {
			return readErr
		}
		if binding == nil {
			continue
		}
		instance := strings.TrimPrefix(name, "nico-vm-")
		for _, channel := range []string{"ttyS0", "ttyS1"} {
			file, info, readErr := securepath.OpenRegular(filepath.Join(simulator, "var", "instances", instance, "disk.qcow2."+channel+".log"))
			if readErr != nil {
				continue
			}
			if !currentOwner(info) {
				_ = file.Close()
				continue
			}
			start := max(int64(0), info.Size()-(4<<20))
			_, readErr = file.Seek(start, io.SeekStart)
			var raw []byte
			if readErr == nil {
				raw, readErr = io.ReadAll(io.LimitReader(file, 4<<20))
			}
			_ = file.Close()
			if readErr == nil {
				_ = privateWrite(filepath.Join(t.session.Work, instance+"."+channel+".log"), raw)
			}
		}
		if t.diagnosticURL == "" {
			continue
		}
		header := filepath.Join(filepath.Dir(t.session.Work), "nico-api-header")
		if readErr = privateHeader(header); readErr != nil {
			continue
		}
		for _, item := range []string{"agent-logs", "kubelet-logs", "containerd-logs"} {
			raw, readErr := t.capture(ctx, []string{"curl", "-q", "--noproxy", "*", "--max-time", "30", "--max-filesize", "1048576", "--fail", "--silent", "--show-error", "--header", "@" + header, t.diagnosticURL + "/v1/orgs/" + OrgID + "/instances/" + instance + "/diagnostics/" + item}, "diagnostic-"+instance+"-"+item, 32*time.Second)
			if errors.Is(readErr, ErrUnjoined) {
				return readErr
			}
			if readErr == nil && len(raw) <= 1<<20 {
				_ = privateWrite(filepath.Join(t.session.Work, instance+"."+item+".log"), raw)
			}
		}
	}
	return nil
}
func (t *tenant) restore(ctx context.Context) error {
	t.cleaning = true
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
	if _, err = t.capture(ctx, []string{t.cli(), "delete", "cluster", t.name}, "delete", 120*time.Second); err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	for {
		row, err = t.row(deadline)
		if err != nil {
			return err
		}
		if row == nil {
			return nil
		}
		if err = pause(deadline, 5*time.Second); err != nil {
			return fmt.Errorf("NKE tenant deletion did not finish: %w", err)
		}
	}
}
func (t *tenant) close(ctx context.Context) error {
	diagnosticErr := t.preserve(ctx)
	if errors.Is(diagnosticErr, ErrUnjoined) {
		return diagnosticErr
	}
	if diagnosticErr != nil && t.session.Runner.Progress != nil {
		fmt.Fprintln(t.session.Runner.Progress, "Some private guest diagnostics unavailable; owned cleanup still proceeds")
	}
	return t.restore(ctx)
}

func consumeTenant(ctx context.Context, cfg Config, guard *NetworkGuard, session ManagementSession, server *OwnedProcess, image lifecycle.Artifact, domains Runner, url string) (result error) {
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	t := tenant{name: "nkd-tenant-" + hex.EncodeToString(random[:]), session: session, simulator: server, image: image, domains: domains, diagnosticURL: url}
	ids, err := ownedManagementNodes(ctx, session.Runner, session.Name)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return errors.New("guest SPIRE route requires one owned management node")
	}
	rows, err := captureJSON[[]dockerContainer](ctx, session.Runner, []string{"docker", "inspect", ids[0]}, "guest-spire-endpoint")
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].ID != ids[0] {
		return errors.New("management node changed before guest route admission")
	}
	defer func() {
		if errors.Is(result, ErrUnjoined) {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Minute)
		defer cancel()
		closeErr := t.close(cleanup)
		result = errors.Join(result, closeErr)
		if errors.Is(closeErr, ErrUnjoined) {
			return
		}
		if withdrawErr := guard.RevokeGuestTCP(cleanup); withdrawErr != nil {
			result = errors.Join(result, ErrRetainedControls, withdrawErr)
		}
	}()
	if err = guard.AllowGuestTCP(ctx, rows[0].NetworkSettings.Networks["kind"].IPAddress, []int{30440, 30443}); err != nil {
		return err
	}
	return t.run(ctx, cfg.Timeout)
}
