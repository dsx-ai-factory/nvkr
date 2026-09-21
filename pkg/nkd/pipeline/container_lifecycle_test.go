package pipeline

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReaperCloseStopsWaiterAndIsConcurrentIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reaper := newReaper(ctx, "unused-runtime")

	const closers = 32
	var wait sync.WaitGroup
	wait.Add(closers)
	for range closers {
		go func() {
			defer wait.Done()
			reaper.close()
		}()
	}
	wait.Wait()

	select {
	case <-reaper.done:
	default:
		t.Fatal("reaper close returned before its context waiter exited")
	}
	if reaper.add("late-container") {
		t.Fatal("closed reaper accepted a container that can no longer be cleaned")
	}
}

func TestReaperContextCancellationStopsWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reaper := newReaper(ctx, "unused-runtime")
	cancel()
	select {
	case <-reaper.done:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not stop the reaper waiter")
	}
	// Joining an already-canceled waiter remains idempotent.
	reaper.close()
}

func TestLocalRunAlwaysClosesReaperWithoutCancelingCallerContext(t *testing.T) {
	for _, test := range []struct {
		name      string
		badDigest bool
	}{
		{name: "success"},
		{name: "early-error", badDigest: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reaper := newReaper(ctx, "unused-runtime")
			graph := sampleGraph()
			digest := mustGraphDigest(t, graph)
			if test.badDigest {
				digest = "sha256:" + repeatHex("f")
			}

			_, err := (Local{}).Run(graph, digest, RunOptions{
				Ctx: ctx, DryRun: true, reap: reaper,
			})
			if test.badDigest && err == nil {
				t.Fatal("invalid graph identity unexpectedly succeeded")
			}
			if !test.badDigest && err != nil {
				t.Fatal(err)
			}
			select {
			case <-reaper.done:
			default:
				t.Fatal("Local.Run returned while its reaper waiter was still live")
			}
		})
	}
}

func TestReaperCleanupIsBoundedAndDoesNotHoldRegistryLock(t *testing.T) {
	reaper := newReaper(context.Background(), "unused-runtime")
	reaper.cleanupTimeout = 200 * time.Millisecond
	type cleanupCall struct {
		started time.Time
		names   []string
	}
	cleanupEntered := make(chan cleanupCall, 2)
	reaper.cleanupCommand = func(ctx context.Context, grace time.Duration, _ string,
		_ *heldRuntimeExecutable, names []string,
	) error {
		cleanupEntered <- cleanupCall{started: time.Now(), names: append([]string(nil), names...)}
		<-ctx.Done()
		// Model a child that ignores SIGTERM until WaitDelay escalates. The reaper
		// must reserve this grace inside, rather than after, its shared deadline.
		timer := time.NewTimer(grace)
		defer timer.Stop()
		<-timer.C
		return ctx.Err()
	}
	if !reaper.add("blocked-container") {
		t.Fatal("open reaper rejected a container")
	}
	if !reaper.add("later-container") {
		t.Fatal("open reaper rejected a second container")
	}
	closed := make(chan struct{})
	go func() {
		reaper.close()
		close(closed)
	}()
	calls := make([]cleanupCall, 0, 2)
	for range 2 {
		select {
		case call := <-cleanupEntered:
			calls = append(calls, call)
		case <-time.After(2 * time.Second):
			t.Fatal("not every registered container received a cleanup attempt")
		}
	}
	cleanupStarted := calls[0].started
	gotNames := []string{calls[0].names[0], calls[1].names[0]}
	sort.Strings(gotNames)
	if got := strings.Join(gotNames, ","); got != "blocked-container,later-container" {
		t.Fatalf("cleanup did not attempt the complete registered set: %q", got)
	}

	registryOperation := make(chan bool, 1)
	go func() {
		reaper.forget("blocked-container")
		registryOperation <- reaper.add("too-late")
	}()
	select {
	case accepted := <-registryOperation:
		if accepted {
			t.Fatal("reaper accepted a container after cleanup began")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cleanup held the registry mutex across subprocess I/O")
	}

	select {
	case <-closed:
		if elapsed := time.Since(cleanupStarted); elapsed > 450*time.Millisecond {
			t.Fatalf("cleanup exceeded its shared deadline: %s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("unresponsive cleanup boundary made reaper.close hang")
	}
}
