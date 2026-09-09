package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nhensley/nkdctl/pkg/nkd/assemble"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/profile"
)

// ctx returns the run's context. Public execution entry points reject nil before
// calling helpers; the background fallback is limited to non-authoritative internal
// helpers.
func (o RunOptions) ctx() context.Context {
	if o.Ctx != nil {
		return o.Ctx
	}
	return context.Background()
}

func mapCancellation(ctx context.Context, err error, operation string) error {
	if err == nil {
		return nil
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return e.Wrap(e.CodeTimeout, ctxErr, "%s canceled", operation)
		}
	}
	return err
}

// progressSink keeps short lifecycle messages live even while each node's detailed
// log remains privately buffered for non-interleaved output. The scheduler and
// concurrently running build nodes share it, so every write is serialized.
type progressSink struct {
	mu sync.Mutex
	w  io.Writer
}

func (p *progressSink) printf(format string, args ...any) {
	if p == nil || p.w == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.w, format, args...)
}

func (p *progressSink) write(data []byte) {
	if p == nil || p.w == nil || len(data) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = p.w.Write(data)
}

// execCmd builds a context-aware subprocess that, on ctx cancel, is sent SIGTERM and
// then SIGKILL after a grace delay (ADR-17 §2), leaving no runaway process.
func execCmd(opts RunOptions, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(opts.ctx(), name, args...)
	configureCommandCancellation(cmd, 10*time.Second)
	return cmd
}

// runOwnedCommand joins the direct child and then force-kills any descendants in
// its private process group. This is required even after a successful direct-child
// exit: qualified commands may not daemonize or transfer process ownership.
func runOwnedCommand(ctx context.Context, cmd *exec.Cmd) error {
	err := cmd.Run()
	cleanupErr := killCommandGroup(cmd)
	if err != nil {
		return err
	}
	if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
		return cleanupErr
	}
	return nil
}

func outputOwnedCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.Output()
	cleanupErr := killCommandGroup(cmd)
	if err != nil {
		return out, err
	}
	if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
		return out, cleanupErr
	}
	return out, nil
}

// runContainerRuntime is the only subprocess boundary for runtime-backed build
// operations. Qualified execution dispatches through the held executable
// capability; development and prequalification acquisition retain the configured
// runtime pathname behavior.
func runContainerRuntime(opts RunOptions, args ...string) error {
	if opts.execution != nil && opts.execution.runtime != nil {
		return opts.execution.runtime.run(opts.ctx(), opts.Stdout, opts.Stderr, args...)
	}
	cmd := execCmd(opts, opts.Runtime, args...)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	return runOwnedCommand(opts.ctx(), cmd)
}

// reaper is the container cleanup registry (ADR-17 §2): every started container is
// registered by name and receives a bounded force-removal attempt on context cancel
// or Run exit. Cleanup never waits indefinitely for an unresponsive daemon.
type reaper struct {
	mu             sync.Mutex
	names          map[string]bool
	docker         string
	runtime        *heldRuntimeExecutable
	cleanupCommand reaperCleanupCommand
	closing        bool
	cleanupTimeout time.Duration
	stop           chan struct{}
	done           chan struct{}
	stopOnce       sync.Once
}

const (
	defaultReaperCleanupTimeout = 2 * time.Second
	reaperTerminationGrace      = 250 * time.Millisecond
)

func newReaper(ctx context.Context, docker string) *reaper {
	if ctx == nil {
		ctx = context.Background()
	}
	r := &reaper{
		names: map[string]bool{}, docker: docker,
		cleanupTimeout: defaultReaperCleanupTimeout,
		cleanupCommand: runReaperCleanupCommand,
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
	}
	go r.watch(ctx)
	return r
}

func (r *reaper) watch(ctx context.Context) {
	defer close(r.done)
	select {
	case <-ctx.Done():
	case <-r.stop:
	}
	r.cleanup()
}

// add registers a container before its runtime subprocess starts. Once cleanup
// begins no new work may enter the registry: the caller must fail without starting
// its subprocess so the cleanup snapshot remains exhaustive.
func (r *reaper) add(name string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return false
	}
	r.names[name] = true
	return true
}

// forget removes a container only after its runtime command succeeded. Failed or
// canceled commands stay registered because the client may have exited while the
// daemon-side container continued running.
func (r *reaper) forget(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.names, name)
	r.mu.Unlock()
}

func (r *reaper) setRuntime(runtimePath string, runtime *heldRuntimeExecutable) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.docker = runtimePath
	r.runtime = runtime
	r.mu.Unlock()
}

func (r *reaper) close() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done
}

// cleanup takes one closed-world snapshot under the registry mutex, then performs
// all subprocess I/O without the mutex. add rejects later registrations, so the
// snapshot cannot miss a container that is allowed to start. One context bounds the
// entire best-effort cleanup rather than multiplying the timeout by container count.
func (r *reaper) cleanup() {
	r.mu.Lock()
	r.closing = true
	names := make([]string, 0, len(r.names))
	for name := range r.names {
		names = append(names, name)
	}
	sort.Strings(names)
	docker, runtimeExecutable := r.docker, r.runtime
	cleanupCommand := r.cleanupCommand
	timeout := r.cleanupTimeout
	r.mu.Unlock()

	if len(names) == 0 {
		return
	}
	if timeout <= 0 {
		timeout = defaultReaperCleanupTimeout
	}
	if cleanupCommand == nil {
		cleanupCommand = runReaperCleanupCommand
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	grace := reaperTerminationGrace
	if grace >= remaining {
		grace = remaining / 2
	}
	if grace <= 0 {
		return
	}
	// Every registered container receives an independent removal attempt under the
	// same overall deadline. One unresponsive client cannot prevent the other names
	// from reaching the daemon.
	commandCtx, commandCancel := context.WithDeadline(ctx, deadline.Add(-grace))
	defer commandCancel()
	var cleanup sync.WaitGroup
	cleanup.Add(len(names))
	for _, name := range names {
		go func() {
			defer cleanup.Done()
			_ = cleanupCommand(commandCtx, grace, docker, runtimeExecutable, []string{name})
		}()
	}
	cleanup.Wait()
}

type reaperCleanupCommand func(context.Context, time.Duration, string, *heldRuntimeExecutable, []string) error

func runReaperCleanupCommand(ctx context.Context, grace time.Duration, docker string,
	runtimeExecutable *heldRuntimeExecutable, names []string,
) error {
	args := append([]string{"rm", "-f"}, names...)
	if runtimeExecutable != nil {
		return runtimeExecutable.runCleanup(ctx, grace, nil, nil, args...)
	}
	cmd := execCmd(RunOptions{Ctx: ctx}, docker, args...) //nkd:allow-nondeterminism best-effort cleanup, not identity
	cmd.WaitDelay = grace
	return runOwnedCommand(ctx, cmd)
}

func registerContainer(opts RunOptions, name string) error {
	if opts.reap != nil && opts.reap.add(name) {
		return nil
	}
	if err := opts.ctx().Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "container cleanup began before %q could start", name)
	}
	return e.New(e.CodeInternal, "container cleanup registry closed before %q could start", name)
}

// containerName is a per-run, per-node container name (execution-only, never identity).
func containerName(n ir.Node) string {
	return fmt.Sprintf("nkd-%d-%s", os.Getpid(), sanitizeName(n.ID))
}

func sanitizeName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

// Local is the ground-truth runner: it executes each IR node directly with a
// container runtime, applying the same pins, env allowlist, and --network=none
// hermeticity the IR declares (NKD-FR-BLD-002).
type Local struct{}

func (Local) Name() string { return "local" }

type nodeOutcome struct {
	node              ir.Node
	res               NodeResult
	art               *ArtifactResult
	err               error
	log               *bytes.Buffer
	sourcePreparation bool
	sourceKey         string
}

// Run executes the graph as a concurrent DAG. Fetches and distinct exact source
// preparations are scheduled before their consumers; builds with identical source
// inputs share one run-owned verified snapshot instead of occupying work slots while
// waiting on duplicate copies. Work runs in parallel up to opts.Jobs, gated by real
// dependencies, and verify waits on every selected producer. It returns a Report
// seeding the expected-digests manifest, failing closed on the first build/verify
// failure unless KeepGoing is set. The report is deterministic (nodes/artifacts
// sorted) regardless of completion order.
func (l Local) Run(g *ir.Graph, recipeDigest string, opts RunOptions) (*Report, error) {
	if err := requireRunContext(opts.Ctx, "local pipeline run"); err != nil {
		return &Report{}, err
	}
	if err := opts.Ctx.Err(); err != nil {
		return &Report{}, e.Wrap(e.CodeTimeout, err, "local pipeline canceled")
	}
	// The legacy packagers reopen producer paths and materialize whole artifacts in
	// memory. Until packaging is descriptor-held, streaming, and context-aware, it
	// cannot be part of a qualified execution claim. Reject the unsupported
	// capability before defaults, cleanup watchers, graph inspection, or path I/O.
	if opts.Qualification != nil && opts.Package {
		return &Report{}, e.New(e.CodeEngineCapability,
			"qualified local packaging requires a descriptor-held streaming packager")
	}
	opts = withDefaults(opts)
	if opts.progress == nil {
		opts.progress = &progressSink{w: opts.Stdout}
	}
	if opts.reap == nil {
		opts.reap = newReaper(opts.ctx(), opts.Runtime)
	}
	opts.sources = newPreparedSourceCache()
	// Own the watcher before any fallible Run work. This defer covers graph/selection
	// validation, fetch, stage two, scheduling, terminal verification, and success.
	// Runtime capability cleanup must follow reaper.close because cleanup executes
	// that held capability when qualification reached stage two.
	defer func() {
		opts.reap.close()
		opts.sources.close()
		if opts.execution != nil {
			opts.execution.cleanupRuntime()
		}
	}()
	if err := validateGraphIdentity(g, recipeDigest); err != nil {
		return &Report{}, err
	}
	rep := &Report{RecipeID: g.RecipeID, RecipeDigest: recipeDigest}

	ordered, err := topoSort(g.Nodes) // fail fast on a cyclic graph
	if err != nil {
		return rep, err
	}
	included, err := selectNodes(g.Nodes, opts.Only)
	if err != nil {
		return rep, err
	}
	var selectedBuildNodes []ir.Node
	for _, node := range g.Nodes {
		if included[node.ID] && node.Kind == ir.KindBuild {
			selectedBuildNodes = append(selectedBuildNodes, node)
		}
	}

	// Snapshot and validate stage one before any network access. This prevents a
	// caller from changing the selected profile between host preflight and stage two,
	// and rejects zero/forged host receipts before source or toolchain materialization.
	if len(selectedBuildNodes) > 0 && !opts.DryRun && opts.Qualification != nil {
		opts.Qualification = &QualifiedExecution{
			Profile:     cloneProfile(opts.Qualification.Profile),
			HostReceipt: opts.Qualification.HostReceipt,
			Observer:    opts.Qualification.Observer,
		}
		if opts.Qualification.Observer == nil {
			return rep, e.New(e.CodePreflightFailed, "qualified execution requires an execution observer")
		}
		if err := opts.Qualification.HostReceipt.RequireFor(opts.ctx(), opts.Qualification.Profile); err != nil {
			return rep, err
		}
		if err := profile.ValidateExecutionPlan(opts.ctx(), opts.Qualification.Profile, selectedBuildNodes); err != nil {
			return rep, err
		}
	}
	// Stage two is intentionally after source/fetch inputs have crossed their trust
	// boundary. Fetching is not artifact production; every selected fetch completes
	// and is pin-verified before the observer measures the actual runner, runtime, and
	// materialized toolchains. Nix remains fail-closed until its realized closure can
	// be measured. No build node can enter the scheduler before this barrier passes.
	materializedFetch := map[string]bool{}
	if len(selectedBuildNodes) > 0 && !opts.DryRun && opts.Qualification != nil {
		for _, node := range ordered {
			if !included[node.ID] || node.Kind != ir.KindFetch {
				continue
			}
			for _, dependency := range node.Needs {
				if included[dependency] && !materializedFetch[dependency] {
					return rep, e.New(e.CodeInvalidCatalog,
						"qualified fetch node %q depends on non-materialized node %q", node.ID, dependency)
				}
			}
			if !opts.DryRun {
				reportNodeStart(opts.progress, node)
			}
			out := l.runNode(node, nil, included, nil, opts)
			if out.log != nil && out.log.Len() > 0 {
				opts.progress.write(out.log.Bytes())
			}
			if !opts.DryRun {
				reportNodeDone(opts.progress, out)
			}
			rep.Nodes = append(rep.Nodes, out.res)
			if out.err != nil {
				sortNodeResults(rep.Nodes)
				return rep, out.err
			}
			materializedFetch[node.ID] = true
		}
		opts.execution, opts.Runtime, err = qualifySelectedExecution(
			opts.ctx(), opts.Qualification, opts.Runtime, selectedBuildNodes, true,
		)
		if err != nil {
			return rep, err
		}
		opts.reap.setRuntime(opts.Runtime, opts.execution.runtime)
	}

	nodes := map[string]ir.Node{}
	producers := map[string]ir.Node{} // nodes that yield an artifact: builds and pulls
	for _, n := range g.Nodes {
		nodes[n.ID] = n
		if n.Kind == ir.KindBuild || n.Kind == ir.KindPull {
			producers[n.ID] = n
		}
		if !included[n.ID] {
			rep.Nodes = append(rep.Nodes, NodeResult{ID: n.ID, Kind: n.Kind, Status: "skipped"})
		}
	}

	// Dependency counts over the INCLUDED subgraph only.
	indeg := map[string]int{}
	children := map[string][]string{}
	for id := range included {
		indeg[id] = 0
	}
	for id := range included {
		for _, dep := range nodes[id].Needs {
			if included[dep] {
				indeg[id]++
				children[dep] = append(children[dep], id)
			}
		}
	}

	jobs := opts.Jobs
	if jobs < 1 {
		jobs = 1
	}

	var (
		mu        sync.Mutex
		scheduled = map[string]bool{}
		preparing = map[string]bool{}
		prepared  = map[string]bool{}
		firstErr  error
		stop      bool
		inflight  int
	)
	for id := range materializedFetch {
		scheduled[id] = true
		for _, child := range children[id] {
			indeg[child]--
		}
	}
	results := make(chan nodeOutcome)

	// launch starts every currently-ready included node, in stable priority order, up
	// to the jobs limit. A build whose exact source key is already being prepared is
	// skipped for this pass so it cannot consume a slot merely waiting on single-flight;
	// another distinct fetch/source/build can use that slot. With jobs==1 this
	// degrades to fully serial, deterministic order.
	launch := func() {
		if stop {
			return
		}
		var ready []string
		for id := range included {
			if indeg[id] == 0 && !scheduled[id] {
				ready = append(ready, id)
			}
		}
		sort.Slice(ready, func(i, j int) bool {
			left, right := nodes[ready[i]], nodes[ready[j]]
			if nodeScheduleRank(left) != nodeScheduleRank(right) {
				return nodeScheduleRank(left) < nodeScheduleRank(right)
			}
			return ready[i] < ready[j]
		})
		for _, id := range ready {
			if inflight >= jobs {
				break
			}
			n := nodes[id]
			if n.Kind == ir.KindBuild && !opts.DryRun {
				key, keyErr := preparedSourceKey(n)
				if keyErr == nil && !prepared[key] {
					if preparing[key] {
						continue
					}
					preparing[key] = true
					inflight++
					opts.progress.printf("[start] source %s for %s — prepare one verified snapshot for identical ready build inputs\n", n.SourceDest, n.Component)
					go func() { results <- l.prepareSource(n, key, opts) }()
					continue
				}
			}
			scheduled[id] = true
			inflight++
			if !opts.DryRun {
				reportNodeStart(opts.progress, n)
			}
			// verify reads the artifacts collected so far; snapshot under the lock.
			// It only becomes ready once all its build deps have reported, so the
			// snapshot is complete.
			collected := append([]ArtifactResult(nil), rep.Artifacts...)
			go func() { results <- l.runNode(n, producers, included, collected, opts) }()
		}
	}

	mu.Lock()
	launch()
	for inflight > 0 {
		mu.Unlock()
		out := <-results
		mu.Lock()
		inflight--
		if out.sourcePreparation {
			delete(preparing, out.sourceKey)
			if out.log != nil && out.log.Len() > 0 {
				opts.progress.write(out.log.Bytes())
			}
			if out.err != nil {
				scheduled[out.node.ID] = true
				out.res.Detail = out.err.Error()
				rep.Nodes = append(rep.Nodes, out.res)
				if firstErr == nil {
					firstErr = out.err
				}
				if !opts.KeepGoing {
					stop = true
				}
				for _, child := range children[out.node.ID] {
					indeg[child]--
				}
			} else {
				prepared[out.sourceKey] = true
				opts.progress.printf("[done] source %s for %s (%s)\n", out.node.SourceDest, out.node.Component, out.res.Duration.Round(time.Millisecond))
			}
			launch()
			continue
		}
		if out.log != nil && out.log.Len() > 0 {
			opts.progress.write(out.log.Bytes())
		}
		if !opts.DryRun {
			reportNodeDone(opts.progress, out)
		}
		rep.Nodes = append(rep.Nodes, out.res)
		if out.art != nil {
			rep.Artifacts = append(rep.Artifacts, *out.art)
		}
		if out.err != nil {
			if firstErr == nil {
				firstErr = out.err
			}
			if !opts.KeepGoing {
				stop = true
			}
		}
		for _, c := range children[out.node.ID] {
			indeg[c]--
		}
		launch()
	}
	mu.Unlock()

	sortNodeResults(rep.Nodes)
	sort.Slice(rep.Artifacts, func(i, j int) bool { return rep.Artifacts[i].Component < rep.Artifacts[j].Component })
	if firstErr == nil && !opts.DryRun {
		terminal, err := issueTerminalReceipt(opts.ctx(), rep, producers, included, opts)
		if err != nil {
			return rep, err
		}
		if len(selectedBuildNodes) == 0 {
			if err := bindProfileFreeReport(opts.ctx(), rep, terminal); err != nil {
				return rep, err
			}
		} else if opts.execution != nil {
			if err := bindQualifiedReport(opts.ctx(), rep, opts.execution, terminal); err != nil {
				return rep, err
			}
		}
	}
	return rep, firstErr
}

func nodeScheduleRank(n ir.Node) int {
	switch n.Kind {
	case ir.KindFetch, ir.KindPull:
		return 0
	case ir.KindBuild:
		return 1
	case ir.KindVerify:
		return 2
	default:
		return 3
	}
}

func (l Local) prepareSource(n ir.Node, key string, opts RunOptions) nodeOutcome {
	buf := &bytes.Buffer{}
	nopts := opts
	nopts.Stdout = buf
	nopts.Stderr = buf
	started := time.Now()
	value, err := materializeSource(n, nopts)
	if value != nil {
		value.cleanup() // the run-owned cache retains the underlying snapshot
	}
	err = mapCancellation(opts.ctx(), err, fmt.Sprintf("source preparation for build node %q", n.ID))
	status := "ok"
	if err != nil {
		status = "failed"
	}
	return nodeOutcome{
		node: n, res: NodeResult{ID: n.ID, Kind: n.Kind, Duration: time.Since(started), Status: status},
		err: err, log: buf, sourcePreparation: true, sourceKey: key,
	}
}

func reportNodeStart(progress *progressSink, n ir.Node) {
	label := n.Component
	if label == "" && n.Kind == ir.KindFetch {
		label = strings.TrimSuffix(path.Base(n.Repo), ".git")
	}
	if label == "" {
		label = n.ID
	}
	switch n.Kind {
	case ir.KindFetch:
		progress.printf("[start] fetch %s — acquire and verify the pinned source closure before any build container\n", label)
	case ir.KindBuild:
		progress.printf("[start] build %s — prepare or reuse an exact verified source snapshot before launching its pinned container\n", label)
	case ir.KindPull:
		progress.printf("[start] pull %s — acquire and verify the pinned artifact\n", label)
	case ir.KindVerify:
		progress.printf("[start] verify %s — re-open and hash every artifact produced by this run\n", label)
	default:
		progress.printf("[start] %s %s\n", n.Kind, label)
	}
}

func reportNodeDone(progress *progressSink, out nodeOutcome) {
	label := out.node.Component
	if label == "" && out.node.Kind == ir.KindFetch {
		label = strings.TrimSuffix(path.Base(out.node.Repo), ".git")
	}
	if label == "" {
		label = out.node.ID
	}
	state := "done"
	if out.err != nil {
		state = "failed"
	}
	progress.printf("[%s] %s %s (%s)\n", state, out.node.Kind, label, out.res.Duration.Round(time.Millisecond))
}

// runNode executes one node, capturing its log into a private buffer so concurrent
// nodes don't interleave output; the scheduler flushes the buffer on completion.
func (l Local) runNode(n ir.Node, producers map[string]ir.Node, included map[string]bool, collected []ArtifactResult, opts RunOptions) nodeOutcome {
	buf := &bytes.Buffer{}
	nopts := opts
	nopts.Stdout = buf
	nopts.Stderr = buf

	start := time.Now()
	var (
		err error
		art *ArtifactResult
	)
	switch n.Kind {
	case ir.KindFetch:
		err = l.fetch(n, nopts)
	case ir.KindBuild:
		art, err = l.build(n, nopts)
	case ir.KindPull:
		art, err = l.pull(n, nopts)
	case ir.KindVerify:
		err = l.verify(n, producers, included, collected, nopts)
	default:
		err = e.New(e.CodeInvalidCatalog, "unknown node kind %q", n.Kind)
	}
	err = mapCancellation(opts.ctx(), err, fmt.Sprintf("%s node %q", n.Kind, n.ID))

	res := NodeResult{ID: n.ID, Kind: n.Kind, Duration: time.Since(start), Status: "ok"}
	if opts.DryRun && n.Kind != ir.KindVerify {
		res.Status = "dry-run"
	}
	if err != nil {
		res.Status = "failed"
		res.Detail = err.Error()
	}
	return nodeOutcome{node: n, res: res, art: art, err: err, log: buf}
}

// sortNodeResults orders the report by phase (fetch, build, verify) then ID, so the
// output is stable no matter what order nodes finished in.
func sortNodeResults(rs []NodeResult) {
	rank := map[string]int{ir.KindFetch: 0, ir.KindPull: 1, ir.KindBuild: 1, ir.KindVerify: 2}
	sort.Slice(rs, func(i, j int) bool {
		if rank[rs[i].Kind] != rank[rs[j].Kind] {
			return rank[rs[i].Kind] < rank[rs[j].Kind]
		}
		return rs[i].ID < rs[j].ID
	})
}

func withDefaults(o RunOptions) RunOptions {
	if o.SourceRoot == "" {
		o.SourceRoot = filepath.Join(".nkd", "work", "src")
	}
	if o.Runtime == "" {
		o.Runtime = "docker"
	}
	if o.OCI == nil {
		o.OCI = registryOCITransport{}
	}
	if o.PackageDir == "" {
		o.PackageDir = filepath.Join(filepath.Dir(o.SourceRoot), "packages")
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	return o
}

// fetch acquires pinned source into SourceRoot/Dest and verifies the commit and
// tree hash, failing closed on any mismatch (NKD-FR-BLD-022, NKD-FR-RC-023). An
// already-checked-out matching source closure is reused only through a re-hashed
// source receipt; HEAD alone is never sufficient.
func (l Local) fetch(n ir.Node, opts RunOptions) error {
	dir, err := pathWithin(opts.SourceRoot, n.Dest)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "resolving fetch destination %q", n.Dest)
	}
	abs, _ := filepath.Abs(dir)

	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[fetch] %s @ %s -> %s (verify tree %s)\n", n.Repo, abbrev(n.Commit), abs, abbrev(n.TreeHash))
		return nil
	}

	if head, err := gitOut(opts.ctx(), dir, "rev-parse", "HEAD"); err == nil && head == n.Commit {
		if err := l.verifyTree(dir, n, opts); err != nil {
			return err
		}
		return l.vendor(n, dir, opts)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "creating source dir %s", dir)
	}
	// Fetch exactly the pinned commit (shallow). GitHub serves reachable SHAs.
	steps := [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", n.Repo},
		{"fetch", "-q", "--depth", "1", "origin", n.Commit},
		{"checkout", "-q", "--detach", "FETCH_HEAD"},
	}
	for _, s := range steps {
		if _, err := gitOut(opts.ctx(), dir, s...); err != nil {
			if opts.ctx().Err() != nil {
				return err
			}
			// `remote add` fails if origin exists on a reused dir; tolerate.
			if s[0] == "remote" {
				continue
			}
			return e.Wrap(e.CodeFetchFailed, err, "git %s in %s", strings.Join(s, " "), dir)
		}
	}
	if err := l.verifyTree(dir, n, opts); err != nil {
		return err
	}
	return l.vendor(n, dir, opts)
}

// vendor completes the fetch closure. The optional vendor command runs in the
// toolchain container with network, after which ensureSourceClosure records and
// verifies the exact bytes consumed by the offline build.
func (l Local) vendor(n ir.Node, dir string, opts RunOptions) error {
	return ensureSourceClosure(n, dir, opts, func() error {
		abs, _ := filepath.Abs(dir)
		cname := "nkd-vendor-" + sanitizeName(n.Dest)
		args := append([]string{"run", "--rm", "--name", cname, "--network=" + ir.NetworkDefault, "-v", abs + ":/src", "-w", "/src", n.VendorImage}, n.VendorCmd...)
		fmt.Fprintf(opts.Stdout, "\n=== vendor %s (network, %s) ===\n", n.Dest, n.VendorImage)
		opts.progress.printf("[container] vendor %s — launching %s with network access\n", n.Dest, n.VendorImage)
		if err := registerContainer(opts, cname); err != nil {
			return err
		}
		if err := runContainerRuntime(opts, args...); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "vendoring %s", n.Dest)
		}
		opts.reap.forget(cname)
		return nil
	})
}

func (l Local) verifyTree(dir string, n ir.Node, opts RunOptions) error {
	head, err := gitOut(opts.ctx(), dir, "rev-parse", "HEAD")
	if err != nil {
		if opts.ctx().Err() != nil {
			return err
		}
		return e.Wrap(e.CodeFetchFailed, err, "resolving HEAD in %s", dir)
	}
	if head != n.Commit {
		return e.New(e.CodeFetchFailed, "commit mismatch in %s: got %s want %s", dir, head, n.Commit)
	}
	tree, err := gitOut(opts.ctx(), dir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		if opts.ctx().Err() != nil {
			return err
		}
		return e.Wrap(e.CodeFetchFailed, err, "resolving tree in %s", dir)
	}
	if tree != n.TreeHash {
		return e.New(e.CodeFetchFailed, "tree-hash mismatch in %s: got %s want %s", dir, tree, n.TreeHash)
	}
	return nil
}

// build runs one from-source build node in its pinned toolchain container, then
// records the artifact digest. When the node is isolated (SourceReadOnly + WriteDir),
// the shared source is mounted read-only and a fresh per-build output dir is mounted
// over WriteDir, so any number of build nodes can run concurrently without clobbering
// each other or mutating the shared source.
func (l Local) build(n ir.Node, opts RunOptions) (*ArtifactResult, error) {
	var executionIdentity profile.VerifiedNodeIdentity
	if opts.execution != nil {
		if err := opts.execution.receipt.RequireFor(opts.ctx(), opts.execution.profile, opts.execution.nodes); err != nil {
			return nil, err
		}
		var ok bool
		executionIdentity, ok = observedNodeIdentity(opts.execution, n.ID)
		if !ok {
			return nil, e.New(e.CodePreflightFailed,
				"build node %q is absent from the qualified execution receipt", n.ID)
		}
	}
	// Nix builder dispatch (ADR-10 §6/§7): a nix component is built from a LOCKED flake
	// inside the pinned nix container, offline. Hermeticity is enforced by the same
	// --network=none the oci-hermetic lane uses (Network==none in the IR), so the locked
	// closure must be pre-realized (no external Nix cache reachable at build time). Defense
	// in depth: fail closed if a nix node reached the runner without its closure pin or the
	// offline guarantee — never silently fall back to an impure build.
	if n.BuilderType == ir.BuilderNix {
		if n.FlakeLockDigest == "" {
			return nil, e.New(e.CodeUnpinnedSource, "nix build %q reached the runner with no locked-closure digest", n.Component)
		}
		if n.Network != ir.NetworkNone {
			return nil, e.New(e.CodeBuildFailed, "nix build %q must run offline (--network=none) so no substituter is reachable; got network=%q", n.Component, n.Network)
		}
	}
	isolated := n.SourceReadOnly && n.WriteDir != ""
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[build:%s] materialize authenticated source; apply %d patch(es); run %s with --network=%s\n",
			n.Component, len(n.Patches), n.Toolchain, n.Network)
		return nil, nil
	}
	snapshot, err := materializeSource(n, opts)
	if err != nil {
		return nil, err
	}
	defer snapshot.cleanup()
	// Docker bind-mount sources MUST be absolute: a relative `-v .nkd/...:/src` is parsed as a
	// (invalid) named volume, not a host path. opts.SourceRoot is relative in CI (the emitted
	// --source-root is repo-relative), so resolve both mount roots against cwd. (l.vendor does
	// the same for its source mount.)
	absSrc, absErr := filepath.Abs(snapshot.Dir)
	if absErr != nil {
		return nil, e.Wrap(e.CodeBuildFailed, absErr, "resolving absolute source dir for %s", n.Component)
	}
	outRoot := privOutDir(opts, n)
	if abs, err := filepath.Abs(outRoot); err == nil {
		outRoot = abs
	}
	if err := os.RemoveAll(outRoot); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resetting output dir for %s", n.Component)
	}
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating output dir for %s", n.Component)
	}

	cname := containerName(n)
	args := []string{"run", "--rm", "--name", cname}
	// Run the toolchain on the build's target platform (model B): forces the native
	// arch variant of a multi-arch toolchain — or emulation on a foreign host — so
	// GOARCH compilation is native, never a cross-compile. Derived from the IR's arch.
	if n.Platform != "" {
		args = append(args, "--platform", n.Platform)
	}
	if n.Network != "" {
		args = append(args, "--network="+n.Network)
	}
	// Only bound per-build CPU when builds actually run concurrently. `go build`
	// already parallelizes across all cores, so with jobs>1 we share the host cores
	// across the budget to avoid oversubscription; with jobs==1 the single build
	// must use ALL cores (measured optimal on one machine — capping it there was a
	// 7x regression). This is host-dependent execution tuning, deliberately NOT part
	// of the IR: GOMAXPROCS does not affect output bytes, so reproducibility holds.
	if opts.Jobs > 1 {
		procs := runtime.NumCPU() / opts.Jobs
		if procs < 1 {
			procs = 1
		}
		args = append(args, "-e", "GOMAXPROCS="+strconv.Itoa(procs))
	}
	for _, ev := range n.Env {
		args = append(args, "-e", ev.Name+"="+ev.Value)
	}
	if isolated {
		mountpoint, err := pathWithin(absSrc, n.WriteDir)
		if err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "resolving output mountpoint for %s", n.Component)
		}
		if err := os.MkdirAll(mountpoint, 0o755); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "preparing source mountpoint for %s", n.Component)
		}
		args = append(args, "-v", absSrc+":/src:ro", "-v", outRoot+":"+path.Join(n.Workdir, n.WriteDir))
	} else {
		args = append(args, "-v", absSrc+":/src")
	}
	args = append(args, "-w", n.Workdir, n.Toolchain)
	args = append(args, n.Command...)

	fmt.Fprintf(opts.Stdout, "\n=== build %s (%s, --network=%s, isolated=%t) ===\n", n.Component, n.Toolchain, n.Network, isolated)
	opts.progress.printf("[container] build %s — launching %s with --network=%s\n", n.Component, n.Toolchain, n.Network)
	if err := registerContainer(opts, cname); err != nil {
		return nil, err
	}
	if err := runContainerRuntime(opts, args...); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "building %s", n.Component)
	}
	opts.reap.forget(cname)

	binPath := artifactHostPath(opts, n)
	artifactRel := artifactOutputRelative(n)
	if !isolated {
		if err := copyCollectedArtifactContext(opts.ctx(), absSrc, n.ArtifactPath, binPath); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "persisting artifact for %s", n.Component)
		}
	}
	sum, size, _, err := hashRegularBeneathContext(opts.ctx(), outRoot, artifactRel)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "collecting artifact for %s", n.Component)
	}
	res := &ArtifactResult{
		Component:     n.Component,
		OutputType:    n.OutputType,
		Qualification: ArtifactUnqualified,
		Toolchain:     n.Toolchain,
		InputDigest:   snapshot.InputDigest,
		BuilderType:   n.BuilderType,
		// Local selected and executed this exact in-process dispatch lane. Recording
		// that observation is useful even on an unqualified development report;
		// ProfileDigest/ExecutionBinding below remain the authority boundary.
		EngineClass: n.BuilderType,
		Path:        n.ArtifactPath,
		SHA256:      sum,
		Size:        size,
		AbsPath:     binPath,
	}
	if opts.execution != nil {
		res.Qualification = ArtifactQualifiedExecution
		res.EngineClass = executionIdentity.EngineClass
		res.ProfileDigest = opts.execution.receipt.ProfileDigest()
		res.ExecutionBinding = opts.execution.receipt.ExecutionBinding()
	}
	// When packaging, assemble the compiled binary into its delivery artifact and
	// record THAT digest — a real oci-image/.deb digest, not a raw-binary hash.
	if opts.Package {
		if err := packageArtifact(n, binPath, opts, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func packageArtifact(n ir.Node, binPath string, opts RunOptions, res *ArtifactResult) error {
	epoch := envEpoch(n.Env)
	if err := os.MkdirAll(opts.PackageDir, 0o755); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "package dir")
	}
	arch := debArch(n.Platform)
	switch n.OutputType {
	case "oci-image":
		dir := filepath.Join(opts.PackageDir, n.Component+".oci")
		//nolint:staticcheck // Qualified Package is rejected before graph/path I/O; this is the explicit unqualified development lane.
		dig, err := assemble.OCIImage(binPath, n.Component, epoch, arch, dir)
		if err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "oci image for %s", n.Component)
		}
		res.SHA256 = dig
		res.Path = dir
		// the manifest blob's file sha256 equals the manifest (image) digest.
		res.AbsPath = filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(dig, "sha256:"))
		res.Packaged = true
	case "host-package":
		// Debian arch name matches GOARCH here (amd64, arm64); take it from the node's
		// target platform so a multi-arch build names its .deb correctly.
		out := filepath.Join(opts.PackageDir, fmt.Sprintf("%s_%s_%s.deb", n.Component, orVersion(n.Version), arch))
		//nolint:staticcheck // Qualified Package is rejected before graph/path I/O; this is the explicit unqualified development lane.
		dig, err := assemble.Deb(binPath, n.Component, orVersion(n.Version), epoch, arch, out)
		if err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "deb for %s", n.Component)
		}
		res.SHA256 = dig
		res.Path = out
		res.AbsPath = out
		res.Packaged = true
	default:
		return e.New(e.CodeUnsupported, "component %q: no packager for outputType %q", n.Component, n.OutputType)
	}
	// Record the packaged artifact's own size, not the raw binary's.
	if fi, err := os.Stat(res.AbsPath); err == nil {
		res.Size = fi.Size()
	}
	return nil
}

func envEpoch(env []ir.EnvVar) int64 {
	for _, ev := range env {
		if ev.Name == "SOURCE_DATE_EPOCH" {
			if v, err := strconv.ParseInt(ev.Value, 10, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

func orVersion(v string) string {
	if v == "" {
		return "0.0.0"
	}
	return v
}

// debArch maps a build node's platform (linux/<arch>) to the Debian arch used in a
// .deb filename. GOARCH and Debian's arch names coincide for the supported set
// (amd64, arm64); an unset platform uses the default amd64 architecture.
func debArch(platform string) string {
	if i := strings.LastIndex(platform, "/"); i >= 0 {
		if a := platform[i+1:]; a != "" {
			return a
		}
	}
	return "amd64"
}

// privOutDir is the per-build writable output directory on the host, shared by
// build (mount + collect) and verify (re-hash) so both resolve the same file. It is
// absolute because `docker -v` requires an absolute host path.
func privOutDir(opts RunOptions, n ir.Node) string {
	abs, _ := filepath.Abs(filepath.Join(filepath.Dir(opts.SourceRoot), "out", sanitizeName(n.Component)))
	return abs
}

// abbrev shortens a hash for display without assuming a minimum length.
func abbrev(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ArtifactPath is the exported resolver for a build node's produced binary on the
// host, so the packaging step can locate what a prior build compiled.
func ArtifactPath(opts RunOptions, n ir.Node) string { return artifactHostPath(opts, n) }

// artifactHostPath resolves the durable collected artifact path. Source snapshots
// are private and ephemeral, so every lane persists its output under privOutDir.
func artifactHostPath(opts RunOptions, n ir.Node) string {
	return filepath.Join(privOutDir(opts, n), artifactOutputRelative(n))
}

func artifactOutputRelative(n ir.Node) string {
	rel := filepath.Clean(n.ArtifactPath)
	if n.WriteDir != "" {
		if r, err := filepath.Rel(filepath.Clean(n.WriteDir), rel); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			rel = r
		}
	}
	return rel
}

// verify is the unconditional terminal node. It is authoritative about the current
// run: every included build must have produced an artifact THIS run (present in
// collected), and each artifact is re-opened and re-hashed and must match the
// digest recorded at build time. A missing, unreadable, or changed artifact fails
// closed — a stale artifact left by a previous run cannot satisfy it, because a
// build that failed this run contributes no collected entry (NKD-FR-BLD-006).
func (l Local) verify(n ir.Node, producers map[string]ir.Node, included map[string]bool, collected []ArtifactResult, opts RunOptions) error {
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[verify] would check %d artifact(s)\n", countIncludedProducers(producers, included))
		return nil
	}
	recorded := make(map[string]ArtifactResult, len(collected))
	for _, a := range collected {
		recorded[a.Component] = a
	}
	var comps []string
	producerByComponent := make(map[string]ir.Node, len(producers))
	for id, pn := range producers {
		if included[id] {
			comps = append(comps, pn.Component)
			producerByComponent[pn.Component] = pn
		}
	}
	sort.Strings(comps)
	var problems []string
	for _, comp := range comps {
		a, ok := recorded[comp]
		if !ok {
			problems = append(problems, comp+": no artifact produced this run")
			continue
		}
		switch {
		case a.Ref != "":
			// A pulled, content-addressed OCI image (no-compile lane): verify the
			// complete transported layout closure offline. No daemon/socket is trusted.
			if _, err := captureRegularTreeContext(opts.ctx(), a.TransportPath); err != nil {
				problems = append(problems, comp+": unsafe OCI artifact tree ("+err.Error()+")")
				continue
			}
			if err := opts.OCI.Verify(opts.ctx(), a.Ref, a.TransportPath); err != nil {
				problems = append(problems, comp+": "+err.Error())
			}
		case a.Packaged && a.OutputType == "oci-image":
			// A from-source assembled OCI layout: re-check every blob (content hash ==
			// filename) so layer/config corruption is caught, not just the manifest blob.
			if _, err := captureRegularTreeContext(opts.ctx(), a.Path); err != nil {
				problems = append(problems, comp+": unsafe OCI artifact tree ("+err.Error()+")")
				continue
			}
			if err := assemble.VerifyLayoutContext(opts.ctx(), a.Path); err != nil {
				problems = append(problems, comp+": "+err.Error())
			}
		default:
			// A binary or .deb (built or acquired): re-hash the single file.
			root, expectedPath, pathErr := trustedArtifactPath(producerByComponent[comp], a, opts)
			if pathErr != nil || a.AbsPath != expectedPath {
				problems = append(problems, comp+": artifact path is not graph-derived")
				continue
			}
			relative, pathErr := relativeBeneath(root, expectedPath)
			if pathErr != nil {
				problems = append(problems, comp+": unsafe artifact path ("+pathErr.Error()+")")
				continue
			}
			sum, _, _, err := hashRegularBeneathContext(opts.ctx(), root, relative)
			if err != nil {
				problems = append(problems, comp+": unreadable ("+err.Error()+")")
				continue
			}
			if sum != a.SHA256 {
				problems = append(problems, comp+": digest changed since acquisition ("+a.SHA256+" != "+sum+")")
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return e.New(e.CodeVerifyFailed, "verify failed: %v", problems)
	}
	return nil
}

func countIncludedProducers(producers map[string]ir.Node, included map[string]bool) int {
	n := 0
	for id := range producers {
		if included[id] {
			n++
		}
	}
	return n
}

// selectNodes computes the set of node IDs to execute. With no Only filter every
// node runs; otherwise only the named build components, the fetch nodes they need,
// and the terminal verify node.
func selectNodes(nodes []ir.Node, only []string) (map[string]bool, error) {
	included := map[string]bool{}
	if len(only) == 0 {
		for _, n := range nodes {
			included[n.ID] = true
		}
		return included, nil
	}
	want := map[string]bool{}
	for _, c := range only {
		want[c] = true
	}
	found := map[string]bool{}
	for _, n := range nodes {
		if (n.Kind == ir.KindBuild || n.Kind == ir.KindPull) && want[n.Component] {
			found[n.Component] = true
			included[n.ID] = true
			for _, dep := range n.Needs {
				included[dep] = true
			}
		}
		if n.Kind == ir.KindVerify {
			included[n.ID] = true
		}
	}
	for c := range want {
		if !found[c] {
			return nil, e.New(e.CodeUnsupported, "--only names component %q not present in the resolved recipe", c)
		}
	}
	return included, nil
}

// topoSort returns nodes in dependency order, deterministically (ties broken by
// ID). It errors on a cycle.
func topoSort(nodes []ir.Node) ([]ir.Node, error) {
	byID := map[string]ir.Node{}
	indeg := map[string]int{}
	for _, n := range nodes {
		byID[n.ID] = n
		if _, ok := indeg[n.ID]; !ok {
			indeg[n.ID] = 0
		}
	}
	children := map[string][]string{}
	for _, n := range nodes {
		for _, dep := range n.Needs {
			children[dep] = append(children[dep], n.ID)
			indeg[n.ID]++
		}
	}
	var ready []string
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	var out []ir.Node
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		out = append(out, byID[id])
		kids := append([]string(nil), children[id]...)
		sort.Strings(kids)
		for _, k := range kids {
			indeg[k]--
			if indeg[k] == 0 {
				ready = append(ready, k)
			}
		}
		sort.Strings(ready)
	}
	if len(out) != len(nodes) {
		return nil, e.New(e.CodeInvalidCatalog, "build graph has a cycle")
	}
	return out, nil
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), size, nil
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		return "", e.New(e.CodeUsage, "git execution requires a non-nil context")
	}
	cmd := execCmd(RunOptions{Ctx: ctx}, "git", append([]string{"-C", dir}, args...)...)
	out, err := outputOwnedCommand(ctx, cmd)
	if err != nil {
		if ctx.Err() != nil {
			return "", e.Wrap(e.CodeTimeout, ctx.Err(), "git execution canceled")
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
