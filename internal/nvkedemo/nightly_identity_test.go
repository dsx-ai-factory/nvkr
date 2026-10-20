// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// These test-only descriptions correlate one admitted nightly cycle. They issue
// no publication, signer, source, runtime or qualification authority.
type NightlyProfile struct {
	Name              string `json:"name"`
	Bundle            string `json:"bundle"`
	Definition        string `json:"definition"`
	Environment       string `json:"environment"`
	Plan              string `json:"plan"`
	BuildWorkflow     string `json:"build_workflow"`
	HarnessCommit     string `json:"harness_commit"`
	RuntimeTarget     string `json:"runtime_target"`
	Network           string `json:"network"`
	DiskOutput        string `json:"disk_output"`
	ControllerOutput  string `json:"controller_output,omitempty"`
	ProfilesOutput    string `json:"profiles_output"`
	OSProfile         string `json:"os_profile"`
	KubernetesVersion string `json:"kubernetes_version"`
	LabelFilter       string `json:"label_filter"`
}

type NightlyBuild struct {
	Profile            NightlyProfile `json:"profile"`
	CoordinatorRun     string         `json:"coordinatorRun"`
	CoordinatorAttempt string         `json:"coordinatorAttempt"`
	ProducerRun        int64          `json:"producerRun"`
	ProducerAttempt    int64          `json:"producerAttempt"`
	SourceCommit       string         `json:"sourceCommit"`
	ProducerURL        string         `json:"producerURL"`
	Trust              string         `json:"trust"`
}

type NightlyInputs struct {
	Reference     string
	Plan          *factory.Plan
	HarnessCommit string
	// LocatorRoot contains an unsigned, restored reference carrier. It is never
	// the layout exported by Consumer.Verify under its exact-input receipt.
	LocatorRoot        string
	RunPath            string
	Run                lifecycle.Run
	ExpectedInvocation string
}

const nightlyRecordLimit int64 = 8 << 20
const nightlyMaxJSONInteger int64 = 1<<53 - 1

// Match the logical output-name contract owned by definition/model.go.
var nightlyOutputNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

func nightlyJSON(raw []byte, out any, strict bool) error {
	if int64(len(raw)) > nightlyRecordLimit {
		return e.New(e.CodeVerifyFailed, "nightly JSON exceeds byte bound")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(out); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding nightly description")
	}
	return nil
}

func loadNightlyProfile(ctx context.Context, file, name string) (NightlyProfile, error) {
	if err := requireContext(ctx); err != nil {
		return NightlyProfile{}, e.Wrap(e.CodeTimeout, err, "nightly profile context")
	}
	var profiles []NightlyProfile
	raw, err := readRunFile(ctx, filepath.Dir(file), filepath.Base(file), nightlyRecordLimit)
	if err != nil {
		return NightlyProfile{}, e.Wrap(e.CodeVerifyFailed, err, "reading maintained nightly profiles")
	}
	if err = nightlyJSON(raw, &profiles, true); err != nil {
		return NightlyProfile{}, err
	}
	seen := map[string]bool{}
	var selected NightlyProfile
	for _, profile := range profiles {
		if err = validateNightlyProfile(profile); err != nil {
			return NightlyProfile{}, err
		}
		if seen[profile.Name] {
			return NightlyProfile{}, e.New(e.CodeVerifyFailed, "duplicate nightly profile")
		}
		seen[profile.Name] = true
		if profile.Name == name {
			selected = profile
		}
	}
	if selected.Name == "" {
		return NightlyProfile{}, e.New(e.CodeUsage, "unknown maintained nightly profile")
	}
	return selected, nil
}

func loadNightlyBuild(ctx context.Context, file string) (NightlyBuild, error) {
	if err := requireContext(ctx); err != nil {
		return NightlyBuild{}, e.Wrap(e.CodeTimeout, err, "nightly build context")
	}
	var build NightlyBuild
	raw, err := readRunFile(ctx, filepath.Dir(file), filepath.Base(file), nightlyRecordLimit)
	if err != nil {
		return build, e.Wrap(e.CodeVerifyFailed, err, "reading nightly build locator")
	}
	err = nightlyJSON(raw, &build, true)
	return build, err
}

func validateNightlyProfile(profile NightlyProfile) error {
	if profile.Name == "" || profile.Name != profile.Bundle {
		return e.New(e.CodeVerifyFailed, "profile name differs from generated namespace")
	}
	if err := definition.ValidateGitHubBundleName(profile.Bundle); err != nil {
		return err
	}
	if profile.BuildWorkflow != "nvkr-run-"+profile.Bundle+".yml" || profile.Plan != ".github/nvkr-run-"+profile.Bundle+"/plan.json" {
		return e.New(e.CodePipelineDrift, "profile workflow or Plan differs from generated namespace")
	}
	for _, path := range []string{profile.Definition, profile.Environment, profile.Plan} {
		if !securepath.WorkspaceRelative(path) || filepath.Clean(path) != path {
			return e.New(e.CodeVerifyFailed, "profile input path is not repository-relative")
		}
	}
	if !nightlyGitCommit(profile.HarnessCommit) || profile.RuntimeTarget == "" || profile.Network == "" || profile.DiskOutput == "" || profile.ProfilesOutput == "" || profile.OSProfile == "" || profile.KubernetesVersion == "" || profile.LabelFilter == "" {
		return e.New(e.CodeVerifyFailed, "profile omits exact harness or runtime intent")
	}
	if profile.RuntimeTarget != "native-arm64-vm" || (profile.Network != "ovn" && profile.Network != "upstream-ovn") {
		return e.New(e.CodeEngineCapability, "nightly adapter requires native ARM64 VMs and an explicit supported OVN network")
	}
	if profile.ControllerOutput != "" && !nightlyOutputNamePattern.MatchString(profile.ControllerOutput) {
		return e.New(e.CodeVerifyFailed, "nightly controller output requires a canonical logical name")
	}
	return nil
}

func nightlyGitCommit(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 20 && hex.EncodeToString(raw) == value
}

func nightlyPositiveID(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || parsed > nightlyMaxJSONInteger || strconv.FormatInt(parsed, 10) != value {
		return 0, e.New(e.CodeUnpinnedInput, "nightly identity requires a canonical positive integer")
	}
	return parsed, nil
}

func validateNightlyCycle(profile NightlyProfile, build NightlyBuild, repository, repositoryID, sha, parentID, parentAttempt string) error {
	if err := validateNightlyProfile(profile); err != nil {
		return err
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." || strings.ContainsAny(repository, "?#%\\\r\n") {
		return e.New(e.CodeUnpinnedInput, "nightly repository identity is invalid")
	}
	for _, value := range []string{repositoryID, parentID, parentAttempt} {
		if _, err := nightlyPositiveID(value); err != nil {
			return err
		}
	}
	if !nightlyGitCommit(sha) || build.SourceCommit != sha || build.CoordinatorRun != parentID || build.CoordinatorAttempt != parentAttempt || build.Profile != profile || build.ProducerRun <= 0 || build.ProducerRun > nightlyMaxJSONInteger || build.ProducerAttempt != 1 || build.ProducerRun == mustNightlyID(parentID) || build.Trust != "github-run-facts; publication-not-yet-verified" || build.ProducerURL != fmt.Sprintf("https://github.com/%s/actions/runs/%d", repository, build.ProducerRun) {
		return e.New(e.CodeUnpinnedInput, "nightly locator differs from exact coordinator, source or producer")
	}
	return nil
}

func mustNightlyID(value string) int64 { parsed, _ := strconv.ParseInt(value, 10, 64); return parsed }

type nightlyGitHubRun struct {
	ID         int64  `json:"id"`
	RunAttempt int64  `json:"run_attempt"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
	Event      string `json:"event"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	HeadRepository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"head_repository"`
}

func validateNightlyGitHubRun(run nightlyGitHubRun, profile NightlyProfile, build NightlyBuild, repository string, repositoryID int64) error {
	if run.ID != build.ProducerRun || run.RunAttempt != build.ProducerAttempt || run.HeadSHA != build.SourceCommit || run.HeadBranch != "main" || run.Event != "workflow_dispatch" || run.Path != ".github/workflows/"+profile.BuildWorkflow || run.Status != "completed" || run.Conclusion != "success" || run.HTMLURL != build.ProducerURL || run.Repository.ID != repositoryID || run.Repository.FullName != repository || run.HeadRepository.ID != repositoryID || run.HeadRepository.FullName != repository {
		return e.New(e.CodeUnpinnedInput, "current GitHub producer run differs from exact successful cycle")
	}
	return nil
}

type nightlyGitHubArtifact struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size_in_bytes"`
	Digest      string `json:"digest"`
	Expired     *bool  `json:"expired"`
	WorkflowRun struct {
		ID               int64  `json:"id"`
		RepositoryID     int64  `json:"repository_id"`
		HeadRepositoryID int64  `json:"head_repository_id"`
		HeadBranch       string `json:"head_branch"`
		HeadSHA          string `json:"head_sha"`
	} `json:"workflow_run"`
}

type nightlyArtifactList struct {
	TotalCount int                     `json:"total_count"`
	Artifacts  []nightlyGitHubArtifact `json:"artifacts"`
}

func selectNightlyArtifact(list nightlyArtifactList, name string, build NightlyBuild, repositoryID int64) (nightlyGitHubArtifact, error) {
	if list.TotalCount != 1 || len(list.Artifacts) != 1 {
		return nightlyGitHubArtifact{}, e.New(e.CodeVerifyFailed, "missing or duplicate exact nightly artifact %s", name)
	}
	artifact := list.Artifacts[0]
	if artifact.Name != name || artifact.ID <= 0 || artifact.ID > nightlyMaxJSONInteger || artifact.Size <= 0 || artifact.Size > diagnosticLimit || !canonical.IsDigest(artifact.Digest) || artifact.Expired == nil || *artifact.Expired || artifact.WorkflowRun.ID != build.ProducerRun || artifact.WorkflowRun.RepositoryID != repositoryID || artifact.WorkflowRun.HeadRepositoryID != repositoryID || artifact.WorkflowRun.HeadBranch != "main" || artifact.WorkflowRun.HeadSHA != build.SourceCommit {
		return nightlyGitHubArtifact{}, e.New(e.CodeUnpinnedInput, "nightly artifact origin, digest, expiration or size differs")
	}
	return artifact, nil
}

// The provider digest binds only the exact ZIP blob. It does not authenticate
// the lifecycle Run or publication described by its member. The existing command
// owner caps captured archive bytes at diagnosticLimit before this decoder runs.
func nightlyArchiveMember(ctx context.Context, archive []byte, artifact nightlyGitHubArtifact, memberName string) ([]byte, error) {
	members, err := nightlyArchiveMembers(ctx, archive, artifact, map[string]int64{memberName: 32 << 20})
	return members[memberName], err
}

func nightlyArchiveMembers(ctx context.Context, archive []byte, artifact nightlyGitHubArtifact, expected map[string]int64) (map[string][]byte, error) {
	if err := requireContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "nightly archive context")
	}
	if len(archive) == 0 || len(archive) > diagnosticLimit || int64(len(archive)) != artifact.Size || pipeline.FileDigest(archive) != artifact.Digest {
		return nil, e.New(e.CodeVerifyFailed, "nightly artifact ZIP differs from its exact provider digest or size bound")
	}
	// Reuse archive/zip rather than supplying another archive grammar. The
	// resource bound follows qualificationorigin/handoff_download.go: cap metadata
	// reads before the decoder allocates a complete central-directory collection.
	input := &nightlyZIPReaderAt{ctx: ctx, reader: bytes.NewReader(archive), metadata: true, remaining: 1 << 20}
	reader, err := zip.NewReader(input, int64(len(archive)))
	if err != nil {
		return nil, nightlyArchiveError(ctx, err, "decoding nightly artifact ZIP")
	}
	if len(reader.File) != len(expected) {
		return nil, e.New(e.CodeVerifyFailed, "nightly artifact ZIP differs from its exact selected member set")
	}
	members := make(map[string][]byte, len(expected))
	input.metadata = false
	remaining := int64(32 << 20)
	for _, member := range reader.File {
		limit, ok := expected[member.Name]
		_, repeated := members[member.Name]
		if !ok || repeated || !member.Mode().IsRegular() || member.UncompressedSize64 > uint64(limit) || member.UncompressedSize64 > uint64(remaining) {
			return nil, e.New(e.CodeVerifyFailed, "nightly artifact ZIP member has an unexpected path, type, duplicate or size")
		}
		held, err := member.Open()
		if err != nil {
			return nil, nightlyArchiveError(ctx, err, "opening nightly artifact ZIP member")
		}
		raw, readErr := io.ReadAll(io.LimitReader(held, limit+1))
		if err = errors.Join(readErr, held.Close()); err != nil {
			return nil, nightlyArchiveError(ctx, err, "reading nightly artifact ZIP member")
		}
		if int64(len(raw)) > limit || uint64(len(raw)) != member.UncompressedSize64 {
			return nil, e.New(e.CodeVerifyFailed, "nightly artifact ZIP member exceeds its bound or declared size")
		}
		remaining -= int64(len(raw))
		members[member.Name] = raw
	}
	if err = ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "nightly artifact ZIP canceled")
	}
	return members, nil
}

func nightlyArchiveError(ctx context.Context, err error, operation string) error {
	if cause := ctx.Err(); cause != nil {
		return e.Wrap(e.CodeTimeout, cause, "%s canceled", operation)
	}
	return e.Wrap(e.CodeVerifyFailed, err, "%s", operation)
}

type nightlyZIPReaderAt struct {
	ctx       context.Context
	reader    io.ReaderAt
	metadata  bool
	remaining int64
}

func (reader *nightlyZIPReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if reader.metadata {
		if int64(len(p)) > reader.remaining {
			return 0, e.New(e.CodeVerifyFailed, "nightly artifact ZIP metadata exceeds its read bound")
		}
		reader.remaining -= int64(len(p))
	}
	n, err := reader.reader.ReadAt(p, offset)
	if cause := reader.ctx.Err(); cause != nil {
		return n, cause
	}
	return n, err
}

type nightlyCapture func(context.Context, []string, string, time.Duration) ([]byte, error)

func nightlyCIStorageOptions() cistore.Options {
	return cistore.Options{Token: os.Getenv("NVKR_CI_STORAGE_TOKEN"), Username: os.Getenv("NVKR_CI_STORAGE_USER"), RepositoryToken: os.Getenv("GH_TOKEN")}
}

func newNightlyInputs(ctx context.Context, profile NightlyProfile, build NightlyBuild, plan *factory.Plan, definition lifecycle.Definition, environment lifecycle.Environment, workspace string) (*NightlyInputs, error) {
	if os.Getenv("GITHUB_SERVER_URL") != "https://github.com" || os.Getenv("GITHUB_REF") != "refs/heads/main" || os.Getenv("GH_TOKEN") == "" {
		return nil, e.New(e.CodeEngineCapability, "nightly artifact retrieval requires explicit main-branch GitHub identity and API token")
	}
	runner := Runner{Work: workspace, Env: os.Environ()}
	return newNightlyInputsWithCapture(ctx, profile, build, plan, definition, environment, workspace, os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_REPOSITORY_ID"), os.Getenv("GITHUB_SHA"), os.Getenv("GITHUB_RUN_ID"), os.Getenv("GITHUB_RUN_ATTEMPT"), runner.Capture)
}

func newNightlyInputsWithCapture(ctx context.Context, profile NightlyProfile, build NightlyBuild, plan *factory.Plan, definition lifecycle.Definition, environment lifecycle.Environment, workspace, repository, repositoryID, sha, parentID, parentAttempt string, capture nightlyCapture) (*NightlyInputs, error) {
	if err := requireContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "nightly input context")
	}
	if err := validateNightlyCycle(profile, build, repository, repositoryID, sha, parentID, parentAttempt); err != nil {
		return nil, err
	}
	if plan == nil || capture == nil {
		return nil, e.New(e.CodeUsage, "nightly handoff requires exact compiled Plan and command owner")
	}
	digest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	if plan.Environment.GitHubBundle != profile.Bundle || plan.DefinitionPath != profile.Definition || plan.EnvironmentPath != profile.Environment || plan.Qualification.String() != lifecycle.LanePipeline {
		return nil, e.New(e.CodePipelineDrift, "nightly Plan selection differs from maintained profile")
	}
	if !environment.GitHubRuntimePlan {
		if _, err := lifecycle.PrepareExactInputs(definition, environment, plan); err != nil {
			return nil, err
		}
	}
	directory, info, err := securepath.OpenDirectory(workspace)
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "opening private nightly workspace")
	}
	_ = directory.Close()
	if info.Mode().Perm() != 0o700 || !currentOwner(info) {
		return nil, e.New(e.CodeImmutableViolation, "nightly workspace must be private and owned")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	repoID := mustNightlyID(repositoryID)
	endpoint := "repos/" + url.PathEscape(strings.Split(repository, "/")[0]) + "/" + url.PathEscape(strings.Split(repository, "/")[1]) + "/actions/runs/" + strconv.FormatInt(build.ProducerRun, 10)
	api := func(path, phase string, out any) error {
		raw, callErr := capture(ctx, []string{"gh", "api", "--hostname", "github.com", "-H", "X-GitHub-Api-Version: 2026-03-10", path}, phase, time.Minute)
		if callErr != nil {
			if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
				return e.Wrap(e.CodeTimeout, callErr, "reading exact nightly GitHub record canceled")
			}
			return e.Wrap(e.CodeFetchFailed, callErr, "reading exact nightly GitHub record")
		}
		return nightlyJSON(raw, out, false)
	}
	checkRun := func(phase string) error {
		var run nightlyGitHubRun
		if err := api(endpoint, phase, &run); err != nil {
			return err
		}
		return validateNightlyGitHubRun(run, profile, build, repository, repoID)
	}
	if err = checkRun("nightly-producer-before"); err != nil {
		return nil, err
	}
	names := []string{"nvkr-lifecycle-run", "nvkr-publication-release"}
	if environment.GitHubRuntimePlan {
		names = append(names, "nvkr-runtime-inputs")
	}
	selected := make([]nightlyGitHubArtifact, len(names))
	for i, name := range names {
		var list nightlyArtifactList
		if err = api(endpoint+"/artifacts?per_page=100&name="+url.QueryEscape(name), "nightly-artifact-before-"+strconv.Itoa(i), &list); err != nil {
			return nil, err
		}
		if selected[i], err = selectNightlyArtifact(list, name, build, repoID); err != nil {
			return nil, err
		}
	}
	archives := make([][]byte, len(names))
	for i := range names {
		artifactEndpoint := "repos/" + repository + "/actions/artifacts/" + strconv.FormatInt(selected[i].ID, 10) + "/zip"
		archives[i], err = capture(ctx, []string{"gh", "api", "--hostname", "github.com", "--allow-escape-sequences", "-H", "X-GitHub-Api-Version: 2026-03-10", artifactEndpoint}, "nightly-artifact-download-"+strconv.Itoa(i), 10*time.Minute)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, e.Wrap(e.CodeTimeout, err, "downloading exact nightly artifact canceled")
			}
			return nil, e.Wrap(e.CodeFetchFailed, err, "downloading exact nightly artifact")
		}
	}
	if err = checkRun("nightly-producer-after"); err != nil {
		return nil, err
	}
	for i, name := range names {
		var list nightlyArtifactList
		if err = api(endpoint+"/artifacts?per_page=100&name="+url.QueryEscape(name), "nightly-artifact-after-"+strconv.Itoa(i), &list); err != nil {
			return nil, err
		}
		actual, selectErr := selectNightlyArtifact(list, name, build, repoID)
		if selectErr != nil {
			return nil, selectErr
		}
		if !reflect.DeepEqual(actual, selected[i]) {
			return nil, e.New(e.CodeUnpinnedInput, "artifact identity changed during download")
		}
	}
	memberNames := []string{"run.json", "publication-release.tar"}
	members := make([][]byte, len(memberNames))
	privateOptions := nightlyCIStorageOptions()
	privateRequest := cistore.Request{Repository: repository, RunID: strconv.FormatInt(build.ProducerRun, 10), Attempt: strconv.FormatInt(build.ProducerAttempt, 10)}
	for i := range memberNames {
		privateRequest.Name = names[i]
		var restored map[string][]byte
		restored, err = nightlyTransportMembers(ctx, archives[i], selected[i], map[string]int64{memberNames[i]: 32 << 20}, workspace, privateRequest, privateOptions, cistore.Download)
		if err != nil {
			return nil, err
		}
		members[i] = restored[memberNames[i]]
	}
	harnessCommit := profile.HarnessCommit
	if environment.GitHubRuntimePlan {
		privateRequest.Name = names[2]
		var restored map[string][]byte
		restored, err = nightlyTransportMembers(ctx, archives[2], selected[2], map[string]int64{"plan.json": factory.MaxPlanBytes, "action-data.json": 32 << 20, "manifest.json": 32 << 20}, workspace, privateRequest, privateOptions, cistore.Download)
		if err != nil {
			return nil, err
		}
		plan, err = nightlyRuntimePlanMembers(ctx, restored, profile, definition, environment)
		if err != nil {
			return nil, err
		}
		digest, err = plan.Digest()
		if err != nil {
			return nil, err
		}
		harnessCommit, err = nightlyPlanHarnessCommit(plan, profile)
		if err != nil {
			return nil, err
		}
	}
	var publication factory.Artifact
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.ID == "publication/release" {
			publication = artifact
		}
	}
	if publication.Kind != "published-reference" || publication.Producer != "publish/release" || publication.Target != "release" || !securepath.WorkspaceRelative(publication.Path) {
		return nil, e.New(e.CodePipelineDrift, "nightly Plan lacks exact aggregate publication reference")
	}
	// No artifact destination is created until both provider-pinned blobs and
	// their exact members have been admitted, including the before/after joins.
	downloads := make([]string, len(memberNames))
	for i := range memberNames {
		downloads[i], err = os.MkdirTemp(workspace, "nightly-download-")
		if err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "creating fresh artifact destination")
		}
		if err = privateWrite(filepath.Join(downloads[i], memberNames[i]), members[i]); err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "staging provider-pinned nightly artifact member")
		}
	}
	raw, err := readRunFile(ctx, downloads[0], "run.json", nightlyRecordLimit)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading downloaded lifecycle Run")
	}
	var run lifecycle.Run
	if err = nightlyJSON(raw, &run, true); err != nil {
		return nil, err
	}
	if err = validateNightlyRun(plan, digest, build, &run); err != nil {
		return nil, err
	}
	locatorRoot, err := os.MkdirTemp(workspace, "nightly-locator-")
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating private publication locator root")
	}
	if err = transportarchive.Unpack(ctx, filepath.Join(downloads[1], "publication-release.tar"), locatorRoot, []string{publication.Path}); err != nil {
		return nil, err
	}
	referenceBytes, err := readRunFile(ctx, locatorRoot, publication.Path, 4096)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading Plan-selected publication reference")
	}
	reference := strings.TrimSuffix(string(referenceBytes), "\n")
	at := strings.LastIndex(reference, "@")
	if at < 1 {
		return nil, e.New(e.CodeVerifyFailed, "aggregate reference is not digest-bound")
	}
	subject := sign.Subject{Ref: reference, Digest: reference[at+1:]}
	if err = subject.Validate(); err != nil {
		return nil, err
	}
	if plan.Environment.Publication == nil || reference[:at] != plan.Environment.Publication.RepositoryPrefix+"/releases" {
		return nil, e.New(e.CodeUnpinnedInput, "aggregate reference differs from Plan publication namespace")
	}
	return &NightlyInputs{Reference: reference, Plan: plan, HarnessCommit: harnessCommit, LocatorRoot: locatorRoot, RunPath: filepath.Join(downloads[0], "run.json"), Run: run, ExpectedInvocation: fmt.Sprintf("https://github.com/%s/actions/runs/%d/attempts/%d", repository, build.ProducerRun, build.ProducerAttempt)}, nil
}

func nightlyRuntimePlanMembers(ctx context.Context, members map[string][]byte, profile NightlyProfile, definition lifecycle.Definition, environment lifecycle.Environment) (*factory.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "nightly runtime Plan admission canceled")
	}
	var plan factory.Plan
	if err := canonical.UnmarshalExact(members["plan.json"], &plan); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "decoding same-Run runtime Plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if !environment.GitHubRuntimePlan || plan.Environment.GitHubBundle != profile.Bundle || plan.DefinitionPath != profile.Definition || plan.EnvironmentPath != profile.Environment || plan.Qualification.String() != lifecycle.LanePipeline {
		return nil, e.New(e.CodePipelineDrift, "runtime Plan selection differs from trusted nightly inputs")
	}
	if _, err := lifecycle.PrepareExactInputs(definition, environment, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

type nightlyPrivateDownload func(context.Context, cistore.Request, cistore.Locator, string, cistore.Options) error

// Provider ZIP pins describe transport bytes, not Release authority. A private
// locator delegates payload admission to cistore and never falls back after
// restoration fails; complete publication verification remains mandatory.
func nightlyTransportMembers(ctx context.Context, archive []byte, artifact nightlyGitHubArtifact, expected map[string]int64, workspace string, request cistore.Request, options cistore.Options, download nightlyPrivateDownload) (members map[string][]byte, resultErr error) {
	members, legacyErr := nightlyArchiveMembers(ctx, archive, artifact, expected)
	if legacyErr == nil {
		return members, nil
	}
	locatorMembers, err := nightlyArchiveMembers(ctx, archive, artifact, map[string]int64{artifact.Name + ".ci-locator.json": cistore.MaxLocatorBytes})
	if err != nil {
		return nil, err
	}
	locator, err := cistore.DecodeLocator(locatorMembers[artifact.Name+".ci-locator.json"])
	if err != nil {
		return nil, err
	}
	if err = locator.Validate(request); err != nil {
		return nil, err
	}
	if request.Name != artifact.Name || locator.ArchiveSize > diagnosticLimit {
		return nil, e.New(e.CodeVerifyFailed, "private nightly locator differs from exact artifact origin or payload bound")
	}
	if options.Token == "" || strings.TrimSpace(options.Username) == "" || download == nil {
		return nil, e.New(e.CodeEngineCapability, "private nightly payload requires an explicit storage username and token")
	}
	root, err := os.MkdirTemp(workspace, "nightly-private-")
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating private nightly payload directory")
	}
	defer func() {
		if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
			members = nil
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeImmutableViolation, cleanupErr, "removing private nightly payload directory"))
		}
	}()
	destination := filepath.Join(root, "payload")
	if err = download(ctx, request, locator, destination, options); err != nil {
		return nil, err
	}
	directory, _, err := securepath.OpenDirectory(destination)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening restored private nightly payload")
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(len(expected) + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading private nightly payload membership")
	}
	if len(entries) != len(expected) {
		return nil, e.New(e.CodeVerifyFailed, "private nightly payload differs from exact expected membership")
	}
	members = make(map[string][]byte, len(expected))
	remaining := int64(32 << 20)
	for _, entry := range entries {
		limit, ok := expected[entry.Name()]
		if !ok {
			return nil, e.New(e.CodeVerifyFailed, "private nightly payload contains an unexpected file")
		}
		if limit > remaining {
			limit = remaining
		}
		raw, readErr := readRunFile(ctx, destination, entry.Name(), limit)
		if readErr != nil {
			return nil, nightlyArchiveError(ctx, readErr, "reading private nightly payload")
		}
		remaining -= int64(len(raw))
		members[entry.Name()] = raw
	}
	if err = ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "private nightly payload canceled")
	}
	return members, nil
}

// The current-source suite follows the one exact NVKE revision that produced all
// four public NVKE roles. Mixed product revisions cannot choose a suite by order.
func nightlyPlanHarnessCommit(plan *factory.Plan, profile NightlyProfile) (string, error) {
	if plan == nil {
		return "", e.New(e.CodeUsage, "nightly harness selection requires a Plan")
	}
	roles := map[string]string{"nvkectl": "nvkectl", nightlyControllerOutput(profile): "nvke-controller", "controller-chart": "nvke-controller-chart", "nktp": "nktp-chart"}
	if len(roles) != 4 {
		return "", e.New(e.CodePipelineDrift, "nightly NVKE roles must be disjoint")
	}
	selected := map[string]bool{}
	commit := ""
	for _, build := range plan.Builds {
		component, ok := roles[build.Name]
		if !ok {
			continue
		}
		if selected[build.Name] || len(build.ResolvedComponents) != 1 {
			return "", e.New(e.CodePipelineDrift, "nightly NVKE role has ambiguous producers")
		}
		producer := build.ResolvedComponents[0]
		if producer.Name != component || !producer.FromSource || strings.TrimSuffix(producer.Repo, ".git") != "https://github.com/dsx-ai-factory/nvke" || !nightlyGitCommit(producer.Commit) || (commit != "" && producer.Commit != commit) {
			return "", e.New(e.CodeUnpinnedInput, "nightly NVKE roles do not share one exact source revision")
		}
		selected[build.Name], commit = true, producer.Commit
	}
	if len(selected) != len(roles) {
		return "", e.New(e.CodeProvenanceMissing, "nightly Plan omits a required NVKE source role")
	}
	// The selected products retain a separate candidate entrypoint. This exact diagnostic pair is the
	// reviewed test-only exception; it does not authorize another product pin.
	if commit == "8db5721ae3031bdcd945e559153a740d46ea209e" {
		if profile.HarnessCommit != "a32067108ab89d1e2b2b6720766ffe97270a166a" {
			return "", e.New(e.CodeUnpinnedInput, "selected products require the reviewed candidate harness pin")
		}
		return profile.HarnessCommit, nil
	}
	return commit, nil
}

func validateNightlyRun(plan *factory.Plan, digest string, build NightlyBuild, run *lifecycle.Run) error {
	attempt := fmt.Sprintf("%d-%d", build.ProducerRun, build.ProducerAttempt)
	expected, err := lifecycle.PrepareRun(plan.Definition, plan.Environment, plan, attempt)
	if err != nil {
		return err
	}
	if run == nil || run.SchemaVersion != lifecycle.RunSchemaVersion || run.Status != lifecycle.RunComplete || run.Failure != nil || run.Lane != lifecycle.LanePipeline || run.Attempt != attempt || run.PlanDigest != digest || run.DefinitionDigest != expected.DefinitionDigest || run.EnvironmentDigest != expected.EnvironmentDigest || !reflect.DeepEqual(run.Expected, expected.Expected) {
		return e.New(e.CodeVerifyFailed, "downloaded Run differs from exact frozen nightly Plan")
	}
	if err = factory.ValidateLifecycleOutputs(plan.LifecycleOutputs, run.Observed); err != nil {
		return err
	}
	for _, output := range run.Observed {
		if output.Disk != nil && output.Disk.PlanDigest != digest {
			return e.New(e.CodeVerifyFailed, "nightly disk observation differs from Plan")
		}
	}
	content, err := delivery.ContentDigest(run.Observed)
	if err != nil {
		return err
	}
	if run.ContentSubject != content {
		return e.New(e.CodeVerifyFailed, "nightly Run content subject differs from complete observations")
	}
	return nil
}
