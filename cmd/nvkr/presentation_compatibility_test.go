// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func TestDefaultLocalAutoWithRedirectedWriterKeepsPlainProducerBytes(t *testing.T) {
	lifecycleCommandRepo(t)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CI", "")
	var out bytes.Buffer
	calls := 0
	execute := func(_ context.Context, prepared *lifecycle.PreparedRun, options lifecycle.LocalOptions) (*lifecycle.Run, error) {
		calls++
		if options.Stdout != &out || options.Stderr != os.Stderr {
			t.Fatal("redirected default captured ordinary producer streams")
		}
		if _, err := fmt.Fprint(options.Stdout, "producer stdout remains visible\n"); err != nil {
			t.Fatal(err)
		}
		return completedPresentationRun(prepared), nil
	}
	// The production adapter selects its real eligibility and display services.
	app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdRunWithLocalExecutor(execute)}}
	if err := app.RunContext(t.Context(), localPresentationArgs()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.HasPrefix(out.String(), "producer stdout remains visible\n") || !strings.Contains(out.String(), "lane: unqualified-local-execution") || strings.ContainsRune(out.String(), '\x1b') {
		t.Fatalf("default ineligible writer lost plain output: calls=%d bytes=%q", calls, out.String())
	}
}

func TestCIRestrictsAutomaticSelectionOnly(t *testing.T) {
	for _, test := range []struct {
		ci, mode string
		want     bool
	}{
		{"", "auto", true}, {"true", "auto", false}, {"1", "auto", false},
		{"false", "auto", false}, {"true", "tui", true}, {"true", "plain", false},
	} {
		t.Run("CI="+test.ci+"/"+test.mode, func(t *testing.T) {
			t.Setenv("CI", test.ci)
			app := &cli.App{Writer: io.Discard, Flags: []cli.Flag{&cli.StringFlag{Name: "ui"}}, Action: func(c *cli.Context) error {
				view, err := selectRunPresentationWithEligibility(c, true, func(io.Writer) bool { return true })
				if err != nil || view.tui != test.want {
					t.Fatalf("CI changed explicit admission or lost automatic plain fallback: %+v %v", view, err)
				}
				return nil
			}}
			if err := app.RunContext(t.Context(), []string{"nvkr", "--ui=" + test.mode}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// These fixed bytes were captured with the pre-dashboard base executable at
// 3558650e986cd7600558e4e38b77da5b89d780f0, not regenerated from this implementation.
// Only input-dependent identities and relocated catalog paths are adapted below.
func TestDefaultLeafBytesMatchPreDashboardFixtures(t *testing.T) {
	lifecycleCommandRepo(t)
	const maintainedPlan = ".github/nvkr-run/plan.json"
	for _, test := range []struct {
		name, fixture string
		args          []string
	}{
		{"Definition validation", "definition-validate.txt", []string{"definition", "validate", "--definition", "docs/examples/nvke-tutorial/definition.yaml"}},
		{"Definition inspection", "definition-inspect.json", []string{"definition", "inspect", "--definition", "docs/examples/nvke-tutorial/definition.yaml"}},
		{"identical Plans", "identical-plans.txt", []string{"run", "diff", "--before", maintainedPlan, "--after", maintainedPlan}},
	} {
		t.Run(test.name, func(t *testing.T) {
			want, err := os.ReadFile("cmd/nvkr/testdata/presentation/" + test.fixture)
			if err != nil {
				t.Fatal(err)
			}
			if test.fixture == "definition-validate.txt" || test.fixture == "definition-inspect.json" {
				definition, err := lifecycle.LoadDefinition(t.Context(), "docs/examples/nvke-tutorial/definition.yaml")
				if err != nil {
					t.Fatal(err)
				}
				if test.fixture == "definition-validate.txt" {
					const capturedDigest = "sha256:9e75b6ddc0ee9d7cb3bf7a55e654cef39d5399eb021a2459332555946a3cb794"
					if bytes.Count(want, []byte(capturedDigest)) != 1 {
						t.Fatal("Definition fixture lost its captured identity")
					}
					digest, err := definition.Digest()
					if err != nil {
						t.Fatal(err)
					}
					want = bytes.ReplaceAll(want, []byte(capturedDigest), []byte(digest))
				} else {
					const capturedCatalog = `"base":"./catalog"`
					if bytes.Count(want, []byte(capturedCatalog)) != 1 {
						t.Fatal("Definition fixture lost its captured catalog path")
					}
					want = bytes.ReplaceAll(want, []byte(capturedCatalog), []byte(fmt.Sprintf(`"base":%q`, definition.Catalog.Base)))
				}
			}
			if test.fixture == "identical-plans.txt" {
				// The maintained output selection changes its Plan identity. Keep
				// the captured wording and formatting, substituting only that input.
				const capturedDigest = "sha256:2a59420b680dc5d5411b570fd72d7ae763cfa7657b02370712e54f092ec9181e"
				if bytes.Count(want, []byte(capturedDigest)) != 2 {
					t.Fatal("identical-Plans fixture lost its captured before/after identity")
				}
				plan, err := factory.LoadPlanContext(t.Context(), maintainedPlan)
				if err != nil {
					t.Fatal(err)
				}
				digest, err := plan.Digest()
				if err != nil {
					t.Fatal(err)
				}
				want = bytes.ReplaceAll(want, []byte(capturedDigest), []byte(digest))
			}
			actual, err := runCLIWithCapturedStdout(t, newApp(), append([]string{"nvkr"}, test.args...)...)
			if err != nil || !bytes.Equal(actual, want) {
				t.Fatalf("default leaf output changed from pre-dashboard fixture: %v\ngot %q\nwant %q", err, actual, want)
			}
		})
	}
}

func TestPlanViewWriteKeepsHistoricErrorWording(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = previous })
	err = writePlanView(t.Context(), []byte("view\n"))
	if cliErrorCode(err) != e.CodeInternal || !strings.Contains(err.Error(), "writing plan view") || strings.Contains(err.Error(), "writing Run result") {
		t.Fatalf("Plan write changed its existing error mapping or wording: %v", err)
	}
}
