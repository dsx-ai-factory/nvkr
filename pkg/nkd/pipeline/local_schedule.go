// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

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

type nodeOutcome struct {
	node              ir.Node
	res               NodeResult
	art               *ArtifactResult
	err               error
	log               *bytes.Buffer
	sourcePreparation bool
	sourceKey         string
}

// localSchedule owns mutable DAG accounting in the one goroutine running run.
// Workers receive immutable graph/options and detached artifact slices, and send
// back outcomes; they never update the schedule or its report directly.
type localSchedule struct {
	runner    Local
	opts      RunOptions
	report    *Report
	nodes     map[string]ir.Node
	producers map[string]ir.Node
	included  map[string]bool
	indegree  map[string]int
	children  map[string][]string
	scheduled map[string]bool
	preparing map[string]bool
	prepared  map[string]bool
	results   chan nodeOutcome
	jobs      int
	inflight  int
	firstErr  error
	stopped   bool
}

func newLocalSchedule(runner Local, nodes []ir.Node, included, materializedFetch map[string]bool,
	report *Report, opts RunOptions,
) *localSchedule {
	s := &localSchedule{
		runner: runner, opts: opts, report: report, included: included,
		nodes: map[string]ir.Node{}, producers: map[string]ir.Node{},
		indegree: map[string]int{}, children: map[string][]string{},
		scheduled: map[string]bool{}, preparing: map[string]bool{}, prepared: map[string]bool{},
		results: make(chan nodeOutcome), jobs: max(1, opts.Jobs),
	}
	for _, node := range nodes {
		s.nodes[node.ID] = node
		if node.Kind == ir.KindBuild || node.Kind == ir.KindPull {
			s.producers[node.ID] = node
		}
		if !included[node.ID] {
			report.Nodes = append(report.Nodes, NodeResult{ID: node.ID, Kind: node.Kind, Status: "skipped"})
		}
	}
	// Dependency counts cover only the selected graph. Qualified fetches have
	// already completed at the execution barrier and must not run a second time.
	for id := range included {
		for _, dependency := range s.nodes[id].Needs {
			if included[dependency] {
				s.indegree[id]++
				s.children[dependency] = append(s.children[dependency], id)
			}
		}
	}
	for id := range materializedFetch {
		s.scheduled[id] = true
		s.releaseChildren(id)
	}
	return s
}

func (s *localSchedule) run() error {
	s.launchReady()
	// A failure stops new work unless KeepGoing is set, but every worker already
	// started must deliver its outcome before the caller can reclaim its inputs.
	for s.inflight > 0 {
		s.accept(<-s.results)
		s.launchReady()
	}
	return s.firstErr
}

func (s *localSchedule) readyNodes() []string {
	var ready []string
	for id := range s.included {
		if s.indegree[id] == 0 && !s.scheduled[id] {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		left, right := s.nodes[ready[i]], s.nodes[ready[j]]
		if nodeScheduleRank(left) != nodeScheduleRank(right) {
			return nodeScheduleRank(left) < nodeScheduleRank(right)
		}
		return ready[i] < ready[j]
	})
	return ready
}

func (s *localSchedule) launchReady() {
	if s.stopped {
		return
	}
	for _, id := range s.readyNodes() {
		if s.inflight >= s.jobs {
			break
		}
		node := s.nodes[id]
		if s.prepareBeforeBuild(node) {
			continue
		}
		s.scheduled[id] = true
		s.inflight++
		if !s.opts.DryRun {
			reportNodeStart(s.opts.progress, node)
		}
		// Terminal verification becomes ready only after every selected producer
		// reports. Its detached slice cannot race later report accounting.
		collected := append([]ArtifactResult(nil), s.report.Artifacts...)
		go func() { s.results <- s.runner.runNode(node, s.producers, s.included, collected, s.opts) }()
	}
}

// prepareBeforeBuild schedules one preparation per exact source key. A sibling
// waiting for the same key consumes no worker slot, allowing other ready work in.
func (s *localSchedule) prepareBeforeBuild(node ir.Node) bool {
	if node.Kind != ir.KindBuild || s.opts.DryRun {
		return false
	}
	key, err := preparedSourceKey(node)
	if err != nil || s.prepared[key] {
		return false
	}
	if !s.preparing[key] {
		s.preparing[key] = true
		s.inflight++
		s.opts.progress.printf("[start] source %s for %s — prepare one verified snapshot for identical ready build inputs\n", node.SourceDest, node.Component)
		go func() { s.results <- s.runner.prepareSource(node, key, s.opts) }()
	}
	return true
}

func (s *localSchedule) accept(out nodeOutcome) {
	s.inflight--
	if out.log != nil && out.log.Len() > 0 {
		s.opts.progress.write(out.log.Bytes())
	}
	if out.sourcePreparation {
		delete(s.preparing, out.sourceKey)
		if out.err == nil {
			s.prepared[out.sourceKey] = true
			s.opts.progress.printf("[done] source %s for %s (%s)\n", out.node.SourceDest, out.node.Component, out.res.Duration.Round(time.Millisecond))
			return
		}
		s.scheduled[out.node.ID] = true
		out.res.Detail = out.err.Error()
	} else {
		if !s.opts.DryRun {
			reportNodeDone(s.opts.progress, out)
		}
		if out.art != nil {
			s.report.Artifacts = append(s.report.Artifacts, *out.art)
		}
	}
	s.report.Nodes = append(s.report.Nodes, out.res)
	if out.err != nil {
		if s.firstErr == nil {
			s.firstErr = out.err
		}
		if !s.opts.KeepGoing {
			s.stopped = true
		}
	}
	s.releaseChildren(out.node.ID)
}

func (s *localSchedule) releaseChildren(id string) {
	for _, child := range s.children[id] {
		s.indegree[child]--
	}
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
			return nil, e.New(e.CodeUnsupported, "--only names component %q not present in the build graph", c)
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
