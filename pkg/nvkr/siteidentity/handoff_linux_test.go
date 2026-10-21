// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package siteidentity

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These fixtures exercise the actual embedded shell control flow with private
// paths and source-owner systemd/ownership checks replaced by test commands.
// They do not establish live systemd behavior or actual guest ownership.
func TestHandoffExplicitRecoveryAlwaysRendersAndRestarts(t *testing.T) {
	for _, tc := range []struct{ startingState, failureStage string }{
		{"new", "renderer"}, {"old", "renderer"},
		{"new", "manager"}, {"old", "manager"},
	} {
		t.Run(tc.startingState+"/"+tc.failureStage, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, raw string, mode os.FileMode) string {
				t.Helper()
				p := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(raw), mode); err != nil {
					t.Fatal(err)
				}
				return p
			}
			r := validRequest()
			r.ServerAddress = "192.168.70.10"
			r.ServerPort = 30440
			oldEnv, newEnv := "original-bootstrap-environment\n", environment(r)
			envBytes := newEnv
			if tc.startingState == "old" {
				envBytes = oldEnv
			}
			envFile := write("etc/nke/spire.env", envBytes, 0600)
			ca := write("etc/nke/trust-bundle.pem", "synthetic-ca", 0600)
			tpm := write("usr/local/lib/spire-agent/tpm_attestor_agent", "synthetic-tpm", 0700)
			failFlag := write(tc.failureStage+"-fails", "yes", 0600)
			renderer := write("usr/lib/nvke/spire-configure.sh", "#!/bin/bash\necho renderer >> "+root+"/calls\necho PRIVATE-RENDERER-DIAGNOSTIC\n[[ ! -e "+root+"/renderer-fails ]]\n", 0700)
			write("bin/id", "#!/bin/bash\necho 0\n", 0700)
			write("bin/stat", "#!/bin/bash\nif [[ $1 == -c && $2 == '%u:%a' ]]; then echo 0:600; else exec /usr/bin/stat \"$@\"; fi\n", 0700)
			write("bin/systemctl", `#!/bin/bash
case "$1" in
 show) if [[ "$3" == --property=MainPID ]]; then echo 4242; fi ;;
 is-active) exit 0 ;;
 restart) echo restart >> `+root+`/calls
  if [[ -e `+root+`/manager-fails ]]; then echo PRIVATE-MANAGER-DIAGNOSTIC; exit 1; fi ;;
 *) exit 91 ;;
esac
`, 0700)
			if err := os.MkdirAll(filepath.Join(root, "var/lib/nvke-native"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "run"), 0700); err != nil {
				t.Fatal(err)
			}
			script := handoffScript
			for _, target := range []string{"/etc/nke/spire.env", "/usr/lib/nvke/spire-configure.sh", "/etc/nke/trust-bundle.pem", "/usr/local/lib/spire-agent/tpm_attestor_agent", "/run/nvkr-native-spire-handoff.lock", "/var/lib/nvke-native/spire-handoff.XXXXXXXX"} {
				script = strings.ReplaceAll(script, target, root+target)
			}
			scriptPath := write("handoff.sh", script, 0700)
			args := []string{scriptPath, r.ServerAddress, "30440", r.TrustDomain, hash(oldEnv), hash(newEnv), hashFile(t, renderer), hashFile(t, ca)}
			run := func() ([]byte, error) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "/bin/bash", args...)
				cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":/usr/bin:/bin")
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
				cmd.WaitDelay = time.Second
				return cmd.CombinedOutput()
			}
			failedOutput, failedErr := run()
			failure, isExit := failedErr.(*exec.ExitError)
			if !isExit || failure.ExitCode() != 125 || strings.TrimSpace(string(failedOutput)) != unknownCompletionSentinel {
				t.Fatalf("failed owner call did not report unknown: %v %s", failedErr, failedOutput)
			}
			if err := os.Remove(failFlag); err != nil {
				t.Fatal(err)
			}
			out, err := run()
			if err != nil {
				t.Fatalf("explicit recovery failed: %v %s", err, out)
			}
			if strings.Contains(string(out), "PRIVATE-RENDERER") {
				t.Fatalf("private renderer output contaminated RPC receipt: %s", out)
			}
			var receipt ScriptResult
			if err := json.Unmarshal(out, &receipt); err != nil {
				t.Fatalf("receipt: %v %s", err, out)
			}
			if receipt.Outcome != "reapplied" || receipt.EnvironmentSHA256 != hash(newEnv) {
				t.Fatalf("unexpected receipt %+v", receipt)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := "renderer\nrenderer\nrestart\n"
			if tc.failureStage == "manager" {
				wantCalls = "renderer\nrestart\nrenderer\nrestart\n"
			}
			if string(calls) != wantCalls {
				t.Fatalf("recovery skipped owner execution: %q", calls)
			}
			log, err := os.ReadFile(receipt.PrivateLog)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(log), "PRIVATE-RENDERER-DIAGNOSTIC") {
				t.Fatal("private diagnostics not retained")
			}
			if hashFile(t, tpm) != hash("synthetic-tpm") {
				t.Fatal("TPM plugin changed")
			}
			if hashFile(t, envFile) != hash(newEnv) {
				t.Fatal("environment differs")
			}
			backups, err := filepath.Glob(filepath.Join(root, "var/lib/nvke-native/spire-handoff.*/spire.env.before"))
			if err != nil {
				t.Fatal(err)
			}
			wantBackups := 0
			if tc.startingState == "old" {
				wantBackups = 1
			}
			if len(backups) != wantBackups {
				t.Fatalf("backups=%d want=%d", len(backups), wantBackups)
			}
		})
	}
}

func hashFile(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return hash(string(raw))
}
