// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package nvkedemo is a downstream local diagnostic consumer. Its descriptions
// and filesystem checks confer no NVKR execution, publication or Release authority.
package nvkedemo

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
	"syscall"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	nvkrerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const diagnosticLimit = 8 << 20

// ErrUnjoined requires retaining controls depended on by possibly live work.
var ErrUnjoined = errors.New("owned process group did not join")

// Issued only after a bounded command joins without output failures while its
// caller remains active. Read-only observers may retry; mutations must not.
var errCommandDeadline = errors.New("joined command exceeded its independent deadline")

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
	// Opted in only for the directly executed final nvkr local Run. Bootstrap
	// shells and every source/utility command receive no inherited activity FD.
	buildActivity bool
	directory     string     // Command cwd, independent of private diagnostic storage.
	extraFiles    []*os.File // Explicitly owned local helper descriptors; never ambient FDs.
	input         *os.File   // Private held regular input; lifetime stays with the caller through join.
}

// SudoAuthorizer is an optional terminal bridge on a downstream progress writer.
// A nil callback selects unattended execution. Authentication is not durable
// authorization: each protected command still executes through sudo policy.
// The presenter owns terminal admission, the bounded prompt and child joining;
// this package never receives, stores or logs a password.
type SudoAuthorizer interface {
	SudoAuthentication() func(context.Context) error
}

func (r Runner) sudoAuthentication() func(context.Context) error {
	if presenter, ok := r.Progress.(SudoAuthorizer); ok {
		return presenter.SudoAuthentication()
	}
	return nil
}

func (r Runner) authenticateSudo(ctx context.Context) (bool, error) {
	if err := requireContext(ctx); err != nil {
		return false, err
	}
	authenticate := r.sudoAuthentication()
	if authenticate == nil {
		return false, nil
	}
	if err := authenticate(ctx); err != nil {
		return true, err
	}
	return true, requireContext(ctx)
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
	outputErr   error
	stdout      *boundedDiagnostic
	stderr      *boundedDiagnostic
	diagnostic  *boundedDiagnostic
	log         *os.File
	logDir      *os.File
	groupProbe  func(int) (bool, error)
	groupSignal func(int, unix.Signal) error
	progress    *progressBoundary

	processWriter   io.Writer
	processExitOnce sync.Once
	activity        *buildactivity.Receiver
	localGitFetch   bool
	outputPipes     []*ownedOutputPipe
}

// The owner drains explicit pipes rather than letting exec.Cmd apply its
// termination grace to successful output copying. The child ends are real files,
// so Wait owns process exit only; done still requires every drain to join.
type ownedOutputPipe struct {
	reader, writer *os.File
	destination    io.Writer
	done           chan error
}

func (p *OwnedProcess) childOutput(destination io.Writer) (*os.File, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	p.outputPipes = append(p.outputPipes, &ownedOutputPipe{
		reader: reader, writer: writer, destination: destination, done: make(chan error, 1),
	})
	return writer, nil
}

func (p *OwnedProcess) startOutputDrains() {
	for _, pipe := range p.outputPipes {
		closeErr := pipe.writer.Close()
		go func() {
			_, err := io.Copy(pipe.destination, pipe.reader)
			pipe.done <- errors.Join(closeErr, err, pipe.reader.Close())
		}()
	}
}

func (p *OwnedProcess) closeUnstartedOutput() {
	for _, pipe := range p.outputPipes {
		_ = pipe.writer.Close()
		_ = pipe.reader.Close()
	}
}

func (p *OwnedProcess) joinOutputDrains() (result error) {
	for _, pipe := range p.outputPipes {
		result = errors.Join(result, <-pipe.done)
	}
	return result
}

func (p *OwnedProcess) PID() int { return p.cmd.Process.Pid }

// Exited is true only after the child and process-group finalizer have finished.
func (p *OwnedProcess) Exited() (bool, error) {
	select {
	case <-p.done:
		p.observeProcessExit()
		p.mu.Lock()
		err := errors.Join(p.waitErr, p.joinErr, p.outputErr)
		p.mu.Unlock()
		if p.progress != nil {
			p.progress.finish(err)
		}
		return true, err
	default:
		return false, nil
	}
}

// Observe only a joined owner from the calling lifecycle, never the Wait
// goroutine. An unobserved exit does not prove the process is still alive.
func (p *OwnedProcess) observeProcessExit() {
	if p.processWriter == nil {
		return
	}
	select {
	case <-p.done:
		p.mu.Lock()
		joined := p.joinErr == nil
		p.mu.Unlock()
		if joined {
			p.processExitOnce.Do(func() {
				reportProgress(p.processWriter, ProgressEvent{Kind: ProcessExited, Name: p.phase, Status: "exited"})
			})
		}
	default:
	}
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

// A failed signal never proves disappearance. Only an independent, successful
// group observation may supersede EPERM after the direct child has been reaped.
func (p *OwnedProcess) confirmGroupJoin(ctx context.Context, signalErr error) error {
	if errors.Is(signalErr, unix.EPERM) {
		alive, err := p.groupProbe(p.PID())
		if err != nil || alive {
			return errors.Join(signalErr, err)
		}
	} else if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
		return signalErr
	}
	return p.awaitGroup(ctx)
}

func (p *OwnedProcess) finish() {
	waitErr := p.cmd.Wait()
	// Join descendants even if the direct leader exited first. Reuse the shared
	// signaling owner, but add explicit bounded disappearance verification.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	joinErr := p.confirmGroupJoin(ctx, ownedcmd.KillDescendants(p.cmd))
	cancel()
	if joinErr != nil {
		joinErr = fmt.Errorf("%w: %s: %w", ErrUnjoined, p.phase, joinErr)
	}
	// A successful leader can finish while its bounded diagnostic sink is
	// still writing. Preserve that output without imposing the TERM grace;
	// waitOwned/Stop retain the existing command and cleanup deadlines.
	outputErr := p.joinOutputDrains()
	if p.activity != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		activityErr := p.activity.Close(ctx)
		cancel()
		if activityErr != nil {
			joinErr = errors.Join(joinErr, fmt.Errorf("%w: build activity reader: %w", ErrUnjoined, activityErr))
		}
	}
	if p.log != nil {
		_ = p.log.Close()
	}
	if p.logDir != nil {
		_ = p.logDir.Close()
	}
	p.mu.Lock()
	p.waitErr, p.joinErr, p.outputErr = waitErr, joinErr, outputErr
	p.mu.Unlock()
	close(p.done)
}

func (p *OwnedProcess) waitOwned(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.reportBuildActivity()
		select {
		case <-p.done:
			p.reportBuildActivity()
			return nil
		case <-ticker.C:
			continue
		case <-ctx.Done():
			// Kernel-blocked work can survive KILL. Preserve the unsafe owner state
			// rather than blocking forever or releasing dependent controls.
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
			defer cancel()
			return errors.Join(p.Stop(cleanupCtx), ctx.Err())
		}
	}
}

func (p *OwnedProcess) reportBuildActivity() {
	for _, record := range p.activity.Drain() {
		reportProgress(p.processWriter, ProgressEvent{Kind: OSBuildLogs, BuildLogs: record})
	}
}

// RunAuthenticationCommand reuses the downstream process owner while retaining
// the caller's direct terminal streams. It never captures or logs authentication
// input/output. The presenter admits the terminal and supplies a bounded context.
func RunAuthenticationCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if _, bounded := ctx.Deadline(); !bounded || cmd == nil || cmd.Path == "" || cmd.Cancel == nil {
		return errors.New("authentication requires a bounded context and CommandContext command")
	}
	if err := ownedcmd.ConfigureCancellation(cmd, ownedcmd.DefaultTerminationGrace); err != nil {
		return err
	}
	p := &OwnedProcess{cmd: cmd, phase: "sudo-authentication", done: make(chan struct{}), groupProbe: groupAlive, groupSignal: signalGroup}
	if err := cmd.Start(); err != nil {
		return errors.Join(err, ctx.Err())
	}
	go p.finish()
	if err := p.waitOwned(ctx); errors.Is(err, ErrUnjoined) {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return errors.Join(p.waitErr, p.joinErr, ctx.Err())
}

// Stop sends TERM to the exact live group, then escalates to KILL and joins.
// A canceled caller is not permission to release dependent controls.
func (p *OwnedProcess) Stop(ctx context.Context) (result error) {
	var observedExit error
	defer func() {
		p.reportBuildActivity()
		p.observeProcessExit()
		if p.progress != nil {
			p.progress.finish(errors.Join(result, observedExit))
		}
	}()
	if ctx == nil {
		return fmt.Errorf("%w: stop requires a context", ErrUnjoined)
	}
	var requestedTerm, requestedKill bool
	joined := func() (bool, error) {
		select {
		case <-p.done:
			p.mu.Lock()
			defer p.mu.Unlock()
			// Preserve failures that preceded Stop or exited normally with a
			// failure code. Only a matching, successfully requested stop signal
			// is expected cleanup, and this never changes the recorded waitErr.
			observedExit = p.waitErr
			if exit, ok := p.waitErr.(*exec.ExitError); ok && exit.ProcessState != nil {
				if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() &&
					(requestedTerm && status.Signal() == syscall.SIGTERM || requestedKill && status.Signal() == syscall.SIGKILL) {
					observedExit = nil
				}
			}
			if p.outputErr != nil {
				return true, errors.Join(p.joinErr, p.outputErr)
			}
			return true, p.joinErr
		default:
			return false, nil
		}
	}
	if complete, err := joined(); complete {
		return err
	}
	signalFailure := func(signalErr error, killed bool) error {
		waitCtx := ctx
		if killed {
			// Escalation already permits a finite join grace after the caller's
			// cancellation. A late signal error must use that same ownership grace.
			waitCtx = context.WithoutCancel(ctx)
		}
		waitCtx, cancel := context.WithTimeout(waitCtx, 5*time.Second)
		defer cancel()
		// Darwin can return EPERM for an exited, unreaped group. Neither that
		// error nor a leader exit proves descendants joined. Only the sole
		// finalizer's completed disappearance check can supersede the error.
		select {
		case <-p.done:
		case <-waitCtx.Done():
		}
		if complete, joinErr := joined(); complete {
			return joinErr
		}
		return fmt.Errorf("%w: %w", ErrUnjoined, signalErr)
	}
	if err := p.groupSignal(p.PID(), unix.SIGTERM); err != nil {
		return signalFailure(err, false)
	}
	requestedTerm = true
	timer := time.NewTimer(40 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		_, joinErr := joined()
		return joinErr
	case <-ctx.Done():
	case <-timer.C:
	}
	if err := p.groupSignal(p.PID(), unix.SIGKILL); err != nil {
		return signalFailure(err, true)
	}
	requestedKill = true
	grace := time.NewTimer(5 * time.Second)
	defer grace.Stop()
	select {
	case <-p.done:
		_, joinErr := joined()
		return joinErr
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

// Command-line tools need an explicit duration in addition to process context
// cancellation. Derive it from the existing operation deadline, never host speed.
func remainingWork(ctx context.Context) (time.Duration, error) {
	if ctx == nil {
		return 0, nvkrerrors.New(nvkrerrors.CodeUsage, "work budget requires a caller context")
	}
	if err := requireContext(ctx); err != nil {
		return 0, nvkrerrors.Wrap(nvkrerrors.CodeTimeout, err, "work budget unavailable")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, nvkrerrors.New(nvkrerrors.CodeUsage, "work command requires a caller deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, nvkrerrors.Wrap(nvkrerrors.CodeTimeout, context.DeadlineExceeded, "work budget expired")
	}
	return remaining, nil
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
	cmd.ExtraFiles = append([]*os.File(nil), r.extraFiles...)
	if r.input != nil {
		info, err := r.input.Stat()
		if err != nil || !info.Mode().IsRegular() || !currentOwner(info) || info.Mode().Perm()&0o022 != 0 {
			return nil, errors.New("command input requires a held owned regular file without group/other write permissions")
		}
		cmd.Stdin = r.input
	}
	localGitFetch := false
	if args[0] == "git" {
		// Admit the same effective directory Git will execute in. Diagnostic
		// storage and command cwd are independent, and a relative -C is relative
		// to command cwd, not this process's cwd.
		directory := r.Work
		if r.directory != "" {
			directory = r.directory
		}
		dir, directoryErr := filepath.Abs(directory)
		if directoryErr != nil {
			return nil, &CommandError{Phase: phase, ExitCode: -1, Cause: directoryErr}
		}
		gitArgs := args[1:]
		if len(gitArgs) >= 2 && gitArgs[0] == "-C" {
			if filepath.IsAbs(gitArgs[1]) {
				dir = gitArgs[1]
			} else {
				dir = filepath.Join(dir, gitArgs[1])
			}
			gitArgs = gitArgs[2:]
		}
		cmd, err = sourcegit.LocalCommand(commandCtx, r.Env, dir, gitArgs...)
		if err != nil {
			return nil, &CommandError{Phase: phase, ExitCode: -1, Cause: err}
		}
		localGitFetch = gitArgs[0] == "fetch"
	}
	if deadline, bounded := ctx.Deadline(); bounded && !ownedLifetime {
		// Go contexts do not cross exec. The child CLI (including one invoked
		// through bootstrap shell work) must retain this absolute work bound.
		cmd.Env = SetEnv(cmd.Env, map[string]string{"NVKR_WORK_DEADLINE": deadline.UTC().Format(time.RFC3339Nano)})
	}
	cmd.Dir = r.Work
	if r.directory != "" {
		cmd.Dir = r.directory
	}
	if err := ownedcmd.ConfigureCancellation(cmd, ownedcmd.DefaultTerminationGrace); err != nil {
		return nil, err
	}
	p := &OwnedProcess{cmd: cmd, phase: phase, done: make(chan struct{}), groupProbe: groupAlive, groupSignal: signalGroup, localGitFetch: localGitFetch}
	started := false
	defer func() {
		if !started {
			p.closeUnstartedOutput()
		}
	}()
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
		p.processWriter = r.Progress
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
	cmd.Stdout, err = p.childOutput(p.stdout)
	if err == nil {
		cmd.Stderr = cmd.Stdout
		if p.stderr != p.stdout {
			cmd.Stderr, err = p.childOutput(p.stderr)
		}
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
	if r.Progress != nil {
		_, _ = fmt.Fprintf(r.Progress, "Demo phase: %s\n", phase)
	}
	var activityWriter *os.File
	if r.buildActivity {
		p.processWriter = r.Progress
		reader, writer, pipeErr := os.Pipe()
		if pipeErr != nil {
			if p.log != nil {
				_ = p.log.Close()
			}
			if p.logDir != nil {
				_ = p.logDir.Close()
			}
			return nil, &CommandError{Phase: phase, ExitCode: -1, Cause: pipeErr}
		}
		p.activity, err = buildactivity.NewReceiver(ctx, reader)
		if err != nil {
			_ = reader.Close()
			_ = writer.Close()
			if p.log != nil {
				_ = p.log.Close()
			}
			if p.logDir != nil {
				_ = p.logDir.Close()
			}
			return nil, &CommandError{Phase: phase, ExitCode: -1, Cause: err}
		}
		activityWriter = writer
		cmd.ExtraFiles = []*os.File{writer}
		cmd.Args = append(cmd.Args, "--build-activity-fd", "3")
	}
	err = requireContext(ctx)
	if err == nil {
		err = cmd.Start()
	}
	if activityWriter != nil {
		_ = activityWriter.Close()
	}
	if err != nil {
		if p.activity != nil {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err = errors.Join(err, p.activity.Close(closeCtx))
			cancel()
		}
		if p.log != nil {
			_ = p.log.Close()
		}
		if p.logDir != nil {
			_ = p.logDir.Close()
		}
		return nil, &CommandError{Phase: phase, ExitCode: -1, Cause: err}
	}
	p.startOutputDrains()
	started = true
	reportProgress(p.processWriter, ProgressEvent{Kind: ProcessStarted, Name: phase, Status: "started"})
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

// A zero timeout inherits the caller's cancelable work context. Positive values
// are reserved for independently bounded control requests and cleanup.
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
	if timeout < 0 || timeout == 0 && ctx.Done() == nil {
		return nil, errors.New("command requires a positive timeout or cancelable caller context")
	}
	caller := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	p, err := r.start(ctx, args, phase, capture, false)
	if err != nil {
		return nil, err
	}
	if err := p.waitOwned(ctx); errors.Is(err, ErrUnjoined) {
		// A diagnostic sink may itself still be blocked while holding its
		// capture mutex. An unjoined owner cannot yield a complete capture;
		// leave its files/handles owned rather than blocking on that mutex.
		return nil, err
	}
	p.observeProcessExit()
	raw, outErr := p.stdout.result()
	_, stderrErr := p.stderr.result()
	if p.diagnostic != nil {
		_, logErr := p.diagnostic.result()
		outErr = errors.Join(outErr, logErr)
	}
	p.mu.Lock()
	waitErr, joinErr, outputErr := p.waitErr, p.joinErr, p.outputErr
	p.mu.Unlock()
	if joinErr != nil {
		return raw, errors.Join(joinErr, waitErr, ctx.Err(), outErr, stderrErr, outputErr)
	}
	if err := errors.Join(waitErr, ctx.Err(), outErr, stderrErr, outputErr); err != nil {
		if timeout > 0 && ctx.Err() == context.DeadlineExceeded && caller.Err() == nil && outErr == nil && stderrErr == nil && outputErr == nil {
			err = errors.Join(err, errCommandDeadline)
		}
		code := -1
		var exit *exec.ExitError
		if outErr == nil && stderrErr == nil && outputErr == nil && errors.As(waitErr, &exit) {
			code = exit.ExitCode()
		}
		if p.localGitFetch && waitErr != nil && ctx.Err() == nil {
			err = nvkrerrors.Wrap(nvkrerrors.CodeFetchFailed, err, "local source Git fetch failed; %s", sourcegit.LocalCredentialPolicy)
		}
		return raw, &CommandError{Phase: phase, ExitCode: code, Cause: err}
	}
	return raw, nil
}
