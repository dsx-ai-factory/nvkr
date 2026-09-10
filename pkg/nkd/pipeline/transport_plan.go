package pipeline

import (
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

const (
	TransportPlanSchema = "nkd.dev/transport-plan/v1"
	TransportPlanFile   = "transport-plan.json"
)

// TransportBinding is the generated topology contract. Content digests that do not
// exist until execution belong in source/result/OCI receipts, not in this plan.
type TransportBinding struct {
	Name           string   `json:"name"`
	Producer       string   `json:"producer"`
	Consumers      []string `json:"consumers"`
	Paths          []string `json:"paths"`
	CarrierPath    string   `json:"carrierPath"`
	MediaType      string   `json:"mediaType"`
	Authentication string   `json:"authentication"`
}

type TransportPlan struct {
	SchemaVersion string             `json:"schemaVersion"`
	Bindings      []TransportBinding `json:"bindings"`
}

// TransportPlanJSON proves that every lowered input has exactly one producer and an
// explicit allow-list. Runtime receipts later bind the bytes observed on each edge.
func TransportPlanJSON(g *ir.Graph) ([]byte, error) {
	jobs, err := LowerJobs(g)
	if err != nil {
		return nil, err
	}
	type bindingState struct {
		artifact  JobArtifact
		producer  string
		consumers []string
	}
	states := map[string]*bindingState{}
	for _, job := range jobs {
		if job.Produces == nil {
			continue
		}
		if _, duplicate := states[job.Produces.Name]; duplicate {
			return nil, e.New(e.CodeInvalidCatalog, "transport artifact %q has multiple producers", job.Produces.Name)
		}
		copyArtifact := *job.Produces
		copyArtifact.Paths = append([]string(nil), job.Produces.Paths...)
		states[copyArtifact.Name] = &bindingState{artifact: copyArtifact, producer: job.Name}
	}
	for _, job := range jobs {
		for _, input := range job.Inputs {
			state, ok := states[input.Name]
			if !ok {
				return nil, e.New(e.CodeInvalidCatalog, "job %q consumes transport artifact %q with no producer", job.Name, input.Name)
			}
			if !sameStrings(state.artifact.Paths, input.Paths) {
				return nil, e.New(e.CodeInvalidCatalog, "job %q transport allow-list for %q diverges from its producer", job.Name, input.Name)
			}
			state.consumers = append(state.consumers, job.Name)
		}
	}
	plan := TransportPlan{SchemaVersion: TransportPlanSchema}
	for name, state := range states {
		if len(state.consumers) == 0 {
			continue
		}
		binding := TransportBinding{
			Name: name, Producer: state.producer,
			Consumers: append([]string(nil), state.consumers...), Paths: append([]string(nil), state.artifact.Paths...),
			CarrierPath: TransportArchivePath(name), MediaType: "application/vnd.nkd.transport.tar",
			Authentication: "runtime-content-receipt+consumer-rehash",
		}
		if name == RunnerBinary {
			binding.CarrierPath = RunnerBinary
			binding.MediaType = "application/vnd.nkd.runner"
			binding.Authentication = "builder-profile-runner-digest+consumer-rehash"
		}
		sort.Strings(binding.Consumers)
		sort.Strings(binding.Paths)
		plan.Bindings = append(plan.Bindings, binding)
	}
	sort.Slice(plan.Bindings, func(i, j int) bool { return plan.Bindings[i].Name < plan.Bindings[j].Name })
	return canonical.Marshal(plan)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac, bc := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
