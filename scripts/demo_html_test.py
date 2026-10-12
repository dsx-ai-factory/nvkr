# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Keep renamed demo links and inline-script hashes consistent with their owners."""

import base64
import hashlib
from html.parser import HTMLParser
import json
from pathlib import Path
import unittest
from urllib.parse import unquote, urlsplit


class DemoHTML(HTMLParser):
    """Use the standard HTML parser to collect the checked-in page's declarations."""

    def __init__(self, text):
        super().__init__(convert_charrefs=False)
        self.policy = ""
        self.links = []
        self.scripts = []
        self.current = None
        self.feed(text)
        self.close()

    def handle_starttag(self, tag, attributes):
        attributes = dict(attributes)
        if "href" in attributes:
            self.links.append(attributes["href"])
        if tag == "meta" and attributes.get("http-equiv", "").lower() == "content-security-policy":
            self.policy = attributes.get("content", "")
        if tag == "script":
            self.current = [attributes, ""]

    def handle_data(self, data):
        if self.current is not None:
            self.current[1] += data

    def handle_endtag(self, tag):
        if tag == "script" and self.current is not None:
            self.scripts.append(self.current)
            self.current = None


class DemoPagesTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        root = Path(__file__).resolve().parents[1] / "demos"
        cls.pages = {path.name: DemoHTML(path.read_text(encoding="utf-8")) for path in sorted(root.glob("*.html"))}

    def test_inline_script_hashes(self):
        for name, page in self.pages.items():
            if not page.policy:
                continue
            directives = [directive.split() for directive in page.policy.split(";") if directive.strip()]
            sources = dict((tokens[0], tokens[1:]) for tokens in directives).get("script-src", [])
            if "'unsafe-inline'" in sources:
                continue
            for attributes, script in page.scripts:
                if attributes.get("src") or attributes.get("type") == "application/json":
                    continue
                with self.subTest(page=name):
                    digest = base64.b64encode(hashlib.sha256(script.encode("utf-8")).digest()).decode("ascii")
                    self.assertIn("'sha256-" + digest + "'", sources)

    def test_concepts_use_existing_source_secret(self):
        page = self.pages["demo-team-concepts.html"]
        examples = "\n".join(
            concept.get("example", "") + json.dumps(concept.get("secondaryExample", {}))
            for attributes, script in page.scripts
            if attributes.get("id") == "concept-data"
            for concept in json.loads(script)
        )
        self.assertIn("credentialRef: github:NKE_SOURCE_TOKEN", examples)
        self.assertNotIn("github:NVKE_SOURCE_TOKEN", examples)

    def test_saved_explorer_links(self):
        explorer = self.pages["demo-team-explorer.html"]
        records = {attributes["id"]: json.loads(script) for attributes, script in explorer.scripts
                   if attributes.get("type") == "application/json"}
        outputs = {
            "plan": {output["name"] for output in records["embedded-plan"]["lifecycleOutputs"]},
            "publication": {output["name"] for output in records["embedded-publication"]["outputs"]},
            "release": {output["name"] for output in records["embedded-release"]["outputs"]},
        }
        checked = 0
        for name, page in self.pages.items():
            links = list(page.links)
            for attributes, script in page.scripts:
                if attributes.get("id") == "concept-data":
                    links.extend(action["href"] for concept in json.loads(script) for action in concept.get("actions", []))
            for link in links:
                parsed = urlsplit(link)
                if parsed.scheme or parsed.netloc or Path(parsed.path).name != "demo-team-explorer.html":
                    continue
                view, separator, output = unquote(parsed.fragment).partition("/")
                if not separator:
                    continue
                with self.subTest(page=name, link=link):
                    self.assertIn(view, outputs)
                    self.assertIn(output, outputs[view])
                checked += 1
        self.assertGreater(checked, 0, "No focused saved-record links were checked")


if __name__ == "__main__":
    unittest.main()
