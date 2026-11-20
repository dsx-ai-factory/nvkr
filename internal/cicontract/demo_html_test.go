// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"
)

type demoScript struct {
	attributes map[string]string
	text       string
}
type demoPage struct {
	policy  string
	links   []string
	scripts []demoScript
}

func demoPages(t *testing.T) map[string]demoPage {
	t.Helper()
	paths, err := filepath.Glob("../../demos/*.html")
	if err != nil || len(paths) == 0 {
		t.Fatalf("demo pages: %v", err)
	}
	pages := make(map[string]demoPage, len(paths))
	for _, path := range paths {
		var page demoPage
		var script *demoScript
		raw := readFile(t, path)
		if !utf8.Valid(raw) {
			t.Fatalf("%s is not UTF-8", path)
		}
		// Match the original text-mode reader's universal newline conversion.
		text := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(raw))
		tokens := html.NewTokenizer(strings.NewReader(text))
		for {
			kind := tokens.Next()
			if kind == html.ErrorToken {
				if err := tokens.Err(); err != io.EOF {
					t.Fatalf("parse %s: %v", path, err)
				}
				break
			}
			switch kind {
			case html.StartTagToken, html.SelfClosingTagToken:
				token := tokens.Token()
				attrs := map[string]string{}
				for _, attr := range token.Attr {
					attrs[attr.Key] = attr.Val
				}
				if href, ok := attrs["href"]; ok {
					page.links = append(page.links, href)
				}
				if token.Data == "meta" && strings.EqualFold(attrs["http-equiv"], "content-security-policy") {
					page.policy = attrs["content"]
				}
				if token.Data == "script" {
					script = &demoScript{attributes: attrs}
				}
			case html.TextToken:
				if script != nil {
					script.text += string(tokens.Text())
				}
			case html.EndTagToken:
				if tokens.Token().Data == "script" && script != nil {
					page.scripts = append(page.scripts, *script)
					script = nil
				}
			}
		}
		pages[filepath.Base(path)] = page
	}
	return pages
}

func TestDemoHTMLScriptHashes(t *testing.T) {
	for name, page := range demoPages(t) {
		t.Run(name, func(t *testing.T) {
			if page.policy == "" {
				return
			}
			var sources []string
			for _, directive := range strings.Split(page.policy, ";") {
				fields := strings.Fields(directive)
				if len(fields) > 0 && fields[0] == "script-src" {
					sources = fields[1:]
				}
			}
			if slices.Contains(sources, "'unsafe-inline'") {
				return
			}
			for _, script := range page.scripts {
				if script.attributes["src"] != "" || script.attributes["type"] == "application/json" {
					continue
				}
				digest := sha256.Sum256([]byte(script.text))
				want := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
				if !slices.Contains(sources, want) {
					t.Errorf("inline script is missing CSP hash %s", want)
				}
			}
		})
	}
}

type demoConcept struct {
	Example          string `json:"example"`
	SecondaryExample any    `json:"secondaryExample"`
	Actions          []struct {
		Href string `json:"href"`
	} `json:"actions"`
}

func demoConcepts(t *testing.T, script demoScript) []demoConcept {
	t.Helper()
	var concepts []demoConcept
	if err := json.Unmarshal([]byte(script.text), &concepts); err != nil {
		t.Fatal(err)
	}
	return concepts
}

func TestDemoHTMLConceptSources(t *testing.T) {
	page, ok := demoPages(t)["demo-team-concepts.html"]
	if !ok {
		t.Fatal("missing concepts demo")
	}
	for _, script := range page.scripts {
		if script.attributes["id"] != "concept-data" {
			continue
		}
		for _, concept := range demoConcepts(t, script) {
			secondary, err := json.Marshal(concept.SecondaryExample)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"sourceAccess:", "github:NVKE_SOURCE_TOKEN"} {
				if strings.Contains(concept.Example+string(secondary), forbidden) {
					t.Errorf("concept example contains %s", forbidden)
				}
			}
		}
	}
}

func TestDemoHTMLSavedExplorerLinks(t *testing.T) {
	pages := demoPages(t)
	explorer, ok := pages["demo-team-explorer.html"]
	if !ok {
		t.Fatal("missing explorer demo")
	}
	outputs := map[string]map[string]bool{}
	for _, script := range explorer.scripts {
		if script.attributes["type"] != "application/json" {
			continue
		}
		view := strings.TrimPrefix(script.attributes["id"], "embedded-")
		if !slices.Contains([]string{"plan", "publication", "release"}, view) {
			continue
		}
		var record struct {
			LifecycleOutputs []struct{ Name string } `json:"lifecycleOutputs"`
			Outputs          []struct{ Name string } `json:"outputs"`
		}
		if err := json.Unmarshal([]byte(script.text), &record); err != nil {
			t.Fatal(err)
		}
		selected := record.Outputs
		if view == "plan" {
			selected = record.LifecycleOutputs
		}
		if selected == nil {
			t.Fatalf("embedded %s record has no outputs array", view)
		}
		outputs[view] = map[string]bool{}
		for _, output := range selected {
			outputs[view][output.Name] = true
		}
	}
	for _, view := range []string{"plan", "publication", "release"} {
		if outputs[view] == nil {
			t.Fatalf("missing embedded %s record", view)
		}
	}
	checked := 0
	for name, page := range pages {
		links := slices.Clone(page.links)
		for _, script := range page.scripts {
			if script.attributes["id"] == "concept-data" {
				for _, concept := range demoConcepts(t, script) {
					for _, action := range concept.Actions {
						links = append(links, action.Href)
					}
				}
			}
		}
		for _, link := range links {
			parsed, err := url.Parse(link)
			if err != nil {
				t.Fatalf("%s link %q: %v", name, link, err)
			}
			if parsed.Scheme != "" || parsed.Host != "" || filepath.Base(parsed.EscapedPath()) != "demo-team-explorer.html" {
				continue
			}
			view, output, focused := strings.Cut(parsed.Fragment, "/")
			if !focused {
				continue
			}
			if !outputs[view][output] {
				t.Errorf("%s link %q has no saved %s output %q", name, link, view, output)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no focused saved-record links were checked")
	}
}
