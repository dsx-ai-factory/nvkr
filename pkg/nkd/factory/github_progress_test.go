package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestGitHubProgressSummaryPreservesPrimaryStatus(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		summary string
	}{
		{"success", 0, "file"},
		{"failure", 23, "file"},
		{"success without summary", 0, "unset"},
		{"failure without summary", 23, "unset"},
		{"success with unwritable summary", 0, "directory"},
		{"failure with unwritable summary", 23, "directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			var summary string
			switch test.summary {
			case "file":
				summary = filepath.Join(root, "summary.md")
			case "directory":
				summary = root
			}
			commands := githubProgressCommands("Build controller", []string{"Node: build/controller", "Component: controller", "Selected output: controller-image"}, []githubProgressCommand{
				{phase: "execute build/controller", run: "(exit " + strconv.Itoa(test.status) + ")"},
				{phase: "report controller-image", run: "printf 'produced-marker\\n'"},
			})
			log := runGitHubScript(t, root, strings.Join(commands, "\n"), summary, test.status)
			if test.status != 0 {
				if !strings.Contains(log, "Failed: Build controller at execute build/controller (exit 23") || strings.Contains(log, "produced-marker") || strings.Contains(log, "Completed:") {
					t.Fatalf("failure location/status lost: %s", log)
				}
			}
			if test.summary == "file" {
				raw, err := os.ReadFile(summary)
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{"### Build controller", "Component: controller", "Selected output: controller-image", "| " + strconv.Itoa(test.status) + " |"} {
					if !strings.Contains(string(raw), want) {
						t.Fatalf("summary lacks %q: %s", want, raw)
					}
				}
				if test.status != 0 && !strings.Contains(string(raw), "| failed | 23 |") {
					t.Fatalf("summary claims success: %s", raw)
				}
			}
		})
	}
}

func TestGitHubProgressTreatsDescriptionAsData(t *testing.T) {
	root := t.TempDir()
	description := "a|b <script> `touch marker` $(touch marker) ${{ secrets.EXAMPLE }}\n::error::fake"
	commands := githubProgressCommands(description, []string{description}, []githubProgressCommand{{phase: description, run: "true"}})
	script := strings.Join(commands, "\n")
	if strings.Contains(script, "${{") || strings.Contains(script, "\n::error::fake") {
		t.Fatalf("provider expression or raw control line in script: %s", script)
	}
	summary := filepath.Join(root, "summary.md")
	log := runGitHubScript(t, root, script, summary, 0)
	if !strings.Contains(log, "${{ secrets.EXAMPLE }}") || strings.Contains(log, "\n::error::fake") {
		t.Fatalf("description was not literal one-line text: %s", log)
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
		t.Fatalf("shell substitution executed: %v", err)
	}
	raw, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "<script>") || strings.Contains(string(raw), "a|b") || strings.Contains(string(raw), "`touch") {
		t.Fatalf("summary markup was not escaped: %s", raw)
	}
}

func TestGitHubNodeContextIncludesExactBindingsWithoutCredentials(t *testing.T) {
	plan := &Plan{Builds: []BuildPlan{{Name: "image", Graph: &ir.Graph{Nodes: []ir.Node{{
		ID: "build/controller", Component: "controller", BuilderType: "oci-hermetic", SourceCommit: strings.Repeat("a", 40),
		Command: []string{"tool", "--password=do-not-log-this"}, Env: []ir.EnvVar{{Name: "TOKEN", Value: "do-not-log-this"}}, Repo: "https://do-not-log-this@example.com/source",
	}}}}}, Graph: ArtifactGraph{Artifacts: []Artifact{
		{ID: "source/controller", Kind: "source-closure", Producer: "fetch/controller"},
		{ID: "artifact/controller", Kind: "oci-image", Name: "controller", Producer: "build/controller"},
	}}}
	node := Node{ID: "build/image/controller", Kind: NodeBuild, IRNode: "build/controller", BuildTarget: "image", Platform: "linux/arm64", Network: "none", Inputs: []string{"source/controller"}, Outputs: []string{"artifact/controller"}}
	text := strings.Join(githubNodeDetails(plan, node), "\n")
	for _, want := range []string{"Node: build/image/controller", "Selected output: image", "Component: controller", "Producer instruction: build/controller", "Platform: linux/arm64", "Input: source/controller", "Expected output: artifact/controller", "producer build/controller"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Contains(strings.Join(githubNodeCommands(node, plan), "\n"), "do-not-log-this") {
		t.Fatal("raw arguments, environment or source credentials entered workflow diagnostics")
	}
}

func runGitHubScript(t *testing.T, root, script, summary string, code int) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GITHUB_STEP_SUMMARY="+summary, "NKD_CREDENTIAL=do-not-log-this")
	raw, err := cmd.CombinedOutput()
	if code == 0 && err != nil {
		t.Fatalf("script failed: %v\n%s", err, raw)
	}
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != code {
		t.Fatalf("exit status: %v, want %d: %s", err, code, raw)
	}
	if strings.Contains(string(raw), "do-not-log-this") {
		t.Fatal("credential leaked to log")
	}
	return string(raw)
}

func TestGitHubProgressClosedLogsCannotMaskPrimaryStatus(t *testing.T) {
	for _, closeFD := range []string{"exec 1>&-", "exec 2>&-", "exec 1>&- 2>&-"} {
		for _, status := range []int{0, 23} {
			t.Run(closeFD+"/"+strconv.Itoa(status), func(t *testing.T) {
				root := t.TempDir()
				work := []githubProgressCommand{{phase: "execute producer", run: closeFD + "\n(exit " + strconv.Itoa(status) + ")"}}
				commands := githubProgressCommands("Build producer", []string{"Node: producer"}, work)
				// The directory also forces the summary fallback itself to fail when
				// stderr is closed. Neither diagnostic failure may replace work status.
				runGitHubScript(t, root, strings.Join(commands, "\n"), root, status)
			})
		}
	}
}

func TestGitHubProgressDistinguishesPreflightFailure(t *testing.T) {
	for _, test := range []struct{ name, first, last, phase string }{
		{"preflight", "(exit 23)", "printf 'operation-marker\\n'", "preflight build/producer"},
		{"operation", "true", "(exit 23)", "execute build/producer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			work := githubNodeWork("build/producer", []string{test.first, test.last})
			commands := githubProgressCommands("Build producer", nil, work)
			log := runGitHubScript(t, root, strings.Join(commands, "\n"), filepath.Join(root, "summary.md"), 23)
			if !strings.Contains(log, "at "+test.phase+" (exit 23") || strings.Contains(log, "operation-marker") {
				t.Fatalf("failure phase or ordering lost: %s", log)
			}
		})
	}
}
