// Package catalogmaintenance owns review-only catalog refreshes and their
// scheduled GitHub adapter. It grants no build, publication or release authority.
package catalogmaintenance

import (
	"context"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/documentpath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"sigs.k8s.io/yaml"
)

const SchemaVersion = "nkd.dev/catalog-maintenance/v1"

// Config is operator-owned maintenance policy, not a public lifecycle object.
// Constraints stay in the catalog. This config never authorizes widening them.
type Config struct {
	APIVersion     string          `json:"apiVersion"`
	Name           string          `json:"name"`
	Catalog        string          `json:"catalog"`
	Sources        []string        `json:"sources"`
	SameRelease    [][]string      `json:"sameRelease,omitempty"`
	Definition     string          `json:"definition"`
	Environment    string          `json:"environment"`
	Schedule       string          `json:"schedule,omitempty"`
	Validation     string          `json:"validation"`
	PullRequest    *PullRequest    `json:"pullRequest,omitempty"`
	SourceCheckout *SourceCheckout `json:"sourceCheckout,omitempty"`
}

// PullRequest describes the reserved opt-in GitHub App publication policy.
// Workflow rejects it with CodeEngineCapability until that stage is implemented.
type PullRequest struct {
	ClientIDVariable string `json:"clientIDVariable"`
	PrivateKeySecret string `json:"privateKeySecret"`
}

// SourceCheckout transports a private Git repository without leaving credentials
// available to catalog resolution, generation or validation subprocesses.
type SourceCheckout struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	Secret     string `json:"secret"`
}

var safeName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
var safePath = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
var secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func safeRelative(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.HasPrefix(value, "../") && !path.IsAbs(value) && path.Clean(value) == value && safePath.MatchString(value)
}

func (config Config) Validate() error {
	if config.APIVersion != SchemaVersion {
		return e.New(e.CodeInvalidCatalog, "unsupported catalog maintenance schema %q", config.APIVersion)
	}
	if !safeName.MatchString(config.Name) || len(config.Name) > 64 {
		return e.New(e.CodeUsage, "maintenance name requires a bounded lowercase identifier")
	}
	for _, value := range []string{config.Catalog, config.Definition, config.Environment} {
		if !safeRelative(value) {
			return e.New(e.CodeUsage, "maintenance paths must be clean repository-relative paths: %q", value)
		}
	}
	if len(config.Sources) == 0 || len(config.Sources) > 128 {
		return e.New(e.CodeUsage, "maintenance requires 1–128 explicit source roots")
	}
	selected := map[string]bool{}
	for _, source := range config.Sources {
		if !safeName.MatchString(source) || selected[source] {
			return e.New(e.CodeUsage, "invalid or repeated maintenance source %q", source)
		}
		selected[source] = true
	}
	grouped := map[string]bool{}
	for _, group := range config.SameRelease {
		if len(group) < 2 {
			return e.New(e.CodeUsage, "same-release groups require at least two sources")
		}
		for _, source := range group {
			if !selected[source] || grouped[source] {
				return e.New(e.CodeUsage, "same-release source %q must be selected once", source)
			}
			grouped[source] = true
		}
	}
	if config.Schedule != "" {
		fields := strings.Fields(config.Schedule)
		if len(fields) != 5 || strings.Join(fields, " ") != config.Schedule {
			return e.New(e.CodeUsage, "schedule requires five cron fields")
		}
		// Deliberately narrow: each field is '*' or one decimal integer. This covers
		// daily/weekly schedules without aliases, steps, lists or injection syntax.
		bounds := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
		for i, field := range fields {
			if field == "*" {
				continue
			}
			n, err := strconv.Atoi(field)
			if err != nil || strconv.Itoa(n) != field || n < bounds[i][0] || n > bounds[i][1] {
				return e.New(e.CodeUsage, "unsupported cron field %q; use '*' or an in-range decimal integer", field)
			}
		}
		if fields[0] == "*" {
			return e.New(e.CodeUsage, "maintenance schedule must select one minute of the hour")
		}
	}
	if config.Validation != "static" && config.Validation != "local-run" {
		return e.New(e.CodeUsage, "maintenance validation must be static or local-run")
	}
	if pr := config.PullRequest; pr != nil {
		if config.Validation != "local-run" || !secretName.MatchString(pr.ClientIDVariable) || !secretName.MatchString(pr.PrivateKeySecret) || strings.HasPrefix(pr.PrivateKeySecret, "GITHUB_") {
			return e.New(e.CodeUsage, "automated PRs require local-run validation and dedicated GitHub App variable/secret names")
		}
	}
	if access := config.SourceCheckout; access != nil {
		if !repositoryName.MatchString(access.Repository) || strings.HasSuffix(access.Repository, ".git") || !commitSHA.MatchString(access.Ref) || !secretName.MatchString(access.Secret) || strings.HasPrefix(access.Secret, "GITHUB_") {
			return e.New(e.CodeUsage, "source checkout requires owner/repository, a full commit and a dedicated secret name")
		}
	}
	return nil
}

func Load(ctx context.Context, filename string) (Config, error) {
	if ctx == nil {
		return Config{}, e.New(e.CodeUsage, "maintenance load requires a context")
	}
	held, err := heldfile.OpenContext(ctx, filename, heldfile.Options{Label: "catalog maintenance", MaxBytes: 64 << 10})
	if err != nil {
		return Config{}, contextError(ctx, e.Wrap(e.CodeInvalidCatalog, err, "opening maintenance config"))
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, 64<<10)
	if err != nil {
		return Config{}, contextError(ctx, e.Wrap(e.CodeInvalidCatalog, err, "reading maintenance config"))
	}
	// Admit the complete document stream before strict typed decoding, which
	// otherwise accepts only the first YAML document and ignores trailing policy.
	if _, err := documentpath.Decode(raw); err != nil {
		return Config{}, e.Wrap(e.CodeInvalidCatalog, err, "decoding maintenance config")
	}
	var config Config
	if err := yaml.UnmarshalStrict(raw, &config); err != nil {
		return Config{}, e.Wrap(e.CodeInvalidCatalog, err, "decoding maintenance config")
	}
	return config, config.Validate()
}

func contextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "catalog maintenance canceled")
	}
	return err
}
