package pipeline

import (
	"context"
	"path/filepath"
	"sync"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// NodeSession retains prepared source snapshots while an owning scheduler runs
// individual producers. It never schedules work or reuses producer results.
// Close cancels and joins calls before releasing their shared snapshots.
type NodeSession struct {
	state *nodeSessionState
}

type nodeSessionState struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	closed  bool
	done    chan struct{}
	active  sync.WaitGroup
	sources map[string]*preparedSourceCache
}

func NewNodeSession(ctx context.Context) (*NodeSession, error) {
	if err := requireRunContext(ctx, "node execution session"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "node execution session canceled")
	}
	owned, cancel := context.WithCancel(ctx)
	return &NodeSession{state: &nodeSessionState{ctx: owned, cancel: cancel, done: make(chan struct{}), sources: make(map[string]*preparedSourceCache)}}, nil
}

func (handle *NodeSession) ExecNode(node ir.Node, options RunOptions) (*ArtifactResult, error) {
	if err := requireRunContext(options.Ctx, "node execution session"); err != nil {
		return nil, err
	}
	if handle == nil || handle.state == nil {
		return nil, e.New(e.CodeEngineCapability, "node execution session is not open")
	}
	if !options.Launch {
		return nil, e.New(e.CodeEngineCapability, "node execution session requires launched producers")
	}
	if options.Qualification != nil {
		return nil, e.New(e.CodeEngineCapability, "qualified execution requires the complete Run admission protocol")
	}
	options = withDefaults(options)
	session := handle.state
	root, err := filepath.Abs(options.SourceRoot)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolving prepared source owner")
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil, e.New(e.CodeEngineCapability, "node execution session is closed")
	}
	if err := session.ctx.Err(); err != nil {
		session.mu.Unlock()
		return nil, e.Wrap(e.CodeTimeout, err, "node execution session canceled")
	}
	if node.Kind == ir.KindBuild {
		cache := session.sources[root]
		if cache == nil {
			cache = newPreparedSourceCache()
			session.sources[root] = cache
		}
		options.sources = cache
	}
	session.active.Add(1)
	session.mu.Unlock()
	defer session.active.Done()
	// Derive directly from the session so Close cancels admitted work before it
	// waits for the producer. Forward caller cancellation into the same child.
	// Reversing these parents leaves a scheduling window where Close has canceled
	// the session but the AfterFunc callback has not canceled the producer yet.
	ctx, cancel := context.WithCancel(session.ctx)
	stop := context.AfterFunc(options.Ctx, cancel)
	defer func() { stop(); cancel() }()
	options.Ctx = ctx
	return (Local{}).ExecNode(node, options)
}

func (handle *NodeSession) Close() {
	if handle == nil || handle.state == nil {
		return
	}
	session := handle.state
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		<-session.done
		return
	}
	session.closed = true
	session.cancel()
	session.mu.Unlock()
	session.active.Wait()
	for _, cache := range session.sources {
		cache.close()
	}
	close(session.done)
}
