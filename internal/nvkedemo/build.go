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
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"golang.org/x/sys/unix"
)

// BuiltInputs describes output paths of the existing launcher; it confers no
// authority. Run independently admits observations and calls nvkr to verify.
type BuiltInputs struct {
	Runs RunInputs
	NVKR string
}

// BuildArtifacts invokes the repository's existing bootstrap and nvkr local
// build flow. It does not import execution from a Go SDK, implement a builder,
// modify NVKE sources or fold deployment into an NVKR Definition.
func BuildArtifacts(ctx context.Context, cfg BuildConfig) (BuiltInputs, error) {
	result, err := buildArtifacts(ctx, cfg)
	if err != nil {
		return BuiltInputs{}, nvkrerr.Wrap(nvkrerr.CodeBuildFailed, err, "local NVKE artifact build")
	}
	return result, nil
}
func buildArtifacts(ctx context.Context, cfg BuildConfig) (BuiltInputs, error) {
	if err := validateAutomaticQualification(cfg.QualifyLocal, cfg.Prepared, cfg.QualificationPolicy, cfg.Qualification); err != nil {
		return BuiltInputs{}, err
	}
	if err := requireContext(ctx); err != nil {
		return BuiltInputs{}, err
	}
	if cfg.Conformance && (cfg.QualifyLocal || cfg.Prepared || cfg.QualificationPolicy != "") {
		return BuiltInputs{}, nvkrerr.New(nvkrerr.CodeEngineCapability, "demo conformance selects a fresh unsigned diagnostic Run; prepared or local-signing combinations are not admitted")
	}
	if err := cfg.Qualification.validate(cfg.QualificationPolicy); err != nil {
		return BuiltInputs{}, err
	}
	if cfg.Prepared && !configString(cfg.QualificationPolicy) {
		return BuiltInputs{}, configUsage("prepared builds require an explicit qualification policy")
	}
	launcherFlags, err := buildSelectionFlags(cfg)
	if err != nil {
		return BuiltInputs{}, err
	}
	if err := validateSelectedBuild(cfg.Definition, cfg.Environment, cfg.NVKR, cfg.Prepared || cfg.QualifyLocal || cfg.QualificationPolicy != ""); err != nil {
		return BuiltInputs{}, err
	}
	if runtime.GOARCH != "arm64" || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return BuiltInputs{}, nvkrerr.New(nvkrerr.CodeEngineCapability, "artifact builds require native Mac or Linux ARM64")
	}
	repo, err := resolveExisting(cfg.Repository)
	if err != nil {
		return BuiltInputs{}, err
	}
	if !safePhase(cfg.AttemptPrefix) || strings.HasPrefix(cfg.AttemptPrefix, "-") {
		return BuiltInputs{}, errors.New("require explicit fresh local attempt prefix")
	}
	if cfg.Jobs < 1 {
		return BuiltInputs{}, errors.New("jobs must be positive")
	}
	nativeDefault := false
	if runtime.GOOS == "darwin" && cfg.Definition == "" {
		executable, err := os.Executable()
		if err != nil {
			return BuiltInputs{}, nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "select native NVKR distribution")
		}
		nativeDefault, err = defaultMacBuild(&cfg, runtime.GOOS+"/"+runtime.GOARCH, executable)
		if err != nil {
			return BuiltInputs{}, err
		}
	}
	script := ""
	if cfg.Definition == "" {
		script = filepath.Join(repo, "scripts/nvke-full-arm64-local.sh")
		if _, err = boundedRegularRead(script, 1<<20); err != nil {
			return BuiltInputs{}, err
		}
	} else if err := resolveSelectedBuild(ctx, repo, &cfg); err != nil {
		return BuiltInputs{}, err
	}
	env, err := localBuildEnvironment(ctx)
	if err != nil {
		return BuiltInputs{}, err
	}
	if cfg.NVKEClone != "" {
		clone, readErr := resolveExisting(cfg.NVKEClone)
		if readErr != nil {
			return BuiltInputs{}, readErr
		}
		env = SetEnv(env, map[string]string{"NVKE_LOCAL_CLONE": clone})
	}
	runner := Runner{Env: env, Progress: cfg.Progress, output: cfg.Progress}
	// All host checks and fresh-path checks precede the first bootstrap. The
	// script remains authoritative for exact runtime/toolchain admission.
	for _, root := range []string{"runs", "laptop-bootstrap", "native-tools/runs"} {
		owned := filepath.Join(repo, ".nvkr", root, cfg.AttemptPrefix)
		if cfg.Prepared && root == "laptop-bootstrap" {
			info, readErr := os.Lstat(owned)
			if readErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return BuiltInputs{}, fmt.Errorf("prepared bootstrap %q must be an existing directory", owned)
			}
			continue
		}
		if _, readErr := os.Lstat(owned); readErr == nil {
			return BuiltInputs{}, fmt.Errorf("attempt destination %q already exists; choose a fresh --attempt", owned)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return BuiltInputs{}, fmt.Errorf("cannot inspect attempt destination %q: %w", owned, readErr)
		}
	}
	// Require the native host accelerator before any
	// bootstrap, material acquisition or producer subprocess is started.
	if err := AdmitNativeDeployment(ctx, ""); err != nil {
		return BuiltInputs{}, err
	}
	if script != "" {
		preflight := append([]string{"bash", script}, launcherFlags...)
		if err = runner.Run(ctx, append(preflight, "--check", "all"), "preflight-all", 120*time.Second); err != nil {
			return BuiltInputs{}, err
		}
	}
	cfg.Repository = repo
	if nativeDefault {
		if err := prepareDefaultMacBuild(ctx, runner, &cfg); err != nil {
			return BuiltInputs{}, err
		}
	}
	built, err := executeBuildQualification(ctx, runner, script, cfg, buildQualificationSteps{
		prepare: prepareLocalArtifacts, setup: SetupQualification,
		build: buildLocalArtifacts, qualify: qualifyBuiltArtifacts, stop: StopQualificationRegistry,
	})
	if err != nil {
		return BuiltInputs{}, err
	}
	toolsRun, err := buildTools(ctx, ToolConfig{
		Repository: repo, NVKR: built.NVKR, NVKEClone: cfg.NVKEClone, Attempt: cfg.AttemptPrefix, Jobs: cfg.Jobs,
		Out:      filepath.Join(repo, ".nvkr", "laptop-bootstrap", cfg.AttemptPrefix, "native-tools"),
		Progress: cfg.Progress,
	}, runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		return BuiltInputs{}, err
	}
	built.Runs.Tools = toolsRun
	return built, nil
}

// The private seam joins admitted operations without replacing their authority
// checks. Production always supplies the concrete owners above.
type buildQualificationSteps struct {
	prepare func(context.Context, Runner, string, BuildConfig) error
	setup   func(context.Context, DemoFile, string, io.Writer) (string, error)
	build   func(context.Context, Runner, string, BuildConfig) error
	qualify func(context.Context, BuildConfig) error
	stop    func(context.Context, string) error
}

func executeBuildQualification(ctx context.Context, runner Runner, script string, cfg BuildConfig, steps buildQualificationSteps) (result BuiltInputs, resultErr error) {
	if cfg.QualifyLocal {
		if err := steps.prepare(ctx, runner, script, cfg); err != nil {
			return BuiltInputs{}, err
		}
		out := filepath.Join(cfg.Repository, ".nvkr/laptop-bootstrap", cfg.AttemptPrefix, "qualification")
		file := DemoFile{Version: 1, Build: BuildFile{Repo: cfg.Repository, NVKEClone: cfg.NVKEClone, Attempt: cfg.AttemptPrefix, Jobs: cfg.Jobs}, Run: RunFile{Network: cfg.Network}}
		path, err := steps.setup(ctx, file, out, cfg.Progress)
		if err != nil {
			return BuiltInputs{}, err
		}
		defer func() {
			resultErr = errors.Join(resultErr, finishAutomaticQualification(ctx, out, steps.stop))
			if resultErr != nil {
				result = BuiltInputs{}
			}
		}()
		selected, err := LoadDemoFile(ctx, path)
		if err != nil {
			return BuiltInputs{}, err
		}
		cfg, err = selected.BuildConfig(cfg.Progress)
		if err != nil {
			return BuiltInputs{}, err
		}
		if cfg.Progress != nil {
			fmt.Fprintf(cfg.Progress, "Local qualification files and evidence: %s\n", out)
		}
	}
	if err := steps.build(ctx, runner, script, cfg); err != nil {
		return BuiltInputs{}, err
	}
	if err := steps.qualify(ctx, cfg); err != nil {
		return BuiltInputs{}, err
	}
	built := builtInputs(cfg.Repository, cfg.AttemptPrefix)
	if cfg.NVKR != "" {
		built.NVKR = cfg.NVKR
	}
	return built, nil
}

func builtInputs(repo, attempt string) BuiltInputs {
	path := filepath.Join(repo, ".nvkr/runs", attempt)
	return BuiltInputs{RunInputs{OS: path, Components: path, Providers: path, Site: path, Tools: path, Profiles: path, Simulator: path}, filepath.Join(repo, ".nvkr/laptop-bootstrap", attempt, "nvkr")}
}

// The native build launcher receives these developer transport settings. The
// sourcegit owner narrows its own Git children to fetch-only credentials; this
// does not isolate every launcher subprocess from the launcher environment.
// Deployment uses a separate environment without ambient source credentials.
func localBuildEnvironment(ctx context.Context) ([]string, error) {
	return localBuildEnvironmentFor(ctx, runtime.GOOS)
}

func localBuildEnvironmentFor(ctx context.Context, platform string) ([]string, error) {
	if err := requireContext(ctx); err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "local build environment")
	}
	redirects, err := sourcegit.LocalRedirectEnvironment(os.Environ())
	if err != nil {
		return nil, err
	}
	env := SetEnv(NativeEnvironment(), map[string]string{"CONTAINER_RUNTIME": "docker"})
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "SSH_AUTH_SOCK",
		"https_proxy", "http_proxy", "no_proxy", "ALL_PROXY", "all_proxy",
		"GIT_SSL_CAINFO", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			env = SetEnv(env, map[string]string{key: value})
		}
	}
	env = append(env, redirects...)
	if platform != "darwin" {
		return env, nil
	}
	// Docker owns context selection. Inspect with its developer selectors, then
	// bind only the admitted local endpoint to build children. Deployment keeps
	// the separate NativeEnvironment and Linux keeps its existing fixed socket.
	probe := SetEnv(env, map[string]string{"DOCKER_HOST": ""})
	for _, key := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG"} {
		if value, ok := os.LookupEnv(key); ok {
			probe = SetEnv(probe, map[string]string{key: value})
		}
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	runner := Runner{Env: probe}
	selected, err := runner.Capture(bounded, []string{"docker", "context", "show"}, "build-docker-context", 0)
	if err == nil && strings.TrimSpace(string(selected)) == "" {
		err = errors.New("no Docker context selected")
	}
	var endpoint []byte
	if err == nil {
		endpoint, err = runner.Capture(bounded, []string{"docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}", "--", strings.TrimSpace(string(selected))}, "build-docker-endpoint", 0)
	}
	if err != nil {
		if bounded.Err() != nil {
			return nil, nvkrerr.Wrap(nvkrerr.CodeTimeout, errors.Join(err, bounded.Err()), "inspect local Docker endpoint")
		}
		return nil, nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "inspect local Docker endpoint")
	}
	host := strings.TrimSpace(string(endpoint))
	parsed, err := url.Parse(host)
	if err != nil || parsed.Scheme != "unix" || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" ||
		!filepath.IsAbs(parsed.Path) || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, nvkrerr.New(nvkrerr.CodeEngineCapability, "native Mac builds require a Docker context with a local Unix socket")
	}
	return SetEnv(env, map[string]string{"DOCKER_HOST": host}), nil
}

func buildLocalArtifacts(ctx context.Context, runner Runner, script string, cfg BuildConfig) (result error) {
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 6*time.Hour)
		defer cancel()
	}
	boundary := beginProgress(runner.Progress, "artifacts", LaneStarted, LaneFinished, LaneFailed)
	defer func() { boundary.finish(result) }()
	if runner.Progress != nil {
		fmt.Fprintln(runner.Progress, "Build the selected Definition through nvkr run local")
	}
	// Bootstrap gets no activity pipe. Its selected Plan, previews and source
	// preparation remain at the existing owner before the final direct child.
	if !cfg.Prepared {
		if err := prepareLocalArtifacts(ctx, runner, script, cfg); err != nil {
			return err
		}
	}
	plan, planErr := factory.LoadPlanContext(ctx, filepath.Join(cfg.Repository, ".nvkr/laptop-bootstrap", cfg.AttemptPrefix, "selected-plan.json"))
	if cfg.QualificationPolicy != "" {
		policy, err := lifecycle.LoadLocalQualificationPolicy(ctx, cfg.QualificationPolicy)
		if err != nil {
			return err
		}
		if policy.Platform != "linux/arm64" {
			return nvkrerr.New(nvkrerr.CodeEngineCapability, "demo qualification policy requires linux/arm64")
		}
		if planErr != nil {
			return planErr
		}
		if err := policy.RequirePlan(plan); err != nil {
			return err
		}
		if cfg.Prepared {
			if err := recheckPreparedQualificationPlan(ctx, runner, cfg, policy); err != nil {
				return err
			}
		}
		if err := preflightDemoQualification(ctx, cfg, plan); err != nil {
			return err
		}
	}
	args, err := preparedBuildInvocation(cfg)
	if err != nil {
		return err
	}
	env, err := preparedBuildEnvironment(runner.Env, cfg)
	if err != nil {
		return err
	}
	runner.Env, runner.directory, runner.buildActivity = env, cfg.Repository, true
	// Resolve a relative bootstrap executable against the admitted repository.
	if !filepath.IsAbs(args[0]) {
		args[0] = filepath.Join(cfg.Repository, args[0])
	}
	reportBuildPlan(runner.Progress, plan, cfg.AttemptPrefix, "artifacts")
	return runner.Run(ctx, args, "build-all", 0)
}

func buildSelectionFlags(cfg BuildConfig) ([]string, error) {
	flags, err := buildNetworkFlags(cfg.Network)
	if err != nil {
		return nil, err
	}
	if _, _, err := LocalArtifactInputsFor(cfg.Network, cfg.Conformance, "linux"); err != nil {
		return nil, err
	}
	if cfg.Conformance {
		flags = append(flags, "--conformance")
	}
	return flags, nil
}

func buildNetworkFlags(network string) ([]string, error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return nil, err
	}
	if mode == "upstream-ovn" {
		return []string{"--network", mode}, nil
	}
	// The explicit Calico diagnostic retains the NVIDIA build inputs; it
	// changes deployment intent only. No ambient variable selects a catalog.
	return nil, nil
}

// LocalArtifactInputs selects the Linux image-build inputs for the existing
// artifact workflow. Guest deployment keeps its shared native KVM/HVF owner.
// The TCG inputs remain explicit diagnostics; KVM never falls back to them.
func LocalArtifactInputs(network string) (definition, environment string, err error) {
	return LocalArtifactInputsFor(network, false, "linux")
}

// LocalArtifactInputsFor selects the shared product Definition and the host
// Environment. Conformance adds version-matched diagnostic tools to the SAME Run.
// Host architecture and execution capabilities remain admitted by the build owner.
func LocalArtifactInputsFor(network string, conformance bool, hostOS string) (definition, environment string, err error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return "", "", err
	}
	definition = "inputs/definitions/demo/arm64-nvidia-local-kvm.json"
	if mode == "upstream-ovn" {
		definition = "inputs/definitions/demo/arm64-upstream-local-kvm.json"
	}
	if conformance {
		if mode == "kind-calico" {
			return "", "", configUsage("conformance requires the selected OVN demo")
		}
		definition = strings.TrimSuffix(definition, ".json") + "-conformance.json"
	}
	switch hostOS {
	case "linux":
		environment = "inputs/environments/local-arm64-kvm.json"
	case "darwin":
		environment = "inputs/environments/local-native-mac.json"
	default:
		return "", "", nvkrerr.New(nvkrerr.CodeEngineCapability, "local demo input selection does not support host OS %q", hostOS)
	}
	return definition, environment, nil
}

func preparedBuildInvocation(cfg BuildConfig) ([]string, error) {
	bootstrap := filepath.ToSlash(filepath.Join(".nvkr/laptop-bootstrap", cfg.AttemptPrefix))
	held, info, err := securepath.OpenRegular(filepath.Join(cfg.Repository, bootstrap, "build-invocation.json"))
	if err != nil {
		return nil, err
	}
	defer held.Close()
	var stat unix.Stat_t
	if info.Mode().Perm() != 0o600 || info.Size() > 16<<10 || unix.Fstat(int(held.Fd()), &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return nil, errors.New("require bounded private owned build invocation")
	}
	raw, err := io.ReadAll(io.LimitReader(held, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return nil, errors.New("cannot read bounded private build invocation")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, errors.New("invalid private build invocation")
	}
	var args []string
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&args); err != nil || len(args) < 5 {
		return nil, errors.New("invalid private build invocation")
	}
	if cfg.Definition != "" {
		want := selectedBuildInvocation(cfg)
		if !slices.Equal(args, want) {
			return nil, errors.New("private build invocation differs from the selected NVKR inputs")
		}
		return args, nil
	}
	definition := args[4]
	authored, environment, err := LocalArtifactInputsFor(cfg.Network, cfg.Conformance, "linux")
	if err != nil {
		return nil, err
	}
	if definition != authored && definition != bootstrap+"/definition.json" {
		return nil, errors.New("private build invocation selected an unsupported definition path")
	}
	want := []string{bootstrap + "/nvkr", "run", "local", "--definition", definition,
		"--environment", environment, "--attempt", cfg.AttemptPrefix,
		"--attempts-root", ".nvkr/runs", "--runner-source", ".", "--jobs", strconv.Itoa(cfg.Jobs), "--runtime", "docker", "--capture-inputs-root=.", "--ui=plain"}
	if !slices.Equal(args, want) {
		return nil, errors.New("private build invocation differs from the supported local command")
	}
	return args, nil
}

func preparedBuildEnvironment(environment []string, cfg BuildConfig) ([]string, error) {
	// The preparation shell's only additional Git configuration is this exact
	// NVKE redirect pair. Recreate it from the already-admitted clone rather than
	// copying arbitrary shell-exported environment into the final process.
	env, err := localBuildSourceEnvironment(environment, environmentValue(environment, "NVKE_LOCAL_CLONE"))
	if err != nil {
		return nil, err
	}
	return SetEnv(env, map[string]string{"TMPDIR": filepath.Join(cfg.Repository, ".nvkr/laptop-bootstrap", cfg.AttemptPrefix, "tmp")}), nil
}

func localBuildSourceEnvironment(environment []string, clone string) ([]string, error) {
	env := append([]string(nil), environment...)
	if clone != "" {
		count := 0
		if declared := environmentValue(env, "GIT_CONFIG_COUNT"); declared != "" {
			var err error
			count, err = strconv.Atoi(declared)
			if err != nil || count < 0 || count > 14 {
				return nil, errors.New("local NVKE redirect requires two free Git entries")
			}
		}
		for _, upstream := range []string{"https://github.com/dsx-ai-factory/nvke.git", "https://github.com/dsx-ai-factory/nvke"} {
			suffix := strconv.Itoa(count)
			env = SetEnv(env, map[string]string{"GIT_CONFIG_KEY_" + suffix: "url." + clone + ".insteadOf", "GIT_CONFIG_VALUE_" + suffix: upstream})
			count++
		}
		env = SetEnv(env, map[string]string{"GIT_CONFIG_COUNT": strconv.Itoa(count)})
	}
	if _, err := sourcegit.LocalRedirectEnvironment(env); err != nil {
		return nil, err
	}
	return env, nil
}

func prepareLocalArtifacts(ctx context.Context, runner Runner, script string, cfg BuildConfig) error {
	if cfg.Definition != "" {
		return prepareSelectedBuild(ctx, runner, cfg)
	}
	flags, err := buildSelectionFlags(cfg)
	if err != nil {
		return err
	}
	args := append([]string{"bash", script, "--prepare-only"}, flags...)
	return runner.Run(ctx, append(args, cfg.AttemptPrefix, strconv.Itoa(cfg.Jobs), "all"), "build-prepare", 0)
}
