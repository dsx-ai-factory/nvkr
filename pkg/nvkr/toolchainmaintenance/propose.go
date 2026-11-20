// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

var reviewRunPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
var appSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func allowedReviewPath(path string) bool {
	if path == "" || !utf8.ValidString(path) || !filepath.IsLocal(path) || strings.Contains(path, `\`) {
		return false
	}
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if p == ".." || p == "." || p == "" {
			return false
		}
	}
	if path == producerOverlayPolicy || path == localToolchainBindings {
		return true
	}
	for _, prefix := range []string{"inputs/catalogs/", "inputs/environments/", "inputs/experiments/", "docs/examples/"} {
		if strings.HasPrefix(path, prefix) {
			return (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".json")) && !strings.HasSuffix(path, "/pins.lock.yaml")
		}
	}
	if len(parts) >= 3 && parts[0] == ".github" && strings.HasPrefix(parts[1], "nvkr-") {
		return true
	}
	return strings.HasPrefix(path, ".github/workflows/nvkr-") || path == ".github/workflows/catalog-refresh.yaml"
}

type gitObject struct {
	SHA string `json:"sha"`
}
type gitReference struct {
	Ref    string    `json:"ref"`
	Object gitObject `json:"object"`
}
type reviewEntry struct {
	Path string  `json:"path"`
	Mode string  `json:"mode"`
	Type string  `json:"type"`
	SHA  *string `json:"sha"`
}
type reviewFile struct {
	entry   reviewEntry
	content []byte
	deleted bool
}

func (c reviewClient) git(ctx context.Context, args ...string) (string, error) {
	raw, err := c.invoke(ctx, append([]string{"git"}, args...), nil)
	return string(raw), err
}
func (c reviewClient) candidateFiles(ctx context.Context) ([]reviewFile, error) {
	changed, err := c.git(ctx, "diff", "--no-renames", "--name-only", "-z", "HEAD")
	if err != nil {
		return nil, err
	}
	untracked, err := c.git(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, path := range strings.Split(changed, "\x00") {
		if path != "" {
			names[path] = true
		}
	}
	for _, path := range strings.Split(untracked, "\x00") {
		if path != "" && !strings.HasPrefix(path, ".nvkr-inputs/source/") {
			names[path] = true
		}
	}
	paths := make([]string, 0, len(names))
	for path := range names {
		if !allowedReviewPath(path) {
			return nil, e.New(e.CodeGateUnmet, "candidate changes a path outside authored pins/generated bundles")
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	files := make([]reviewFile, 0, len(paths))
	// Admit the entire local candidate before creating any remote blob/tree.
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "read review candidate")
		}
		file := reviewFile{entry: reviewEntry{Path: path, Mode: "100644", Type: "blob"}}
		filename := filepath.Join(c.root, path)
		info, err := os.Lstat(filename)
		if os.IsNotExist(err) {
			file.deleted = true
			files = append(files, file)
			continue
		}
		if err != nil {
			return nil, e.Wrap(e.CodeGateUnmet, err, "inspect review candidate")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, e.New(e.CodeGateUnmet, "candidate symlinks are forbidden")
		}
		if !info.Mode().IsRegular() || info.Size() > maxReviewBytes {
			return nil, e.New(e.CodeGateUnmet, "candidate file is not a bounded regular file")
		}
		held, err := heldfile.OpenContext(ctx, filename, heldfile.Options{Label: "review candidate", MaxBytes: maxReviewBytes})
		if err != nil {
			return nil, e.Wrap(e.CodeGateUnmet, err, "open review candidate")
		}
		raw, readErr := held.ReadAllContext(ctx, maxReviewBytes)
		closeErr := held.Close()
		if readErr != nil {
			return nil, e.Wrap(e.CodeGateUnmet, readErr, "read review candidate")
		}
		if closeErr != nil {
			return nil, e.Wrap(e.CodeGateUnmet, closeErr, "close review candidate")
		}
		file.content = raw
		if info.Mode().Perm()&0o111 != 0 {
			file.entry.Mode = "100755"
		}
		files = append(files, file)
	}
	return files, nil
}

// Propose publishes only the already checked candidate diff on a review branch.
// It never updates main, force-pushes, reopens a closed proposal, or merges.
func Propose(ctx context.Context, root string) (string, error) {
	if err := hosted(); err != nil {
		return "", err
	}
	return propose(ctx, newReviewClient(root), os.Getenv)
}
func propose(ctx context.Context, c reviewClient, getenv func(string) string) (string, error) {
	source, run, slug := getenv("GITHUB_SHA"), getenv("APPROVAL_RUN"), getenv("APP_SLUG")
	if getenv("GITHUB_REPOSITORY") != Repository || getenv("GITHUB_REF") != "refs/heads/main" || getenv("GITHUB_WORKFLOW_REF") != Repository+"/"+Workflow+"@refs/heads/main" || !commitPattern.MatchString(source) || !reviewRunPattern.MatchString(run) || !appSlugPattern.MatchString(slug) {
		return "", e.New(e.CodeGateUnmet, "review publication requires the admitted main workflow")
	}
	head, err := c.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(head) != source {
		return "", e.New(e.CodeGateUnmet, "checkout differs from triggering main revision")
	}
	var main gitReference
	if err := c.repoAPI(ctx, "git/ref/heads/main", nil, &main); err != nil {
		return "", err
	}
	if main.Object.SHA != source {
		return "", e.New(e.CodeGateUnmet, "main advanced; retry maintenance against current main")
	}
	files, err := c.candidateFiles(ctx)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "Selected toolchain pins and generated bundles already match.", nil
	}
	branch := "automation/toolchain-refresh-" + run
	var refs []gitReference
	if err := c.repoAPI(ctx, "git/matching-refs/heads/"+branch, nil, &refs); err != nil {
		return "", err
	}
	if refs == nil {
		return "", e.New(e.CodeGateUnmet, "missing review branch lookup")
	}
	var matches []gitReference
	for _, ref := range refs {
		if ref.Ref == "refs/heads/"+branch {
			matches = append(matches, ref)
		}
	}
	if len(matches) > 1 || len(refs) >= 1000 {
		return "", e.New(e.CodeGateUnmet, "ambiguous review branch")
	}
	entries := make([]reviewEntry, 0, len(files))
	for _, file := range files {
		if !file.deleted {
			var blob gitObject
			if err := c.repoAPI(ctx, "git/blobs", map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString(file.content)}, &blob); err != nil {
				return "", err
			}
			if blob.SHA == "" {
				return "", e.New(e.CodeGateUnmet, "missing review blob identity")
			}
			file.entry.SHA = &blob.SHA
		}
		entries = append(entries, file.entry)
	}
	baseTree, err := c.git(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	var tree gitObject
	if err := c.repoAPI(ctx, "git/trees", map[string]any{"base_tree": strings.TrimSpace(baseTree), "tree": entries}, &tree); err != nil {
		return "", err
	}
	if tree.SHA == "" {
		return "", e.New(e.CodeGateUnmet, "missing review tree identity")
	}
	if len(matches) > 0 {
		var commit struct {
			Tree gitObject `json:"tree"`
		}
		if err := c.repoAPI(ctx, "git/commits/"+matches[0].Object.SHA, nil, &commit); err != nil {
			return "", err
		}
		if commit.Tree.SHA != tree.SHA {
			return "", e.New(e.CodeGateUnmet, "existing review branch differs; retain it for owner review instead of force pushing")
		}
	} else {
		var bot struct {
			ID int64 `json:"id"`
		}
		if err := c.api(ctx, "users/"+slug+"%5Bbot%5D", nil, &bot); err != nil {
			return "", err
		}
		if bot.ID <= 0 {
			return "", e.New(e.CodeGateUnmet, "invalid review App bot identity")
		}
		identity := fmt.Sprintf("%s[bot] <%d+%s[bot]@users.noreply.github.com>", slug, bot.ID, slug)
		var commit gitObject
		if err := c.repoAPI(ctx, "git/commits", map[string]any{"message": "build(toolchains): refresh native candidate images\n\nSigned-off-by: " + identity, "tree": tree.SHA, "parents": []string{source}}, &commit); err != nil {
			return "", err
		}
		if commit.SHA == "" {
			return "", e.New(e.CodeGateUnmet, "missing review commit identity")
		}
		var response json.RawMessage
		if err := c.repoAPI(ctx, "git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": commit.SHA}, &response); err != nil {
			return "", err
		}
	}
	type pull struct {
		URL  string `json:"html_url"`
		Head struct {
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	}
	var pulls []pull
	if err := c.repoAPI(ctx, "pulls?state=all&base=main&head=dsx-ai-factory:"+branch+"&per_page=100", nil, &pulls); err != nil {
		return "", err
	}
	if pulls == nil {
		return "", e.New(e.CodeGateUnmet, "missing pull request lookup")
	}
	if len(pulls) >= 100 {
		return "", e.New(e.CodeGateUnmet, "pull request lookup may be incomplete")
	}
	owned := []pull{}
	for _, p := range pulls {
		if p.Head.Repo != nil && p.Head.Repo.FullName == Repository {
			owned = append(owned, p)
		}
	}
	if len(owned) > 1 {
		return "", e.New(e.CodeGateUnmet, "ambiguous maintenance pull request")
	}
	if len(owned) > 0 {
		if owned[0].URL == "" {
			return "", e.New(e.CodeGateUnmet, "missing review URL")
		}
		return owned[0].URL, nil
	}
	raw, err := read(ctx, filepath.Join(c.root, ApprovalDirectory, run+".json"))
	if err != nil {
		return "", err
	}
	var record Record
	if err := decode(raw, &record); err != nil {
		return "", err
	}
	subjects := []string{}
	for _, image := range record.Images {
		subjects = append(subjects, fmt.Sprintf("- %s/%s: `%s`", image.Arch, image.Kind, image.ManifestDigest))
	}
	slices.Sort(subjects)
	url := "https://github.com/" + Repository + "/actions/runs/" + getenv("GITHUB_RUN_ID")
	body := fmt.Sprintf("Refresh both native Packer and inspector pins and their dependent generated bundles.\n\nPreparation: https://github.com/%s/actions/runs/%s\nPublication, independent-reader verification and native pull checks: %s\nSource: `%s`. Reviewed exceptions: `%s/%s.json`.\n\n%s\n\nCandidate-only license/security exceptions remain scoped to these subjects. General organizational approval and qualified reproduction are not asserted. Generation and drift checks passed before this proposal. Roll back by reverting this pin change and regenerating; old registry subjects are retained.", Repository, run, url, record.SourceCommit, ApprovalDirectory, run, strings.Join(subjects, "\n"))
	var created pull
	if err := c.repoAPI(ctx, "pulls", map[string]any{"head": branch, "base": "main", "title": "build(toolchains): refresh native candidate images", "body": body, "draft": false}, &created); err != nil {
		return "", err
	}
	if created.URL == "" {
		return "", e.New(e.CodeGateUnmet, "missing created review URL")
	}
	return created.URL, nil
}
