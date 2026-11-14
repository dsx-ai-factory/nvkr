// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// nvkr-results-publish is the generated publish job's helper. select chooses what
// one producer Run publishes and writes the one errored outcome it may build;
// publish admits and plans the same outcomes again and writes them to the results
// bucket. The selection lives in pkg/nvkr/qualificationoutcome and the bucket
// layout in pkg/nvkr/resultspublish; this command maps flags, the job's environment
// and the store. It is the only NVKR code that links the cloud SDK (gcs.go), so it
// is not part of the release CLI. Its errors name no bucket, identity or project.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish"
)

// bucketEnv names the results bucket. The publish job sets it from an Environment
// secret, so the name appears in neither the generated workflow nor its log.
const bucketEnv = "NVKR_RESULTS_BUCKET"

// verdictNote ends every error: publishing never changes the qualification. It names
// the publish job alone, because re-running failed jobs can qualify again.
const verdictNote = "Qualification verdict unaffected. Re-run this publish job alone within 24 hours; if it persists, open an issue."

// selectionRequired are the flags select and publish both require.
var selectionRequired = []string{"outcomes", "event", "source-workflow", "source-branch", "qualify-workflow", "qualify-result"}

// storeOpener opens the results bucket's store and its release.
type storeOpener func(ctx context.Context, bucket string) (resultspublish.ObjectStore, func() error, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	status := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv, time.Now, openGCSStore)
	stop()
	if status != 0 {
		os.Exit(status)
	}
}

// run maps a subcommand to its owner and every error to the frozen exit table.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, lookup func(string) (string, bool), now func() time.Time, open storeOpener) int {
	err := dispatch(ctx, args, stdout, stderr, lookup, now, open)
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "nvkr-results-publish: %v. %s\n", err, verdictNote)
	return e.ExitCode(err)
}

func dispatch(ctx context.Context, args []string, stdout, stderr io.Writer, lookup func(string) (string, bool), now func() time.Time, open storeOpener) error {
	if len(args) > 0 {
		switch args[0] {
		case "select":
			return runSelect(ctx, args[1:], stdout, stderr, lookup, now)
		case "publish":
			return runPublish(ctx, args[1:], stderr, lookup, now, open)
		}
	}
	return e.New(e.CodeUsage, "nvkr-results-publish requires the select or publish subcommand")
}

// selectionFlags registers the flags select and publish share: where the Run's
// outcomes and event are, the workflows it trusts, and what the qualify job
// reported. Its result reads them once they are parsed.
func selectionFlags(flags *flag.FlagSet) func() (qualificationoutcome.Selection, error) {
	outcomes := flags.String("outcomes", "", "directory of the Run's downloaded outcome artifacts")
	eventPath := flags.String("event", "", "the workflow_run event, GITHUB_EVENT_PATH")
	sourceWorkflow := flags.String("source-workflow", "", "the producer workflow path")
	sourceBranch := flags.String("source-branch", "", "the branch the producer runs on")
	qualifyWorkflow := flags.String("qualify-workflow", "", "this qualify workflow's path")
	result := flags.String("qualify-result", "", "the qualify job's result: success, failure, cancelled or skipped") //nolint:misspell // GitHub's job result spelling.
	attempt := flags.String("qualify-attempt", "", "the run attempt the qualify job last ran in; empty when its report step did not run")
	written := flags.String("outcome-written", "", "whether that attempt wrote an outcome, true or false; empty when its report step did not run")
	return func() (qualificationoutcome.Selection, error) {
		report := qualificationoutcome.QualifyReport{Result: *result, Attempt: *attempt}
		// Both come from the qualify job's report step: both set, or both empty.
		switch {
		case *attempt == "" && *written == "":
		case *attempt != "" && (*written == "true" || *written == "false"):
			report.Written = *written == "true"
		default:
			return qualificationoutcome.Selection{}, e.New(e.CodeUsage,
				"%s --qualify-attempt and --outcome-written come from one report step: give both, written true or false, or neither", flags.Name())
		}
		return qualificationoutcome.Selection{Outcomes: *outcomes, EventPath: *eventPath, SourceWorkflow: *sourceWorkflow,
			SourceBranch: *sourceBranch, QualifyWorkflow: *qualifyWorkflow, Qualify: report}, nil
	}
}

// runSelect prints exactly built=true or built=false on stdout, for the job to
// read; everything else goes to stderr.
func runSelect(ctx context.Context, args []string, stdout, stderr io.Writer, lookup func(string) (string, bool), now func() time.Time) error {
	flags := newFlags("select")
	selection := selectionFlags(flags)
	write := flags.String("write", "", "where to write a built outcome; it must not exist")
	if err := parse(flags, args, append(slices.Clone(selectionRequired), "write")...); err != nil {
		return err
	}
	request, err := selection()
	if err != nil {
		return err
	}
	publish, built, err := qualificationoutcome.Select(ctx, lookup, request, now())
	if err != nil {
		return err
	}
	if built != nil {
		raw, err := built.Marshal()
		if err != nil {
			return err
		}
		if err := writeNew(*write, raw); err != nil {
			return err
		}
	}
	fmt.Fprintf(stderr, "nvkr-results-publish: %d outcome(s) to publish; built %t\n", len(publish), built != nil)
	if _, err := fmt.Fprintf(stdout, "built=%t\n", built != nil); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing the selection")
	}
	return nil
}

// runPublish admits and plans the Run's outcomes again, exactly as select did,
// before it opens the store, so it writes only outcomes of the admitted Run. The
// record select builds must already be among them.
func runPublish(ctx context.Context, args []string, stderr io.Writer, lookup func(string) (string, bool), now func() time.Time, open storeOpener) error {
	flags := newFlags("publish")
	selection := selectionFlags(flags)
	group := flags.String("group", "", "the Definition's group")
	definition := flags.String("definition", "", "the Definition's name")
	localDir := flags.String("local-dir", "", "publish into this directory instead of the results bucket, as a dry run")
	if err := parse(flags, args, append(slices.Clone(selectionRequired), "group", "definition")...); err != nil {
		return err
	}
	if err := resultspublish.CheckNames(*group, *definition); err != nil {
		return err
	}
	request, err := selection()
	if err != nil {
		return err
	}
	publish, err := qualificationoutcome.SelectRecorded(ctx, lookup, request, now())
	if err != nil {
		return err
	}
	if len(publish) == 0 {
		fmt.Fprintln(stderr, "nvkr-results-publish: nothing to publish")
		return nil
	}
	var store resultspublish.ObjectStore
	if *localDir != "" {
		store = localStore{root: *localDir}
	} else {
		bucket, ok := lookup(bucketEnv)
		if !ok || bucket == "" {
			return e.New(e.CodeUsage, "publish requires %s or --local-dir", bucketEnv)
		}
		opened, release, err := open(ctx, bucket)
		if err != nil {
			return err
		}
		defer func() { _ = release() }()
		store = opened
	}
	if err := resultspublish.Publish(ctx, store, publish, *group, *definition); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "nvkr-results-publish: published %d outcome(s)\n", len(publish))
	return nil
}

func newFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func parse(flags *flag.FlagSet, args []string, required ...string) error {
	if err := flags.Parse(args); err != nil {
		return e.Wrap(e.CodeUsage, err, "%s flags", flags.Name())
	}
	if flags.NArg() != 0 {
		return e.New(e.CodeUsage, "%s takes no positional arguments", flags.Name())
	}
	for _, name := range required {
		if strings.TrimSpace(flags.Lookup(name).Value.String()) == "" {
			return e.New(e.CodeUsage, "%s requires --%s", flags.Name(), name)
		}
	}
	return nil
}

// writeNew writes a built outcome where none exists, owner-only like run qualify's.
func writeNew(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return e.Wrap(e.CodeInternal, err, "creating the built outcome's directory")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return e.New(e.CodeUsage, "select --write names an existing file")
	}
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating the built outcome")
	}
	_, writeErr := file.Write(raw)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(path)
		return e.Wrap(e.CodeInternal, err, "writing the built outcome")
	}
	return nil
}
