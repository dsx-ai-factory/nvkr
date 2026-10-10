// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package nkedemo is a downstream local diagnostic consumer. Its descriptions
// and filesystem checks confer no NKD execution, publication or Release authority.
package nkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/sourcegit"
	"golang.org/x/sys/unix"
)

const diagnosticLimit = 8 << 20

// ErrUnjoined requires retaining controls depended on by possibly live work.
var ErrUnjoined = errors.New("owned process group did not join")

// CommandError preserves the command's exit status and original cause without
// printing its arguments, environment, or potentially sensitive output.
type CommandError struct {
	Phase    string
	ExitCode int
	Cause    error
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s: command failed (exit %d): %v", e.Phase, e.ExitCode, e.Cause)
}
func (e *CommandError) Unwrap() error { return e.Cause }

// CommandExitCode distinguishes an ordinary tool exit from timeout or unsafe
// process ownership. Callers must not treat a joined exit-1 as masking either.
func CommandExitCode(err error) int {
	if errors.Is(err, ErrUnjoined) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return -1
	}
	var command *CommandError
	if errors.As(err, &command) {
		return command.ExitCode
	}
	return -1
}

// Runner uses an exact caller-admitted environment, never ambient credentials.
// Empty Work permits memory-only bounded cleanup even when storage is unavailable.
// Nonempty Work must be a private, owned directory; logs are exclusive and bounded.
type Runner struct {
	Work     string
	Env      []string
	Progress io.Writer
	// Only the artifact launcher opts into streaming public build output. Other
	// commands keep potentially sensitive deployment diagnostics private.
	output io.Writer
}

type boundedDiagnostic struct {
	mu           sync.Mutex
	writer       io.Writer
	buffer       bytes.Buffer
	remaining    int
	overflow     bool
	err          error
	capture      bool
	publicStream bool
}

func (w *boundedDiagnostic) Write(raw []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(raw)
	if !w.publicStream {
		if len(raw) > w.remaining {
			raw = raw[:w.remaining]
			w.overflow = true
		}
		w.remaining -= len(raw)
	}
	if w.capture {
		_, _ = w.buffer.Write(raw)
	}
	if w.writer != nil && w.err == nil {
		written, err := w.writer.Write(raw)
		if err != nil {
			w.err = err
		} else if written != len(raw) {
			w.err = io.ErrShortWrite
		}
	}
	// Drain after overflow or storage failure so a child cannot hang on our sink.
	return n, nil
}

func (w *boundedDiagnostic) result() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var err error
	if w.overflow {
		err = fmt.Errorf("diagnostic exceeds %d-byte bound", diagnosticLimit)
	}
	return append([]byte(nil), w.buffer.Bytes()...), errors.Join(err, w.err)
}

// OwnedProcess owns one live process group and the sole Wait goroutine. No PID
// decoded from a journal can construct this value or authorize signaling.
type OwnedProcess struct {
	cmd         *exec.Cmd
	phase       string
	done        chan struct{}
	mu          sync.Mutex
	waitErr     error
	joinErr     error
	stdout      *boundedDiagnostic
	stderr      *boundedDiagnostic
	diagnostic  *boundedDiagnostic
	log         *os.File
	logDir      *os.File
	groupProbe  func(int) (bool, error)
	groupSignal func(int, unix.Signal) error
	progress    *progressBoundary
}

func (p *OwnedProcess) PID() int { return p.cmd.Process.Pid }

// Exited is true only after the child and process-group finalizer have finished.
func (p *OwnedProcess) Exited() (bool, error) {
	select {
	case <-p.done:
		p.mu.Lock()
		err := errors.Join(p.waitErr, p.joinErr)
		p.mu.Unlock()
		if p.progress != nil {
			p.progress.finish(err)
		}
		return true, err
	default:
		return false, nil
	}
}

func groupAlive(pid int) (bool, error) {
	err := unix.Kill(-pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	return err == nil, err
}

func signalGroup(pid int, signal unix.Signal) error {
	err := unix.Kill(-pid, signal)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func (p *OwnedProcess) awaitGroup(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive, err := p.groupProbe(p.PID())
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *OwnedProcess) finish() {
	waitErr := p.cmd.Wait()
	// Join descendants even if the direct leader exited first. Reuse the shared
	// signaling owner, but add explicit bounded disappearance verification.
	joinErr := ownedcmd.KillDescendants(p.cmd)
	if errors.Is(joinErr, os.ErrProcessDone) {
		joinErr = nil
	}
	if joinErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		joinErr = p.awaitGroup(ctx)
		cancel()
	}
	if joinErr != nil {
		joinErr = fmt.Errorf("%w: %s: %w", ErrUnjoined, p.phase, joinErr)
	}
	if p.log != nil {
		_ = p.log.Close()
	}
	if p.logDir != nil {
		_ = p.logDir.Close()
	}
	p.mu.Lock()
	p.waitErr, p.joinErr = waitErr, joinErr
	p.mu.Unlock()
	close(p.done)
}

// Stop sends TERM to the exact live group, then escalates to KILL and joins.
// A canceled caller is not permission to release dependent controls.
func (p *OwnedProcess) Stop(ctx context.Context) (result error) {
	var observedExit error
	defer func() {
		if p.progress != nil {
			p.progress.finish(errors.Join(result, observedExit))
		}
	}()
	if ctx == nil {
		return fmt.Errorf("%w: stop requires a context", ErrUnjoined)
	}
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		// Cleanup succeeds when the group joined, but a child that failed
		// before this stop request must not become successful presentation.
		observedExit = p.waitErr
		return p.joinErr
	default:
	}
	if err := p.groupSignal(p.PID(), unix.SIGTERM); err != nil {
		return fmt.Errorf("%w: %w", ErrUnjoined, err)
	}
	timer := time.NewTimer(40 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.joinErr
	case <-ctx.Done():
	case <-timer.C:
	}
	if err := p.groupSignal(p.PID(), unix.SIGKILL); err != nil {
		return fmt.Errorf("%w: %w", ErrUnjoined, err)
	}
	grace := time.NewTimer(5 * time.Second)
	defer grace.Stop()
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.joinErr
	case <-grace.C:
		return fmt.Errorf("%w: %s did not finish after KILL", ErrUnjoined, p.phase)
	}
}

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("non-nil context required")
	}
	return ctx.Err()
}

func safePhase(phase string) bool {
	if phase == "" || phase == "." || phase == ".." || len(phase) > 128 {
		return false
	}
	for _, c := range phase {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func commandPath(name string, environment []string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		return exec.LookPath(name)
	}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		if key != "PATH" {
			continue
		}
		for _, directory := range filepath.SplitList(value) {
			// Relative/empty PATH entries would silently adopt the caller's cwd.
			if !filepath.IsAbs(directory) {
				continue
			}
			if path, err := exec.LookPath(filepath.Join(directory, name)); err == nil {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("executable %q absent from explicit PATH", name)
}

func (r Runner) start(ctx context.Context, args []string, phase string, capture, ownedLifetime bool) (*OwnedProcess, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if !safePhase(phase) || len(args) == 0 || args[0] == "" || r.Env == nil {
		return nil, errors.New("require safe phase, command and explicit environment")
	}
	path, err := commandPath(args[0], r.Env)
	if err != nil {
		return nil, err
	}
	commandCtx := ctx
	if ownedLifetime {
		// Services must outlive canceled work until dependent tenant cleanup
		// finishes. The caller retains the explicit Stop-and-join obligation.
		commandCtx = context.WithoutCancel(ctx)
	}
	cmd := exec.CommandContext(commandCtx, path, args[1:]...)
	cmd.Env = append([]string{}, r.Env...)
	if args[0] == "git" {
		gitArgs, dir := args[1:], ""
		if len(gitArgs) >= 2 && gitArgs[0] == "-C" {
			dir, gitArgs = gitArgs[1], gitArgs[2:]
		}
		cmd, err = sourcegit.LocalCommand(commandCtx, r.Env, dir, gitArgs...)
		if err != nil {
			return nil, &CommandError{Phase: phase, ExitCode: -1, Cause: err}
		}
	}
	cmd.Dir = r.Work
	if err := ownedcmd.ConfigureCancellation(cmd, ownedcmd.DefaultTerminationGrace); err != nil {
		return nil, err
	}
	p := &OwnedProcess{cmd: cmd, phase: phase, done: make(chan struct{}), groupProbe: groupAlive, groupSignal: signalGroup}
	if r.Work != "" {
		p.logDir, _, err = securepath.OpenDirectory(r.Work)
		if err != nil {
			return nil, err
		}
		info, statErr := p.logDir.Stat()
		if statErr != nil || info.Mode().Perm() != 0o700 {
			_ = p.logDir.Close()
			return nil, errors.New("require private 0700 command workspace")
		}
		var stat unix.Stat_t
		if err := unix.Fstat(int(p.logDir.Fd()), &stat); err != nil || stat.Uid != uint32(os.Getuid()) {
			_ = p.logDir.Close()
			return nil, errors.New("command workspace is not owned by current user")
		}
		fd, openErr := unix.Openat(int(p.logDir.Fd()), phase+".log", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if openErr != nil {
			_ = p.logDir.Close()
			return nil, openErr
		}
		p.log = os.NewFile(uintptr(fd), phase+".log")
	}
	var sink io.Writer
	if p.log != nil {
		p.diagnostic = &boundedDiagnostic{writer: p.log, remaining: diagnosticLimit}
		sink = p.diagnostic
	}
	if r.output != nil {
		if sink == nil {
			sink = r.output
		} else {
			sink = io.MultiWriter(sink, r.output)
		}
	}
	// The uncaptured public build stream owns its retention at the presenter.
	// Private command logs and captured diagnostics retain their failure bound.
	p.stdout = &boundedDiagnostic{writer: sink, remaining: diagnosticLimit, capture: capture,
		publicStream: r.output != nil && r.Work == "" && !capture}
	p.stderr = p.stdout
	if capture {
		p.stderr = &boundedDiagnostic{writer: sink, remaining: diagnosticLimit}
	}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	if r.Progress != nil {
		_, _ = fmt.Fprintf(r.Progress, "Demo phase: %s\n", phase)
	}
	err = requireContext(ctx)
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		if p.log != nil {
			_ = p.log.Close()
		}
		if p.logDir != nil {
			_ = p.logDir.Close()
		}
		return nil, &CommandError{Phase: phase, ExitCode: -1, Cause: err}
	}
	go p.finish()
	return p, nil
}

// Start admits the work context, then transfers an explicit Stop-and-join
// obligation to the caller. Work cancellation does not stop the owned service.
func (r Runner) Start(ctx context.Context, args []string, phase string) (*OwnedProcess, error) {
	boundary := beginProgress(r.Progress, phase, PhaseStarted, PhaseFinished, PhaseFailed)
	process, err := r.start(ctx, args, phase, false, true)
	if err != nil {
		boundary.finish(err)
		return nil, err
	}
	process.progress = boundary
	return process, nil
}

func (r Runner) Capture(ctx context.Context, args []string, phase string, timeout time.Duration) ([]byte, error) {
	return r.execute(ctx, args, phase, timeout, true)
}
func (r Runner) Run(ctx context.Context, args []string, phase string, timeout time.Duration) error {
	_, err := r.execute(ctx, args, phase, timeout, false)
	return err
}

func (r Runner) execute(ctx context.Context, args []string, phase string, timeout time.Duration, capture bool) (result []byte, resultErr error) {
	boundary := beginProgress(r.Progress, phase, PhaseStarted, PhaseFinished, PhaseFailed)
	defer func() { boundary.finish(resultErr) }()
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, errors.New("positive command timeout required")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p, err := r.start(ctx, args, phase, capture, false)
	if err != nil {
		return nil, err
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		// A child blocked in the kernel can outlive SIGKILL. Bound our join,
		// report ErrUnjoined, and retain its dependent controls instead of
		// waiting indefinitely or claiming the command has stopped.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		joinErr := p.Stop(cleanupCtx)
		cleanupCancel()
		if joinErr != nil {
			raw, diagnosticErr := p.stdout.result()
			return raw, errors.Join(joinErr, ctx.Err(), diagnosticErr)
		}
	}
	raw, outErr := p.stdout.result()
	_, stderrErr := p.stderr.result()
	if p.diagnostic != nil {
		_, logErr := p.diagnostic.result()
		outErr = errors.Join(outErr, logErr)
	}
	p.mu.Lock()
	waitErr, joinErr := p.waitErr, p.joinErr
	p.mu.Unlock()
	if joinErr != nil {
		return raw, errors.Join(joinErr, waitErr, ctx.Err(), outErr, stderrErr)
	}
	if err := errors.Join(waitErr, ctx.Err(), outErr, stderrErr); err != nil {
		code := -1
		var exit *exec.ExitError
		if outErr == nil && stderrErr == nil && errors.As(waitErr, &exit) {
			code = exit.ExitCode()
		}
		return raw, &CommandError{Phase: phase, ExitCode: code, Cause: err}
	}
	return raw, nil
}
