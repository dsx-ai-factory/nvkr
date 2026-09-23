package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// This opt-in test exercises the real Docker or Podman launch settings. Repeated
// Dockerfile copies must not multiply storage for unchanged files on hosted runners.
func TestBuildKitSnapshotStorageBudget(t *testing.T) {
	image := os.Getenv("NKD_BUILDKIT_TEST_IMAGE")
	if image == "" {
		t.Skip("set NKD_BUILDKIT_TEST_IMAGE to the pinned rootless BuildKit image")
	}
	runtime := os.Getenv("NKD_BUILDKIT_TEST_RUNTIME")
	if runtime == "" {
		runtime = "docker"
	}
	if runtime != "docker" && runtime != "podman" {
		t.Fatalf("NKD_BUILDKIT_TEST_RUNTIME must be docker or podman, got %q", runtime)
	}
	platform := os.Getenv("NKD_BUILDKIT_TEST_PLATFORM")
	if platform == "" {
		platform = "linux/amd64"
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "payload"), bytes.Repeat([]byte{0x5a}, 16<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte("snapshot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dockerfile := "FROM scratch\nCOPY payload /payload\n"
	for i := range 8 {
		dockerfile += "COPY marker /marker" + strconv.Itoa(i) + "\n"
	}
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "nkd-storage-" + filepath.Base(filepath.Dir(root))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, runtime, "rm", "-f", name).Run()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	args := []string{"run", "--rm", "--init", "--name", name, "--platform", platform, "--network", "none"}
	args = append(args, buildKitRuntimeArgs(runtime)...)
	args = append(args, "-v", root+":/src:ro", image, "sh", "-ec",
		"buildctl-daemonless.sh build --frontend dockerfile.v0 --local context=/src --local dockerfile=/src; "+
			"find /home/user/.local/share/buildkit -name work -prune -o -type f -exec stat -c %b '{}' ';' > /tmp/blocks; "+
			"awk '{blocks += $1} END {print \"NKD_STORAGE_KIB=\" blocks / 2}' /tmp/blocks")
	raw, err := exec.CommandContext(ctx, runtime, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("rootless build: %v\n%s", err, raw)
	}
	_, value, found := strings.Cut(string(raw), "NKD_STORAGE_KIB=")
	if !found {
		t.Fatalf("missing storage measurement: %s", raw)
	}
	kib, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || kib <= 0 || kib > 64*1024 {
		t.Fatalf("16 MiB payload exceeded 64 MiB storage budget: %q (%v)", value, err)
	}
	t.Logf("BuildKit allocated %d KiB for the repeated-copy fixture", kib)
}

func TestBuildKitRuntimeArgumentsIsolatePodmanSELinuxLabel(t *testing.T) {
	docker := strings.Join(buildKitRuntimeArgs("/usr/bin/docker"), " ")
	podman := strings.Join(buildKitRuntimeArgs("/usr/bin/podman"), " ")
	if strings.Contains(docker, "label=disable") {
		t.Fatal("Docker BuildKit unexpectedly disables SELinux label separation")
	}
	if !strings.Contains(podman, "--security-opt label=disable") {
		t.Fatal("Podman BuildKit lacks the required nested-worker SELinux mount admission")
	}
	for _, forbidden := range []string{"--privileged", "/dev/fuse", "docker.sock", "podman.sock"} {
		if strings.Contains(podman, forbidden) {
			t.Fatalf("Podman BuildKit gained forbidden capability %q", forbidden)
		}
	}
}

func TestBuildKitAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		launch    bool
		runtime   string
		qualified bool
		packaged  bool
		code      e.Code
	}{
		{name: "launched Docker", launch: true, runtime: "docker"},
		{name: "launched Podman", launch: true, runtime: "podman"},
		{name: "direct execution", runtime: "docker", code: e.CodeEngineCapability},
		{name: "unsupported runtime", launch: true, runtime: "containerd", code: e.CodeEngineCapability},
		{name: "qualified", launch: true, runtime: "docker", qualified: true, code: e.CodeEngineCapability},
		{name: "legacy packaging", launch: true, runtime: "docker", packaged: true, code: e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := sampleGraph().Nodes[0]
			node.BuilderType = ir.BuilderBuildKit
			node.Network = ir.NetworkDefault
			node.OutputType = "oci-image"
			opts := RunOptions{Ctx: context.Background(), Launch: tc.launch, Runtime: tc.runtime, DryRun: true, Package: tc.packaged, SourceRoot: filepath.Join(t.TempDir(), "missing")}
			if tc.qualified {
				opts.Qualification = &QualifiedExecution{}
			}
			_, err := (Local{}).ExecNode(node, opts)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("error=%v want %s", err, tc.code)
			}
		})
	}
}

func TestBuildKitOutputPromotionAndCleanup(t *testing.T) {
	for _, promote := range []bool{false, true} {
		t.Run(map[bool]string{false: "abort", true: "promote"}[promote], func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "collected")
			if err := os.Mkdir(out, 0o755); err != nil {
				t.Fatal(err)
			}
			mount, commit, cleanup, err := prepareBuildKitOutput(context.Background(), out)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			parent, err := os.Stat(filepath.Dir(mount))
			if err != nil {
				t.Fatal(err)
			}
			if parent.Mode().Perm() != 0o700 {
				t.Fatalf("private parent mode=%v", parent.Mode())
			}
			if err := os.WriteFile(filepath.Join(mount, "image.tar"), []byte("observed export"), 0o644); err != nil {
				t.Fatal(err)
			}
			if promote {
				if err := commit(); err != nil {
					t.Fatal(err)
				}
			}
			cleanup()
			if _, err := os.Stat(filepath.Dir(mount)); !os.IsNotExist(err) {
				t.Fatalf("staging parent remains: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(out, "image.tar"))
			if promote {
				if err != nil || string(raw) != "observed export" {
					t.Fatalf("promoted output=%q %v", raw, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("aborted output exposed: %v", err)
			}
		})
	}
}
