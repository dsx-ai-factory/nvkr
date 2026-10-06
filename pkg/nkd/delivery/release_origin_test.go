package delivery

import (
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestReleasePublicationOrigin(t *testing.T) {
	for _, mutation := range []string{"valid", "tag", "missing preparation", "missing signing", "oversized", "missing run"} {
		t.Run(mutation, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "arm64")
			request.Release.PlanDigest = digestBytes([]byte("plan"))
			request.Release.InvocationID = "https://github.com/example/nkd/actions/runs/1/attempts/3"
			origin := &PublicationOrigin{Ref: "example.test/nkd/releases-node-image@" + digestBytes([]byte("source")),
				PreparedInvocationID: "https://github.com/example/nkd/actions/runs/1/attempts/1",
				SignedInvocationID:   "https://github.com/example/nkd/actions/runs/1/attempts/2"}
			request.Objects[0].Output.Publication = origin
			switch mutation {
			case "tag":
				origin.Ref = "example.test/nkd/releases-node-image:latest"
			case "missing preparation":
				origin.PreparedInvocationID = ""
			case "missing signing":
				origin.SignedInvocationID = ""
			case "oversized":
				origin.SignedInvocationID = strings.Repeat("a", 2049)
			case "missing run":
				request.Release.PlanDigest, request.Release.InvocationID = "", ""
			}
			out := filepath.Join(t.TempDir(), "publication")
			_, err := BuildPublicationRoot(t.Context(), out, request)
			if mutation != "valid" {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), out)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, object := range inspection.Root.Objects {
				if object.Output != nil {
					found = true
					if object.Output.Publication == nil || *object.Output.Publication != *origin {
						t.Fatal("origin lost in canonical publication")
					}
				}
			}
			if !found {
				t.Fatal("output absent")
			}
		})
	}
}
