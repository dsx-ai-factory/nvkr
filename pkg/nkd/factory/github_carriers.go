package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// RestoreGitHubInputs restores the exact external carriers of one generated
// stage. The complete Plan owns selection; transport conveys no authority.
func RestoreGitHubInputs(ctx context.Context, plan *Plan, stageID string, options GitHubStepOptions) error {
	stages, nodeJobs, artifacts, err := admitGitHubCarriers(ctx, plan)
	if err != nil {
		return err
	}
	var selected *githubStage
	for i := range stages {
		if stages[i].ID == stageID {
			selected = &stages[i]
			break
		}
	}
	if selected == nil {
		return e.New(e.CodeInvalidCatalog, "GitHub plan has no stage %q", stageID)
	}
	inputs, err := selected.carrierInputs(plan, artifacts, nodeJobs)
	if err != nil {
		return err
	}
	details := make([]string, 0, len(inputs))
	for _, id := range inputs {
		details = append(details, githubArtifactDetail(plan, "Input", id))
	}
	return githubProgress(ctx, options, "Restore stage inputs", details, func(progress *githubStepProgress) error {
		for _, id := range inputs {
			progress.phase("restore " + id)
			progress.log("Restore: " + id)
			if err := transportarchive.Unpack(ctx, carrierPath(id), ".", []string{artifacts[id].Path}); err != nil {
				return err
			}
		}
		return nil
	})
}

// PackGitHubOutputs prepares each exact output carrier for provider upload.
// A missing or invalid output stops preparation at that artifact.
func PackGitHubOutputs(ctx context.Context, plan *Plan, nodeID string, options GitHubStepOptions) error {
	stages, _, artifacts, err := admitGitHubCarriers(ctx, plan)
	if err != nil {
		return err
	}
	var selected *Node
	for _, stage := range stages {
		for i := range stage.Nodes {
			if stage.Nodes[i].ID == nodeID {
				selected = &stage.Nodes[i]
			}
		}
	}
	if selected == nil {
		return e.New(e.CodeInvalidCatalog, "GitHub plan has no node %q", nodeID)
	}
	outputs := uniqueSorted(selected.Outputs)
	details := make([]string, 0, len(outputs))
	for _, id := range outputs {
		details = append(details, githubArtifactDetail(plan, "Output", id))
	}
	return githubProgress(ctx, options, "Prepare output carriers", details, func(progress *githubStepProgress) error {
		for _, id := range outputs {
			progress.phase("archive " + id)
			progress.log("Archive: " + id)
			if err := transportarchive.Pack(ctx, carrierPath(id), ".", []string{artifacts[id].Path}); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecoverGitHubArtifacts records the transport outcome of every planned carrier.
// providerResults is the bounded JSON description from GitHub's needs context;
// neither these provider descriptions nor the resulting statuses confer authority.
// Ordinary carrier failures are recorded and recovery continues. Cancellation or
// failure to persist an outcome stops recovery and returns a coded error.
func RecoverGitHubArtifacts(ctx context.Context, plan *Plan, providerResults string, options GitHubStepOptions) error {
	return recoverGitHubArtifacts(ctx, plan, providerResults, options, recoverGitHubCarrier, writeGitHubRestoreOutcome)
}

// The public operation always supplies the concrete held-carrier restorer and
// outcome writer. These private seams permit resource-failure tests without
// exhausting the test host.
func recoverGitHubArtifacts(ctx context.Context, plan *Plan, providerResults string, options GitHubStepOptions,
	restore func(context.Context, Artifact) (string, error),
	writeOutcome func(context.Context, string, string) error,
) error {
	if err := admitGitHubRecoveryPlatform(); err != nil {
		return err
	}
	_, nodeJobs, _, err := admitGitHubCarriers(ctx, plan)
	if err != nil {
		return err
	}
	jobs, err := githubFinalizerJobs(plan, nodeJobs)
	if err != nil {
		return err
	}
	results, err := parseGitHubProviderResults(ctx, providerResults, jobs)
	if err != nil {
		return err
	}
	carriers := append([]Artifact(nil), plan.Graph.Artifacts...)
	sort.Slice(carriers, func(i, j int) bool { return carriers[i].ID < carriers[j].ID })
	return githubProgress(ctx, options, "Recover artifacts for finalization", []string{
		"Transport outcomes are diagnostic; the finalizer authenticates completeness.",
	}, func(progress *githubStepProgress) error {
		counts := map[string]int{}
		visited, restoreNoSpace, outcomeWriteFailed, outcomeNoSpace := 0, 0, 0, 0
		defer func() {
			progress.detail(fmt.Sprintf("Recovery totals: visited=%d restored=%d restore-failed=%d (no-space=%d) missing=%d job-failed=%d; persistence: outcome-write-failed=%d (no-space=%d)",
				visited, counts["restored:success"], counts["restore-failed"], restoreNoSpace, counts["missing"], counts["job-failed"], outcomeWriteFailed, outcomeNoSpace))
			if restoreNoSpace+outcomeNoSpace > 0 {
				// A fixed annotation cannot include archive-controlled paths or text.
				_, _ = fmt.Fprintln(progress.options.Stderr, "::error::Artifact recovery exhausted storage; restoration or outcome persistence is incomplete.")
			}
		}()
		for _, artifact := range carriers {
			if _, raw := diskPublicationRequirement(plan, artifact); raw {
				continue
			}
			if err := githubCarrierContext(ctx); err != nil {
				return err
			}
			progress.phase("recover " + artifact.ID)
			progress.log("Recover: " + artifact.ID + " (" + githubArtifactLabel(plan, artifact) + ")")
			status := "job-failed"
			if results[nodeJobs[artifact.Producer]] == "success" {
				var restoreErr error
				status, restoreErr = restore(ctx, artifact)
				if restoreErr != nil {
					progress.detail("Recovery failure: " + artifact.ID + " — " + githubRecoveryErrorDetail(restoreErr))
					if errors.Is(restoreErr, syscall.ENOSPC) || errors.Is(restoreErr, syscall.EDQUOT) {
						restoreNoSpace++
					}
				}
			}
			// Restoration describes every visited carrier, independently of whether
			// its diagnostic status can subsequently be persisted.
			visited++
			counts[status]++
			// Cancellation must not become an ordinary restore failure or allow
			// a partially visited carrier set to appear complete.
			if err := githubCarrierContext(ctx); err != nil {
				return err
			}
			if err := writeOutcome(ctx, artifact.ID, status); err != nil {
				outcomeWriteFailed++
				progress.detail("Recovery outcome failure: " + artifact.ID + " — " + githubRecoveryErrorDetail(err))
				if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
					outcomeNoSpace++
				}
				return err
			}
			progress.detail("Recovery: " + artifact.ID + " — " + status)
		}
		return nil
	})
}

func admitGitHubCarriers(ctx context.Context, plan *Plan) ([]githubStage, map[string]string, map[string]Artifact, error) {
	if err := admitGitHubPlan(ctx, plan); err != nil {
		return nil, nil, nil, err
	}
	stages, nodeJobs, err := githubStages(plan)
	if err != nil {
		return nil, nil, nil, err
	}
	_, artifacts := planIndex(plan)
	return stages, nodeJobs, artifacts, githubCarrierContext(ctx)
}

func githubCarrierContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "GitHub carrier operation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "GitHub carrier operation canceled")
	}
	return nil
}

// githubFinalizerJobs is shared with emission so recovery accepts exactly the
// finalizer's stage and runner-bootstrap dependencies, including grouped stages.
func githubFinalizerJobs(plan *Plan, nodeJobs map[string]string) ([]string, error) {
	jobs := make([]string, 0, len(nodeJobs)+len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		job := nodeJobs[node.ID]
		if job == "" {
			return nil, e.New(e.CodeInvalidCatalog, "GitHub node %q has no producer job", node.ID)
		}
		resolved, err := plan.ExecutionFor(node)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job, "build-runner-"+shortStableName(resolved.RunsOn))
	}
	return uniqueSorted(jobs), nil
}

func recoverGitHubCarrier(ctx context.Context, artifact Artifact) (string, error) {
	if ctx.Err() != nil {
		return "restore-failed", e.Wrap(e.CodeTimeout, ctx.Err(), "carrier recovery canceled")
	}
	archive, _, err := securepath.OpenRegular(carrierPath(artifact.ID))
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", nil
		}
		return "restore-failed", e.Wrap(e.CodeVerifyFailed, err, "opening recovery carrier")
	}
	defer archive.Close()
	if err := transportarchive.UnpackOpen(ctx, archive, ".", []string{artifact.Path}); err != nil {
		return "restore-failed", err
	}
	return "restored:success", nil
}

// Report the owning code and known resource failures without logging untrusted
// archive entry names or arbitrary error text. Recovery status remains diagnostic;
// the finalizer still rejects every missing or unsuccessfully restored carrier.
func githubRecoveryErrorDetail(err error) string {
	detail := string(e.CodeInternal)
	var coded *e.Error
	if errors.As(err, &coded) {
		detail = string(coded.Code)
	}
	switch {
	case errors.Is(err, syscall.ENOSPC):
		return detail + ": no space left on device"
	case errors.Is(err, syscall.EDQUOT):
		return detail + ": disk quota exceeded"
	case errors.Is(err, os.ErrPermission):
		return detail + ": permission denied"
	default:
		return detail
	}
}

const (
	maxGitHubProviderBytes  = 1 << 20
	maxGitHubProviderTokens = 32768
)

// Read the tiny provider schema token-by-token: generic unmarshalling would
// silently accept duplicate names, while recursive ignored values would admit
// unnecessary depth and complexity. Outputs are accepted only as string maps and
// discarded; provider-controlled values never enter diagnostic messages.
func parseGitHubProviderResults(ctx context.Context, raw string, expected []string) (map[string]string, error) {
	if len(raw) > maxGitHubProviderBytes || !utf8.ValidString(raw) {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub job results exceed their size bound or contain invalid UTF-8")
	}
	parser := githubProviderParser{ctx: ctx, decoder: json.NewDecoder(strings.NewReader(raw))}
	if err := parser.delimiter('{'); err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(expected))
	for _, job := range expected {
		allowed[job] = true
	}
	results := make(map[string]string, len(expected))
	for parser.decoder.More() {
		job, err := parser.stringValue()
		if err != nil {
			return nil, err
		}
		if !allowed[job] || results[job] != "" {
			return nil, e.New(e.CodeInvalidCatalog, "GitHub job results contain an unexpected or duplicate job")
		}
		result, err := parser.job()
		if err != nil {
			return nil, err
		}
		results[job] = result
	}
	if err := parser.delimiter('}'); err != nil {
		return nil, err
	}
	if _, err := parser.decoder.Token(); err != io.EOF {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub job results contain trailing data")
	}
	if len(results) != len(allowed) {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub job results omit a required stage or runner bootstrap")
	}
	return results, githubCarrierContext(ctx)
}

type githubProviderParser struct {
	ctx     context.Context
	decoder *json.Decoder
	tokens  int
}

func (parser *githubProviderParser) token() (json.Token, error) {
	if err := githubCarrierContext(parser.ctx); err != nil {
		return nil, err
	}
	parser.tokens++
	if parser.tokens > maxGitHubProviderTokens {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub job results exceed their complexity bound")
	}
	token, err := parser.decoder.Token()
	if err != nil {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub job results contain invalid JSON")
	}
	return token, nil
}

func (parser *githubProviderParser) delimiter(want json.Delim) error {
	token, err := parser.token()
	if err != nil {
		return err
	}
	if token != want {
		return e.New(e.CodeInvalidCatalog, "GitHub job results contain an invalid object")
	}
	return nil
}

func (parser *githubProviderParser) stringValue() (string, error) {
	token, err := parser.token()
	if err != nil {
		return "", err
	}
	value, ok := token.(string)
	if !ok {
		return "", e.New(e.CodeInvalidCatalog, "GitHub job results require string fields")
	}
	return value, nil
}

func (parser *githubProviderParser) job() (string, error) {
	if err := parser.delimiter('{'); err != nil {
		return "", err
	}
	seen := map[string]bool{}
	result := ""
	for parser.decoder.More() {
		field, err := parser.stringValue()
		if err != nil {
			return "", err
		}
		if seen[field] {
			return "", e.New(e.CodeInvalidCatalog, "GitHub job results contain a duplicate field")
		}
		seen[field] = true
		switch field {
		case "result":
			result, err = parser.stringValue()
			if err != nil {
				return "", err
			}
			switch result {
			case "success", "failure", "cancelled", "skipped": //nolint:misspell // GitHub result enum spelling.
			default:
				return "", e.New(e.CodeInvalidCatalog, "GitHub job results contain an unsupported result")
			}
		case "outputs":
			if err := parser.outputs(); err != nil {
				return "", err
			}
		default:
			return "", e.New(e.CodeInvalidCatalog, "GitHub job results contain an unexpected field")
		}
	}
	if err := parser.delimiter('}'); err != nil {
		return "", err
	}
	if result == "" {
		return "", e.New(e.CodeInvalidCatalog, "GitHub job results omit a result")
	}
	return result, nil
}

func (parser *githubProviderParser) outputs() error {
	if err := parser.delimiter('{'); err != nil {
		return err
	}
	seen := map[string]bool{}
	for parser.decoder.More() {
		field, err := parser.stringValue()
		if err != nil {
			return err
		}
		if seen[field] {
			return e.New(e.CodeInvalidCatalog, "GitHub job outputs contain a duplicate field")
		}
		seen[field] = true
		if _, err := parser.stringValue(); err != nil {
			return err
		}
	}
	return parser.delimiter('}')
}
