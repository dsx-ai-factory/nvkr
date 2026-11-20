// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

func cmdCatalogInventory(name string) *cli.Command {
	command := &cli.Command{Name: name, Flags: []cli.Flag{
		&cli.StringFlag{Name: "catalog", Aliases: []string{"data", "dir"}, Required: true, Usage: "catalog directory or root to scan offline"},
	}}
	if name == "find" {
		command.Usage = "find every exact component match with its catalog path and recorded pins"
		command.ArgsUsage = "<component>"
		command.Flags = append(command.Flags, &cli.StringFlag{Name: "format", Value: "text", Usage: "text or json"})
	} else {
		command.Usage = "print a generated Markdown catalog inventory"
	}
	command.Action = func(c *cli.Context) error {
		component := ""
		if name == "find" {
			if c.Args().Len() != 1 || c.Args().First() == "" {
				return e.New(e.CodeUsage, "catalog find requires exactly one component name")
			}
			component = c.Args().First()
		} else if err := rejectUnusedArguments(c, "catalog"); err != nil {
			return err
		}
		return presentCatalogInventory(c, component, "", "")
	}
	return command
}

func presentCatalogInventory(c *cli.Context, name, platform, outputType string) error {
	index := c.Command.Name == "index"
	format := c.String("format")
	if !index && format != "text" && format != "json" {
		return e.New(e.CodeUsage, "--format must be text or json, got %q", format)
	}
	view, err := selectCommandPresentation(c)
	if err != nil {
		return err
	}
	if index && view.tui {
		return e.New(e.CodeUsage, "catalog index emits Markdown; use --ui plain")
	}
	display := catalogCommandDisplay("Catalog inventory")
	display.Note = "Recorded declarations and pins only; no producer selection or upstream pin-set verification."
	entries, err := catalog.InventoryContext(c.Context, c.String("catalog"))
	if err != nil {
		return showCommandResult(c, view, display, err)
	}
	entries, err = catalog.FilterInventory(entries, name, platform, outputType)
	if err != nil {
		return showCommandResult(c, view, display, err)
	}
	if index {
		_, err := os.Stdout.Write(catalog.RenderInventoryIndex(entries))
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "writing catalog index")
		}
		return nil
	}
	if format == "json" {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(entries); err != nil {
			return e.Wrap(e.CodeInternal, err, "writing catalog inventory JSON")
		}
		return nil
	}
	if view.tui {
		display.Fields = []progress.Field{{Label: "Scanned root", Value: c.String("catalog")}, {Label: "Catalogs", Value: strconv.Itoa(len(entries))}}
		for _, entry := range entries {
			for _, component := range entry.Components {
				item := progress.Item{ID: entry.Path + "/" + component.Name, Name: component.Name, Kind: component.OutputType, State: "recorded", Fields: []progress.Field{{Label: "Catalog path", Value: entry.Path}}}
				for _, field := range component.Fields() {
					item.Fields = append(item.Fields, progress.Field{Label: field.Label, Value: field.Value})
				}
				display.Items = append(display.Items, item)
			}
		}
		return showCommandResult(c, view, display, nil)
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "catalog root: %q\n%s\n", c.String("catalog"), display.Note)
	for _, entry := range entries {
		fmt.Fprintf(&out, "\ncatalog: %q (%q)\n", entry.Path, entry.Name)
		for _, component := range entry.Components {
			if name == "" {
				fmt.Fprintf(&out, "  %q  %s  %s\n", component.Name, component.Mode, component.OutputType)
				continue
			}
			raw, err := component.Render("text")
			if err != nil {
				return err
			}
			out.Write(raw)
		}
	}
	if _, err := os.Stdout.Write(out.Bytes()); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing catalog inventory")
	}
	return nil
}
