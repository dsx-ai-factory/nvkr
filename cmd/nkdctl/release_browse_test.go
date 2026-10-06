package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
	"github.com/urfave/cli/v2"
)

func TestReleaseBrowseRejectsPresentationBeforeAcquisition(t *testing.T) {
	for _, extra := range [][]string{{"--format", "html"}, {"--timeout", "0s"}, {"--layout", "missing", "--anonymous"}, {"unexpected"}} {
		var out bytes.Buffer
		app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
		args := []string{"nkdctl", "release", "browse", "--reference", "127.0.0.1:1/releases@sha256:" + strings.Repeat("a", 64), "--workspace", filepath.Join(t.TempDir(), "must-not-exist")}
		err := app.RunContext(t.Context(), append(args, extra...))
		if cliErrorCode(err) != e.CodeUsage || out.Len() != 0 {
			t.Fatalf("invalid options reached acquisition or output: %v, %q", err, out.String())
		}
	}
}

func TestReleaseBrowseRejectsInvalidReferencesWithoutEffects(t *testing.T) {
	for _, reference := range []string{"", " ", "registry.example.test/releases", "registry.example.test/releases:latest", "registry.example.test/releases@sha256:bad"} {
		t.Run(reference, func(t *testing.T) {
			var out bytes.Buffer
			app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
			workspace := filepath.Join(t.TempDir(), "must-not-exist")
			err := app.RunContext(t.Context(), []string{"nkdctl", "release", "browse", "--reference", reference, "--workspace", workspace})
			if cliErrorCode(err) != e.CodeUnverifiedRef || out.Len() != 0 {
				t.Fatalf("invalid reference reached acquisition or output: %v, %q", err, out.String())
			}
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid reference touched workspace: %v", err)
			}
		})
	}
}

func TestReleaseVerifyRequiresCompleteExactInputSetBeforeAcquisition(t *testing.T) {
	for _, flags := range [][]string{
		{"--definition", "definition.yaml"},
		{"--environment", "environment.yaml"},
		{"--plan", "plan.json"},
		{"--definition", "definition.yaml", "--environment", "environment.yaml"},
		{"--definition", "definition.yaml", "--plan", "plan.json"},
		{"--environment", "environment.yaml", "--plan", "plan.json"},
		{"--definition", "", "--environment", "", "--plan", ""},
	} {
		workspace := filepath.Join(t.TempDir(), "must-not-exist")
		args := []string{"nkdctl", "release", "verify",
			"--reference", "127.0.0.1:1/releases@sha256:" + strings.Repeat("a", 64),
			"--lane", "pipeline-attested", "--scope", "artifact-publication",
			"--identity", "^builder$", "--issuer", "issuer", "--workspace", workspace,
		}
		app := &cli.App{Commands: []*cli.Command{cmdRelease()}}
		err := app.RunContext(t.Context(), append(args, flags...))
		if cliErrorCode(err) != e.CodeUsage {
			t.Fatalf("partial exact-input flags reached acquisition: flags=%v error=%v", flags, err)
		}
	}
}

func TestReleaseVerifyLoadsCompleteExactInputsBeforeAcquisition(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "must-not-exist")
	missing := filepath.Join(t.TempDir(), "missing-definition.yaml")
	args := []string{"nkdctl", "release", "verify",
		"--reference", "registry.example.test/releases@sha256:" + strings.Repeat("a", 64),
		"--lane", "pipeline-attested", "--scope", "artifact-publication",
		"--identity", "^builder$", "--issuer", "issuer", "--workspace", workspace,
		"--definition", missing, "--environment", "environment.yaml", "--plan", "plan.json",
	}
	app := &cli.App{Commands: []*cli.Command{cmdRelease()}}
	err := app.RunContext(t.Context(), args)
	if cliErrorCode(err) != e.CodeInvalidCatalog {
		t.Fatalf("complete exact-input load returned %v", err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid exact inputs touched workspace: %v", err)
	}
}

func TestReleaseVerifyForwardsCompleteExactInputsToLifecycleUseCase(t *testing.T) {
	lifecycleCommandRepo(t)
	const reference = "registry.example.test/releases@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const policyDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const planDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	called := false
	verify := func(_ context.Context, gotReference string, policy lifecycle.VerificationPolicy,
		options lifecycle.ConsumerOptions, expectation *lifecycle.ExactInputExpectation,
	) (releaseVerificationResult, error) {
		called = true
		if gotReference != reference || policy.Lane != lifecycle.LanePipeline ||
			policy.Scope != lifecycle.ScopeArtifactPublication || expectation == nil {
			t.Fatalf("exact verification was not forwarded: reference=%q policy=%+v expectation=%v",
				gotReference, policy, expectation)
		}
		if options.Registry != (delivery.RegistryOptions{Anonymous: true}) {
			t.Fatalf("consumer options were not forwarded: %+v", options)
		}
		exact := lifecycle.ExactInputDescription{ReleaseSubject: reference, PolicyDigest: policyDigest,
			DefinitionDigest:  "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			EnvironmentDigest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			PlanDigest:        planDigest, ContentSubject: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
		return releaseVerificationResult{Release: lifecycle.Release{SchemaVersion: lifecycle.ReleaseSchemaVersion,
			Subject: reference, Lane: policy.Lane, Scope: policy.Scope, PolicyDigest: policyDigest,
			ContentSubject: exact.ContentSubject}, ExactInputs: &exact}, nil
	}
	args := []string{"nkdctl", "release", "verify",
		"--reference", reference,
		"--lane", "pipeline-attested", "--scope", "artifact-publication",
		"--identity", "^builder$", "--issuer", "issuer", "--anonymous",
		"--definition", "definitions/nke-deploy/definition.yaml",
		"--environment", "definitions/nke-deploy/environment-github.yaml",
		"--plan", ".github/nkd-run/plan.json",
	}
	app := &cli.App{Commands: []*cli.Command{cmdReleaseWithVerify(verify)}}
	raw, err := runCLIWithCapturedStdout(t, app, args...)
	if err != nil || !called {
		t.Fatalf("complete exact-input path did not reach the lifecycle use case: called=%v error=%v", called, err)
	}
	var result releaseVerificationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Release.Subject != reference || result.ExactInputs == nil ||
		result.ExactInputs.ReleaseSubject != reference || result.ExactInputs.PolicyDigest != policyDigest ||
		result.ExactInputs.PlanDigest != planDigest {
		t.Fatalf("exact verification receipt was not serialized: %+v", result)
	}
}
