package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestPrivateImageCredentialLifetime(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "pull failure"}[failure], func(t *testing.T) {
			var directory string
			err := pullGitHubImages(t.Context(), []string{"ghcr.io/example/tool@sha256:" + strings.Repeat("a", 64)}, "linux/amd64", "reader", "synthetic-token",
				func(ctx context.Context, config, platform, image string) error {
					directory = config
					if _, ok := ctx.Deadline(); !ok || platform != "linux/amd64" || !strings.Contains(image, "@sha256:") {
						t.Fatal("unbounded or unpinned pull")
					}
					for _, path := range []string{config, filepath.Join(config, "config.json")} {
						info, err := os.Stat(path)
						if err != nil || info.Mode().Perm()&0o077 != 0 {
							t.Fatalf("authentication is not private: %s / %v", path, err)
						}
					}
					if failure {
						return errors.New("synthetic pull failure")
					}
					return nil
				})
			if directory == "" {
				t.Fatal("pull was not attempted")
			}
			if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
				t.Fatal("credential directory survived the preparation step")
			}
			if failure {
				assertFactoryErrorCode(t, err, e.CodeFetchFailed)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, token := range []string{"", "bad\ntoken"} {
		err := pullGitHubImages(t.Context(), nil, "linux/amd64", "reader", token,
			func(context.Context, string, string, string) error {
				t.Fatal("invalid credential reached runtime")
				return nil
			})
		assertFactoryErrorCode(t, err, e.CodeFetchFailed)
	}
}

func TestMaintainedM2PrivateImageCredentialBoundary(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nkd-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
	disks, fetches := 0, 0
	for _, job := range document.Jobs {
		for _, step := range job.Steps {
			if step.Env[githubImageToken] == "" {
				continue
			}
			if job.Permissions["packages"] != "read" || job.Permissions["id-token"] != "" || step.Env[sourceTokenEnv] != "" ||
				!strings.Contains(step.Run, "factory github prepare-images ") {
				t.Fatal("toolchain credential escaped its read-only preparation step")
			}
			if job.Environment == githubARCEnvironment {
				disks++
			} else {
				fetches++
			}
		}
	}
	if disks != 2 || fetches == 0 {
		t.Fatalf("missing preparation for native disks or their source vendors: %d/%d", disks, fetches)
	}
	for _, test := range []struct{ name, before, after string }{
		{"write scope", "packages: read", "packages: write"},
		{"wrong credential", "NKD_TOOLCHAIN_READ_TOKEN: '${{ github.token }}'", "NKD_TOOLCHAIN_READ_TOKEN: '${{ secrets.NKE_SOURCE_TOKEN }}'"},
		{"wrong helper", "factory github prepare-images --plan", "factory github run-node --plan"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *bundle
			changed.Files = make(map[string][]byte, len(bundle.Files))
			for name, value := range bundle.Files {
				changed.Files[name] = value
			}
			changed.Files[GitHubWorkflowPath] = bytes.Replace(bundle.Files[GitHubWorkflowPath], []byte(test.before), []byte(test.after), 1)
			if bytes.Equal(changed.Files[GitHubWorkflowPath], bundle.Files[GitHubWorkflowPath]) {
				t.Fatal("mutation did not change workflow")
			}
			changed.WorkflowDigest = pipeline.FileDigest(changed.Files[GitHubWorkflowPath])
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodeEngineCapability)
		})
	}
}

func TestPrivateImageActiveInterruption(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancellation", true: "deadline"}[deadline], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			var directory string
			err := pullGitHubImages(ctx, []string{"ghcr.io/example/tool@sha256:" + strings.Repeat("a", 64)}, "linux/amd64", "reader", "synthetic-token",
				func(active context.Context, config, _, _ string) error {
					directory = config
					if !deadline {
						cancel()
					}
					<-active.Done()
					return active.Err()
				})
			assertFactoryErrorCode(t, err, e.CodeTimeout)
			if !errors.Is(err, ctx.Err()) {
				t.Fatalf("lost context cause: %v", err)
			}
			if directory == "" {
				t.Fatal("pull did not start")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("credential directory survived interruption")
			}
		})
	}
}
