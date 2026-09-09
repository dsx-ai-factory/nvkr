package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/urfave/cli/v2"
)

// cmdActions manages the pinned GitHub Actions the CI emitters render. The pins live in
// pkg/nkd/pipeline/actions.lock.yaml (embedded into the binary) — data, not code — mirroring
// catalog pin locks, so a supply-chain bump is a reviewable lockfile change followed by a
// rebuild + regenerate, not a source edit. This keeps the pins auditable in one manifest and
// updatable without touching emitter logic, and it keeps the drift gate authoritative: the
// generated workflow's action SHAs move only when this lockfile does.
func cmdActions() *cli.Command {
	return &cli.Command{
		Name:  "actions",
		Usage: "manage the pinned GitHub Actions the CI emitters render (data in actions.lock.yaml, not code)",
		Subcommands: []*cli.Command{
			{
				Name:  "bump",
				Usage: "resolve each pinned action's track to its current commit sha and rewrite the lockfile",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "lockfile", Value: "pkg/nkd/pipeline/actions.lock.yaml",
						Usage: "path to the embedded action lockfile source"},
					&cli.BoolFlag{Name: "check", Usage: "resolve and report but write nothing; exit non-zero if any pin is stale"},
					&cli.StringSliceFlag{Name: "only", Usage: "bump only these action names (repeatable)"},
					&cli.StringFlag{Name: "token", EnvVars: []string{"GITHUB_TOKEN"},
						Usage: "GitHub token for the API (raises rate limits; optional for public actions)"},
				},
				Action: runActionsBump,
			},
		},
	}
}

func runActionsBump(c *cli.Context) error {
	path := c.String("lockfile")
	raw, err := os.ReadFile(path)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "reading action lockfile %s", path)
	}
	lock, err := pipeline.ParseActionLock(raw)
	if err != nil {
		return err
	}
	resolver := newGitHubRefResolver(c.String("token"))
	bumped, changes, err := lock.Bump(c.Context, resolver, c.StringSlice("only"))
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Println("action pins are current")
		return nil
	}
	for _, ch := range changes {
		fmt.Printf("  %-18s %s (%s) -> %s (%s)\n", ch.Name,
			shortSHA(ch.OldSHA), ch.OldVersion, shortSHA(ch.NewSHA), ch.NewVersion)
	}
	if c.Bool("check") {
		return e.New(e.CodePipelineDrift, "%d action pin(s) are stale; run `nkdctl actions bump`, then rebuild nkdctl and regenerate workflows", len(changes))
	}
	out, err := bumped.Marshal()
	if err != nil {
		return err
	}
	if err := writeAtomic(c.Context, path, out, 0o644); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing %s", path)
	}
	fmt.Printf("updated %s (%d change(s)); rebuild nkdctl and regenerate workflows (nkdctl run generate / nkdctl bundle)\n", path, len(changes))
	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// githubRefResolver resolves a repo's moving track (e.g. "v5") to the immutable commit sha it
// points to and the most specific vX.Y.Z release tag at that sha, via the public GitHub REST
// API. baseURL is overridable so the resolver is exercised hermetically against httptest.
type githubRefResolver struct {
	baseURL string
	client  *http.Client
	token   string
}

func newGitHubRefResolver(token string) *githubRefResolver {
	return &githubRefResolver{
		baseURL: "https://api.github.com",
		client:  &http.Client{Timeout: 20 * time.Second},
		token:   token,
	}
}

var semverTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

func (g *githubRefResolver) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (g *githubRefResolver) Resolve(ctx context.Context, repo, track string) (string, string, error) {
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := g.get(ctx, "/repos/"+repo+"/commits/"+track, &commit); err != nil {
		return "", "", err
	}
	if commit.SHA == "" {
		return "", "", fmt.Errorf("no commit resolved for %s@%s", repo, track)
	}
	// Best-effort precise release label: the vX.Y.Z tag at this sha. The SHA is the security
	// pin; version is a human label, so a tag lookup failure falls back to the track.
	version := track
	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := g.get(ctx, "/repos/"+repo+"/tags?per_page=100", &tags); err == nil {
		for _, t := range tags {
			if t.Commit.SHA == commit.SHA && semverTag.MatchString(t.Name) {
				version = t.Name
				break
			}
		}
	}
	return commit.SHA, version, nil
}
