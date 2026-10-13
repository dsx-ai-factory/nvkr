// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

func cmdCatalog() *cli.Command {
	command := &cli.Command{
		Name:  "catalog",
		Usage: "inspect and validate a catalog",
		Subcommands: []*cli.Command{
			{
				Name:      "source-pin",
				Usage:     "print one validated source pin as JSON",
				ArgsUsage: "<source>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"},
				},
				Action: func(c *cli.Context) error {
					view, err := selectCommandPresentation(c)
					if err != nil {
						return err
					}
					display := catalogCommandDisplay("Catalog source pin")
					if c.Args().Len() != 1 {
						return showCommandResult(c, view, display, e.New(e.CodeUsage, "catalog source-pin requires exactly one source name"))
					}
					loaded, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return showCommandResult(c, view, display, err)
					}
					pin, err := loaded.SourcePin(c.Args().First())
					if err != nil {
						return showCommandResult(c, view, display, err)
					}
					if view.tui {
						display.Items = []progress.Item{{ID: c.Args().First(), Name: c.Args().First(), Kind: "source pin", State: "recorded", Fields: sourcePinFields(pin)}}
						return showCommandResult(c, view, display, nil)
					}
					return json.NewEncoder(os.Stdout).Encode(struct {
						Commit   string `json:"commit"`
						TreeHash string `json:"treeHash"`
					}{Commit: pin.Commit, TreeHash: pin.TreeHash})
				},
			},
			{
				Name:  "digest",
				Usage: "print the exact resolved catalog snapshot digest used to pin an ordered component overlay",
				Flags: []cli.Flag{&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"}},
				Action: func(c *cli.Context) error {
					view, err := selectCommandPresentation(c)
					if err != nil {
						return err
					}
					display := catalogCommandDisplay("Catalog snapshot digest")
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return showCommandResult(c, view, display, err)
					}
					digest, err := cat.SnapshotDigest()
					if err != nil {
						return showCommandResult(c, view, display, err)
					}
					if view.tui {
						display.Fields = []progress.Field{{Label: "Catalog", Value: cat.Metadata.Name}, {Label: "Snapshot digest", Value: digest}}
						return showCommandResult(c, view, display, nil)
					}
					fmt.Println(digest)
					return nil
				},
			},
			{
				Name:  "validate",
				Usage: "load and fail-closed-validate a catalog",
				Flags: []cli.Flag{&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"}},
				Action: func(c *cli.Context) error {
					view, err := selectCommandPresentation(c)
					if err != nil {
						return err
					}
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return showCommandResult(c, view, catalogCommandDisplay("Catalog validation"), err)
					}
					if view.tui {
						return showCommandResult(c, view, catalogComponentsDisplay(cat, "Catalog validation"), nil)
					}
					fmt.Printf("ok: %s — %d component(s)\n", cat.Metadata.Name, len(cat.Components))
					return nil
				},
			},
			{
				Name:  "list",
				Usage: "list components",
				Flags: []cli.Flag{&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"}},
				Action: func(c *cli.Context) error {
					view, err := selectCommandPresentation(c)
					if err != nil {
						return err
					}
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return showCommandResult(c, view, catalogCommandDisplay("Catalog components"), err)
					}
					if view.tui {
						return showCommandResult(c, view, catalogComponentsDisplay(cat, "Catalog components"), nil)
					}
					fmt.Printf("catalog: %s\n\n", cat.Metadata.Name)
					fmt.Printf("components:\n")
					names := make([]string, 0, len(cat.Components))
					for n := range cat.Components {
						names = append(names, n)
					}
					sort.Strings(names)
					tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
					for _, n := range names {
						fmt.Fprintf(tw, "  %s\t%s\n", n, cat.Components[n].String())
					}
					tw.Flush()
					return nil
				},
			},
			{
				Name:      "pin",
				Usage:     "resolve a source release or exact Git commit to pins, then write the lock",
				ArgsUsage: "<source>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "tag", Usage: "tag to pin (default: newest matching the source constraint)"},
					&cli.StringFlag{Name: "commit", Usage: "full Git source commit to pin without a release-tag claim; incompatible with --tag"},
					&cli.BoolFlag{Name: "check", Usage: "resolve and show the complete pin update without writing"},
					&cli.StringFlag{Name: "dir", Required: true, Usage: "on-disk catalog directory to update"},
					&cli.StringFlag{Name: "runtime", Value: "docker", Usage: "legacy default-only option (docker); image pins use anonymous HTTPS"},
					&cli.DurationFlag{Name: "timeout", Value: 5 * time.Minute, Usage: "maximum duration for upstream git/container resolution"},
				},
				Action: func(c *cli.Context) error { return pinSources(c, false) },
			},
			{
				Name:      "bump",
				Usage:     "pin the newest constraint-matching source releases, including declared tool assets",
				ArgsUsage: "[source...]",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "all", Usage: "bump every source"},
					&cli.BoolFlag{Name: "check", Usage: "dry-run: print the lockfile diff, write nothing (ADR-22 §4)"},
					&cli.StringFlag{Name: "dir", Required: true, Usage: "on-disk catalog directory to update"},
					&cli.StringFlag{Name: "runtime", Value: "docker", Usage: "legacy default-only option (docker); image pins use anonymous HTTPS"},
					&cli.DurationFlag{Name: "timeout", Value: 5 * time.Minute, Usage: "maximum duration for upstream git/container resolution"},
				},
				Action: func(c *cli.Context) error { return pinSources(c, true) },
			},
			{
				Name:      "pin-artifacts",
				Usage:     "resolve prebuilt component digests (per arch) and write them to the lock's artifacts section",
				ArgsUsage: "[component...]",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "all", Usage: "pin every prebuilt component"},
					&cli.StringSliceFlag{Name: "arch", Usage: "optional exact declared architecture set to pin"},
					&cli.StringFlag{Name: "dir", Required: true, Usage: "on-disk catalog directory to update"},
					&cli.StringFlag{Name: "runtime", Aliases: []string{"docker"}, Value: "docker", Usage: "legacy default-only option (docker); image pins use anonymous HTTPS"},
					&cli.DurationFlag{Name: "timeout", Value: 5 * time.Minute, Usage: "maximum duration for artifact digest resolution"},
				},
				Action: pinArtifacts,
			},
			cmdCatalogRefresh(),
			cmdCatalogUpdateCI(),
		},
	}
	for _, leaf := range command.Subcommands {
		if leaf.Name != "refresh" && leaf.Name != "update-ci" {
			leaf.Flags = append(leaf.Flags, commandPresentationFlags()...)
		}
	}
	return command
}

// pinSources maps command flags and presents the catalog-owned update result.
func pinSources(c *cli.Context, bump bool) error {
	view, err := selectCommandPresentation(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Context, c.Duration("timeout"))
	defer cancel()
	update, err := catalog.UpdateSourcePins(ctx, catalog.SourcePinRequest{
		Directory: c.String("dir"), Sources: c.Args().Slice(), Tag: c.String("tag"), Commit: c.String("commit"),
		Bump: bump, All: c.Bool("all"), Check: c.Bool("check"), Runtime: c.String("runtime"),
	})
	if view.tui {
		return showCommandResult(c, view, catalogSourceUpdateDisplay(update, c.Bool("check"), err), err)
	}
	for _, change := range update.Pins {
		if !change.Changed {
			fmt.Printf("  %s: already at %s (%s)\n", change.Name, change.After.Tag, change.After.Commit[:12])
		} else {
			fmt.Printf("  %s: %s -> %s (%s, epoch %d)\n", change.Name, orNone(change.Before.Tag), change.After.Tag, change.After.Commit[:12], change.After.SourceDateEpoch)
		}
	}
	var coded *e.Error
	for _, change := range update.Artifacts {
		if !change.Changed {
			fmt.Printf("  artifact %s: already at %s (derived from %s)\n", change.Name, change.After.Tag, change.After.DerivedFrom)
			continue
		}
		fmt.Printf("  artifact %s: %s -> %s (derived from %s)\n", change.Name, orNone(change.Before.Tag), change.After.Tag, change.After.DerivedFrom)
		arches := make([]string, 0, len(change.After.Digests))
		for arch := range change.After.Digests {
			arches = append(arches, arch)
		}
		sort.Strings(arches)
		for _, arch := range arches {
			fmt.Printf("    %s: %s -> %s\n", arch, orNone(change.Before.Digests[arch]), change.After.Digests[arch])
		}
	}
	if errors.As(err, &coded) && coded.Code == e.CodeDiff {
		fmt.Printf("(dry-run) %d pin(s) would change in %s/%s\n", update.Changed, c.String("dir"), catalog.LockFile)
	}
	if err != nil {
		return err
	}
	if update.Changed == 0 {
		fmt.Println("no changes")
	} else {
		fmt.Printf("wrote %d pin(s) to %s/%s\n", update.Changed, c.String("dir"), catalog.LockFile)
	}
	return nil
}

func pinArtifacts(c *cli.Context) error {
	view, err := selectCommandPresentation(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Context, c.Duration("timeout"))
	defer cancel()
	update, err := catalog.UpdateArtifactPins(ctx, catalog.ArtifactPinRequest{
		Directory: c.String("dir"), Components: c.Args().Slice(), All: c.Bool("all"),
		Architectures: c.StringSlice("arch"), Runtime: c.String("runtime"),
	})
	if view.tui {
		display := catalogCommandDisplay("Catalog artifact pin update")
		display.Fields = []progress.Field{{Label: "Changed pins", Value: strconv.Itoa(update.Changed)}, {Label: "Write result", Value: catalogUpdateState(false, update.Changed != 0, err)}}
		for _, change := range update.Pins {
			display.Items = append(display.Items, artifactPinItem(change, false, err))
		}
		return showCommandResult(c, view, display, err)
	}
	for _, change := range update.Pins {
		if !change.Changed {
			fmt.Printf("  %s: already pinned (%s, %d arch)\n", change.Name, change.After.Tag, len(change.After.Digests))
		} else {
			fmt.Printf("  %s: pinned %d arch digest(s) @ %s\n", change.Name, len(change.After.Digests), change.After.Tag)
		}
	}
	if err != nil {
		return err
	}
	if update.Changed == 0 {
		fmt.Println("no changes")
	} else {
		fmt.Printf("wrote artifact pin(s) to %s/%s\n", c.String("dir"), catalog.LockFile)
	}
	return nil
}

func catalogCommandDisplay(title string) commandDisplay {
	return commandDisplay{Title: title, Note: "Catalog metadata and maintenance only; producer execution and release verification are separate."}
}

func sourcePinFields(pin catalog.Pin) []progress.Field {
	return []progress.Field{{Label: "Commit", Value: pin.Commit}, {Label: "Tree hash", Value: pin.TreeHash}, {Label: "Source epoch", Value: strconv.FormatInt(pin.SourceDateEpoch, 10)}}
}

func catalogComponentsDisplay(loaded *catalog.Catalog, title string) commandDisplay {
	display := catalogCommandDisplay(title)
	display.Fields = []progress.Field{{Label: "Catalog", Value: loaded.Metadata.Name}, {Label: "Components", Value: strconv.Itoa(len(loaded.Components))}}
	names := make([]string, 0, len(loaded.Components))
	for name := range loaded.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		component := loaded.Components[name]
		mode := "excluded"
		if component.FromSource {
			mode = "source"
		} else if component.Prebuilt != nil {
			mode = "prebuilt"
		}
		display.Items = append(display.Items, progress.Item{ID: name, Name: name, Kind: component.OutputType, State: "declared", Fields: []progress.Field{
			{Label: "Producer mode", Value: mode}, {Label: "Builder", Value: component.BuilderType}, {Label: "Platforms", Value: strings.Join(component.Platforms, ", ")},
		}})
	}
	return display
}

// catalogUpdateState distinguishes check-only proposals from confirmed writes.
// Any failure other than the owner's check-mode diff leaves writes unconfirmed.
func catalogUpdateState(check, changed bool, ownerErr error) string {
	if ownerErr != nil && !(check && runErrorCode(ownerErr) == e.CodeDiff) {
		return "resolution description; write outcome unconfirmed"
	}
	if !changed {
		return "unchanged"
	}
	if check {
		return "proposed; no write requested"
	}
	return "written"
}

func catalogSourceUpdateDisplay(update catalog.SourcePinUpdate, check bool, ownerErr error) commandDisplay {
	display := catalogCommandDisplay("Catalog source pin update")
	if check {
		display.Note = "Check requested; pin changes are proposals and no lock write was requested. Producer execution and release verification are separate."
	}
	display.Fields = []progress.Field{{Label: "Changed pins", Value: strconv.Itoa(update.Changed)}, {Label: "Write result", Value: catalogUpdateState(check, update.Changed != 0, ownerErr)}}
	for _, change := range update.Pins {
		fields := []progress.Field{{Label: "Write result", Value: catalogUpdateState(check, change.Changed, ownerErr)}, {Label: "Previous commit", Value: change.Before.Commit}, {Label: "Previous tree hash", Value: change.Before.TreeHash}}
		fields = append(fields, sourcePinFields(change.After)...)
		display.Items = append(display.Items, progress.Item{ID: "source/" + change.Name, Name: change.Name, Kind: "source pin", State: catalogPinItemState(check, change.Changed, ownerErr), Fields: fields})
	}
	for _, change := range update.Artifacts {
		display.Items = append(display.Items, artifactPinItem(change, check, ownerErr))
	}
	return display
}

func artifactPinItem(change catalog.ArtifactPinChange, check bool, ownerErr error) progress.Item {
	item := progress.Item{ID: "artifact/" + change.Name, Name: change.Name, Kind: "artifact pin", State: catalogPinItemState(check, change.Changed, ownerErr), Fields: []progress.Field{{Label: "Write result", Value: catalogUpdateState(check, change.Changed, ownerErr)}}}
	if change.After.ImageParentDigest != "" {
		item.Fields = append(item.Fields, progress.Field{Label: "Previous image parent digest", Value: change.Before.ImageParentDigest}, progress.Field{Label: "Image parent digest", Value: change.After.ImageParentDigest})
	}
	arches := make([]string, 0, len(change.After.Digests))
	for arch := range change.After.Digests {
		arches = append(arches, arch)
	}
	sort.Strings(arches)
	for _, arch := range arches {
		item.Fields = append(item.Fields, progress.Field{Label: arch + " previous digest", Value: change.Before.Digests[arch]}, progress.Field{Label: arch + " digest", Value: change.After.Digests[arch]})
	}
	return item
}

func catalogPinItemState(check, changed bool, ownerErr error) string {
	if ownerErr != nil && !(check && runErrorCode(ownerErr) == e.CodeDiff) {
		return "resolved"
	}
	if changed {
		return "changed"
	}
	return "unchanged"
}
