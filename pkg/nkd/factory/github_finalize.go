package factory

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

// writeGitHubFinalizerRestoreSteps emits the complete carrier download and restore
// sequence for the lifecycle finalizer. actions/download-artifact v4.3.0 expands
// the exact minimatch brace set, downloads up to five artifacts concurrently, and
// merges their uniquely named carrier files into one directory. The action succeeds
// when the pattern matches no artifacts, so every expected artifact still reaches
// the per-producer status logic and the finalizer can persist a failed Run.
func writeGitHubFinalizerRestoreSteps(out *bytes.Buffer, plan *Plan, nodeJobs map[string]string) error {
	if out == nil || plan == nil {
		return e.New(e.CodeUsage, "GitHub finalizer restore rendering requires output and plan")
	}
	if len(plan.Graph.Artifacts) == 0 {
		return e.New(e.CodeInvalidCatalog, "GitHub lifecycle finalizer requires at least one carrier")
	}

	type carrier struct {
		artifact Artifact
		name     string
		path     string
		job      string
	}
	carriers := make([]carrier, 0, len(plan.Graph.Artifacts))
	seenNames := map[string]string{}
	seenPaths := map[string]string{}
	for _, artifact := range plan.Graph.Artifacts {
		job := nodeJobs[artifact.Producer]
		if job == "" {
			return e.New(e.CodeInvalidCatalog,
				"GitHub finalizer artifact %q has no producer job", artifact.ID)
		}
		name := githubArtifactName(artifact.ID)
		path := carrierPath(artifact.ID)
		if previous := seenNames[name]; previous != "" {
			return e.New(e.CodeInvalidCatalog,
				"GitHub finalizer artifacts %q and %q collide at provider name %q", previous, artifact.ID, name)
		}
		if previous := seenPaths[path]; previous != "" {
			return e.New(e.CodeInvalidCatalog,
				"GitHub finalizer artifacts %q and %q collide at carrier path %q", previous, artifact.ID, path)
		}
		seenNames[name] = artifact.ID
		seenPaths[path] = artifact.ID
		carriers = append(carriers, carrier{artifact: artifact, name: name, path: path, job: job})
	}
	sort.Slice(carriers, func(i, j int) bool { return carriers[i].artifact.ID < carriers[j].artifact.ID })
	names := make([]string, len(carriers))
	for index := range carriers {
		names[index] = carriers[index].name
	}
	pattern := names[0]
	if len(names) > 1 {
		pattern = "{" + strings.Join(names, ",") + "}"
	}

	var rendered bytes.Buffer
	fmt.Fprintln(&rendered, "      - name: Prepare restore outcomes\n        run: mkdir -p .nkd/restore-outcomes")
	fmt.Fprintf(&rendered, "      - name: Download Run carriers\n        uses: %s\n        continue-on-error: true\n        with:\n          pattern: %s\n          path: .nkd/carriers\n          merge-multiple: 'true'\n",
		githubDownloadAction, yamlSingle(pattern))
	for _, carrier := range carriers {
		status := filepath.ToSlash(HostedRestoreOutcomePath(".nkd/factory", carrier.artifact.ID))
		fmt.Fprintf(&rendered, "      - name: Restore %s when produced\n        run: if [ '${{ needs.%s.result }}' != 'success' ]; then printf 'job-failed\\n' > %s; elif [ ! -f %s ]; then printf 'missing\\n' > %s; elif ./nkd-exec transport-unpack --archive %s --root . --path %s; then printf 'restored:success\\n' > %s; else printf 'restore-failed\\n' > %s; fi\n",
			yamlSingle(carrier.artifact.ID), carrier.job, shellQuote(status), shellQuote(carrier.path), shellQuote(status), shellQuote(carrier.path), shellQuote(carrier.artifact.Path), shellQuote(status), shellQuote(status))
	}
	out.Write(rendered.Bytes())
	return nil
}
