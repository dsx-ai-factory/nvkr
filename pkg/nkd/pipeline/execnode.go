package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// ExecNode executes one validated producer instruction. Launch selects the same
// pinned-container mechanics used by Local.Run; otherwise the instruction runs in
// the current environment. Shared execution code does not establish qualification:
// runtime observations and terminal artifact receipts remain separate gates.
func (l Local) ExecNode(n ir.Node, opts RunOptions) (*ArtifactResult, error) {
	if err := requireRunContext(opts.Ctx, "exec-node"); err != nil {
		return nil, err
	}
	if err := n.Validate(); err != nil {
		return nil, err
	}
	if opts.SourceWorkspace != nil && (n.Kind != ir.KindFetch || !opts.Launch || len(n.VendorCmd) != 0 || opts.Qualification != nil) {
		return nil, e.New(e.CodeEngineCapability, "held source workspace requires launched, unqualified source fetching without dependency vendoring")
	}
	if opts.SourceGitEnvironment != nil && opts.SourceWorkspace == nil {
		return nil, e.New(e.CodeEngineCapability, "source Git environment requires its held source workspace")
	}
	if err := rejectUnsupportedExecutionRequirements([]ir.Node{n}, "exec-node"); err != nil {
		return nil, err
	}
	if n.BuilderType == ir.BuilderBuildKit && (!opts.Launch || opts.Qualification != nil || opts.Package) {
		return nil, e.New(e.CodeEngineCapability, "rootless BuildKit requires launched, unqualified OCI export execution")
	}
	opts = withDefaults(opts)
	if err := opts.ctx().Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "exec-node %q canceled", n.ID)
	}
	if opts.Launch {
		return l.execNodeLaunch(n, opts)
	}
	var (
		artifact *ArtifactResult
		err      error
	)
	switch n.Kind {
	case ir.KindFetch:
		err = l.execFetch(n, opts)
	case ir.KindPull:
		artifact, err = l.pull(n, opts)
	case ir.KindBuild:
		artifact, err = l.execBuild(n, opts)
	case ir.KindVerify:
		return nil, nil // the terminal verify is orchestration, not a per-node exec
	default:
		return nil, e.New(e.CodeInternal, "exec-node: unknown node kind %q", n.Kind)
	}
	return artifact, mapCancellation(opts.ctx(), err, fmt.Sprintf("exec-node %q", n.ID))
}

// execNodeLaunch is the ADR-18 §8 execution: rather than running the node's work
// directly and trusting an ambient engine-provided container, the helper launches the
// pinned toolchain container itself (fetch/vendor with network; build with
// --network=none) via the SAME Local.Run code paths (l.fetch/l.build/l.pull). It owns a
// container-cleanup reaper for the duration so a canceled job leaves no live containers.
// Isolation is enforced by the flag the verified helper passes, portable to any generic
// runner — no engine-native container block, no bespoke network-none runner tag.
func (l Local) execNodeLaunch(n ir.Node, opts RunOptions) (*ArtifactResult, error) {
	if opts.progress == nil {
		opts.progress = &progressSink{w: opts.Stdout}
	}
	if opts.reap == nil {
		opts.reap = newReaper(opts.ctx(), opts.Runtime)
		defer opts.reap.close()
	}
	var (
		artifact *ArtifactResult
		err      error
	)
	switch n.Kind {
	case ir.KindFetch:
		err = l.fetch(n, opts)
	case ir.KindPull:
		artifact, err = l.pull(n, opts)
	case ir.KindBuild:
		artifact, err = l.build(n, opts)
	case ir.KindVerify:
		return nil, nil // the terminal verify is orchestration, not a per-node exec
	default:
		return nil, e.New(e.CodeInternal, "exec-node --launch: unknown node kind %q", n.Kind)
	}
	return artifact, mapCancellation(opts.ctx(), err, fmt.Sprintf("exec-node --launch %q", n.ID))
}

// execBuild runs a build node's command DIRECTLY (no docker), with the node's pinned
// env, in the checked-out source dir, then collects + hashes the artifact. This is the
// in-container half of a build: the engine (or the local orchestrator) has supplied
// the pinned toolchain, so exec-node just runs the command the IR pins.
func (l Local) execBuild(n ir.Node, opts RunOptions) (*ArtifactResult, error) {
	if len(n.Command) == 0 {
		return nil, e.New(e.CodeBuildFailed, "exec-node build %q has no command", n.Component)
	}
	if !n.SourceReadOnly || n.WriteDir == "" {
		return nil, e.New(e.CodeEngineCapability, "exec-node build %q requires sourceReadOnly=true and an explicit writeDir", n.Component)
	}
	if len(n.Materials) != 0 {
		return nil, e.New(e.CodeEngineCapability,
			"exec-node build %q with material inputs requires --launch", n.Component)
	}
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[exec-node:%s] materialize authenticated source; apply %d patch(es); sandbox=%s; (%s) %v\n", n.Component, len(n.Patches), ExecutionSandboxMode, n.BuilderType, n.Command)
		return nil, nil
	}
	snapshot, err := materializeSource(n, opts)
	if err != nil {
		return nil, err
	}
	defer snapshot.cleanup()
	workdir, err := sourceWorkdir(snapshot.Dir, n.Workdir)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolving exec-node workdir for %s", n.Component)
	}
	writeDir, err := pathWithin(snapshot.Dir, n.WriteDir)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolving exec-node output dir for %s", n.Component)
	}
	if err := os.MkdirAll(writeDir, 0o755); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating exec-node output dir for %s", n.Component)
	}
	sandboxState := filepath.Join(writeDir, ".nkd-sandbox")
	sandboxEnv := map[string]string{
		"HOME":       filepath.Join(sandboxState, "home"),
		"TMPDIR":     filepath.Join(sandboxState, "tmp"),
		"GOCACHE":    filepath.Join(sandboxState, "go-build"),
		"GOPATH":     filepath.Join(sandboxState, "go-path"),
		"GOMODCACHE": filepath.Join(sandboxState, "go-mod"),
	}
	for _, dir := range sandboxEnv {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "creating private sandbox state for %s", n.Component)
		}
	}

	self, err := os.Executable()
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "resolving runner executable for sandboxed build %s", n.Component)
	}
	sandboxArgs := []string{"sandbox-exec", "--write-root", writeDir, "--"}
	sandboxArgs = append(sandboxArgs, n.Command...)
	fmt.Fprintf(opts.Stdout, "\n=== exec-node build %s (%s, sandbox=%s) ===\n", n.Component, n.BuilderType, ExecutionSandboxMode)
	cmd := execCmd(opts, self, sandboxArgs...)
	cmd.Dir = workdir
	cmd.Env = deterministicExecEnv(n.Env, sandboxEnv)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if err := runOwnedCommand(opts.ctx(), cmd); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "exec-node building %s", n.Component)
	}

	artPath := artifactHostPath(opts, n)
	if err := os.RemoveAll(privOutDir(opts, n)); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resetting exec-node output for %s", n.Component)
	}
	if err := copyCollectedArtifactContext(opts.ctx(), snapshot.Dir, n.ArtifactPath, artPath); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "persisting exec-node artifact for %s", n.Component)
	}
	sum, size, _, err := hashRegularBeneathContext(opts.ctx(), privOutDir(opts, n), artifactOutputRelative(n))
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "exec-node collecting artifact for %s", n.Component)
	}
	return &ArtifactResult{
		Component: n.Component, OutputType: n.OutputType,
		Qualification: ArtifactUnqualified, Toolchain: n.Toolchain,
		InputDigest: snapshot.InputDigest,
		// Direct exec-node cannot observe its engine-provided container manifest from
		// inside a generic CI job. Without a provider attestation adapter this result is
		// deliberately unqualified: it must never copy the declared builder into the
		// observed engineClass field.
		BuilderType: n.BuilderType,
		Path:        n.ArtifactPath, SHA256: sum, Size: size, AbsPath: artPath,
	}, nil
}

// execFetch acquires + pin-verifies source and vendors DIRECTLY (no docker), because
// exec-node already runs inside the engine-provided container (ADR-18 §3, no DinD).
// It mirrors Local.fetch but runs the vendor command in-process rather than launching
// a toolchain container.
func (l Local) execFetch(n ir.Node, opts RunOptions) error {
	dir, err := pathWithin(opts.SourceRoot, n.Dest)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "resolving exec-node fetch destination %q", n.Dest)
	}
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[exec-node:fetch] %s @ %s -> %s (verify tree %s)\n", n.Repo, abbrev(n.Commit), dir, abbrev(n.TreeHash))
		return nil
	}
	if head, err := gitOut(opts.ctx(), dir, "rev-parse", "HEAD"); err != nil || head != n.Commit {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "creating source dir %s", dir)
		}
		for _, s := range [][]string{
			{"init", "-q"}, {"remote", "add", "origin", n.Repo},
			{"fetch", "-q", "--depth", "1", "origin", n.Commit},
			{"checkout", "-q", "--detach", "FETCH_HEAD"},
		} {
			if _, err := gitOut(opts.ctx(), dir, s...); err != nil {
				if opts.ctx().Err() != nil {
					return err
				}
				if s[0] != "remote" {
					return e.Wrap(e.CodeFetchFailed, err, "git %s in %s", strings.Join(s, " "), dir)
				}
			}
		}
	}
	if err := l.verifyTree(dir, n, opts); err != nil {
		return err
	}
	return ensureSourceClosure(n, dir, opts, func() error {
		fmt.Fprintf(opts.Stdout, "[exec-node:vendor] %v\n", n.VendorCmd)
		cmd := execCmd(opts, n.VendorCmd[0], n.VendorCmd[1:]...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = opts.Stdout, opts.Stderr
		if err := runOwnedCommand(opts.ctx(), cmd); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "vendoring %s", n.Dest)
		}
		return nil
	})
}

// deterministicExecEnv keeps the pinned toolchain's small execution substrate and
// overlays the IR-owned variables. CI-injected secrets, locale, proxy, and arbitrary
// host variables never enter an offline build. The selected container image pins the
// meaning of the retained toolchain paths/flags.
func deterministicExecEnv(env []ir.EnvVar, overrides ...map[string]string) []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true,
		"GOROOT": true, "GOPATH": true, "GOCACHE": true, "GOMODCACHE": true,
		"CC": true, "CXX": true, "AR": true, "LD": true, "PKG_CONFIG_PATH": true,
		"CGO_CFLAGS": true, "CGO_CPPFLAGS": true, "CGO_CXXFLAGS": true, "CGO_LDFLAGS": true,
	}
	values := map[string]string{}
	for _, pair := range os.Environ() {
		name, value, ok := strings.Cut(pair, "=")
		if ok && allowed[name] {
			values[name] = value
		}
	}
	for _, ev := range env {
		values[ev.Name] = ev.Value
	}
	for _, override := range overrides {
		for name, value := range override {
			values[name] = value
		}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+values[k])
	}
	return out
}

func sourceWorkdir(root, workdir string) (string, error) {
	if workdir == "" || workdir == "/src" {
		return root, nil
	}
	if !strings.HasPrefix(workdir, "/src/") {
		return "", fmt.Errorf("workdir %q is outside /src", workdir)
	}
	return pathWithin(root, strings.TrimPrefix(workdir, "/src/"))
}
