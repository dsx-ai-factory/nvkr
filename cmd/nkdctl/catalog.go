package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/urfave/cli/v2"
)

func cmdCatalog() *cli.Command {
	return &cli.Command{
		Name:  "catalog",
		Usage: "inspect and validate a catalog",
		Subcommands: []*cli.Command{
			{
				Name:  "digest",
				Usage: "print the exact resolved catalog snapshot digest used to pin an ordered component overlay",
				Flags: []cli.Flag{&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"}},
				Action: func(c *cli.Context) error {
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return err
					}
					digest, err := cat.SnapshotDigest()
					if err != nil {
						return err
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
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return err
					}
					fmt.Printf("ok: %s — %d component(s), %d AICR profile(s)\n", cat.Metadata.Name, len(cat.Components), len(cat.AICRProfiles))
					return nil
				},
			},
			{
				Name:  "list",
				Usage: "list components and AICR profiles",
				Flags: []cli.Flag{&cli.StringFlag{Name: "data", Required: true, Usage: "path to the catalog directory"}},
				Action: func(c *cli.Context) error {
					cat, err := catalog.LoadContext(c.Context, providerFor(c))
					if err != nil {
						return err
					}
					fmt.Printf("catalog: %s\n\n", cat.Metadata.Name)
					profiles := make([]string, 0, len(cat.AICRProfiles))
					for name := range cat.AICRProfiles {
						profiles = append(profiles, name)
					}
					sort.Strings(profiles)
					fmt.Println("AICR profiles:")
					for _, name := range profiles {
						fmt.Printf("  %s\t%s\n", name, cat.AICRProfiles[name].Digest)
					}

					fmt.Printf("\ncomponents:\n")
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
				Usage:     "resolve a source tag and any declared tool asset to exact pins, then write the lock",
				ArgsUsage: "<source>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "tag", Usage: "tag to pin (default: newest matching the source constraint)"},
					&cli.StringFlag{Name: "dir", Required: true, Usage: "on-disk catalog directory to update"},
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
					&cli.StringFlag{Name: "runtime", Aliases: []string{"docker"}, Value: "docker", Usage: "container runtime for image manifest inspection"},
					&cli.DurationFlag{Name: "timeout", Value: 5 * time.Minute, Usage: "maximum duration for artifact digest resolution"},
				},
				Action: pinArtifacts,
			},
			{
				Name:  "update-ci",
				Usage: "emit a scheduled GitHub Actions workflow that bumps pins, verifies, and opens a PR",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "dir", Required: true, Usage: "catalog directory the workflow updates"},
					&cli.StringFlag{Name: "cron", Value: "0 6 * * 1", Usage: "schedule (default: weekly Mon 06:00 UTC)"},
				},
				Action: func(c *cli.Context) error {
					return e.New(e.CodeEngineCapability,
						"catalog update-ci is unavailable until a provider adapter can regenerate, authenticate, reproduce, verify, and open a PR with every exact input transported")
				},
			},
		},
	}
}

// pinSources maps command flags and presents the catalog-owned update result.
func pinSources(c *cli.Context, bump bool) error {
	ctx, cancel := context.WithTimeout(c.Context, c.Duration("timeout"))
	defer cancel()
	update, err := catalog.UpdateSourcePins(ctx, catalog.SourcePinRequest{
		Directory: c.String("dir"), Sources: c.Args().Slice(), Tag: c.String("tag"),
		Bump: bump, All: c.Bool("all"), Check: c.Bool("check"),
	})
	for _, change := range update.Pins {
		if !change.Changed {
			fmt.Printf("  %s: already at %s (%s)\n", change.Name, change.After.Tag, change.After.Commit[:12])
		} else {
			fmt.Printf("  %s: %s -> %s (%s, epoch %d)\n", change.Name, orNone(change.Before.Tag), change.After.Tag, change.After.Commit[:12], change.After.SourceDateEpoch)
		}
	}
	var coded *e.Error
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
	ctx, cancel := context.WithTimeout(c.Context, c.Duration("timeout"))
	defer cancel()
	update, err := catalog.UpdateArtifactPins(ctx, catalog.ArtifactPinRequest{
		Directory: c.String("dir"), Components: c.Args().Slice(), All: c.Bool("all"),
		Architectures: c.StringSlice("arch"), Runtime: c.String("runtime"),
	})
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
