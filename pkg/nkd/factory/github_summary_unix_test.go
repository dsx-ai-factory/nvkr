//go:build linux || darwin

package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestGitHubPackerDiagnosticRouteWrite(t *testing.T) {
	for _, mode := range []string{"published", "write failure", "absent output", "absent route", "successful build"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "github-output")
			retained := "diagnostics/packer/run-0123456789abcdef"
			var primary error = e.New(e.CodeFetchFailed, "original producer failure")
			switch mode {
			case "write failure":
				output = root // A directory cannot be the provider output file.
			case "absent output":
				output = ""
			case "absent route":
				retained = ""
			case "successful build":
				primary = nil
			}
			err := publishGitHubPackerDiagnosticRoute(primary, output, retained)
			if mode == "write failure" {
				assertFactoryErrorCode(t, err, e.CodeFetchFailed)
				if !errors.Is(err, primary) || !errors.Is(err, unix.EISDIR) || err == primary || !strings.Contains(err.Error(), "publish ARC Packer diagnostic route") {
					t.Fatalf("output-write failure did not preserve and augment the producer error: %v", err)
				}
				return
			}
			if err != primary {
				t.Fatalf("diagnostic publication changed the producer outcome: %v", err)
			}
			raw, readErr := os.ReadFile(filepath.Join(root, "github-output"))
			if mode == "published" {
				if readErr != nil || string(raw) != "packer_diagnostics="+retained+"\n" {
					t.Fatalf("diagnostic route output %q: %v", raw, readErr)
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatalf("unavailable/nonfailure route created provider output: %v", readErr)
			}
		})
	}
}

// A real descriptor 1/2 pipe with no reader exercises Go's SIGPIPE path, which a
// writer returning io.ErrClosedPipe cannot reproduce. These subprocesses perform
// no container or producer work; the marker proves diagnostics did not skip work.
func TestGitHubReporterClosedPipeProcess(t *testing.T) {
	if value := os.Getenv("NKD_TEST_BROKENPIPE_STATUS"); value != "" {
		status, err := strconv.Atoi(value)
		if err != nil {
			os.Exit(90)
		}
		err = githubProgress(context.Background(), GitHubStepOptions{Stdout: os.Stdout, Stderr: os.Stderr}, "Operation", nil, func(progress *githubStepProgress) error {
			_, _ = fmt.Fprintln(progress.options.Stdout, "child stdout")
			_, _ = fmt.Fprintln(progress.options.Stderr, "child stderr")
			if err := os.WriteFile(os.Getenv("NKD_TEST_BROKENPIPE_MARKER"), []byte("ran"), 0o600); err != nil {
				return e.Wrap(e.CodeInternal, err, "writing test marker")
			}
			if status != 0 {
				return e.New(e.CodeVerifyFailed, "expected test failure")
			}
			return nil
		})
		os.Exit(GitHubStepExitCode(err))
	}
	for _, stream := range []string{"stdout", "stderr", "both"} {
		for _, status := range []int{0, 22} {
			t.Run(fmt.Sprintf("%s/%d", stream, status), func(t *testing.T) {
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
				marker := filepath.Join(t.TempDir(), "ran")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGitHubReporterClosedPipeProcess$")
				cmd.Env = append(os.Environ(), "NKD_TEST_BROKENPIPE_STATUS="+strconv.Itoa(status), "NKD_TEST_BROKENPIPE_MARKER="+marker)
				if stream == "stdout" || stream == "both" {
					cmd.Stdout = writer
				}
				if stream == "stderr" || stream == "both" {
					cmd.Stderr = writer
				}
				err = cmd.Run()
				if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != status {
					t.Fatalf("broken pipe replaced status %d: %v", status, err)
				}
				if raw, err := os.ReadFile(marker); err != nil || string(raw) != "ran" {
					t.Fatalf("broken pipe prevented work: %q %v", raw, err)
				}
			})
		}
	}
}

func TestGitHubSummaryRejectsSpecialFilesWithoutChangingWork(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			sink := filepath.Join(root, "summary")
			if kind == "symlink" {
				if err := os.Symlink(target, sink); err != nil {
					t.Fatal(err)
				}
			} else if err := unix.Mkfifo(sink, 0o600); err != nil {
				t.Fatal(err)
			}
			before := time.Now()
			if err := githubProgress(t.Context(), GitHubStepOptions{SummaryPath: sink}, "work", nil, func(*githubStepProgress) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if time.Since(before) > time.Second {
				t.Fatal("diagnostic blocked operation")
			}
			raw, err := os.ReadFile(target)
			if err != nil || string(raw) != "unchanged" {
				t.Fatalf("summary followed symlink: %q %v", raw, err)
			}
		})
	}
}
