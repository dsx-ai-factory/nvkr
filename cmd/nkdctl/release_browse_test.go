package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
