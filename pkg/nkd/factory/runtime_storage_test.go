package factory

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestFactoryStorageOverrideAdmissionBeforeWork(t *testing.T) {
	for _, runtimeName := range []string{"docker", "podman"} {
		for _, driver := range []definition.PodmanStorageDriver{"", definition.PodmanStorageVFS, definition.PodmanStorageOverlay} {
			t.Run(runtimeName+"/"+string(driver), func(t *testing.T) {
				plan := githubTestPlan(t)
				plan.Environment.Runtime.Runtime = runtimeName
				plan.Environment.Runtime.PodmanStorageDriver = driver
				// Docker overrides are rejected by authored schema admission itself.
				if runtimeName == "docker" && driver != "" {
					assertFactoryErrorCode(t, plan.Environment.Validate(), e.CodeInvalidCatalog)
					return
				}
				var err error
				plan.EnvironmentDigest, err = plan.Environment.Digest()
				if err != nil {
					t.Fatal(err)
				}
				if err := plan.Validate(); err != nil {
					t.Fatal(err)
				}
				for name, admit := range map[string]func(context.Context, *Plan) error{
					"local": admitLocalPlanBase, "github": admitGitHubPlan,
				} {
					t.Run(name, func(t *testing.T) {
						err := admit(t.Context(), plan)
						if driver == "" {
							if err != nil {
								t.Fatal(err)
							}
						} else {
							assertFactoryErrorCode(t, err, e.CodeEngineCapability)
						}
					})
				}
				var logs bytes.Buffer
				if driver != "" {
					bundle, err := RenderGitHub(t.Context(), plan)
					assertFactoryErrorCode(t, err, e.CodeEngineCapability)
					if bundle != nil || !strings.Contains(err.Error(), "podmanStorageDriver") {
						t.Fatalf("unsupported storage reached rendering: bundle=%v err=%v", bundle, err)
					}
				}
				err = ExecuteNode(t.Context(), plan, "not-a-node", &logs, &logs)
				want := e.CodeInvalidCatalog
				if driver != "" {
					want = e.CodeEngineCapability
				}
				assertFactoryErrorCode(t, err, want)
				if logs.Len() != 0 {
					t.Fatal("rejected storage admission wrote logs")
				}
			})
		}
	}
}
