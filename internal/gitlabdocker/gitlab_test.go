//go:build integration

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package gitlabdocker exercises the generated adapter against GitLab itself.
package gitlabdocker

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/name"
)

type pins struct {
	Server, Runner, Glab, Go, Docker string
	Helpers                          map[string]string
}
type lab struct {
	fixtureCommit, buildMount, toolsVolume                         string
	t                                                              *testing.T
	root, work, name, arch, socket, tool, server, runner, evidence string
	pins                                                           pins
	containers, volumes                                            []string
	project, pipeline                                              int
	pipelineTerminal                                               bool
	cleanupRun                                                     func(context.Context, ...string) ([]byte, error)
}
type job struct {
	ID            int `json:"id"`
	Name, Status  string
	Retried       bool
	FailureReason string `json:"failure_reason"`
}
type pipeline struct {
	ID          int
	Status, SHA string
}

// TestGitLabNVKE runs a real generated nvkectl pipeline. Selecting this opt-in
// test requires its resources; missing prerequisites fail, rather than skip.
func TestGitLabNVKE(t *testing.T) {
	signalCtx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, 110*time.Minute)
	defer cancel()
	_, source, _, _ := runtime.Caller(0)
	l := &lab{t: t, root: filepath.Clean(filepath.Join(filepath.Dir(source), "../..")), work: t.TempDir()}
	l.name = "nvkr-gl-" + randomHex(t, 6)
	l.tool, l.server, l.runner = l.name+"-cli", l.name+"-server", l.name+"-runner"
	l.evidence = os.Getenv("NVKR_GITLAB_EVIDENCE")
	if l.evidence == "" {
		l.evidence = filepath.Join(l.work, "evidence")
	}
	must(t, os.MkdirAll(l.evidence, 0700))
	identities := l.evidence + "-binary-identities"
	must(t, os.Mkdir(identities, 0700))
	writeJSON(t, filepath.Join(identities, "generator.json"), map[string]any{"status": "unknown", "reason": "host generator has not been observed"})
	writeJSON(t, filepath.Join(identities, "bootstrap.json"), map[string]any{"status": "unknown", "reason": "terminal bootstrap artifacts have not been observed"})
	must(t, json.Unmarshal(read(t, filepath.Join(l.root, "toolchains/gitlab-docker/pins.json")), &l.pins))
	var info struct {
		Architecture, OSType, ID string
		MemTotal                 int64
		NCPU                     int
	}
	must(t, json.Unmarshal(l.command(ctx, l.root, nil, "docker", "info", "--format", "{{json .}}"), &info))
	switch info.Architecture {
	case "aarch64", "arm64":
		l.arch = "arm64"
	case "x86_64", "amd64":
		l.arch = "amd64"
	default:
		t.Fatalf("native AMD64/ARM64 Docker required, got %s", info.Architecture)
	}
	if info.OSType != "linux" || info.MemTotal < 12<<30 || info.NCPU < 4 {
		t.Fatal("real GitLab acceptance requires native Linux Docker, at least 12 GiB RAM and four CPUs")
	}
	l.socket = strings.TrimSpace(string(l.command(ctx, l.root, nil, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")))
	if os.Getenv("DOCKER_HOST") != "" && os.Getenv("DOCKER_CONTEXT") == "" {
		l.socket = os.Getenv("DOCKER_HOST")
	}
	if !strings.HasPrefix(l.socket, "unix:///") {
		t.Fatal("acceptance requires the selected local Unix Docker socket, not a remote daemon")
	}
	// Register cleanup before creating anything; exact names are owned even if a
	// CLI acknowledgement is lost. No pre-existing resources are removed.
	t.Cleanup(l.cleanup)
	l.command(ctx, l.root, nil, "docker", "network", "create", "--label", "nvkr.integration="+l.name, l.name)
	// The small CLI prerequisite is pulled first so free space is measured on
	// an actual daemon volume before pulling the much larger server/helper set.
	l.command(ctx, l.root, nil, "docker", "pull", l.pins.Docker)
	probe := l.name + "-socket-probe"
	l.containers = append(l.containers, probe)
	observed := strings.TrimSpace(string(l.command(ctx, l.root, nil, "docker", "run", "--rm", "--name", probe, "--mount", "type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock", l.pins.Docker, "docker", "info", "--format", "{{.ID}}")))
	if observed != info.ID || observed == "" {
		t.Fatal("daemon-side socket does not identify the selected Docker daemon")
	}
	l.socket = "/var/run/docker.sock"
	diskVolume := l.volume(ctx, "disk-probe")
	diskProbe := l.name + "-disk-probe"
	l.containers = append(l.containers, diskProbe)
	space := l.command(ctx, l.root, nil, "docker", "run", "--rm", "--name", diskProbe, "--network", "none", "--mount", "type=volume,src="+diskVolume+",dst=/fixture-space", "--entrypoint", "/bin/stat", l.pins.Docker, "-f", "-c", "%a %S", "/fixture-space")
	must(t, requireGitLabDiskSpace(space))
	for _, image := range []string{l.pins.Server, l.pins.Runner, l.pins.Glab, l.pins.Go, l.pins.Docker, l.pins.Helpers[l.arch]} {
		t.Logf("pulling pinned native image %s", strings.Split(image, "@")[0])
		l.command(ctx, l.root, nil, "docker", "pull", image)
		got := strings.TrimSpace(string(l.command(ctx, l.root, nil, "docker", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image)))
		if got != "linux/"+l.arch {
			t.Fatalf("image platform %s differs from native linux/%s", got, l.arch)
		}
	}
	secrets := filepath.Join(l.work, "secrets")
	must(t, os.Mkdir(secrets, 0700))
	cert, key := certificate(t)
	write(t, filepath.Join(secrets, "gitlab.test.crt"), cert, 0600)
	write(t, filepath.Join(secrets, "gitlab.test.key"), key, 0600)
	token := randomHex(t, 10)
	password := randomHex(t, 24)
	config := fmt.Sprintf("external_url 'https://gitlab.test'\nletsencrypt['enable'] = false\nnginx['ssl_certificate'] = '/etc/gitlab/ssl/gitlab.test.crt'\nnginx['ssl_certificate_key'] = '/etc/gitlab/ssl/gitlab.test.key'\ngitlab_rails['initial_root_password'] = '%s'\nprometheus_monitoring['enable'] = false\ngitlab_kas['enable'] = false\npuma['worker_processes'] = 2\nsidekiq['concurrency'] = 5\n", password)
	write(t, filepath.Join(secrets, "gitlab.rb"), []byte(config), 0600)
	l.containers = append(l.containers, l.server)
	args := []string{"run", "--detach", "--name", l.server, "--hostname", "gitlab.test", "--network", l.name, "--network-alias", "gitlab.test", "--memory", "8g", "--cpus", "4", "--shm-size", "256m", "--publish", "127.0.0.1::443"}
	for _, pair := range [][2]string{{"config", "/etc/gitlab"}, {"logs", "/var/log/gitlab"}, {"data", "/var/opt/gitlab"}} {
		volume := l.name + "-" + pair[0]
		l.volumes = append(l.volumes, volume)
		l.command(ctx, l.root, nil, "docker", "volume", "create", "--label", "nvkr.integration="+l.name, volume)
		args = append(args, "--mount", "type=volume,src="+volume+",dst="+pair[1])
	}
	// Starting with a complete config avoids a second reconfigure/restart and
	// keeps all passwords and private key bytes out of command arguments.
	l.containers = append(l.containers, l.server+"-seed")
	l.command(ctx, l.root, nil, "docker", "create", "--name", l.server+"-seed", "--mount", "type=volume,src="+l.name+"-config,dst=/etc/gitlab", "--entrypoint", "/bin/sh", l.pins.Server, "-c", "mkdir -p /etc/gitlab/ssl")
	l.command(ctx, l.root, nil, "docker", "start", "--attach", l.server+"-seed")
	for _, item := range []struct{ file, dest string }{{"gitlab.rb", "/etc/gitlab/gitlab.rb"}, {"gitlab.test.crt", "/etc/gitlab/ssl/gitlab.test.crt"}, {"gitlab.test.key", "/etc/gitlab/ssl/gitlab.test.key"}} {
		l.command(ctx, l.root, nil, "docker", "cp", filepath.Join(secrets, item.file), l.server+"-seed:"+item.dest)
	}
	l.command(ctx, l.root, nil, "docker", append(args, l.pins.Server)...)
	// Official glab handles all GitLab HTTP, pagination and JSON framing. Its
	// external-tool license boundary adds no linked runtime dependency to NVKR.
	glabConfig := map[string]any{"check_update": false, "hosts": map[string]any{"gitlab.test": map[string]any{"token": token, "api_protocol": "https", "git_protocol": "https"}}}
	writeJSON(t, filepath.Join(secrets, "config.yml"), glabConfig)
	toolVolume := l.volume(ctx, "cli-config")
	l.containers = append(l.containers, l.tool)
	l.command(ctx, l.root, nil, "docker", "create", "--name", l.tool, "--network", l.name, "--mount", "type=volume,src="+toolVolume+",dst=/fixture-secrets", "--env", "GLAB_CONFIG_DIR=/fixture-secrets", "--env", "SSL_CERT_FILE=/fixture-secrets/gitlab.test.crt", "--env", "NO_COLOR=1", "--entrypoint", "/bin/sh", l.pins.Glab, "-c", "sleep infinity")
	for _, file := range []string{"config.yml", "gitlab.test.crt"} {
		l.command(ctx, l.root, nil, "docker", "cp", filepath.Join(secrets, file), l.tool+":/fixture-secrets/"+file)
	}
	l.command(ctx, l.root, nil, "docker", "start", l.tool)
	t.Log("waiting for the owned GitLab server's real readiness endpoint")
	deadline := time.Now().Add(18 * time.Minute)
	for {
		if _, err := l.invoke(ctx, l.root, nil, "docker", "exec", l.server, "curl", "--max-time", "10", "--fail", "--silent", "--cacert", "/etc/gitlab/ssl/gitlab.test.crt", "https://localhost/-/readiness"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("GitLab did not become ready within 18 minutes; no pipeline ran")
		}
		pause(t, ctx, 5*time.Second)
	}
	// GitLab documents Rails runner for initial PAT bootstrap. Read a generated
	// token from stdin; do not print it or place it in argv, traces or artifacts.
	ruby := "token = User.find_by_username('root').personal_access_tokens.create!(scopes: ['api', 'create_runner'], name: 'nvkr-owned-test', expires_at: 1.day.from_now); token.set_token(STDIN.read.strip); token.save!"
	l.command(ctx, l.root, []byte(token), "docker", "exec", "-i", l.server, "gitlab-rails", "runner", ruby)
	var project struct{ ID int }
	t.Log("HTTPS readiness passed; provisioning real project and scoped credentials")
	l.api(ctx, "POST", "projects", map[string]any{"name": l.name, "path": l.name, "visibility": "private", "initialize_with_readme": false, "shared_runners_enabled": false}, &project)
	if project.ID <= 0 {
		t.Fatal("GitLab did not create an actual project")
	}
	l.project = project.ID
	// A separate read-only project credential is deliberately not the admin PAT.
	var reader struct{ Token string }
	l.api(ctx, "POST", fmt.Sprintf("projects/%d/access_tokens", l.project), map[string]any{"name": "nvkr-api-reader", "scopes": []string{"read_api"}, "access_level": 30, "expires_at": time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02")}, &reader)
	if reader.Token == "" {
		t.Fatal("missing ephemeral read-only token")
	}
	l.api(ctx, "POST", fmt.Sprintf("projects/%d/variables", l.project), map[string]any{"key": "NVKR_API_READ", "value": reader.Token, "masked": true, "protected": false, "raw": true}, nil)
	t.Log("preparing the pinned native bootstrap and Docker CLI prerequisite")
	bootstrap := l.bootstrap(ctx)
	repo := filepath.Join(l.work, "source")
	l.copySource(ctx, repo)
	t.Log("generating the real single-output nvkectl pipeline using NVKR CLI")
	l.authorAndGenerate(ctx, repo, bootstrap)
	l.push(ctx, repo, token, secrets)
	var lint struct {
		Valid  bool
		Errors []string
		Jobs   []struct{ Name string }
	}
	l.api(ctx, "POST", fmt.Sprintf("projects/%d/ci/lint", l.project), map[string]any{"content": string(read(t, filepath.Join(repo, ".gitlab-ci.yml"))), "ref": "main", "dry_run": true, "include_jobs": true}, &lint)
	if !lint.Valid || len(lint.Errors) > 0 {
		t.Fatalf("GitLab CI lint refused generated pipeline: %v", lint.Errors)
	}
	plan, err := factory.LoadPlanContext(ctx, filepath.Join(repo, factory.GitLabPlanPath))
	must(t, err)
	names, err := factory.GitLabJobNames(ctx, plan)
	must(t, err)
	lintNames := []string{}
	for _, j := range lint.Jobs {
		lintNames = append(lintNames, j.Name)
	}
	slices.Sort(lintNames)
	if !slices.Equal(names, lintNames) {
		t.Fatalf("real CI lint job inventory differs: got%d expected%d", len(lintNames), len(names))
	}
	l.registerRunner(ctx, secrets, bootstrap)
	var p pipeline
	l.api(ctx, "POST", fmt.Sprintf("projects/%d/pipeline", l.project), map[string]any{"ref": "main"}, &p)
	l.pipeline = p.ID
	if p.ID <= 0 || p.SHA != l.fixtureCommit {
		t.Fatal("real pipeline does not identify the exact pushed fixture commit")
	}
	t.Logf("real GitLab project%d pipeline%d started (%d expected jobs)", l.project, p.ID, len(names))
	var jobs []job
	lastStatus := ""
	for {
		l.api(ctx, "GET", fmt.Sprintf("projects/%d/pipelines/%d", l.project, p.ID), nil, &p)
		l.api(ctx, "GET", fmt.Sprintf("projects/%d/pipelines/%d/jobs?include_retried=true&per_page=100", l.project, p.ID), nil, &jobs)
		l.evidenceJSON("status.json", map[string]any{"pipeline": p, "jobs": jobs, "platform": "linux/" + l.arch, "images": l.pins, "bootstrapImage": bootstrap})
		if p.Status != lastStatus {
			t.Logf("pipeline %d status %s", p.ID, p.Status)
			lastStatus = p.Status
		}
		if slices.Contains([]string{"success", "failed", "canceled", "skipped"}, p.Status) {
			l.pipelineTerminal = true
			break
		}
		pause(t, ctx, 10*time.Second)
	}
	// Capture both successful and failed terminal bootstrap roles before any
	// failure assertion or teardown. ZIP bytes and executable bodies stay in memory.
	var bootstrapIdentities []map[string]any
	for _, j := range jobs {
		if j.Name != "nvkr:bootstrap:linux-"+l.arch {
			continue
		}
		identity := map[string]any{"status": "unknown", "jobID": j.ID, "jobStatus": j.Status, "reason": "bootstrap artifact identity capture unavailable"}
		raw, fetchErr := l.invoke(ctx, l.root, nil, "docker", "exec", l.tool, "glab", "api", "--hostname", "gitlab.test", fmt.Sprintf("projects/%d/jobs/%d/artifacts", l.project, j.ID))
		if fetchErr == nil {
			archive, zipErr := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if zipErr == nil {
				var members []map[string]any
				directory := ".nvkr/gitlab/bootstrap/linux-" + l.arch + "/"
				for _, member := range []string{directory + "nvkr-exec", directory + "SHA256SUMS"} {
					var selected *zip.File
					ambiguous := false
					for _, file := range archive.File {
						if file.Name == member {
							if selected != nil {
								ambiguous = true
							}
							selected = file
						}
					}
					if selected == nil || ambiguous || !selected.Mode().IsRegular() {
						continue
					}
					reader, openErr := selected.Open()
					if openErr != nil {
						continue
					}
					hash := sha256.New()
					n, readErr := io.Copy(hash, io.LimitReader(reader, (128<<20)+1))
					closeErr := reader.Close()
					if readErr != nil || closeErr != nil || n <= 0 || n > 128<<20 || uint64(n) != selected.UncompressedSize64 {
						continue
					}
					members = append(members, map[string]any{"path": member, "sha256": "sha256:" + hex.EncodeToString(hash.Sum(nil)), "bytes": n, "mode": fmt.Sprintf("%04o", selected.Mode().Perm())})
				}
				if len(members) == 2 {
					identity = map[string]any{"status": "observed", "jobID": j.ID, "jobStatus": j.Status, "members": members}
				}
			}
		}
		bootstrapIdentities = append(bootstrapIdentities, identity)
	}
	bootstrapStatus := "observed"
	if len(bootstrapIdentities) == 0 {
		bootstrapStatus = "unknown"
	}
	for _, identity := range bootstrapIdentities {
		if identity["status"] != "observed" {
			bootstrapStatus = "unknown"
		}
	}
	writeJSON(t, filepath.Join(identities, "bootstrap.json"), map[string]any{"status": bootstrapStatus, "projectID": l.project, "pipelineID": l.pipeline, "fixtureCommit": l.fixtureCommit, "jobs": bootstrapIdentities})
	l.reportFailures(ctx, jobs)
	actual := []string{}
	for _, j := range jobs {
		actual = append(actual, j.Name)
		if j.Status != "success" || j.Retried {
			t.Errorf("job%s ID%d status%s retried%t", j.Name, j.ID, j.Status, j.Retried)
		}
	}
	slices.Sort(actual)
	if p.Status != "success" || !slices.Equal(names, actual) {
		t.Fatalf("real pipeline status%s; exact job inventory match%t", p.Status, slices.Equal(names, actual))
	}
	l.checkArtifacts(ctx, plan, p, jobs)
	t.Log("real generated nvkectl pipeline succeeded; exact jobs, Run and executable bundle verified")
}

func (l *lab) api(ctx context.Context, method, path string, body any, out any) {
	l.t.Helper()
	var input []byte
	if body != nil {
		var err error
		input, err = json.Marshal(body)
		must(l.t, err)
	}
	args := []string{"exec", "-i", l.tool, "glab", "api", "--hostname", "gitlab.test", "--method", method, path}
	if body != nil {
		args = append(args, "--input", "-")
	}
	raw := l.command(ctx, l.root, input, "docker", args...)
	if out != nil {
		must(l.t, json.Unmarshal(raw, out))
	}
}

// bootstrap provisions only the Docker client prerequisite. The official Go
// image remains the exact immutable selected bootstrap image; neither an image
// registry nor a handwritten product build is required to run this test.
func (l *lab) bootstrap(ctx context.Context) string {
	l.toolsVolume = l.volume(ctx, "docker-client")
	seed := l.name + "-tools-seed"
	l.containers = append(l.containers, seed)
	l.command(ctx, l.root, nil, "docker", "create", "--name", seed, "--mount", "type=volume,src="+l.toolsVolume+",dst=/fixture-tools", "--entrypoint", "/bin/sh", l.pins.Docker, "-c", "cp /usr/local/bin/docker /fixture-tools/docker && chmod 0755 /fixture-tools/docker")
	l.command(ctx, l.root, nil, "docker", "start", "--attach", seed)
	probe := l.name + "-tools-probe"
	l.containers = append(l.containers, probe)
	l.command(ctx, l.root, nil, "docker", "run", "--rm", "--name", probe, "--mount", "type=volume,src="+l.toolsVolume+",dst=/fixture-tools,readonly", "--mount", "type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock", l.pins.Go, "/fixture-tools/docker", "info", "--format", "{{.ID}}")
	image, err := name.NewDigest(l.pins.Go, name.StrictValidation)
	must(l.t, err)
	return image.Name()
}

func (l *lab) copySource(ctx context.Context, dest string) {
	must(l.t, os.Mkdir(dest, 0700))
	paths := l.command(ctx, l.root, nil, "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	for _, p := range strings.Split(string(paths), "\x00") {
		if p == "" {
			continue
		}
		info, err := os.Lstat(filepath.Join(l.root, p))
		if os.IsNotExist(err) {
			continue
		}
		must(l.t, err)
		target := filepath.Join(dest, p)
		must(l.t, os.MkdirAll(filepath.Dir(target), 0700))
		if info.Mode()&os.ModeSymlink != 0 {
			s, err := os.Readlink(filepath.Join(l.root, p))
			must(l.t, err)
			must(l.t, os.Symlink(s, target))
			continue
		}
		if !info.Mode().IsRegular() {
			l.t.Fatalf("source entry is not regular or symlink:%s", p)
		}
		write(l.t, target, read(l.t, filepath.Join(l.root, p)), info.Mode().Perm())
	}
	// This is a disposable test repository, never a commit in the source checkout.
	l.command(ctx, dest, nil, "git", "init", "--initial-branch=main")
	l.command(ctx, dest, nil, "git", "config", "user.name", "NVKR integration fixture")
	l.command(ctx, dest, nil, "git", "config", "user.email", "nvkr-test@example.invalid")
	l.command(ctx, dest, nil, "git", "config", "commit.gpgsign", "false")
}
func (l *lab) authorAndGenerate(ctx context.Context, repo, bootstrap string) {
	path := "docs/examples/nvke-tutorial/definition-" + l.arch + ".yaml"
	d, err := definition.LoadDefinition(ctx, filepath.Join(repo, path))
	must(l.t, err)
	selected := d.Outputs[:0]
	for _, o := range d.Outputs {
		if o.Name == "nvkectl" {
			selected = append(selected, o)
		}
	}
	d.Outputs = selected
	if len(d.Outputs) != 1 {
		l.t.Fatal("maintained tutorial no longer contains exactly one nvkectl output")
	}
	d.Metadata.Name = "gitlab-nvkectl-" + l.arch
	// Keep paths beside the original tutorial so relative catalog imports retain
	// their authored meaning. This is the real catalog, not a synthetic builder.
	defPath := "docs/examples/nvke-tutorial/gitlab-definition.json"
	envPath := "docs/examples/nvke-tutorial/gitlab-environment.json"
	writeJSON(l.t, filepath.Join(repo, defPath), d)
	env, err := definition.LoadEnvironment(ctx, filepath.Join(repo, "docs/examples/nvke-tutorial/environment-local-"+l.arch+".yaml"))
	must(l.t, err)
	env.Signing = nil
	env.Publication = nil
	env.GitLab = &definition.GitLab{APIURL: "https://gitlab.test/api/v4", ProjectID: uint64(l.project), APIReadCredentialRef: "gitlab:NVKR_API_READ", APIReadCredentialKind: "private-token", Runners: map[string]definition.GitLabRunner{"linux/" + l.arch: {Tags: []string{l.name}, BootstrapImage: bootstrap}}}
	writeJSON(l.t, filepath.Join(repo, envPath), env)
	write(l.t, filepath.Join(repo, ".gitlab-ci.yml"), []byte("include:\n  - local: .gitlab/nvkr-run/pipeline.yml\n"), 0600)
	binary := filepath.Join(l.work, "nvkr")
	l.command(ctx, repo, nil, "go", "build", "-o", binary, "./cmd/nvkr")
	// Retain exact-blob metadata only, outside the exported evidence directory.
	generator := map[string]any{"status": "unknown", "reason": "host generator identity capture unavailable"}
	if file, err := os.Open(binary); err == nil {
		info, statErr := file.Stat()
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(file, (128<<20)+1))
		closeErr := file.Close()
		if statErr == nil && readErr == nil && closeErr == nil && info.Mode().IsRegular() && n == info.Size() && n > 0 && n <= 128<<20 {
			generator = map[string]any{"status": "observed", "sha256": "sha256:" + hex.EncodeToString(hash.Sum(nil)), "bytes": n, "mode": fmt.Sprintf("%04o", info.Mode().Perm())}
		}
	}
	writeJSON(l.t, filepath.Join(l.evidence+"-binary-identities", "generator.json"), generator)
	l.command(ctx, repo, nil, binary, "run", "generate", "--definition", defPath, "--environment", envPath, "--engine", "gitlab", "--root", repo)
	l.command(ctx, repo, nil, binary, "run", "generate", "--definition", defPath, "--environment", envPath, "--engine", "gitlab", "--root", repo, "--check")
}
func (l *lab) push(ctx context.Context, repo, token, secrets string) {
	tokenPath := filepath.Join(secrets, "git-token")
	write(l.t, tokenPath, []byte(token), 0600)
	ask := filepath.Join(secrets, "askpass")
	write(l.t, ask, []byte("#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' root;; *) cat \"$NVKR_TEST_GIT_TOKEN_FILE\";; esac\n"), 0700)
	l.command(ctx, repo, nil, "git", "add", "--all")
	l.command(ctx, repo, nil, "git", "commit", "--message", "test: capture exact GitLab nvkectl fixture")
	l.fixtureCommit = strings.TrimSpace(string(l.command(ctx, repo, nil, "git", "rev-parse", "HEAD")))
	// Private input records sit outside the explicitly exported evidence folder.
	// Preserve the exact authored/generated bytes before submitting any workload.
	records := l.evidence + "-fixture-inputs"
	must(l.t, os.Mkdir(records, 0700))
	for _, f := range []struct{ source, name string }{{"docs/examples/nvke-tutorial/gitlab-definition.json", "definition.json"}, {"docs/examples/nvke-tutorial/gitlab-environment.json", "environment.json"}, {factory.GitLabPlanPath, "plan.json"}} {
		write(l.t, filepath.Join(records, f.name), read(l.t, filepath.Join(repo, f.source)), 0600)
	}
	write(l.t, filepath.Join(records, "fixture-commit.txt"), []byte(l.fixtureCommit+"\n"), 0600)
	port := strings.TrimSpace(string(l.command(ctx, l.root, nil, "docker", "inspect", "--format", `{{(index (index .NetworkSettings.Ports "443/tcp") 0).HostPort}}`, l.server)))
	remote := fmt.Sprintf("https://127.0.0.1:%s/root/%s.git", port, l.name)
	cmd := exec.CommandContext(ctx, "git", "-c", "credential.helper=", "push", "--push-option=ci.skip", remote, "HEAD:main")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_ASKPASS="+ask, "GIT_TERMINAL_PROMPT=0", "NVKR_TEST_GIT_TOKEN_FILE="+tokenPath, "GIT_SSL_CAINFO="+filepath.Join(secrets, "gitlab.test.crt"))
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := ownedcmd.Run(ctx, cmd); err != nil {
		l.t.Fatalf("pushing owned fixture:%v (credential-bearing output withheld)", err)
	}
}

func (l *lab) registerRunner(ctx context.Context, secrets, bootstrap string) {
	var r struct {
		ID    int
		Token string
	}
	l.api(ctx, "POST", "user/runners", map[string]any{"runner_type": "project_type", "project_id": l.project, "description": l.name, "locked": true, "run_untagged": false, "tag_list": l.name, "maximum_timeout": 7200}, &r)
	if r.ID <= 0 || r.Token == "" {
		l.t.Fatal("no real project runner authentication token")
	}
	buildVolume := l.volume(ctx, "builds")
	builds := strings.TrimSpace(string(l.command(ctx, l.root, nil, "docker", "volume", "inspect", "--format", "{{.Mountpoint}}", buildVolume)))
	if !filepath.IsAbs(builds) || filepath.Clean(builds) != builds {
		l.t.Fatal("Docker did not return an absolute owned build-volume mountpoint")
	}
	l.buildMount = builds
	trustVolume := l.volume(ctx, "runner-trust")
	runnerVolume := l.volume(ctx, "runner-config")
	// The identical host/container build path is necessary: NVKR's Docker calls
	// bind the source workspace into producer containers on this same daemon.
	config := fmt.Sprintf("concurrent = 2\ncheck_interval = 2\nshutdown_timeout = 30\n[[runners]]\n  name = %q\n  url = \"https://gitlab.test\"\n  token = %q\n  executor = \"docker\"\n  builds_dir = %q\n  tls-ca-file = \"/fixture-trust/gitlab.test.crt\"\n  environment = [\"SSL_CERT_FILE=/fixture-trust/gitlab.test.crt\", \"GIT_SSL_CAINFO=/fixture-trust/gitlab.test.crt\", \"GOMAXPROCS=2\", \"PATH=/fixture-tools:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\"]\n  [runners.docker]\n    image = %q\n    helper_image = %q\n    privileged = false\n    disable_cache = true\n    network_mode = %q\n    pull_policy = \"if-not-present\"\n    memory = \"2g\"\n    cpus = \"2\"\n    volumes = [%q, %q, %q, %q]\n", l.name, r.Token, builds, bootstrap, l.pins.Helpers[l.arch], l.name, buildVolume+":"+builds, l.socket+":/var/run/docker.sock", trustVolume+":/fixture-trust:ro", l.toolsVolume+":/fixture-tools:ro")
	runnerDir := filepath.Join(l.work, "runner")
	must(l.t, os.Mkdir(runnerDir, 0700))
	write(l.t, filepath.Join(runnerDir, "config.toml"), []byte(config), 0600)
	l.containers = append(l.containers, l.runner)
	l.command(ctx, l.root, nil, "docker", "create", "--name", l.runner, "--network", l.name, "--mount", "type=volume,src="+runnerVolume+",dst=/etc/gitlab-runner", "--mount", "type=volume,src="+trustVolume+",dst=/fixture-trust", "--mount", "type=bind,src="+l.socket+",dst=/var/run/docker.sock", l.pins.Runner)
	l.command(ctx, l.root, nil, "docker", "cp", filepath.Join(runnerDir, "config.toml"), l.runner+":/etc/gitlab-runner/config.toml")
	l.command(ctx, l.root, nil, "docker", "cp", filepath.Join(secrets, "gitlab.test.crt"), l.runner+":/fixture-trust/gitlab.test.crt")
	l.command(ctx, l.root, nil, "docker", "start", l.runner)

}

func (l *lab) checkArtifacts(ctx context.Context, plan *factory.Plan, p pipeline, jobs []job) {
	l.t.Helper()
	var run lifecycle.Run
	var gotRun, gotOutput bool
	var outputRoot string
	var finalizerID uint64
	slot := plan.LifecycleOutputs[0]
	artifacts := map[string]factory.Artifact{}
	for _, a := range plan.Graph.Artifacts {
		artifacts[a.ID] = a
	}
	for _, j := range jobs {
		if j.Name == "nvkr:finalize" {
			finalizerID = uint64(j.ID)
			raw := l.command(ctx, l.root, nil, "docker", "exec", l.tool, "glab", "api", "--hostname", "gitlab.test", fmt.Sprintf("projects/%d/jobs/%d/artifacts/.nvkr/gitlab/run.json", l.project, j.ID))
			must(l.t, json.Unmarshal(raw, &run))
			l.evidenceJSON("run.json", run)
			gotRun = true
			continue
		}
		if strings.HasPrefix(j.Name, "nvkr:bootstrap:") {
			continue
		}
		// Select the frozen output's expected carrier paths through the public
		// path owner. This diagnostic does not parse producer outcome records;
		// provider/outcome authentication remains with the runtime finalizer.
		raw := l.command(ctx, l.root, nil, "docker", "exec", l.tool, "glab", "api", "--hostname", "gitlab.test", fmt.Sprintf("projects/%d/jobs/%d/artifacts", l.project, j.ID))
		archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		must(l.t, err)
		for _, node := range plan.Graph.Nodes {
			if !slices.Contains(node.Outputs, slot.ArtifactID) {
				continue
			}
			for _, file := range archive.File {
				if file.Name != factory.GitLabCarrierPath(node.ID, slot.ArtifactID) {
					continue
				}
				reader, err := file.Open()
				must(l.t, err)
				restored := filepath.Join(l.work, "restored")
				must(l.t, os.MkdirAll(restored, 0700))
				carrier := filepath.Join(l.work, "output-carrier.tar")
				f, err := os.OpenFile(carrier, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				must(l.t, err)
				_, copyErr := io.Copy(f, io.LimitReader(reader, transportarchive.MaxArchiveBytes+1))
				closeErr := f.Close()
				readErr := reader.Close()
				must(l.t, copyErr)
				must(l.t, closeErr)
				must(l.t, readErr)
				must(l.t, transportarchive.Unpack(ctx, carrier, restored, []string{artifacts[slot.ArtifactID].Path}))
				bundle, result, err := delivery.InspectBinaryBundle(ctx, filepath.Join(restored, artifacts[slot.ArtifactID].Path))
				must(l.t, err)
				if bundle.OS != "linux" || bundle.Arch != l.arch || len(bundle.Entries) != 1 || bundle.Entries[0].Name != "nvkectl" || bundle.Entries[0].Size <= 0 {
					l.t.Fatal("downloaded executable bundle differs from exact nvkectl output")
				}
				descriptor, err := delivery.PayloadDescriptor(ctx, filepath.Join(restored, artifacts[slot.ArtifactID].Path))
				must(l.t, err)
				l.evidenceJSON("executable.json", map[string]any{"bundle": bundle, "ociIndexDigest": result.RootDigest, "semanticDigest": result.SemanticDigest, "descriptor": descriptor})
				gotOutput = true
				outputRoot = descriptor.Digest
			}
		}
	}

	if !gotRun || !gotOutput || run.Status != lifecycle.RunComplete || run.Lane != "static-unqualified" || len(run.Expected) != 1 || len(run.Observed) != 1 || run.Observed[0].Name != "nvkectl" || run.Observed[0].Digest != outputRoot || run.Observed[0].BuildDigest != slot.BuildDigest {
		l.t.Fatal("real provider artifacts do not contain the complete exact nvkectl Run and executable")
	}
	want, err := plan.Digest()
	must(l.t, err)
	attempt, err := factory.GitLabAttempt(factory.GitLabJobContext{ProjectID: uint64(l.project), PipelineID: uint64(l.pipeline), JobID: finalizerID, Commit: l.fixtureCommit})
	must(l.t, err)
	if run.Attempt != attempt || run.PlanDigest != want || run.DefinitionDigest != plan.DefinitionDigest || run.EnvironmentDigest != plan.EnvironmentDigest || p.SHA != l.fixtureCommit {
		l.t.Fatal("Run did not bind generated inputs and provider commit")
	}
}

func (l *lab) evidenceJSON(name string, v any) { writeJSON(l.t, filepath.Join(l.evidence, name), v) }
func (l *lab) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if l.pipeline > 0 && !l.pipelineTerminal {
		_, _ = l.cleanupDocker(ctx, "exec", l.tool, "glab", "api", "--hostname", "gitlab.test", "--method", "POST", fmt.Sprintf("projects/%d/pipelines/%d/cancel", l.project, l.pipeline))
	}
	if slices.Contains(l.containers, l.runner) {
		_, _ = l.cleanupDocker(ctx, "stop", "--time", "30", l.runner)
	}
	// Remove every owned-network creator before taking the producer snapshot.
	raw, err := l.cleanupDocker(ctx, "network", "inspect", "--format", "{{range $id,$c := .Containers}}{{$id}} {{end}}", l.name)
	if err == nil {
		for _, id := range strings.Fields(string(raw)) {
			if _, err := l.cleanupDocker(ctx, "rm", "--force", "--volumes", id); err != nil {
				l.t.Errorf("remove owned-network container:%v", err)
			}
		}
	}
	// Product utilities may use bridge/none rather than the Runner network. A
	// mount strictly beneath our exact owned volume identifies those children.
	if l.buildMount != "" {
		ids, err := l.cleanupDocker(ctx, "ps", "--all", "--quiet", "--no-trunc")
		if err != nil {
			l.t.Errorf("inventory owned producer cleanup:%v", err)
		} else {
			for _, id := range strings.Fields(string(ids)) {
				raw, err := l.cleanupMounts(ctx, id)
				if err != nil {
					l.t.Errorf("inspect producer cleanup:%v", err)
					continue
				}
				if raw == nil { // Unrelated auto-removed container disappeared.
					continue
				}
				var mounts []struct{ Source string }
				if err := json.Unmarshal(raw, &mounts); err != nil {
					l.t.Errorf("decode producer mounts:%v", err)
					continue
				}
				for _, m := range mounts {
					if m.Source == l.buildMount || strings.HasPrefix(m.Source, l.buildMount+"/") {
						if _, err := l.cleanupDocker(ctx, "rm", "--force", "--volumes", id); err != nil {
							l.t.Errorf("remove owned producer:%v", err)
						}
						break
					}
				}
			}
		}
	}
	for i := len(l.containers) - 1; i >= 0; i-- {
		_, _ = l.cleanupDocker(ctx, "rm", "--force", "--volumes", l.containers[i])
	}
	for _, v := range l.volumes {
		if _, err := l.cleanupDocker(ctx, "volume", "rm", v); err != nil {
			l.t.Errorf("remove owned GitLab volume:%v", err)
		}
	}
	if _, err := l.cleanupDocker(ctx, "network", "rm", l.name); err != nil {
		l.t.Errorf("remove owned GitLab network:%v", err)
	}
	l.checkKnownContainersGone(ctx)
}

// Same bounded-output pattern as internal/quicke2e, with an artifact-sized
// stdout budget. Provider credentials and unfiltered traces never become logs.
type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 512<<20 {
		return 0, fmt.Errorf("test command output exceeds 512 MiB")
	}
	return b.Buffer.Write(p)
}
func (l *lab) invoke(ctx context.Context, dir string, input []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(input)
	var stdout boundedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	err := ownedcmd.Run(ctx, cmd)
	return stdout.Bytes(), err
}
func (l *lab) command(ctx context.Context, dir string, input []byte, name string, args ...string) []byte {
	l.t.Helper()
	out, err := l.invoke(ctx, dir, input, name, args...)
	if err != nil {
		l.t.Fatalf("%s %v failed:%v (credential-bearing output withheld)", name, args, err)
	}
	return out
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}
func write(t *testing.T, path string, b []byte, mode os.FileMode) {
	t.Helper()
	must(t, os.WriteFile(path, b, mode))
}
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	must(t, err)
	write(t, path, append(b, '\n'), 0600)
}
func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	must(t, err)
	return hex.EncodeToString(b)
}
func pause(t *testing.T, ctx context.Context, d time.Duration) {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
}
func certificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nvkr-owned-gitlab"}, DNSNames: []string{"gitlab.test", "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(6 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	must(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	must(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func (l *lab) volume(ctx context.Context, suffix string) string {
	name := l.name + "-" + suffix
	l.volumes = append(l.volumes, name)
	l.command(ctx, l.root, nil, "docker", "volume", "create", "--label", "nvkr.integration="+l.name, name)
	return name
}

// reportFailures never prints or persists a trace. The Go standard parser
// selects only fixed literal error prefixes from the actual owning source;
// matching that fixed text cannot disclose a credential or a dynamic URL.
func (l *lab) reportFailures(ctx context.Context, jobs []job) {
	for _, j := range jobs {
		if j.Status != "failed" {
			continue
		}
		trace, err := l.invoke(ctx, l.root, nil, "docker", "exec", l.tool, "glab", "api", "--hostname", "gitlab.test", fmt.Sprintf("projects/%d/jobs/%d/trace", l.project, j.ID))
		if err != nil {
			l.t.Logf("job %d failure trace unavailable (content withheld)", j.ID)
			continue
		}
		var sourcePaths []string
		for _, dir := range []string{"pkg/nvkr/gitlab", "pkg/nvkr/factory", "pkg/nvkr/lifecycle", "cmd/nvkr", "internal/sourcegit"} {
			paths, err := filepath.Glob(filepath.Join(l.root, dir, "*.go"))
			must(l.t, err)
			for _, p := range paths {
				if !strings.HasSuffix(p, "_test.go") {
					sourcePaths = append(sourcePaths, p)
				}
			}
		}
		matched, err := sourceOwnedFailureMessages(ctx, sourcePaths, trace)
		must(l.t, err)
		var phases []string
		for _, phase := range []string{"Getting source from Git repository", "Downloading artifacts", "Executing \"step_script\"", "Uploading artifacts", "Permission denied", "not found", "Exec format error", "unknown flag", "unknown command", "flag provided but not defined", "No such file", "checksum did NOT match", "include.path", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_COUNT", "NVKR-E-"} {
			if bytes.Contains(trace, []byte(phase)) {
				phases = append(phases, phase)
			}
		}
		l.t.Logf("job %d reason %s trace-bytes %d phases %v source-owned candidate prefixes: %v (raw trace withheld)", j.ID, j.FailureReason, len(trace), phases, matched)
	}
}

func sourceOwnedFailureMessages(ctx context.Context, sourcePaths []string, trace []byte) ([]string, error) {
	var matched []string
	for _, path := range sourcePaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return nil, err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			index := 1
			switch selector.Sel.Name {
			case "New":
			case "Wrap":
				index = 2
			default:
				return true
			}
			if len(call.Args) <= index {
				return true
			}
			code, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			text, ok := call.Args[index].(*ast.BasicLit)
			if !ok || text.Kind != token.STRING {
				return true
			}
			message, err := strconv.Unquote(text.Value)
			message, _, _ = strings.Cut(message, "%")
			if err != nil || len(message) < 8 || !bytes.Contains(trace, []byte(message)) {
				return true
			}
			matched = append(matched, filepath.Base(path)+": "+code.Sel.Name+": "+message)
			return true
		})
	}
	slices.Sort(matched)
	return slices.Compact(matched), nil
}

// A remove error may mean an already absent container. Prove the final state
// through Docker's JSON inventory instead of classifying human error strings.
func (l *lab) checkKnownContainersGone(ctx context.Context) {
	raw, err := l.cleanupDocker(ctx, "container", "ls", "--all", "--format", "{{json .Names}}")
	if err != nil {
		l.t.Errorf("final owned-container inventory unavailable: %v", err)
		return
	}
	remaining, err := remainingKnownContainers(raw, l.containers)
	if err != nil {
		l.t.Errorf("final owned-container inventory invalid: %v", err)
		return
	}
	if len(remaining) != 0 {
		l.t.Errorf("owned containers remain after cleanup: %v", remaining)
	}
}
func remainingKnownContainers(raw []byte, known []string) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	present := map[string]bool{}
	for {
		var name *string
		err := decoder.Decode(&name)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if name == nil || *name == "" || strings.Contains(*name, ",") {
			return nil, fmt.Errorf("Docker inventory did not provide one exact container name")
		}
		present[*name] = true
	}
	var remaining []string
	for _, name := range known {
		if present[name] {
			remaining = append(remaining, name)
		}
	}
	return remaining, nil
}

// The injectable command is only for deterministic cleanup race regressions;
// the real acceptance always uses the owned, bounded Docker subprocess.
func (l *lab) cleanupDocker(ctx context.Context, args ...string) ([]byte, error) {
	if l.cleanupRun != nil {
		return l.cleanupRun(ctx, args...)
	}
	return l.invoke(ctx, l.root, nil, "docker", args...)
}

func (l *lab) cleanupMounts(ctx context.Context, id string) ([]byte, error) {
	raw, err := l.cleanupDocker(ctx, "inspect", "--format", "{{json .Mounts}}", id)
	if err == nil {
		return raw, nil
	}
	// An unrelated --rm container may exit after the snapshot. Recheck exact
	// full IDs, never human-readable CLI errors or ambiguous short IDs.
	current, inventoryErr := l.cleanupDocker(ctx, "ps", "--all", "--quiet", "--no-trunc")
	if inventoryErr != nil {
		return nil, fmt.Errorf("inspection failed and current container inventory is unavailable: %w", inventoryErr)
	}
	if !slices.Contains(strings.Fields(string(current)), id) {
		return nil, nil
	}
	return nil, err
}

func requireGitLabDiskSpace(raw []byte) error {
	// Same pinned-container stat observation as diskInspectionScratchPrepare;
	// fmt owns integer decoding. Refuse unknown, trailing and overflowed facts.
	var blocks, blockSize uint64
	var trailing string
	n, err := fmt.Fscan(bytes.NewReader(raw), &blocks, &blockSize, &trailing)
	if n != 2 || err != io.EOF || blockSize == 0 || blocks > ^uint64(0)/blockSize {
		return fmt.Errorf("real GitLab acceptance could not establish available Docker volume space")
	}
	if blocks*blockSize < 35<<30 {
		return fmt.Errorf("real GitLab acceptance requires at least 35 GiB available on Docker volume storage; no resources were pruned")
	}
	return nil
}
