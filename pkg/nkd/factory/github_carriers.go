package factory

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
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
	inputs := selected.inputs(artifacts, nodeJobs)
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
	if PerOutputPublication(plan) {
		status := "restored:success"
		for _, job := range jobs {
			if results[job] != "success" {
				status = "job-failed"
				break
			}
		}
		if err := writeGitHubRestoreOutcome(ctx, "all-provider-jobs", status); err != nil {
			return err
		}
	}
	carriers := append([]Artifact(nil), plan.Graph.Artifacts...)
	sort.Slice(carriers, func(i, j int) bool { return carriers[i].ID < carriers[j].ID })
	return githubProgress(ctx, options, "Recover artifacts for finalization", []string{
		"Transport outcomes are diagnostic; the finalizer authenticates completeness.",
	}, func(progress *githubStepProgress) error {
		for _, artifact := range carriers {
			if !finalizerArtifact(plan, artifact) {
				continue
			}
			if err := githubCarrierContext(ctx); err != nil {
				return err
			}
			progress.phase("recover " + artifact.ID)
			progress.log("Recover: " + artifact.ID + " (" + githubArtifactLabel(plan, artifact) + ")")
			status := "job-failed"
			if results[nodeJobs[artifact.Producer]] == "success" {
				status = recoverGitHubCarrier(ctx, artifact)
			}
			// Cancellation must not become an ordinary restore failure or allow
			// a partially visited carrier set to appear complete.
			if err := githubCarrierContext(ctx); err != nil {
				return err
			}
			if err := writeGitHubRestoreOutcome(ctx, artifact.ID, status); err != nil {
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

func recoverGitHubCarrier(ctx context.Context, artifact Artifact) string {
	if ctx.Err() != nil {
		return "restore-failed"
	}
	archive, _, err := securepath.OpenRegular(carrierPath(artifact.ID))
	if err != nil {
		if os.IsNotExist(err) {
			return "missing"
		}
		return "restore-failed"
	}
	defer archive.Close()
	if err := transportarchive.UnpackOpen(ctx, archive, ".", []string{artifact.Path}); err != nil {
		return "restore-failed"
	}
	return "restored:success"
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
