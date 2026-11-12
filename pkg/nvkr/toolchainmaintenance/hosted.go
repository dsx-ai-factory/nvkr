// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// admission is process-local and never accepted from a caller's JSON or boolean.
// It binds the reviewed policy bytes to the exact authenticated preparation run.
type admission struct{ record Record }

func hosted() error {
	if runtime.GOOS != "linux" || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_REPOSITORY") != Repository || os.Getenv("GITHUB_REF") != "refs/heads/main" || os.Getenv("GITHUB_WORKFLOW_REF") != Repository+"/"+Workflow+"@refs/heads/main" {
		return e.New(e.CodeEngineCapability, "toolchain publication requires the repository's main-branch native maintenance workflow on Linux")
	}
	if os.Getenv("GITHUB_EVENT_NAME") != "workflow_dispatch" {
		return e.New(e.CodeEngineCapability, "approved publication requires explicit main-branch dispatch")
	}
	return nil
}

func github(ctx context.Context, token, endpoint string, result any) error {
	if token == "" {
		return e.New(e.CodeTrustPolicyMissing, "GitHub read credential is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/"+endpoint, nil)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "create GitHub request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return e.New(e.CodeUnavailable, "GitHub maintenance request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return e.New(e.CodeGateUnmet, "GitHub maintenance admission returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxRecordBytes+1))
	if err != nil || len(raw) > maxRecordBytes {
		return e.New(e.CodeVerifyFailed, "GitHub response exceeds the maintenance bound or is incomplete")
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decode GitHub response")
	}
	return nil
}

type preparationRun struct {
	ID         int64  `json:"id"`
	Attempt    int    `json:"run_attempt"`
	SHA        string `json:"head_sha"`
	Branch     string `json:"head_branch"`
	Path       string `json:"path"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

func validateRun(r Record, run preparationRun) error {
	if run.ID != r.RunID || run.Attempt != r.Attempt || run.SHA != r.SourceCommit || run.Branch != "main" || run.Path != Workflow || run.Status != "completed" || run.Conclusion != "success" || (run.Event != "push" && run.Event != "schedule" && run.Event != "workflow_dispatch") {
		return e.New(e.CodeGateUnmet, "approval does not identify a successful native preparation attempt on main")
	}
	return nil
}

func admit(ctx context.Context, root, run string) (*admission, error) {
	if err := hosted(); err != nil {
		return nil, err
	}
	r, err := LoadApproval(ctx, root, run, time.Now())
	if err != nil {
		return nil, err
	}
	// Do not accept edited or untracked policy files in the trusted checkout.
	path := ApprovalDirectory + "/" + run + ".json"
	committed, err := command(ctx, root, "git", "show", os.Getenv("GITHUB_SHA")+":"+path)
	if err != nil {
		return nil, err
	}
	raw, err := read(ctx, filepath.Join(root, path))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(raw, committed) {
		return nil, e.New(e.CodeUnverifiedRef, "approval must be exact reviewed main-branch bytes")
	}
	head, err := command(ctx, root, "git", "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(head)) != os.Getenv("GITHUB_SHA") {
		return nil, e.New(e.CodeUnverifiedRef, "maintenance checkout is not the triggering main revision")
	}
	if err := checkPreparationInputs(ctx, root, r.SourceCommit); err != nil {
		return nil, err
	}
	var preparation preparationRun
	if err := github(ctx, os.Getenv("GH_TOKEN"), fmt.Sprintf("repos/%s/actions/runs/%d/attempts/%d", Repository, r.RunID, r.Attempt), &preparation); err != nil {
		return nil, err
	}
	if err := validateRun(r, preparation); err != nil {
		return nil, err
	}
	return &admission{record: r}, nil
}

// Admit is the read-only preflight before downloading retained images or
// entering the protected publication environment. The returned record is data.
func Admit(ctx context.Context, root, run string) (Record, error) {
	a, err := admit(ctx, root, run)
	if err != nil {
		return Record{}, err
	}
	return a.record, nil
}

func command(ctx context.Context, root, executable string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = root
	if executable == "git" {
		// The explicit checkout owns repository, index and object selection;
		// inherited hook/tool routing must not redirect admission or repinning.
		inherited := cmd.Environ()
		cmd.Env = make([]string, 0, len(inherited))
		for _, entry := range inherited {
			key, _, _ := strings.Cut(entry, "=")
			switch key {
			case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
				"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_SHALLOW_FILE", "GIT_PREFIX":
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
	}
	var output limitedBuffer
	cmd.Stdout = &output
	// Child stderr can contain credential-bearing URLs; retain the coded boundary.
	if err := ownedcmd.Run(ctx, cmd); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "maintenance %s failed", executable)
	}
	return output.Bytes(), nil
}

type limitedBuffer struct{ data []byte }

func (b *limitedBuffer) Bytes() []byte { return b.data }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > maxRecordBytes {
		return 0, fmt.Errorf("command output exceeds bound")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func internalPackages(ctx context.Context) error {
	for _, kind := range []string{"inspector", "packer"} {
		var pkg struct {
			Visibility string `json:"visibility"`
		}
		endpoint := "orgs/dsx-ai-factory/packages/container/" + url.PathEscape("nvkr/m2-candidate/toolchains/"+kind+"-native")
		if err := github(ctx, os.Getenv("NVKR_REGISTRY_TOKEN"), endpoint, &pkg); err != nil {
			return err
		}
		if pkg.Visibility != "internal" {
			return e.New(e.CodeGateUnmet, "toolchain package must already exist with internal visibility")
		}
	}
	return nil
}

func registryOptions(ctx context.Context) ([]remote.Option, error) {
	user, token := os.Getenv("NVKR_REGISTRY_USER"), os.Getenv("NVKR_REGISTRY_TOKEN")
	if user == "" || token == "" {
		return nil, e.New(e.CodeTrustPolicyMissing, "explicit phase-scoped registry credential is required")
	}
	return []remote.Option{remote.WithContext(ctx), remote.WithAuth(&authn.Basic{Username: user, Password: token})}, nil
}

// Publish admits all four archives and exceptions before the first registry
// write. A failed or interrupted push never changes consumers; retries address
// the same manifests and old pinned subjects are never deleted.
func Publish(ctx context.Context, root, run, directory string) error {
	a, err := admit(ctx, root, run)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	options, err := registryOptions(ctx)
	if err != nil {
		return err
	}
	if err := internalPackages(ctx); err != nil {
		return err
	}
	images, err := openApprovedImages(ctx, directory, a.record)
	if err != nil {
		return err
	}
	defer closeImages(images)
	// Recheck expiry after potentially lengthy archive admission.
	if err := a.record.validate(time.Now(), true); err != nil {
		return err
	}
	for i, im := range images {
		if err := a.record.validate(time.Now(), true); err != nil {
			return err
		}
		ref, err := name.NewDigest(a.record.Images[i].Reference(), name.StrictValidation)
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "parse publication subject")
		}
		if err := remote.Write(ref, im.image, options...); err != nil {
			return e.New(e.CodeUnavailable, "publish %s failed; retained pins remain unchanged", im.description.Key())
		}
	}
	return nil
}

// Verify uses only the independent reader credential. It downloads and verifies
// every compressed layer as well as manifest/config identities before repinning.
func Verify(ctx context.Context, root, run string) (Record, error) {
	a, err := admit(ctx, root, run)
	if err != nil {
		return Record{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	options, err := registryOptions(ctx)
	if err != nil {
		return Record{}, err
	}
	if err := internalPackages(ctx); err != nil {
		return Record{}, err
	}
	for _, expected := range a.record.Images {
		ref, err := name.NewDigest(expected.Reference(), name.StrictValidation)
		if err != nil {
			return Record{}, e.Wrap(e.CodeInvalidCatalog, err, "parse read-back reference")
		}
		im, err := remote.Image(ref, options...)
		if err != nil {
			return Record{}, e.New(e.CodeVerifyFailed, "independent reader cannot fetch %s", expected.Key())
		}
		if err := verifyImage(ctx, im, expected); err != nil {
			return Record{}, err
		}
	}
	return a.record, nil
}

func verifyImage(ctx context.Context, image v1.Image, expected Image) error {
	manifest, err := image.RawManifest()
	if err != nil || fmt.Sprintf("sha256:%x", sha256.Sum256(manifest)) != expected.ManifestDigest {
		return e.New(e.CodeVerifyFailed, "manifest read-back mismatch")
	}
	configBytes, err := image.RawConfigFile()
	if err != nil || fmt.Sprintf("sha256:%x", sha256.Sum256(configBytes)) != expected.ImageID {
		return e.New(e.CodeVerifyFailed, "config read-back mismatch")
	}
	config, err := image.ConfigFile()
	if err != nil || config.OS != "linux" || config.Architecture != expected.Arch {
		return e.New(e.CodeVerifyFailed, "platform read-back mismatch")
	}
	layers, err := image.Layers()
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "enumerate read-back layers")
	}
	var total int64
	for _, layer := range layers {
		size, err := layer.Size()
		if err != nil || size < 0 || size > maxArchiveBytes-total {
			return e.New(e.CodeVerifyFailed, "read-back image exceeds byte bound")
		}
		total += size
		body, err := layer.Compressed()
		if err != nil {
			return e.New(e.CodeVerifyFailed, "read-back layer unavailable")
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, io.LimitReader(contextReader{ctx, body}, size+1))
		closeErr := body.Close()
		expectedDigest, digestErr := layer.Digest()
		if copyErr != nil || closeErr != nil || digestErr != nil || n != size || fmt.Sprintf("sha256:%x", hash.Sum(nil)) != expectedDigest.String() {
			return e.New(e.CodeVerifyFailed, "read-back layer integrity check failed")
		}
	}
	return nil
}
