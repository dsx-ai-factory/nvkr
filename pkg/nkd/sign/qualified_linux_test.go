//go:build linux

package sign

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("NKD_COSIGN_HELD_HELPER") == "1" {
		os.Exit(runHeldCosignHelper())
	}
	if os.Getenv("NKD_COSIGN_SIGN_HELD_HELPER") == "1" {
		os.Exit(runHeldCosignSignHelper())
	}
	os.Exit(m.Run())
}

var heldHelperFixture struct {
	sync.Once
	bytes []byte
	err   error
}

// Race-enabled Go tests can be dynamically linked. The production boundary
// correctly refuses those executables, so use a static helper while leaving
// the parent signer/verifier tests race-instrumented.
func heldCosignHelperBytes(t *testing.T) []byte {
	t.Helper()
	heldHelperFixture.Do(func() {
		self, err := os.Executable()
		if err != nil {
			heldHelperFixture.err = err
			return
		}
		raw, err := os.ReadFile(self)
		if err != nil {
			heldHelperFixture.err = err
			return
		}
		binary, err := elf.NewFile(bytes.NewReader(raw))
		if err != nil {
			heldHelperFixture.err = err
			return
		}
		dynamic := false
		for _, program := range binary.Progs {
			if program.Type == elf.PT_INTERP {
				dynamic = true
			}
		}
		if !dynamic {
			heldHelperFixture.bytes = raw
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		helper := filepath.Join(t.TempDir(), "held-cosign-helper")
		command := exec.CommandContext(ctx, "go", "test", "-c", "-o", helper, ".")
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=", "GOTOOLCHAIN=local")
		if output, err := command.CombinedOutput(); err != nil {
			heldHelperFixture.err = fmt.Errorf("building static held-cosign test helper: %w: %s", err, output)
			return
		}
		heldHelperFixture.bytes, heldHelperFixture.err = os.ReadFile(helper)
	})
	if heldHelperFixture.err != nil {
		t.Fatal(heldHelperFixture.err)
	}
	return append([]byte(nil), heldHelperFixture.bytes...)
}

func runHeldCosignHelper() int {
	for _, name := range []string{
		"LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "COSIGN_INSECURE_IGNORE_TLOG",
		"SIGSTORE_ROOT_FILE", "TUF_ROOT_JSON", "SOURCE_DATE_EPOCH",
	} {
		if os.Getenv(name) != "" {
			return 19
		}
	}
	for _, flag := range []string{"--bundle", "--trusted-root"} {
		path := processArgAfter(os.Args[1:], flag)
		if path == "" || !strings.HasPrefix(path, "/proc/self/fd/") {
			return 20
		}
	}
	if processArgAfter(os.Args[1:], "--signature") != "" ||
		processArgAfter(os.Args[1:], "--certificate") != "" {
		return 28
	}
	if len(os.Args) == 1 || os.Args[len(os.Args)-1] != "-" {
		return 21
	}
	if err := os.WriteFile(os.Getenv("NKD_COSIGN_READY"), []byte("ready"), 0o600); err != nil {
		return 22
	}
	if !waitForTestPath(os.Getenv("NKD_COSIGN_CONTINUE")) {
		return 23
	}
	document, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 24
	}
	for _, flag := range []string{"--bundle", "--trusted-root"} {
		if _, err := os.ReadFile(processArgAfter(os.Args[1:], flag)); err != nil {
			return 25
		}
	}
	if err := os.WriteFile(os.Getenv("NKD_COSIGN_CAPTURED"), document, 0o600); err != nil {
		return 26
	}
	if !waitForTestPath(os.Getenv("NKD_COSIGN_FINISH")) {
		return 27
	}
	return 0
}

func processArgAfter(args []string, flag string) string {
	for i := range args {
		if args[i] == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func waitForTestPath(path string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestProductionVerifierConsumesHeldBytesDuringRenameRestoreAttack(t *testing.T) {
	const identity = "release@example.test"
	const issuer = "https://issuer.example.test"
	req, material := qualifiedRequest(t, identity, issuer, true)
	document := []byte(`{"name":"signed"}`)
	req.ExactBytes = append([]byte(nil), document...)

	binaryBytes := heldCosignHelperBytes(t)
	binary := filepath.Join(t.TempDir(), "cosign")
	if err := os.WriteFile(binary, binaryBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	binarySum := sha256.Sum256(binaryBytes)
	verifier, err := NewPinnedCosignVerifier(binary, "sha256:"+hex.EncodeToString(binarySum[:]))
	if err != nil {
		t.Fatal(err)
	}

	coordination := t.TempDir()
	ready := filepath.Join(coordination, "ready")
	continuePath := filepath.Join(coordination, "continue")
	captured := filepath.Join(coordination, "captured")
	finish := filepath.Join(coordination, "finish")
	t.Setenv("NKD_COSIGN_HELD_HELPER", "1")
	t.Setenv("NKD_COSIGN_READY", ready)
	t.Setenv("NKD_COSIGN_CONTINUE", continuePath)
	t.Setenv("NKD_COSIGN_CAPTURED", captured)
	t.Setenv("NKD_COSIGN_FINISH", finish)
	t.Setenv("LD_PRELOAD", "/tmp/attacker.so")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/tmp/attacker.dylib")
	t.Setenv("COSIGN_INSECURE_IGNORE_TLOG", "true")
	t.Setenv("SIGSTORE_ROOT_FILE", "/tmp/attacker-root.pem")
	t.Setenv("TUF_ROOT_JSON", "/tmp/attacker-tuf.json")
	t.Setenv("SOURCE_DATE_EPOCH", "1")

	attackErr := make(chan error, 1)
	go func() {
		if !waitForTestPath(ready) {
			attackErr <- fmt.Errorf("helper never became ready")
			return
		}
		paths := []string{req.SnapshotPath, material.SignaturePath, material.CertificatePath,
			material.BundlePath, material.TrustedRootPath, binary}
		for _, path := range paths {
			if err := os.Rename(path, path+".valid"); err != nil {
				attackErr <- err
				return
			}
			if err := os.WriteFile(path, []byte("attacker"), 0o700); err != nil {
				attackErr <- err
				return
			}
		}
		if err := os.WriteFile(continuePath, []byte("continue"), 0o600); err != nil {
			attackErr <- err
			return
		}
		if !waitForTestPath(captured) {
			attackErr <- fmt.Errorf("helper never captured held inputs")
			return
		}
		for _, path := range paths {
			if err := os.Remove(path); err != nil {
				attackErr <- err
				return
			}
			if err := os.Rename(path+".valid", path); err != nil {
				attackErr <- err
				return
			}
		}
		if err := os.WriteFile(finish, []byte("finish"), 0o600); err != nil {
			attackErr <- err
			return
		}
		attackErr <- nil
	}()

	evidence, verifyErr := verifier.VerifyDocument(context.Background(), req)
	if attack := <-attackErr; attack != nil {
		t.Fatal(attack)
	}
	if verifyErr != nil {
		t.Fatal(verifyErr)
	}
	if evidence.OIDCIssuer != issuer {
		t.Fatalf("evidence=%+v", evidence)
	}
	got, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, document) {
		t.Fatalf("cosign consumed %q instead of authenticator bytes", got)
	}
}
