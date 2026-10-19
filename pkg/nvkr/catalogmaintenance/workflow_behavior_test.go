// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

// stepScript renders the workflow and returns one step's shell, so the tests run
// the exact bytes an operator installs against fixtures instead of describing them.
func stepScript(t *testing.T, job, name string) string {
	t.Helper()
	cfg, _ := maintenanceFixture(t)
	enroll(t, cfg)
	raw, err := Workflow(t.Context(), WorkflowOptions{Schedule: DefaultSchedule}, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `json:"name"`
				Run  string `json:"run"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, step := range workflow.Jobs[job].Steps {
		if step.Name == name {
			return step.Run
		}
	}
	t.Fatalf("no step %q in job %q", name, job)
	return ""
}

// requireShellToolsVariable makes the rendered-step tools mandatory. CI is set on GitHub-hosted runners.
const requireShellToolsVariable = "NVKR_REQUIRE_SHELL_TOOLS"

// requireTools skips a rendered-step test when a tool is missing locally, but fails it when the tools are
// mandatory, so a gate without them cannot pass by skipping the step contracts.
func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	mandatory := os.Getenv(requireShellToolsVariable) == "1" || os.Getenv("CI") != ""
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			if mandatory {
				t.Fatalf("%s is required to run the rendered workflow steps (%s=1 or CI is set)", tool, requireShellToolsVariable)
			}
			t.Skipf("%s is required to run the rendered workflow steps; set %s=1 to make this a failure", tool, requireShellToolsVariable)
		}
	}
}

func runScript(t *testing.T, dir, script string, env ...string) (string, error) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "bash", "-c", script)
	command.Dir = dir
	command.Env = append(os.Environ(), env...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestSelectStepHandlesEveryArtifactLayout(t *testing.T) {
	requireTools(t, "bash", "jq", "find", "grep")
	script := stepScript(t, "select", "List the refreshed Definitions")
	for _, tc := range []struct {
		name      string
		artifacts map[string][2]string // directory ("" is the download root) -> changed, definition ("-" writes no marker)
		want      string
		fails     bool
	}{
		{"no artifacts", nil, "[]", false},
		{"one changed artifact extracted directly", map[string][2]string{"": {"true", "alpha"}}, `[{"definition":"alpha","changed":true}]`, false},
		{"one unchanged artifact extracted directly", map[string][2]string{"": {"false", "alpha"}}, `[{"definition":"alpha","changed":false}]`, false},
		{"one changed of two", map[string][2]string{"catalog-candidate-alpha": {"false", "alpha"}, "catalog-candidate-beta": {"true", "beta"}},
			`[{"definition":"alpha","changed":false},{"definition":"beta","changed":true}]`, false},
		{"both changed", map[string][2]string{"catalog-candidate-alpha": {"true", "alpha"}, "catalog-candidate-beta": {"true", "beta"}},
			`[{"definition":"alpha","changed":true},{"definition":"beta","changed":true}]`, false},
		{"unexpected marker", map[string][2]string{"": {"maybe", "alpha"}}, "", true},
		{"missing Definition marker", map[string][2]string{"": {"true", "-"}}, "", true},
		{"empty Definition marker", map[string][2]string{"": {"true", ""}}, "", true},
		{"Definition marker with a path", map[string][2]string{"": {"true", "../alpha"}}, "", true},
		{"Definition marker with a ref separator", map[string][2]string{"": {"true", "a/b"}}, "", true},
		{"uppercase Definition marker", map[string][2]string{"": {"true", "Alpha"}}, "", true},
		{"leading hyphen", map[string][2]string{"": {"true", "-alpha"}}, "", true},
		{"two-line Definition marker", map[string][2]string{"": {"true", "alpha\nbeta"}}, "", true},
		{"one bad marker among good ones", map[string][2]string{"catalog-candidate-alpha": {"true", "alpha"}, "catalog-candidate-beta": {"true", "Beta"}}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			temp := t.TempDir()
			results := filepath.Join(temp, "results")
			if err := os.MkdirAll(results, 0o700); err != nil {
				t.Fatal(err)
			}
			for dir, files := range tc.artifacts {
				if err := os.MkdirAll(filepath.Join(results, dir), 0o700); err != nil {
					t.Fatal(err)
				}
				for name, value := range map[string]string{"changed": files[0], "definition": files[1]} {
					if name == "definition" && value == "-" {
						continue
					}
					if err := os.WriteFile(filepath.Join(results, dir, name), []byte(value+"\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			out := filepath.Join(temp, "output")
			output, err := runScript(t, temp, script, "RUNNER_TEMP="+temp, "GITHUB_OUTPUT="+out)
			if tc.fails {
				if err == nil {
					t.Fatalf("accepted an unexpected marker: %s", output)
				}
				if got, _ := os.ReadFile(out); len(got) != 0 {
					t.Fatalf("a refused result still selected legs: %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %s", err, output)
			}
			got, err := os.ReadFile(out)
			if err != nil || strings.TrimSpace(string(got)) != "legs="+tc.want {
				t.Fatalf("output %q, want legs=%s (%v)", got, tc.want, err)
			}
		})
	}
}

func TestDiscoverStepRequiresExactlyOnePolicy(t *testing.T) {
	requireTools(t, "bash", "jq", "find", "grep", "sed", "sort")
	script := stepScript(t, "discover", "List the maintenance policy")
	for _, tc := range []struct {
		name  string
		names []string
		want  string
	}{
		{"none", nil, ""},
		{"one", []string{"alpha"}, `definitions=["alpha"]`},
		{"two", []string{"alpha", "beta"}, ""},
		{"unsupported name", []string{"Bad_Name"}, ""},
		// Discovery is one level deep, like the renderer's glob, and a hidden name is unsupported in both.
		{"only a nested policy", []string{"nested/alpha"}, ""},
		{"one beside a nested policy", []string{"alpha", "nested/beta"}, `definitions=["alpha"]`},
		{"hidden name", []string{".hidden"}, ""},
		{"one beside a hidden policy", []string{"alpha", ".hidden"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			temp := t.TempDir()
			if err := os.MkdirAll(filepath.Join(temp, PolicyDirectory), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.names {
				path := filepath.Join(temp, PolicyDirectory, name+PolicySuffix)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(temp, "output")
			output, err := runScript(t, temp, script, "GITHUB_OUTPUT="+out)
			got, _ := os.ReadFile(out)
			if tc.want == "" {
				if err == nil || len(got) != 0 {
					t.Fatalf("accepted %v: %s", tc.names, output)
				}
				return
			}
			if err != nil || strings.TrimSpace(string(got)) != tc.want {
				t.Fatalf("%v: %s %q", err, output, got)
			}
		})
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestApplyStepAdmitsOnlyGeneratedRegularFiles(t *testing.T) {
	requireTools(t, "bash", "git", "yq")
	script := stepScript(t, "pull-request", "Apply and check the candidate patch")
	write := func(repo, name, content string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(repo string)
		ok     bool
	}{
		{"lock and generated files", func(r string) {
			write(r, "cat/pins.lock.yaml", "new\n", 0o644)
			write(r, ".github/nvkr-run/plan.json", "new\n", 0o644)
			write(r, ".github/workflows/nvkr-run.yml", "new\n", 0o644)
		}, true},
		{"generated deletion", func(r string) {
			if err := os.Remove(filepath.Join(r, ".github/nvkr-run/old.json")); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"unexpected file", func(r string) { write(r, "README.md", "new\n", 0o644) }, false},
		{"lock of another catalog", func(r string) { write(r, "other/pins.lock.yaml", "new\n", 0o644) }, false},
		{"another workflow", func(r string) { write(r, ".github/workflows/other.yml", "new\n", 0o644) }, false},
		{"sibling of the generated directory", func(r string) { write(r, ".github/nvkr-runX/a", "new\n", 0o644) }, false},
		{"the policy itself", func(r string) { write(r, PolicyDirectory+"/alpha"+PolicySuffix, "catalog: other\n", 0o644) }, false},
		{"documentation", func(r string) { write(r, "docs/x", "new\n", 0o644) }, false},
		{"executable generated file", func(r string) { write(r, ".github/nvkr-run/plan.json", "new\n", 0o755) }, false},
		{"symlink", func(r string) {
			if err := os.Symlink("/etc/passwd", filepath.Join(r, ".github/nvkr-run/link")); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init", "-q")
			write(repo, PolicyDirectory+"/alpha"+PolicySuffix, "catalog: cat\n", 0o644)
			write(repo, "cat/pins.lock.yaml", "old\n", 0o644)
			write(repo, ".github/nvkr-run/plan.json", "old\n", 0o644)
			write(repo, ".github/nvkr-run/old.json", "old\n", 0o644)
			write(repo, ".github/workflows/nvkr-run.yml", "old\n", 0o644)
			write(repo, "README.md", "old\n", 0o644)
			git(t, repo, "add", "-A")
			git(t, repo, "commit", "-q", "-m", "base")
			tc.change(repo)
			git(t, repo, "add", "-A")
			patch := git(t, repo, "diff", "--cached", "--binary")
			git(t, repo, "reset", "-q", "--hard")
			git(t, repo, "clean", "-fdq")
			temp := t.TempDir()
			if err := os.MkdirAll(filepath.Join(temp, "candidate"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(temp, "candidate", "candidate.patch"), []byte(patch), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := runScript(t, repo, script, "RUNNER_TEMP="+temp, "DEFINITION=alpha")
			if tc.ok != (err == nil) {
				t.Fatalf("admitted=%v, want %v: %s", err == nil, tc.ok, output)
			}
		})
	}
}

func TestDefinitionNameStepRefusesNamesThatCouldReachAPathOrARef(t *testing.T) {
	requireTools(t, "bash")
	script := stepScript(t, "pull-request", "Check the Definition name")
	for _, tc := range []struct {
		name, definition string
		ok               bool
	}{
		{"plain", "alpha", true},
		{"digits and hyphen", "nvke-m2", true},
		{"empty", "", false},
		{"traversal", "../x", false},
		{"leading hyphen", "-alpha", false},
		{"uppercase", "Alpha", false},
		{"space", "a b", false},
		{"slash", "a/b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runScript(t, t.TempDir(), script, "DEFINITION="+tc.definition)
			if tc.ok != (err == nil) {
				t.Fatalf("admitted=%v, want %v: %s", err == nil, tc.ok, output)
			}
			if !tc.ok && !strings.Contains(output, "unsupported Definition name") {
				t.Fatalf("the refusal must say why: %s", output)
			}
		})
	}
}

// publishRepo returns a repository whose index holds an added, a modified and a deleted file over a base commit.
func publishRepo(t *testing.T) (repo, baseTree string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init", "-q")
	for name, content := range map[string]string{"cat/pins.lock.yaml": "old\n", ".github/nvkr-run/old.json": "old\n"} {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "base")
	baseTree = strings.TrimSpace(git(t, repo, "rev-parse", "HEAD^{tree}"))
	if err := os.WriteFile(filepath.Join(repo, "cat/pins.lock.yaml"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".github/nvkr-run/plan.json"), []byte("plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, ".github/nvkr-run/old.json")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "-A")
	return repo, baseTree
}

// ghStub answers like the GitHub CLI and API, recording each call and its request body. FAIL names a
// substring of the one call that must fail. Answers are JSON documents from the scenario's environment, and
// each call's own --jq filter runs over them, so the tests exercise the step's real filters:
// PRS lists pull requests ({number, state, isCrossRepository, mergedAt, headRefOid}), COMMITS maps a commit SHA
// to its API document, and REFS lists the matching refs.
const ghStub = `set -o pipefail
echo "gh $*" >> "$LOG"
case "$*" in *"$FAIL"*) [ -n "$FAIL" ] && exit 1 ;; esac
filter= state= sha= prev=
for arg in "$@"; do
  case "$prev" in --jq) filter=$arg ;; --state) state=$arg ;; esac
  case "$arg" in */git/commits/*) sha=${arg##*/} ;; esac
  prev=$arg
done
case "$*" in *"--input -"*) body=$(cat | jq -c .); echo "body $body" >> "$LOG" ;; esac
answer() { if [ -n "$filter" ]; then jq -r "$filter"; else cat; fi; }
case "$*" in
  "pr list"*) jq -c --arg state "$state" '[.[] | select(if $state == "open" then .state == "OPEN" elif $state == "closed" then .state != "OPEN" else true end)]' <<< "${PRS:-[]}" | answer ;;
  "pr "*) ;;
  *git/blobs*) n=$(wc -l < "$LOG"); echo "{\"sha\":\"blob-$n\"}" | answer ;;
  *git/trees*) echo '{"sha":"tree-sha"}' | answer ;;
  *git/commits/*) jq -c --arg sha "$sha" '.[$sha] // error("no commit " + $sha)' <<< "${COMMITS:-{\}}" | answer ;;
  *git/commits*) echo '{"sha":"commit-sha"}' | answer ;;
  *matching-refs*) jq -c . <<< "${REFS:-[]}" | answer ;;
esac
`

// runWithGH runs a rendered step with the stub as gh and returns the call log.
func runWithGH(t *testing.T, dir, script string, env ...string) (log, output string, err error) {
	t.Helper()
	temp := t.TempDir()
	bin := filepath.Join(temp, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/bash\n"+ghStub), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(temp, "log")
	output, err = runScript(t, dir, script, append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "LOG=" + logPath, "RUNNER_TEMP=" + temp,
		"DEFINITION=alpha", "GH_REPO=owner/repo", "BASE_BRANCH=main", "BASE_SHA=basesha", "BOT_NAME=release-bot[bot]",
		"BOT_EMAIL=123+release-bot[bot]@users.noreply.github.com"}, env...)...)
	raw, _ := os.ReadFile(logPath)
	return string(raw), output, err
}

// calls returns the gh invocations in the log, without the recorded request bodies.
func calls(log string) []string {
	var result []string
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "gh ") {
			result = append(result, line)
		}
	}
	return result
}

// pr is one pull request as gh pr list reports it; a fork's head branch is in another repository.
type pr struct {
	Number            int     `json:"number"`
	State             string  `json:"state"`
	IsCrossRepository bool    `json:"isCrossRepository"`
	MergedAt          *string `json:"mergedAt"`
	HeadRefOid        string  `json:"headRefOid"`
}

// fullPage is a lookup page as long as the step's limit, all fork pull requests, so the bot's own pull
// request could lie beyond it; the step must fail rather than filter a possibly truncated page.
func fullPage(state string) []pr {
	page := make([]pr, 1000)
	for i := range page {
		page[i] = pr{Number: 1000 + i, State: state, IsCrossRepository: true, HeadRefOid: "forksha"}
	}
	return page
}

func ghJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// refs answers the branch lookup: the bot branch at head, or no branch.
func refs(head string) []map[string]any {
	if head == "" {
		return []map[string]any{}
	}
	return []map[string]any{{"ref": "refs/heads/catalog-maintenance/alpha", "object": map[string]string{"sha": head}}}
}

// commit is an API commit document holding tree on parent.
func commit(tree, parent string) map[string]any {
	return map[string]any{"tree": map[string]string{"sha": tree}, "parents": []map[string]string{{"sha": parent}}}
}

func TestPublishStepCreatesTheCommitThroughTheAPIAndChoosesPullRequestAction(t *testing.T) {
	requireTools(t, "bash", "base64", "tr", "git", "jq")
	script := stepScript(t, "pull-request", "Open or update the pull request")
	const token = "ghs_secrettoken"
	merged := "2026-01-01T00:00:00Z"
	// Commits: head is the branch commit; "candidate" holds this tree on the base commit, the others do not.
	type scenario struct {
		name, head, fail       string
		held                   string // what the branch commit holds: candidate, othertree or otherbase
		prs                    []pr
		wantCalls, wantNoCalls []string
		wantErr                bool
	}
	fork := pr{Number: 9, State: "OPEN", IsCrossRepository: true, HeadRefOid: "forksha"}
	forkClosedCandidate := pr{Number: 8, State: "CLOSED", IsCrossRepository: true, HeadRefOid: "headsha"}
	for _, tc := range []scenario{
		{name: "new branch and pull request", wantCalls: []string{"POST repos/owner/repo/git/refs", "gh pr create --head catalog-maintenance/alpha"}, wantNoCalls: []string{"PATCH", "gh pr edit"}},
		{name: "stale branch and open pull request", head: "headsha", held: "othertree", prs: []pr{{Number: 7, State: "OPEN"}},
			wantCalls: []string{"PATCH repos/owner/repo/git/refs/heads/catalog-maintenance/alpha", "gh pr edit 7 --title"}, wantNoCalls: []string{"gh pr create", "--method POST repos/owner/repo/git/refs"}},
		{name: "branch on another base commit", head: "headsha", held: "otherbase", prs: []pr{{Number: 7, State: "OPEN"}},
			wantCalls: []string{"PATCH repos/owner/repo/git/refs/heads/catalog-maintenance/alpha", "gh pr edit 7"}},
		{name: "stale branch and a closed pull request for an older candidate", head: "headsha", held: "othertree", prs: []pr{{Number: 6, State: "CLOSED", HeadRefOid: "oldsha"}},
			wantCalls: []string{"PATCH", "gh pr create"}},
		{name: "current branch and open pull request", head: "headsha", held: "candidate", prs: []pr{{Number: 7, State: "OPEN"}}, wantNoCalls: []string{"--method", "gh pr edit", "gh pr create"}},
		{name: "current branch and a pull request closed for this candidate", head: "headsha", held: "candidate", prs: []pr{{Number: 6, State: "CLOSED", HeadRefOid: "headsha"}},
			wantNoCalls: []string{"--method", "gh pr edit", "gh pr create"}},
		// A maintainer closed the pull request and deleted its branch. GitHub keeps the closed head commit, so the
		// declined candidate is recognized before any write and is not recreated.
		{name: "deleted branch and a pull request closed for this candidate", prs: []pr{{Number: 6, State: "CLOSED", HeadRefOid: "closedsha"}},
			wantCalls: []string{"git/commits/closedsha"}, wantNoCalls: []string{"--method", "gh pr edit", "gh pr create"}},
		{name: "moved branch and a pull request closed for this candidate", head: "headsha", held: "othertree", prs: []pr{{Number: 6, State: "CLOSED", HeadRefOid: "closedsha"}},
			wantNoCalls: []string{"--method", "gh pr edit", "gh pr create"}},
		// An older candidate's pull request was closed; a later run moved the branch to this candidate and was
		// canceled before it opened a pull request. The retry must open one.
		{name: "retry after an interrupted run with an older closed pull request", head: "headsha", held: "candidate", prs: []pr{{Number: 6, State: "CLOSED", HeadRefOid: "oldsha"}},
			wantCalls: []string{"gh pr create"}, wantNoCalls: []string{"--method", "gh pr edit"}},
		{name: "current branch whose pull request was merged", head: "headsha", held: "candidate", prs: []pr{{Number: 6, State: "MERGED", MergedAt: &merged, HeadRefOid: "headsha"}},
			wantCalls: []string{"gh pr create"}},
		{name: "current branch whose pull request was never opened", head: "headsha", held: "candidate", wantCalls: []string{"gh pr create"}, wantNoCalls: []string{"--method", "gh pr edit"}},
		{name: "a newer fork pull request beside the open bot pull request", head: "headsha", held: "othertree", prs: []pr{fork, {Number: 7, State: "OPEN"}},
			wantCalls: []string{"gh pr edit 7"}, wantNoCalls: []string{"gh pr edit 9", "gh pr create"}},
		{name: "only a fork pull request is open", prs: []pr{fork}, wantCalls: []string{"gh pr create"}, wantNoCalls: []string{"gh pr edit"}},
		{name: "a fork pull request closed for this candidate", head: "headsha", held: "candidate", prs: []pr{forkClosedCandidate}, wantCalls: []string{"gh pr create"}},
		{name: "a full page of open pull requests", prs: fullPage("OPEN"), wantErr: true, wantNoCalls: []string{"--method", "gh pr create", "gh pr edit"}},
		{name: "a full page of closed pull requests", prs: fullPage("CLOSED"), wantErr: true, wantNoCalls: []string{"--method", "gh pr create", "gh pr edit"}},
		{name: "branch lookup failure", fail: "matching-refs", wantErr: true},
		{name: "held commit lookup failure", head: "headsha", held: "candidate", fail: "git/commits/headsha", wantErr: true},
		{name: "blob failure", fail: "git/blobs", wantErr: true},
		{name: "tree failure", fail: "git/trees", wantErr: true},
		{name: "commit failure", fail: "POST repos/owner/repo/git/commits", wantErr: true},
		{name: "ref creation failure", fail: "POST repos/owner/repo/git/refs", wantErr: true},
		{name: "ref update failure", head: "headsha", held: "othertree", fail: "git/refs/heads", wantErr: true},
		{name: "open pull request lookup failure", fail: "--state open", wantErr: true},
		{name: "closed pull request lookup failure", head: "headsha", held: "candidate", fail: "--state closed", wantErr: true},
		{name: "closed pull request commit lookup failure", head: "headsha", held: "candidate", prs: []pr{{Number: 6, State: "CLOSED", HeadRefOid: "oldsha"}}, fail: "git/commits/oldsha", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, baseTree := publishRepo(t)
			tree := strings.TrimSpace(git(t, repo, "write-tree"))
			commits := map[string]any{"oldsha": commit("oldtree", "basesha"), "forksha": commit(tree, "basesha"), "closedsha": commit(tree, "basesha")}
			switch tc.held {
			case "candidate":
				commits["headsha"] = commit(tree, "basesha")
			case "othertree":
				commits["headsha"] = commit("othertree", "basesha")
			case "otherbase":
				commits["headsha"] = commit(tree, "otherbase")
			}
			prs := tc.prs
			if prs == nil {
				prs = []pr{}
			}
			log, output, err := runWithGH(t, repo, script, "GH_TOKEN="+token, "REFS="+ghJSON(t, refs(tc.head)), "COMMITS="+ghJSON(t, commits),
				"PRS="+ghJSON(t, prs), "FAIL="+tc.fail)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v: %s\n%s", err, output, log)
			}
			for _, want := range tc.wantCalls {
				if !strings.Contains(log, want) {
					t.Fatalf("missing %q in %s", want, log)
				}
			}
			for _, unwanted := range tc.wantNoCalls {
				for _, call := range calls(log) {
					if strings.Contains(call, unwanted) && !strings.Contains(call, "git/commits/") {
						t.Fatalf("unexpected %q in %s", unwanted, log)
					}
				}
			}
			if strings.Contains(log, token) || strings.Contains(output, token) || strings.Contains(log, "git push") {
				t.Fatalf("the token reached an argument or the branch did not move through the API: %s", log)
			}
			if tc.wantErr {
				// A failure stops the step at the failing call: nothing later runs, and nothing falls through to another write.
				all := calls(log)
				if last := all[len(all)-1]; !strings.Contains(last, tc.fail) {
					t.Fatalf("the step went on after the failing %q call: %s", tc.fail, log)
				}
				return
			}
			if !strings.Contains(log, "git/blobs") {
				return // a current branch makes no commit
			}
			var treeBody, commitBody, refBody string
			for _, line := range strings.Split(log, "\n") {
				if !strings.HasPrefix(line, "body ") {
					continue
				}
				switch body := strings.TrimPrefix(line, "body "); {
				case strings.Contains(body, `"tree"`) && strings.Contains(body, `"base_tree"`):
					treeBody = body
				case strings.Contains(body, `"parents"`):
					commitBody = body
				case strings.Contains(body, `"sha":"commit-sha"`):
					refBody = body
				}
			}
			for _, want := range []string{`"base_tree":"` + baseTree + `"`, `"path":"cat/pins.lock.yaml"`, `"path":".github/nvkr-run/plan.json"`,
				`"path":".github/nvkr-run/old.json","mode":"100644","type":"blob","sha":null`} {
				if !strings.Contains(treeBody, want) {
					t.Fatalf("tree request omits %s: %s", want, treeBody)
				}
			}
			if strings.Count(treeBody, `"sha":"blob-`) != 2 {
				t.Fatalf("want one blob per added or modified file: %s", treeBody)
			}
			for _, want := range []string{`"parents":["basesha"]`, `"tree":"tree-sha"`,
				`Signed-off-by: release-bot[bot] <123+release-bot[bot]@users.noreply.github.com>`, `chore(catalog): refresh alpha pins`} {
				if !strings.Contains(commitBody, want) {
					t.Fatalf("commit request omits %s: %s", want, commitBody)
				}
			}
			if strings.Contains(commitBody, `"author"`) || strings.Contains(commitBody, `"committer"`) {
				t.Fatalf("an author or committer would stop GitHub signing the commit: %s", commitBody)
			}
			if (tc.head != "") != strings.Contains(refBody, `"force":true`) {
				t.Fatalf("force must be set exactly when the branch exists: %s", refBody)
			}
			if strings.Index(log, "git/blobs") > strings.Index(log, "git/trees") || strings.Index(log, "git/trees") > strings.Index(log, "--method POST repos/owner/repo/git/commits") ||
				strings.Index(log, "--method POST repos/owner/repo/git/commits") > strings.Index(log, "git/refs") {
				t.Fatalf("want blobs, tree, commit, then ref: %s", log)
			}
		})
	}
}

func TestCleanupStepClosesAStalePullRequestAndDeletesItsBranch(t *testing.T) {
	requireTools(t, "bash", "jq")
	script := stepScript(t, "pull-request", "Close the stale pull request")
	const token = "ghs_secrettoken"
	open := pr{Number: 7, State: "OPEN"}
	fork := pr{Number: 9, State: "OPEN", IsCrossRepository: true}
	for _, tc := range []struct {
		name, head, fail       string
		prs                    []pr
		wantCalls, wantNoCalls []string
		wantErr                bool
	}{
		{name: "open pull request and branch", prs: []pr{open}, head: "headsha", wantCalls: []string{"gh pr close 7 --comment", "--method DELETE repos/owner/repo/git/refs/heads/catalog-maintenance/alpha"}},
		{name: "branch left by an earlier run", head: "headsha", wantCalls: []string{"--method DELETE"}, wantNoCalls: []string{"gh pr close"}},
		{name: "pull request without a branch", prs: []pr{open}, wantCalls: []string{"gh pr close 7"}, wantNoCalls: []string{"--method DELETE"}},
		{name: "nothing stale", wantNoCalls: []string{"gh pr close", "--method DELETE"}},
		{name: "a newer fork pull request beside the bot pull request", prs: []pr{fork, open}, head: "headsha", wantCalls: []string{"gh pr close 7"}, wantNoCalls: []string{"gh pr close 9"}},
		{name: "only a fork pull request", prs: []pr{fork}, wantNoCalls: []string{"gh pr close"}},
		{name: "pull request lookup failure", prs: []pr{open}, head: "headsha", fail: "--state open", wantErr: true},
		{name: "a full page of open pull requests", prs: fullPage("OPEN"), head: "headsha", wantErr: true, wantNoCalls: []string{"gh pr close", "--method DELETE"}},
		{name: "close failure", prs: []pr{open}, head: "headsha", fail: "pr close", wantErr: true},
		{name: "branch lookup failure", head: "headsha", fail: "matching-refs", wantErr: true},
		{name: "branch deletion failure", head: "headsha", fail: "--method DELETE", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prs := tc.prs
			if prs == nil {
				prs = []pr{}
			}
			log, output, err := runWithGH(t, t.TempDir(), script, "GH_TOKEN="+token, "REFS="+ghJSON(t, refs(tc.head)), "PRS="+ghJSON(t, prs), "FAIL="+tc.fail)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v: %s\n%s", err, output, log)
			}
			for _, want := range tc.wantCalls {
				if !strings.Contains(log, want) {
					t.Fatalf("missing %q in %s", want, log)
				}
			}
			for _, unwanted := range tc.wantNoCalls {
				if strings.Contains(log, unwanted) {
					t.Fatalf("unexpected %q in %s", unwanted, log)
				}
			}
			if strings.Contains(log, token) || strings.Contains(output, token) {
				t.Fatalf("the token reached an argument or the output: %s", log)
			}
			if tc.wantErr {
				all := calls(log)
				if last := all[len(all)-1]; !strings.Contains(last, tc.fail) {
					t.Fatalf("the step went on after the failing %q call: %s", tc.fail, log)
				}
			}
		})
	}
}

func TestValidateSourceRepository(t *testing.T) {
	for _, repository := range []string{"", "owner/repo", "owner/repo.name"} {
		if err := ValidateSourceRepository(repository); err != nil {
			t.Fatalf("%q: %v", repository, err)
		}
	}
	for _, repository := range []string{"owner", "owner/repo.git", "owner/repo/extra", "owner/re po", "https://github.com/owner/repo"} {
		assertCode(t, ValidateSourceRepository(repository), e.CodeUsage)
	}
}

// describe runs the "Describe the changed pins" step on a refresh result and returns its body.
func describe(t *testing.T, script string, result []byte) (string, string, error) {
	t.Helper()
	temp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(temp, "candidate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(temp, "refresh.json"), result, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runScript(t, temp, script, "RUNNER_TEMP="+temp, "POLICY_DEFINITION=alpha")
	body, _ := os.ReadFile(filepath.Join(temp, "candidate", "pull-request.md"))
	return string(body), output, err
}

func TestDescribeStepShowsProvenanceOnlyArtifactChanges(t *testing.T) {
	requireTools(t, "bash", "jq")
	script := stepScript(t, "refresh", "Describe the changed pins")
	// The member keeps its tag and digests; only the umbrella it derives from advances.
	pin := func(derivedFrom string) string {
		return `{"tag":"v1.2.3","digests":{"amd64":"sha256:aa"},"derivedFrom":"` + derivedFrom + `"}`
	}
	result := `{"changed":true,"planDigest":"sha256:plan","update":{"Changed":1,"Artifacts":[{"Name":"spire-chart","Changed":true,"Before":` +
		pin("umbrella-v1") + `,"After":` + pin("umbrella-v2") + `}]}}`
	body, output, err := describe(t, script, []byte(result))
	if err != nil {
		t.Fatalf("describe step failed: %v\n%s", err, output)
	}
	for _, want := range []string{"| Pin | Tag | Digest | Derived from |", "`umbrella-v1` → `umbrella-v2`", "`v1.2.3` → `v1.2.3`"} {
		if !strings.Contains(body, want) {
			t.Fatalf("pull request body omits %q:\n%s", want, body)
		}
	}
}

// The step reads field paths of the refresh result, so it runs against the result a real Refresh returns.
func TestDescribeStepRendersARealRefreshResultAndRefusesHostileValues(t *testing.T) {
	requireTools(t, "bash", "jq")
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := stepScript(t, "refresh", "Describe the changed pins")
	t.Chdir(pkgDir) // the fixture locates its testdata relative to the package
	cfg, promote := maintenanceFixture(t)
	promote("1.1.0")
	result, err := Refresh(t.Context(), cfg, "test (deadbeef)", false, "")
	if err != nil || !result.Changed {
		t.Fatalf("refresh: %+v %v", result, err)
	}
	real, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	body, output, err := describe(t, script, real)
	if err != nil {
		t.Fatalf("describe step failed on a real result: %v\n%s", err, output)
	}
	for _, want := range []string{"Automated pin refresh for `alpha`.", "Generated Run plan: `" + result.PlanDigest + "`", "## Source pins", "| Pin | Tag | Commit | Tree hash | Derived from | Changed fields |",
		"| `root` | `release/v1.0.0` → `release/v1.1.0` |"} {
		if !strings.Contains(body, want) {
			t.Fatalf("pull request body omits %q:\n%s", want, body)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"backtick", func(m map[string]any) { setTag(t, m, "release/v1`1") }},
		{"pipe", func(m map[string]any) { setTag(t, m, "release/v1|1") }},
		{"newline", func(m map[string]any) { setTag(t, m, "release/v1\n## injected") }},
		{"bidirectional override", func(m map[string]any) { setTag(t, m, "release/v1\u202e1") }},
		{"zero-width space", func(m map[string]any) { setTag(t, m, "release/v1\u200b1") }},
		{"C1 control", func(m map[string]any) { setTag(t, m, "release/v1\u00851") }},
		{"other non-ASCII text", func(m map[string]any) { setTag(t, m, "release/v1é") }},
		{"missing plan digest", func(m map[string]any) { delete(m, "planDigest") }},
		{"empty plan digest", func(m map[string]any) { m["planDigest"] = "" }},
		{"no change", func(m map[string]any) { m["changed"] = false }},
		{"no pin changes counted", func(m map[string]any) { m["update"].(map[string]any)["Changed"] = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var decoded map[string]any
			if err := json.Unmarshal(real, &decoded); err != nil {
				t.Fatal(err)
			}
			tc.mutate(decoded)
			mutated, err := json.Marshal(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if _, output, err := describe(t, script, mutated); err == nil {
				t.Fatalf("rendered a pull request body from a result it must refuse: %s", output)
			}
		})
	}
}

func setTag(t *testing.T, result map[string]any, tag string) {
	t.Helper()
	pins, ok := result["update"].(map[string]any)["Pins"].([]any)
	if !ok || len(pins) == 0 {
		t.Fatal("the refresh result has no source pins")
	}
	pins[0].(map[string]any)["After"].(map[string]any)["tag"] = tag
}

// The policy step runs on exactly the policy files the refresh admitted, but it must still refuse a value that
// could reach its step outputs or select an unknown validation mode.
func TestPolicyStepReadsAnAdmittedPolicyAndRefusesTheRest(t *testing.T) {
	requireTools(t, "bash", "yq")
	script := stepScript(t, "refresh", "Read the maintenance policy")
	const valid = "catalog: definitions/alpha/catalog\ndefinition: definitions/alpha/definition.yaml\nenvironment: definitions/alpha/environment.yaml\n"
	const outputs = "catalog=definitions/alpha/catalog\ndefinition=definitions/alpha/definition.yaml\nenvironment=definitions/alpha/environment.yaml\n"
	for _, tc := range []struct {
		name, policy, want string
	}{
		{"static", valid + "validation: static\n", outputs + "validation=static\n"},
		{"local-run", valid + "validation: local-run\n", outputs + "validation=local-run\n"},
		{"missing policy", "", ""},
		{"missing validation", valid, ""},
		{"unknown validation", valid + "validation: hosted\n", ""},
		{"missing catalog", "definition: d.yaml\nenvironment: e.yaml\nvalidation: static\n", ""},
		{"output injection", "catalog: \"c\\nvalidation=local-run\"\ndefinition: d.yaml\nenvironment: e.yaml\nvalidation: static\n", ""},
		{"space in a path", "catalog: c d\ndefinition: d.yaml\nenvironment: e.yaml\nvalidation: static\n", ""},
		{"not YAML", "catalog: [unterminated\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if tc.policy != "" {
				if err := os.MkdirAll(filepath.Join(repo, PolicyDirectory), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, PolicyDirectory, "alpha"+PolicySuffix), []byte(tc.policy), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(t.TempDir(), "output")
			output, err := runScript(t, repo, script, "DEFINITION=alpha", "GITHUB_OUTPUT="+out)
			got, _ := os.ReadFile(out)
			if tc.want == "" {
				if err == nil || len(got) != 0 {
					t.Fatalf("read a policy it must refuse: %s %q", output, got)
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("%v: %s\n%q", err, output, got)
			}
		})
	}
}

// typedResult serializes a refresh result the way catalog refresh --json does, from the typed update.
func typedResult(t *testing.T, update catalog.SourcePinUpdate) []byte {
	t.Helper()
	raw, err := canonical.Marshal(Result{Changed: true, PlanDigest: "sha256:" + strings.Repeat("e", 64), Update: update})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDescribeStepRendersTypedArtifactAndMixedResults(t *testing.T) {
	requireTools(t, "bash", "jq")
	script := stepScript(t, "refresh", "Describe the changed pins")
	digest := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	chart := &catalog.HelmChart{Name: "spire", Repository: "oci://registry.example.test/charts"}
	member := func(tag, amd64, derivedFrom, url string) catalog.ArtifactPin {
		return catalog.ArtifactPin{Chart: chart, Tag: tag, Digests: map[string]string{"amd64": amd64}, DerivedFrom: derivedFrom, URL: url}
	}
	source := catalog.SourcePinChange{Name: "nvke", Changed: true,
		Before: catalog.Pin{Tag: "v0.21.0", Commit: strings.Repeat("1", 40), TreeHash: digest("2")},
		After:  catalog.Pin{Tag: "v0.22.0", Commit: strings.Repeat("3", 40), TreeHash: digest("4")}}
	upgraded := catalog.ArtifactPinChange{Name: "spire-chart", Changed: true,
		Before: member("0.1.0", digest("a"), "nvke@"+strings.Repeat("1", 40), "https://registry.example.test/spire-0.1.0.tgz"),
		After:  member("0.2.0", digest("b"), "nvke@"+strings.Repeat("3", 40), "https://registry.example.test/spire-0.2.0.tgz")}
	unchanged := catalog.ArtifactPinChange{Name: "other-chart", Before: member("1.0.0", digest("c"), "", ""), After: member("1.0.0", digest("c"), "", "")}
	for _, tc := range []struct {
		name         string
		update       catalog.SourcePinUpdate
		want, unwant []string
	}{
		{"artifact only", catalog.SourcePinUpdate{Artifacts: []catalog.ArtifactPinChange{upgraded, unchanged}, Changed: 1},
			[]string{"## Artifact pins", "| `spire-chart` | `0.1.0` → `0.2.0` | `amd64 " + digest("a") + "` → `amd64 " + digest("b") + "` |",
				"`chart oci://registry.example.test/charts/spire; url https://registry.example.test/spire-0.1.0.tgz`", "| `tag, digests, derivedFrom, url` |"},
			[]string{"## Source pins", "other-chart"}},
		{"mixed source and artifact", catalog.SourcePinUpdate{Pins: []catalog.SourcePinChange{source}, Artifacts: []catalog.ArtifactPinChange{upgraded}, Changed: 2},
			[]string{"## Source pins", "| `nvke` | `v0.21.0` → `v0.22.0` | `" + strings.Repeat("1", 40) + "` → `" + strings.Repeat("3", 40) + "` |",
				"## Artifact pins", "| `spire-chart` |"},
			nil},
		// An umbrella advance can keep a derived source's tag, commit and tree and move only its provenance.
		{"source provenance only", catalog.SourcePinUpdate{Pins: []catalog.SourcePinChange{{Name: "nvke-helm", Changed: true,
			Before: catalog.Pin{Tag: "v1.0.0", Commit: strings.Repeat("5", 40), TreeHash: digest("6"), DerivedFrom: "nvke@" + strings.Repeat("1", 40)},
			After:  catalog.Pin{Tag: "v1.0.0", Commit: strings.Repeat("5", 40), TreeHash: digest("6"), DerivedFrom: "nvke@" + strings.Repeat("3", 40)}}}, Changed: 1},
			[]string{"| `nvke@" + strings.Repeat("1", 40) + "` → `nvke@" + strings.Repeat("3", 40) + "` | `derivedFrom` |"},
			[]string{"## Artifact pins"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, output, err := describe(t, script, typedResult(t, tc.update))
			if err != nil {
				t.Fatalf("describe step failed on a typed result: %v\n%s", err, output)
			}
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Fatalf("pull request body omits %q:\n%s", want, body)
				}
			}
			for _, unwanted := range tc.unwant {
				if strings.Contains(body, unwanted) {
					t.Fatalf("pull request body has %q:\n%s", unwanted, body)
				}
			}
		})
	}
}

// A member whose tag, digests and provenance stay the same can still move to another transport location.
func TestDescribeStepNamesTransportOnlyArtifactChanges(t *testing.T) {
	requireTools(t, "bash", "jq")
	script := stepScript(t, "refresh", "Describe the changed pins")
	pin := func(url string) catalog.ArtifactPin {
		return catalog.ArtifactPin{Tag: "v1.2.3", Digests: map[string]string{"amd64": "sha256:" + strings.Repeat("a", 64)}, URL: url}
	}
	update := catalog.SourcePinUpdate{Changed: 1, Artifacts: []catalog.ArtifactPinChange{{Name: "spire-chart", Changed: true,
		Before: pin("https://old.example.test/spire.tgz"), After: pin("https://new.example.test/spire.tgz")}}}
	body, output, err := describe(t, script, typedResult(t, update))
	if err != nil {
		t.Fatalf("describe step failed: %v\n%s", err, output)
	}
	for _, want := range []string{"| Pin | Tag | Digest | Derived from | Transport | Changed fields |",
		"`url https://old.example.test/spire.tgz` → `url https://new.example.test/spire.tgz`", "| `url` |"} {
		if !strings.Contains(body, want) {
			t.Fatalf("pull request body omits %q:\n%s", want, body)
		}
	}
}
