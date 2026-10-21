// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChartProjectionIsNarrow(t *testing.T) {
	for _, item := range []struct {
		name, input string
		valid       bool
	}{{"repository", "chart:\n  name: spire\n  repository: upstream\nvalues:\n  - existing.yaml\n", true}, {"path", "chart:\n  path: upstream.tgz\n", true}, {"missing", "chart:\n  name: spire\n", false}, {"ambiguous", "chart:\n  path: x\n  repository: y\n", false}} {
		t.Run(item.name, func(t *testing.T) {
			raw, err := projectChartSource([]byte(item.input), "charts/spire.tgz")
			if (err == nil) != item.valid {
				t.Fatalf("projection:%v", err)
			}
			if item.valid && !strings.Contains(string(raw), "  path: charts/spire.tgz\n") {
				t.Fatal("missing exact chart source")
			}
		})
	}
}

func TestChartPreparationPreservesSourceAndAdmission(t *testing.T) {
	for _, test := range []string{"exact", "localVersion", "missingLocalVersion", "versionMismatch", "wrongPin", "dirty", "readerChanged", "missingChart", "extraChart", "missingPin", "duplicateItems", "badReader", "symlinkSource", "extractFailure", "unjoined", "existingOutput", "outputSymlink", "sourceOutput", "layoutOutput", "changedSemantics"} {
		t.Run(test, func(t *testing.T) {
			base := t.TempDir()
			checkout := filepath.Join(base, "nvke")
			reader := filepath.Join(checkout, "deploy/lib/nvke-deploy-items.py")
			if err := os.MkdirAll(filepath.Dir(reader), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(reader, []byte("pinned reader"), 0o600); err != nil {
				t.Fatal(err)
			}
			reader, err := filepath.EvalSymlinks(reader)
			if err != nil {
				t.Fatal(err)
			}
			tool := filepath.Join(base, "nvkr")
			if err := os.WriteFile(tool, nil, 0o700); err != nil {
				t.Fatal(err)
			}
			tool, _ = resolveExisting(tool)
			charts := make(map[string]string)
			pins := make(map[string]string)
			versions := make(map[string]string)
			sources := map[string]string{"nvke-deploy.yaml": "items: pinned\n"}
			for _, name := range []string{"controller", "spire"} {
				layout := filepath.Join(base, name)
				if err := os.Mkdir(layout, 0o700); err != nil {
					t.Fatal(err)
				}
				charts[name] = layout
				pins[name] = "sha256:" + strings.Repeat("a", 64)
				sources[name+"/release.yaml"] = "chart:\n  name: " + name + "\n  version: 1.2.3\n  repository: upstream\nvalues:\n  - values.yaml\n"
				sources[name+"/values.yaml"] = "preserve: exact\n"
			}
			if test == "localVersion" || test == "missingLocalVersion" {
				sources["controller/release.yaml"] = "chart:\n  name: controller\n  path: ../../controller\nvalues:\n  - values.yaml\n"
				if test == "localVersion" {
					versions["controller"] = "0.22.0-beta.1"
				}
			}
			if test == "versionMismatch" {
				versions["controller"] = "9.9.9"
			}
			out := filepath.Join(base, "prepared")
			switch test {
			case "missingChart":
				delete(charts, "spire")
			case "extraChart":
				charts["extra"] = base
			case "missingPin":
				delete(pins, "spire")
			case "existingOutput":
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
			case "outputSymlink":
				if err := os.Symlink(filepath.Join(base, "not-created"), out); err != nil {
					t.Fatal(err)
				}
			case "sourceOutput":
				out = filepath.Join(checkout, "prepared")
			case "layoutOutput":
				out = filepath.Join(charts["controller"], "prepared")
			}
			capture := func(_ context.Context, args []string, _ string, _ time.Duration) ([]byte, error) {
				if args[0] == "git" {
					switch args[3] {
					case "rev-parse":
						if test == "wrongPin" {
							return []byte("wrong"), nil
						}
						return []byte(InstallerCommit), nil
					case "status":
						if test == "dirty" {
							return []byte("dirty"), nil
						}
						return nil, nil
					case "show":
						if strings.HasSuffix(args[4], ":deploy/lib/nvke-deploy-items.py") {
							if test == "readerChanged" {
								return []byte("different"), nil
							}
							return []byte("pinned reader"), nil
						}
						path := strings.SplitN(args[4], ":deploy/nico/", 2)[1]
						return []byte(sources[path]), nil
					case "ls-tree":
						mode := "100644"
						if test == "symlinkSource" {
							mode = "120000"
						}
						var listed strings.Builder
						for path := range sources {
							listed.WriteString(mode + " blob " + strings.Repeat("b", 40) + "\tdeploy/nico/" + path + "\x00")
						}
						return []byte(listed.String()), nil
					}
				}
				if args[0] == tool {
					if test == "localVersion" && strings.Contains(args[len(args)-1], "controller.tgz") && args[len(args)-3] != "0.22.0-beta.1" {
						t.Fatal("local chart did not use its selected observed version")
					}
					if test == "extractFailure" {
						return nil, errors.New("extract failed")
					}
					if test == "unjoined" {
						return nil, ErrUnjoined
					}
					if err := os.WriteFile(args[len(args)-1], []byte("exact chart bytes"), 0o600); err != nil {
						return nil, err
					}
					return []byte("{}"), nil
				}
				if args[0] == "python3" {
					if args[1] != reader {
						t.Fatalf("wrong pinned reader: %v", args)
					}
					switch args[2] {
					case "items":
						listing := "controller\thelm\nspire\thelm\n"
						if test == "duplicateItems" {
							listing += "controller\thelm\n"
						}
						return []byte(listing), nil
					case "release":
						if test == "badReader" {
							return []byte("unsupported"), nil
						}
						name := filepath.Base(filepath.Dir(args[3]))
						raw, err := os.ReadFile(args[3])
						if err != nil {
							return nil, err
						}
						repository, path, version := "upstream", "", "1.2.3"
						if name == "controller" && (test == "localVersion" || test == "missingLocalVersion") {
							repository, path, version = "", "../../controller", ""
						}
						if strings.Contains(string(raw), "  path: charts/") {
							repository = ""
							path = "charts/" + name + ".tgz"
						}
						namespace := "test"
						if test == "changedSemantics" && path != "" {
							namespace = "foreign"
						}
						return []byte(strings.Join([]string{"single", name, version, repository, path, name, namespace, "values.yaml", "gate", "chart-artifact", "controller-image", "profiles-image", "ca=/owned/ca.pem"}, "\x1f")), nil
					}
				}
				t.Fatalf("unexpected external call:%v", args)
				return nil, nil
			}
			deploy, err := prepareChartInputs(context.Background(), ChartInputsConfig{NVKECheckout: checkout, NVKR: tool, Out: out, Charts: charts, Manifests: pins, Versions: versions}, capture)
			if test == "exact" || test == "localVersion" {
				if err != nil {
					t.Fatal(err)
				}
				raw, readErr := os.ReadFile(filepath.Join(deploy, "controller/values.yaml"))
				if readErr != nil || string(raw) != "preserve: exact\n" {
					t.Fatal("changed values")
				}
				raw, readErr = os.ReadFile(filepath.Join(deploy, "controller/release.yaml"))
				if readErr != nil || !strings.Contains(string(raw), "  path: charts/controller.tgz\n") {
					t.Fatal("missing prepared source")
				}
			} else {
				if err == nil {
					t.Fatal("unsafe input accepted")
				}
				if !strings.Contains(test, "Output") && test != "outputSymlink" {
					if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
						t.Fatal("published partial inputs")
					}
				}
			}
			stages, _ := filepath.Glob(filepath.Join(base, ".nvkr-chart-inputs-*"))
			if test == "unjoined" {
				if len(stages) != 1 {
					t.Fatal("unjoined stage removed")
				}
			} else if len(stages) != 0 {
				t.Fatal("private stage leaked")
			}
			actual, _ := os.ReadFile(reader)
			if string(actual) != "pinned reader" {
				t.Fatal("modified NVKE checkout")
			}
		})
	}
}
