//go:build integration

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tektonkind

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationpolicy"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releasemanifest"
	"sigs.k8s.io/yaml"
)

type pins struct {
	KindVersion, NodeImage, TektonVersion, TektonURL, TektonSHA256 string
	RegistryImage, RunnerBase, RuntimeImage                        string
}

type lab struct {
	t                                                                                     *testing.T
	root, scratch, name, node, kubeconfig, dockerConfig, dockerHost, dockerUser, evidence string
	pins                                                                                  pins
}

// TestKindCore runs the maintained core Definition's complete AMD64 selection,
// including its image-bound chart, through the actual generated Tekton runner.
// It fails (never skips) when this explicit acceptance test cannot execute.
func TestKindCore(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 140*time.Minute)
	defer cancel()
	_, source, _, _ := runtime.Caller(0)
	l := &lab{t: t, root: filepath.Clean(filepath.Join(filepath.Dir(source), "../..")), scratch: t.TempDir()}
	// TempDir names can contain uppercase test names; Kubernetes names cannot.
	l.name = "nvkr-tekton-" + fmt.Sprintf("%x", sha256.Sum256([]byte(l.scratch)))[:12]
	l.node, l.kubeconfig = l.name+"-control-plane", filepath.Join(l.scratch, "kubeconfig")
	l.dockerConfig = filepath.Join(l.scratch, "docker")
	must(t, os.Mkdir(l.dockerConfig, 0700))
	// Preserve the selected daemon endpoint without importing registry auth
	// from its context. kind and every Docker invocation use this same socket.
	if os.Getenv("DOCKER_CONTEXT") == "" && os.Getenv("DOCKER_HOST") != "" {
		l.dockerHost = os.Getenv("DOCKER_HOST")
	} else {
		l.dockerHost = strings.TrimSpace(string(l.command(ctx, nil, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")))
	}
	if !strings.HasPrefix(l.dockerHost, "unix:///") || strings.ContainsAny(l.dockerHost, "\r\n\x00") {
		t.Fatal("kind acceptance requires the explicitly selected local Unix Docker socket")
	}
	l.evidence = os.Getenv("NVKR_TEKTON_EVIDENCE")
	if l.evidence == "" {
		l.evidence = filepath.Join(l.scratch, "evidence")
	}
	must(t, os.MkdirAll(l.evidence, 0700))
	must(t, json.Unmarshal(read(t, filepath.Join(l.root, "toolchains/tekton-kind/pins.json")), &l.pins))
	var info struct {
		Architecture    string
		MemTotal        int64
		NCPU            int
		SecurityOptions []string
	}
	must(t, json.Unmarshal(l.docker(ctx, nil, "info", "--format", "{{json .}}"), &info))
	l.dockerUser = keyGenerationUser(info.SecurityOptions, os.Getuid(), os.Getgid())
	if info.Architecture != "x86_64" && info.Architecture != "amd64" {
		t.Fatalf("native core acceptance requires an AMD64 Docker daemon; got %s (emulation is not admitted)", info.Architecture)
	}
	if info.MemTotal < 6<<30 {
		t.Fatalf("core acceptance requires at least 6 GiB Docker memory; got %d bytes", info.MemTotal)
	}
	if info.NCPU < 4 {
		t.Fatalf("core acceptance requires at least four Docker CPUs for the two-CPU build pods and cluster overhead; got %d", info.NCPU)
	}
	version := strings.Fields(string(l.command(ctx, nil, "kind", "version")))
	if len(version) < 2 || version[1] != l.pins.KindVersion {
		t.Fatalf("kind must be exactly %s", l.pins.KindVersion)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	must(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	must(t, listener.Close())
	config := fmt.Sprintf("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n- role: control-plane\n  extraPortMappings:\n  - containerPort: 32000\n    hostPort: %d\n    listenAddress: 127.0.0.1\n", port)
	configPath := filepath.Join(l.scratch, "kind.yaml")
	must(t, os.WriteFile(configPath, []byte(config), 0600))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Minute)
		defer stop()
		if _, err := l.invoke(cleanup, nil, "kind", "delete", "cluster", "--name", l.name, "--kubeconfig", l.kubeconfig); err != nil {
			t.Errorf("cleaning owned kind cluster: %v", err)
		}
	})
	l.command(ctx, nil, "kind", "create", "cluster", "--name", l.name, "--image", l.pins.NodeImage, "--config", configPath, "--kubeconfig", l.kubeconfig, "--wait", "180s")
	t.Cleanup(func() { l.capture() })
	l.kube(ctx, nil, "create", "namespace", "nvkr")
	l.kube(ctx, l.download(ctx, l.pins.TektonURL, l.pins.TektonSHA256, 32<<20), "apply", "-f", "-")
	l.kube(ctx, nil, "-n", "tekton-pipelines", "rollout", "status", "deployment/tekton-pipelines-controller", "--timeout=300s")
	l.kube(ctx, nil, "-n", "tekton-pipelines", "rollout", "status", "deployment/tekton-pipelines-webhook", "--timeout=300s")
	l.kube(ctx, []byte(fmt.Sprintf(registryResources, l.pins.RegistryImage)), "apply", "-f", "-")
	l.kube(ctx, nil, "-n", "nvkr", "wait", "--for=condition=Ready", "pod/registry", "--timeout=180s")
	hostRegistry := fmt.Sprintf("127.0.0.1:%d", port)
	registryHosts := "/etc/containerd/certs.d/" + hostRegistry
	l.docker(ctx, nil, "exec", l.node, "mkdir", "-p", registryHosts)
	l.docker(ctx, []byte("server = \"http://localhost:32000\"\n[host.\"http://localhost:32000\"]\n  capabilities = [\"pull\", \"resolve\"]\n"), "exec", "-i", l.node, "tee", registryHosts+"/hosts.toml")
	image, cosign := l.runner(ctx, hostRegistry)
	keys := l.keys(ctx, image, cosign)
	ca := l.kube(ctx, nil, "-n", "nvkr", "get", "configmap", "kube-root-ca.crt", "-o", "jsonpath={.data.ca\\.crt}")
	plan := l.corePlan(ctx, image, ca, keys)
	bundle, err := factory.Render(ctx, plan, factory.EngineTekton)
	must(t, err)
	generated := filepath.Join(l.scratch, "generated")
	must(t, os.Mkdir(generated, 0700))
	must(t, factory.WriteGeneratedBundle(ctx, generated, bundle))
	must(t, factory.CheckGenerated(ctx, plan, factory.EngineTekton, generated))
	var resources struct {
		Items []json.RawMessage `json:"items"`
	}
	must(t, json.Unmarshal(bundle.Files[bundle.WorkflowPath], &resources))
	var run []byte
	for _, item := range resources.Items {
		var header struct{ Kind string }
		must(t, json.Unmarshal(item, &header))
		if header.Kind == "PipelineRun" {
			run = item
		} else {
			l.kube(ctx, item, "apply", "--server-side", "--field-manager=nvkr-kind", "-f", "-")
		}
	}
	if run == nil {
		t.Fatal("generated resources omit PipelineRun")
	}
	created := l.kube(ctx, run, "create", "-f", "-", "-o", "json")
	var identity struct{ Metadata struct{ Name, UID string } }
	must(t, json.Unmarshal(created, &identity))
	l.waitRun(ctx, identity.Metadata.Name, true)
	// Copy only the final carrier using a read-only PVC reader. The finalizer
	// has already re-downloaded and verified all published bytes and signatures.
	manifest := l.result(ctx, identity.Metadata.Name, image)
	decoded, err := releasemanifest.Decode(manifest)
	must(t, err)
	if decoded.Lane != "pipeline-attested" || decoded.Source.Provider != "tekton" || len(decoded.Members) != len(plan.LifecycleOutputs) {
		t.Fatal("final manifest does not cover the complete native core")
	}
	if !strings.Contains(decoded.Source.InvocationID, identity.Metadata.UID) {
		t.Fatal("manifest does not bind the actual PipelineRun UID")
	}
	must(t, os.WriteFile(filepath.Join(l.evidence, "manifest.json"), manifest, 0600))
	// Fault cases reuse this exact generated execution and the live API receipt.
	l.checkCarriers(ctx, identity.Metadata.Name, identity.Metadata.UID, run, bundle.PlanDigest, image)
	l.checkTerminalFailure(ctx, run)
}

func (l *lab) corePlan(ctx context.Context, image string, ca []byte, keys map[string]string) *factory.Plan {
	l.t.Chdir(l.root)
	definitionPath := "inputs/definitions/nvke/core.yaml"
	environmentPath := "inputs/environments/github-nvke-core.yaml"
	d, err := lifecycle.LoadDefinition(ctx, definitionPath)
	must(l.t, err)
	env, err := lifecycle.LoadEnvironment(ctx, environmentPath)
	must(l.t, err)
	var outputs []definition.DefinitionOutput
	for _, output := range d.Outputs {
		if len(output.Platforms) > 0 {
			output.Platforms = nil
			output.Platform = "linux/amd64"
		}
		if output.Platform == "linux/amd64" {
			outputs = append(outputs, output)
		}
	}
	d.Outputs = outputs
	env.GitHubBundle = ""
	env.GitHubRunners = nil
	env.SourceAccess = nil
	env.Publication = &definition.Publication{Mode: "per-output-v1", RepositoryPrefix: "registry.nvkr.svc.cluster.local:5000/core", Anonymous: true, PlainHTTP: true}
	policyRaw := read(l.t, filepath.Join(l.root, "inputs/policies/nvke-qualification.yaml"))
	var hosted lifecycle.QualificationPolicy
	must(l.t, yaml.UnmarshalStrict(policyRaw, &hosted))
	smoke := qualificationpolicy.Smoke{Images: map[string]string{"linux/amd64": hosted.Smoke.Images["linux/amd64"]}, Tools: map[string]string{"nvkectl": "nvkectl-version-json", "helm": "helm-version", "kubectl": "kubectl-client-version-json"}}
	for _, output := range outputs {
		if output.Format == "oci" {
			smoke.NonToolOutputs = append(smoke.NonToolOutputs, qualificationpolicy.NonToolOutput{Output: output.Name, Kind: "oci", Components: output.Components, Reason: "deployment-image"})
		}
	}
	env.Tekton = &definition.TektonExecution{Namespace: "nvkr", Platform: "linux/amd64", APIServer: "https://kubernetes.default.svc", CertificateAuthority: string(ca), RunnerImage: image, RuntimeImage: l.pins.RuntimeImage, StorageClass: "standard", WorkspaceGiB: 60, PublisherPublicKey: keys["publisher"], SmokePublicKey: keys["smoke"], QualifierPublicKey: keys["qualifier"], Smoke: smoke}
	env.Signing = &definition.Signing{Mode: "cosign", Issuer: env.Tekton.APIServer, Identity: "^" + regexp.QuoteMeta(env.Tekton.APIServer+"/api/v1/namespaces/nvkr/serviceaccounts/nvkr-publisher") + "$"}
	plan, err := lifecycle.Compile(ctx, definitionPath, d, environmentPath, env, "tekton-kind-acceptance")
	must(l.t, err)
	must(l.t, lifecycle.ValidateTektonPlan(plan))
	return plan
}

func (l *lab) runner(ctx context.Context, registry string) (string, string) {
	build := filepath.Join(l.scratch, "runner")
	must(l.t, os.Mkdir(build, 0700))
	must(l.t, os.WriteFile(filepath.Join(build, "Dockerfile"), read(l.t, filepath.Join(l.root, "toolchains/tekton-kind/Dockerfile")), 0600))
	goPath, err := exec.LookPath("go")
	must(l.t, err)
	bounded, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(bounded, goPath, "build", "-p", "1", "-trimpath", "-o", filepath.Join(build, "nvkr"), "./cmd/nvkr")
	cmd.Dir = l.root
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOMAXPROCS=2", "GOWORK=off", "GOFLAGS=", "GOENV=off")
	cmd.WaitDelay = 5 * time.Second
	var output limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err = cmd.Run()
	if err != nil {
		l.t.Fatalf("building native runner: %v\n%s", err, output.Bytes())
	}
	image := registry + "/runner:" + l.name
	l.docker(ctx, nil, "build", "--platform", "linux/amd64", "--tag", image, build)
	l.docker(ctx, nil, "push", image)
	digest := strings.TrimSpace(string(l.docker(ctx, nil, "image", "inspect", image, "--format", "{{index .RepoDigests 0}}")))
	var lock struct {
		Pins map[string]struct {
			Tool struct{ URL, ArchiveDigest string }
		}
	}
	must(l.t, yaml.Unmarshal(read(l.t, filepath.Join(l.root, "inputs/catalogs/nvke/module-tags/pins.lock.yaml")), &lock))
	tool := lock.Pins["cosign"].Tool
	cosign := filepath.Join(l.scratch, "cosign")
	must(l.t, os.WriteFile(cosign, l.download(ctx, tool.URL, strings.TrimPrefix(tool.ArchiveDigest, "sha256:"), 256<<20), 0700))
	return digest, cosign
}

// Rootless Docker maps container root to the daemon owner. A numeric host UID
// would instead select a subordinate UID that cannot access the private mounts.
func keyGenerationUser(securityOptions []string, uid, gid int) string {
	for _, option := range securityOptions {
		if option == "name=rootless" {
			return "0:0"
		}
	}
	return fmt.Sprintf("%d:%d", uid, gid)
}

func (l *lab) keys(ctx context.Context, image, cosign string) map[string]string {
	directory := filepath.Join(l.scratch, "keys")
	must(l.t, os.Mkdir(directory, 0700))
	keys := map[string]string{}
	for _, role := range []string{"publisher", "smoke", "qualifier"} {
		l.docker(ctx, nil, "run", "--rm", "--network=none", "--user", l.dockerUser, "--entrypoint=/cosign", "-e", "COSIGN_PASSWORD=", "-v", cosign+":/cosign:ro", "-v", directory+":/keys", image, "generate-key-pair", "--output-key-prefix", "/keys/"+role)
		keys[role] = string(read(l.t, filepath.Join(directory, role+".pub")))
		private := read(l.t, filepath.Join(directory, role+".key"))
		secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]string{"name": "nvkr-" + role + "-key", "namespace": "nvkr"}, "type": "Opaque", "data": map[string]string{"private.pem": base64.StdEncoding.EncodeToString(private)}}
		clear(private)
		raw, err := json.Marshal(secret)
		must(l.t, err)
		// Do not include admission diagnostics that might quote Secret bytes.
		_, err = l.invoke(ctx, raw, "docker", l.dockerArgs("exec", "-i", l.node, "kubectl", "--kubeconfig=/etc/kubernetes/admin.conf", "create", "-f", "-")...)
		clear(raw)
		if err != nil {
			l.t.Fatal("creating scoped test signing Secret failed")
		}
	}
	return keys
}

func (l *lab) waitRun(ctx context.Context, name string, success bool) {
	nextReport := time.Time{}
	for {
		raw := l.kube(ctx, nil, "-n", "nvkr", "get", "pipelinerun", name, "-o", "json")
		var run struct {
			Status struct {
				Conditions []struct{ Type, Status, Reason, Message string }
			}
		}
		must(l.t, json.Unmarshal(raw, &run))
		if !time.Now().Before(nextReport) {
			l.t.Logf("PipelineRun %s pods:\n%s", name, l.kube(ctx, nil, "-n", "nvkr", "get", "pods", "-l", "tekton.dev/pipelineRun="+name))
			nextReport = time.Now().Add(time.Minute)
		}
		for _, condition := range run.Status.Conditions {
			if condition.Type == "Succeeded" && condition.Status != "Unknown" {
				if (condition.Status == "True") != success {
					l.t.Fatalf("PipelineRun %s: %s: %s", name, condition.Reason, condition.Message)
				}
				return
			}
		}
		select {
		case <-ctx.Done():
			l.t.Fatal(ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}

func (l *lab) kube(ctx context.Context, input []byte, args ...string) []byte {
	return l.docker(ctx, input, append([]string{"exec", "-i", l.node, "kubectl", "--kubeconfig=/etc/kubernetes/admin.conf"}, args...)...)
}
func (l *lab) dockerArgs(args ...string) []string {
	return append([]string{"--config", l.dockerConfig, "--host", l.dockerHost}, args...)
}
func (l *lab) docker(ctx context.Context, input []byte, args ...string) []byte {
	return l.command(ctx, input, "docker", l.dockerArgs(args...)...)
}
func (l *lab) command(ctx context.Context, input []byte, name string, args ...string) []byte {
	l.t.Helper()
	raw, err := l.invoke(ctx, input, name, args...)
	if err != nil {
		l.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, raw)
	}
	return raw
}
func (l *lab) invoke(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(bounded, name, args...)
	cmd.Dir = l.root
	if l.dockerHost != "" {
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+l.dockerHost, "DOCKER_CONTEXT=", "DOCKER_CONFIG="+l.dockerConfig, "DOCKER_TLS_VERIFY=", "DOCKER_CERT_PATH=")
	}
	cmd.Stdin = bytes.NewReader(input)
	cmd.WaitDelay = 5 * time.Second
	var output limitedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.Bytes(), err
}

type limitedBuffer struct{ bytes.Buffer }

func (buffer *limitedBuffer) Write(raw []byte) (int, error) {
	if buffer.Len()+len(raw) > 16<<20 {
		return 0, fmt.Errorf("acceptance command output exceeds 16 MiB")
	}
	return buffer.Buffer.Write(raw)
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	must(t, err)
	return raw
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (l *lab) download(ctx context.Context, address, digest string, limit int64) []byte {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(bounded, http.MethodGet, address, nil)
	must(l.t, err)
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 5 {
			return fmt.Errorf("untrusted download redirect")
		}
		return nil
	}}
	response, err := client.Do(request)
	must(l.t, err)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		l.t.Fatalf("pinned download returned %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	must(l.t, err)
	sum := sha256.Sum256(raw)
	if int64(len(raw)) > limit {
		l.t.Fatalf("pinned download %s exceeds %d-byte limit", address, limit)
	}
	if actual := hex.EncodeToString(sum[:]); actual != digest {
		l.t.Fatalf("pinned download %s failed exact byte verification: got %s, want %s", address, actual, digest)
	}
	return raw
}

func (l *lab) capture() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, kind := range []string{"pipelineruns", "taskruns", "pods", "events"} {
		raw, err := l.invoke(ctx, nil, "docker", l.dockerArgs("exec", l.node, "kubectl", "--kubeconfig=/etc/kubernetes/admin.conf", "-n", "nvkr", "get", kind, "-o", "json")...)
		if err == nil {
			if err := os.WriteFile(filepath.Join(l.evidence, kind+".json"), raw, 0600); err != nil {
				l.t.Log(err)
			}
			if kind == "pods" {
				l.captureFailedSteps(ctx, raw)
			}
		}
	}
}

func (l *lab) captureFailedSteps(ctx context.Context, raw []byte) {
	var pods struct {
		Items []struct {
			Metadata struct{ Name string }
			Status   struct {
				ContainerStatuses []struct {
					Name  string
					State struct{ Terminated *struct{ ExitCode int } }
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &pods); err != nil {
		l.t.Log(err)
		return
	}
	for _, pod := range pods.Items {
		for _, container := range pod.Status.ContainerStatuses {
			if !strings.HasPrefix(container.Name, "step-") || container.State.Terminated == nil || container.State.Terminated.ExitCode == 0 {
				continue
			}
			name := pod.Metadata.Name + "-" + container.Name + ".log"
			if filepath.Base(name) != name {
				continue
			}
			output, err := l.invoke(ctx, nil, "docker", l.dockerArgs("exec", l.node, "kubectl", "--kubeconfig=/etc/kubernetes/admin.conf", "-n", "nvkr", "logs", pod.Metadata.Name, "-c", container.Name, "--tail=200", "--limit-bytes=65536")...)
			if err == nil {
				if err := os.WriteFile(filepath.Join(l.evidence, name), output, 0600); err != nil {
					l.t.Log(err)
				}
			}
		}
	}
}

const registryResources = `apiVersion: v1
kind: Pod
metadata:
  name: registry
  namespace: nvkr
  labels: {app: registry}
spec:
  automountServiceAccountToken: false
  containers:
  - name: registry
    image: %s
    ports: [{containerPort: 5000}]
    volumeMounts: [{name: data, mountPath: /var/lib/registry}]
    readinessProbe:
      httpGet: {path: /v2/, port: 5000}
  volumes: [{name: data, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: registry, namespace: nvkr}
spec:
  type: NodePort
  selector: {app: registry}
  ports: [{port: 5000, targetPort: 5000, nodePort: 32000}]
`
