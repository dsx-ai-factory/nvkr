// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/buildactivity"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// This fixed, metadata-only observer runs in the admitted toolchain, with only
// the attempt's work volume mounted read-only. Names, inodes and directory paths
// stay inside the observer; no file contents are read or sent. The directory is
// the existing Plan-bound log destination, not an inferred serial-console path.
const packerActivityScript = `import os, stat, time, json, sys
ROOT = sys.argv[1]
PERIOD = 5.0
NOFOLLOW = os.O_NOFOLLOW | os.O_NONBLOCK

def scan():
    entries, files, total = 0, 0, 0
    observed = {}
    root = os.open(ROOT, os.O_RDONLY | os.O_DIRECTORY | NOFOLLOW)
    def walk(fd, prefix, depth):
        nonlocal entries, files, total
        if depth > 8:
            raise ValueError()
        with os.scandir(fd) as stream:
            for entry in stream:
                entries += 1
                if entries > 128:
                    raise ValueError()
                before = os.stat(entry.name, dir_fd=fd, follow_symlinks=False)
                key = prefix + (entry.name,)
                if stat.S_ISDIR(before.st_mode):
                    child = os.open(entry.name, os.O_RDONLY | os.O_DIRECTORY | NOFOLLOW, dir_fd=fd)
                    try:
                        held = os.fstat(child)
                        if (held.st_dev, held.st_ino) != (before.st_dev, before.st_ino):
                            raise ValueError()
                        observed[key] = (held.st_dev, held.st_ino, -1)
                        walk(child, key, depth + 1)
                    finally:
                        os.close(child)
                elif stat.S_ISREG(before.st_mode) and before.st_nlink == 1:
                    child = os.open(entry.name, os.O_RDONLY | NOFOLLOW, dir_fd=fd)
                    try:
                        held = os.fstat(child)
                        if not stat.S_ISREG(held.st_mode) or held.st_nlink != 1 or (held.st_dev, held.st_ino) != (before.st_dev, before.st_ino):
                            raise ValueError()
                        files += 1
                        total += held.st_size
                        if files > 64 or held.st_size < 0 or held.st_size > 16 * 1024 * 1024 or total > 64 * 1024 * 1024:
                            raise ValueError()
                        observed[key] = (held.st_dev, held.st_ino, held.st_size)
                    finally:
                        os.close(child)
                else:
                    raise ValueError()
                after = os.stat(entry.name, dir_fd=fd, follow_symlinks=False)
                if (after.st_dev, after.st_ino, stat.S_IFMT(after.st_mode)) != (before.st_dev, before.st_ino, stat.S_IFMT(before.st_mode)):
                    raise ValueError()
    try:
        held = os.fstat(root)
        observed[()] = (held.st_dev, held.st_ino, -1)
        walk(root, (), 0)
        current = os.stat(ROOT, follow_symlinks=False)
        if (current.st_dev, current.st_ino) != (held.st_dev, held.st_ino):
            raise ValueError()
        return observed, files, total
    finally:
        os.close(root)

class Observer:
    def __init__(self):
        self.previous = None
        self.growth = None
    def sample(self):
        result = dict(available=False, files=0, bytes=0, growth_observed=False, growth_age_millis=0)
        try:
            current, files, total = scan()
            now = time.monotonic()
            prior = self.previous
            self.previous = current
            if prior is not None and any(key not in current or current[key][:2] != value[:2] or current[key][2] < value[2] for key, value in prior.items()):
                self.growth = None
                return result
            if prior is not None and any(key in prior and value[2] > prior[key][2] >= 0 for key, value in current.items()):
                self.growth = now
            result.update(available=True, files=files, bytes=total,
                growth_observed=self.growth is not None,
                growth_age_millis=min(21600000, int((now-self.growth)*1000)) if self.growth is not None else 0)
        except (OSError, ValueError):
            self.previous = None
            self.growth = None
        return result

observer = Observer()
while True:
    print(json.dumps(observer.sample(), sort_keys=True, separators=(',', ':')), flush=True)
    time.sleep(PERIOD)
`

type packerActivitySample struct {
	Available       bool  `json:"available"`
	Files           int   `json:"files"`
	Bytes           int64 `json:"bytes"`
	GrowthObserved  bool  `json:"growth_observed"`
	GrowthAgeMillis int64 `json:"growth_age_millis"`
}

// Docker attachment is consumed into this finite collector, never through a
// presentation callback. Oversize/malformed lines are drained, not retained.
type packerActivityWriter struct {
	sink      *buildactivity.Sink
	operation string
	partial   []byte
	dropping  bool
}

func (w *packerActivityWriter) unavailable() {
	w.sink.Offer(buildactivity.Record{Version: 1, Operation: w.operation})
}

func (w *packerActivityWriter) Write(data []byte) (int, error) {
	if w.partial == nil {
		w.partial = make([]byte, 0, 1024)
	}
	for _, value := range data {
		if value == '\n' {
			if !w.dropping {
				w.sample(w.partial)
			}
			w.partial, w.dropping = w.partial[:0], false
			continue
		}
		if w.dropping {
			continue
		}
		if len(w.partial) >= 1024 {
			w.partial, w.dropping = w.partial[:0], true
			w.unavailable()
			continue
		}
		w.partial = append(w.partial, value)
	}
	return len(data), nil
}

func (w *packerActivityWriter) sample(raw []byte) {
	var sample packerActivitySample
	if canonical.UnmarshalExact(raw, &sample) != nil {
		w.unavailable()
		return
	}
	record := buildactivity.Record{Version: 1, Operation: w.operation, Available: sample.Available,
		Files: sample.Files, Bytes: sample.Bytes, GrowthObserved: sample.GrowthObserved, GrowthAgeMillis: sample.GrowthAgeMillis}
	if record.Validate() != nil {
		w.unavailable()
		return
	}
	w.sink.Offer(record)
}

type packerActivityObserver struct {
	stopOnce  sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
	runtime   packerContainerRuntime
	resources *packerContainerResources
	name      string
	work      string
	retained  bool
	err       error
}

// Issued only by the stop owner after an observer client fails to join. The
// observer also binds identity files from the private workspace, so that tree
// remains held. Runtime executable/configuration retention belongs to its owner.
type packerActivityRetention struct {
	container, work string
}

func (*packerActivityRetention) err() error {
	return e.New(e.CodeTimeout, "owned Packer log observer did not join; dependent resources retained")
}

func (observer *packerActivityObserver) unjoined() bool {
	return observer != nil && observer.retained
}

func startPackerActivity(ctx context.Context, request packerContainerRunRequest, resources *packerContainerResources,
	work, name string, labels []string,
) *packerActivityObserver {
	if request.activity == nil || request.runtime == nil || request.runtime.RuntimeName() != "docker" || request.bindings.buildLogDirectoryVariable == "" ||
		(buildactivity.Record{Version: 1, Operation: request.activityOperation}).Validate() != nil {
		return nil
	}
	writer := &packerActivityWriter{sink: request.activity, operation: request.activityOperation}
	writer.unavailable()
	create := packerContainerCreateRequest{name: name, image: request.node.Toolchain, platform: request.node.ToolchainPlatform,
		work: work, workReadOnly: true, project: packerContainerWorkRoot, entrypoint: "python3",
		// Isolated mode excludes the source-writable workdir and ambient Python
		// paths from imports. Only the pinned toolchain's stdlib may execute.
		packerArgs: []string{"-I", "-u", "-c", packerActivityScript, packerContainerBuildLogPath}, labels: labels, identity: request.identity}
	// Register before create: a runtime failure may leave its exact object alive.
	resources.containers = append(resources.containers, name)
	if resources.privateObservers == nil {
		resources.privateObservers = make(map[string]struct{})
	}
	resources.privateObservers[name] = struct{}{}
	if createPackerContainer(ctx, request.runtime, create) != nil {
		return nil
	}
	observeCtx, cancel := context.WithCancel(ctx)
	observer := &packerActivityObserver{cancel: cancel, done: make(chan struct{}), runtime: request.runtime, resources: resources, name: name, work: work}
	go func() {
		defer close(observer.done)
		// Command/metadata loss is descriptive only. stderr is never a public log.
		_ = request.runtime.Run(observeCtx, nil, writer, io.Discard, "start", "--attach", name)
		writer.unavailable()
	}()
	return observer
}

func (observer *packerActivityObserver) stop(ctx context.Context) error {
	return observer.stopWithTimeout(ctx, packerContainerCleanupTimeout)
}

func (observer *packerActivityObserver) stopWithTimeout(ctx context.Context, joinTimeout time.Duration) error {
	if observer == nil {
		return nil
	}
	observer.stopOnce.Do(func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), joinTimeout)
		defer cancel()
		observer.cancel()
		cleanupDone := make(chan error, 1)
		go func() {
			err := observer.runtime.Cleanup(stopCtx, packerContainerTerminationGrace, io.Discard, io.Discard, "rm", "-fv", observer.name)
			if err != nil {
				err = e.New(e.CodeInternal, "owned Packer log observer cleanup failed")
			}
			cleanupDone <- err
		}()
		// Both runtime clients can kernel-block despite their cancellation
		// deadlines. Bound the complete stop task and retain every dependent
		// resource unless BOTH clients have joined. No late dependent cleanup is
		// scheduled; the already-issued stop owns only this exact observer.
		attach, cleanup := observer.done, cleanupDone
		var cleanupErr error
		for attach != nil || cleanup != nil {
			select {
			case <-attach:
				attach = nil
			case cleanupErr = <-cleanup:
				cleanup = nil
			case <-stopCtx.Done():
				// Propagate retention to the exact executable owner too: a
				// kernel-blocked Start can still hold its launch lock. The
				// enclosing session joins this stop before closing that runtime.
				if owner, ok := observer.runtime.(interface{ retainUnjoinedPackerActivity() }); ok {
					owner.retainUnjoinedPackerActivity()
				}
				retained := &packerActivityRetention{container: observer.name, work: observer.work}
				observer.resources.mu.Lock()
				observer.resources.retainedObserver = retained
				observer.resources.mu.Unlock()
				observer.retained, observer.err = true, retained.err()
				return
			}
		}
		if cleanupErr != nil {
			// Runtime error text can contain daemon paths/object identifiers. The
			// public failure names only this owner boundary, never those details.
			observer.err = e.New(e.CodeInternal, "owned Packer log observer cleanup failed")
			return
		}
		observer.resources.mu.Lock()
		defer observer.resources.mu.Unlock()
		delete(observer.resources.privateObservers, observer.name)
		for index, current := range observer.resources.containers {
			if current == observer.name {
				observer.resources.containers = append(observer.resources.containers[:index], observer.resources.containers[index+1:]...)
				break
			}
		}
	})
	return observer.err
}
