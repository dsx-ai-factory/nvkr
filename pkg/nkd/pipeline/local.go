package pipeline

import (
	"context"
	"io"
	"path/filepath"
	"sort"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
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

// Local is the ground-truth runner: it executes each IR node directly with a
// container runtime, applying the same pins, env allowlist, and --network=none
// hermeticity the IR declares (NKD-FR-BLD-002).
type Local struct{}

func (Local) Name() string { return "local" }

// Run executes the graph as a concurrent DAG. Fetches and distinct exact source
// preparations are scheduled before their consumers; builds with identical source
// inputs share one run-owned verified snapshot instead of occupying work slots while
// waiting on duplicate copies. Work runs in parallel up to opts.Jobs, gated by real
// dependencies, and verify waits on every selected producer. It returns a Report
// seeding the expected-digests manifest, failing closed on the first build/verify
// failure unless KeepGoing is set. The report is deterministic (nodes/artifacts
// sorted) regardless of completion order.
func (l Local) Run(g *ir.Graph, buildDigest string, opts RunOptions) (*Report, error) {
	if err := requireRunContext(opts.Ctx, "local pipeline run"); err != nil {
		return &Report{}, err
	}
	if opts.SourceWorkspace != nil || opts.SourceGitEnvironment != nil {
		return &Report{}, e.New(e.CodeEngineCapability, "held source workspace is restricted to source-only ExecNode")
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
	if err := validateGraphIdentity(g, buildDigest); err != nil {
		return &Report{}, err
	}
	rep := &Report{BuildDigest: buildDigest}

	ordered, err := topoSort(g.Nodes) // fail fast on a cyclic graph
	if err != nil {
		return rep, err
	}
	included, err := selectNodes(g.Nodes, opts.Only)
	if err != nil {
		return rep, err
	}
	selectedNodes := make([]ir.Node, 0, len(g.Nodes))
	for _, node := range g.Nodes {
		if included[node.ID] {
			selectedNodes = append(selectedNodes, node)
		}
	}
	if err := rejectUnsupportedExecutionRequirements(selectedNodes, "local pipeline execution"); err != nil {
		return rep, err
	}
	var selectedBuildNodes []ir.Node
	for _, node := range g.Nodes {
		if included[node.ID] && node.Kind == ir.KindBuild {
			if err := admitBuildKit(node, opts); err != nil {
				return rep, err
			}
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

	// The scheduler owns readiness, worker slots, source preparation, and result
	// accounting. It joins every launched worker before receipt construction.
	schedule := newLocalSchedule(l, g.Nodes, included, materializedFetch, rep, opts)
	firstErr := schedule.run()

	sortNodeResults(rep.Nodes)
	sort.Slice(rep.Artifacts, func(i, j int) bool { return rep.Artifacts[i].Component < rep.Artifacts[j].Component })
	if firstErr == nil && !opts.DryRun {
		terminal, err := issueTerminalReceipt(opts.ctx(), rep, schedule.producers, included, opts)
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
