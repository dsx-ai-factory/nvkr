// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"maps"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

const githubCIStorageInput = "nvkr-ci-storage-write-token"
const githubCIReadInput = "nvkr-ci-storage-read-token"
const githubCIAlwaysInput = "nvkr-ci-always"

// Keep the generated upload budget within the serial locator acquisition bound.
const maxGitHubLocatorArtifacts = cistore.MaxGitHubLocatorArtifacts

const githubCIUploadCondition = "${{ always() && (inputs.nvkr-ci-always == 'true' || success()) }}"
const githubCIStoreCondition = "${{ always() && steps.directory.outcome == 'success' && (inputs.nvkr-ci-always == 'true' || success()) }}"
const githubCIIndexCondition = "${{ always() && steps.store.outcome == 'success' && steps.store.outputs.locator_path != '' && (inputs.nvkr-ci-always == 'true' || success()) }}"

type githubCITransfer struct{ upload, download bool }

func githubCIUserEnvironment() map[string]string {
	return map[string]string{
		"NVKR_CI_STORAGE_READ_USER":  "${{ vars.NVKR_CI_STORAGE_READ_USER }}",
		"NVKR_CI_STORAGE_WRITE_USER": "${{ vars.NVKR_CI_STORAGE_WRITE_USER }}",
	}
}

// Composite action manifests cannot resolve the workflow-only vars context.
// Bind the public account names once in the entry workflow; only the leaf
// transfer command selects its account. Tokens remain explicit step inputs.
func admitGitHubCIUserEnvironment(documents map[string]*yamlv3.Node, entry string) error {
	root := documents[entry].Content[0]
	expected := githubCIUserEnvironment()
	if !githubInterfaceEnvironmentEqual(githubYAMLField(root, "env"), expected) {
		return e.New(e.CodeEngineCapability, "private transport requires its exact workflow account bindings")
	}
	var visit func(*yamlv3.Node) error
	visit = func(node *yamlv3.Node) error {
		if node != root {
			for key := range expected {
				if githubYAMLField(githubYAMLField(node, "env"), key) != nil {
					return e.New(e.CodeEngineCapability, "private transport account binding must not be overridden below workflow scope")
				}
			}
		}
		for _, child := range node.Content {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range sortedKeys(documents) {
		if err := visit(documents[name]); err != nil {
			return err
		}
	}
	return nil
}

// Credentials are distinct explicit inputs and never add package authority to
// the repository token. Complete sequence sharing compares both bindings.
func githubCISequenceArgumentLength(node *yamlv3.Node) int {
	length := githubYAMLLength(node)
	for _, key := range []string{githubCIReadInput, githubCIStorageInput} {
		if githubYAMLValue(node, key) == githubCICredential(key, true) {
			length -= 2
		}
	}
	return length
}

func githubCICredential(input string, caller bool) string {
	if !caller {
		return "${{ inputs." + input + " }}"
	}
	if input == githubCIReadInput {
		return "${{ secrets.NVKR_CI_STORAGE_READ_TOKEN }}"
	}
	return "${{ secrets.NVKR_CI_STORAGE_WRITE_TOKEN }}"
}

func (t githubCITransfer) inputs() []string {
	var result []string
	if t.download {
		result = append(result, githubCIReadInput)
	}
	if t.upload {
		result = append(result, githubCIStorageInput)
	}
	return result
}

func githubStagePermissions(plan *Plan, stage githubStage) (map[string]string, error) {
	node := stage.Nodes[0]
	if signFamilyNode(node.Kind) {
		if plan.Environment.Signing == nil {
			return nil, e.New(e.CodeTrustPolicyMissing, "GitHub node %q signs without a delivery signing profile", node.ID)
		}
		return githubSigningPermissionsFor(plan, node.Kind), nil
	}
	if node.Kind == NodePublish {
		permissions := map[string]string{"contents": "read"}
		target := plan.Targets[node.Target]
		if target.CredentialRef != "" && strings.HasPrefix(target.Ref, "ghcr.io/") && !plan.Environment.GitHubDevelopment {
			permissions["packages"] = "write"
		}
		return permissions, nil
	}
	if len(githubStagePrivateImages(plan, stage)) > 0 {
		return map[string]string{"contents": "read", "packages": "read"}, nil
	}
	return nil, nil
}

func githubFinalizerPermissions(plan *Plan) map[string]string {
	if PerOutputPublication(plan) {
		permissions := map[string]string{"contents": "read"}
		if !plan.Environment.GitHubDevelopment {
			permissions["packages"] = "read"
		}
		return permissions
	}
	if GitHubDiskBuildOnly(plan) {
		return map[string]string{"contents": "read"}
	}
	return nil
}

func writeGitHubPermissions(out *bytes.Buffer, permissions map[string]string) {
	if permissions == nil {
		return
	}
	fmt.Fprintln(out, "    permissions:")
	for _, name := range sortedKeys(permissions) {
		fmt.Fprintf(out, "      %s: %s\n", name, permissions[name])
	}
}

func githubCITransferPath(plan *Plan, mode string) string {
	return githubActionsDirectoryFor(plan) + "ci-" + mode
}

func githubCITransferMode(plan *Plan, uses string) string {
	for _, mode := range []string{"upload", "download", "bootstrap-download"} {
		if uses == "./"+githubCITransferPath(plan, mode) {
			return mode
		}
	}
	return ""
}

// The source bootstrap retries only the observed public-proxy module body
// failure, then compiles once offline. Every child shares one deadline and is
// stopped and joined before its disposable caches are removed.
func githubCIBootstrapBuildCommand(linker string) string {
	return fmt.Sprintf(`set +x +v
set -eu
if ! command -v python3 >/dev/null 2>&1; then
  printf '%[1]s: private bootstrap requires Python 3\n' >&2
  exit %[2]d
fi
exec python3 -I - %[3]s <<'PY'
import json
import os
import re
import selectors
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time

canceled = False

def stop(_signal, _frame):
    global canceled
    canceled = True

signal.signal(signal.SIGINT, stop)
signal.signal(signal.SIGTERM, stop)

def check_deadline(deadline):
    if canceled or time.monotonic() >= deadline:
        raise TimeoutError("bootstrap canceled or timed out")

def stop_child(child):
    try:
        os.killpg(child.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    except OSError:
        print("%[4]s: private bootstrap child could not be stopped", file=sys.stderr, flush=True)
        os._exit(%[5]d)
    try:
        child.wait(timeout=5)
    except (OSError, subprocess.TimeoutExpired):
        # Retain caches if the child cannot be joined; never delete beneath it.
        print("%[4]s: private bootstrap child did not stop", file=sys.stderr, flush=True)
        os._exit(%[5]d)

def run(args, env, deadline, capture=False):
    check_deadline(deadline)
    output = [bytearray(), bytearray()]
    child = subprocess.Popen(args, env=env, stdin=subprocess.DEVNULL,
                             stdout=subprocess.PIPE if capture else None,
                             stderr=subprocess.PIPE if capture else None, start_new_session=True)
    joined = False
    try:
        with selectors.DefaultSelector() as streams:
            if capture:
                streams.register(child.stdout, selectors.EVENT_READ, 0)
                streams.register(child.stderr, selectors.EVENT_READ, 1)
            while True:
                check_deadline(deadline)
                if child.poll() is not None and not joined:
                    stop_child(child)
                    joined = True
                if joined and not streams.get_map():
                    return child.returncode, bytes(output[0]), bytes(output[1])
                if not capture:
                    try:
                        child.wait(timeout=0.2)
                    except subprocess.TimeoutExpired:
                        pass
                    continue
                for key, _events in streams.select(timeout=0.2):
                    chunk = os.read(key.fd, 65536)
                    if not chunk:
                        streams.unregister(key.fileobj)
                    elif len(output[0]) + len(output[1]) + len(chunk) > 4 * 1024 * 1024:
                        raise ValueError("module acquisition output exceeds its bound")
                    else:
                        output[key.data].extend(chunk)
    finally:
        if not joined:
            stop_child(child)
        for pipe in (child.stdout, child.stderr):
            if pipe is not None:
                pipe.close()

def manifests(directory="."):
    result = []
    for name in ("go.mod", "go.sum"):
        with os.fdopen(os.open(os.path.join(directory, name), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as source:
            if not stat.S_ISREG(os.fstat(source.fileno()).st_mode):
                raise ValueError("source module manifest is not regular")
            raw = source.read(4 * 1024 * 1024 + 1)
            if len(raw) > 4 * 1024 * 1024:
                raise ValueError("source module manifest exceeds its bound")
            result.append(raw)
    return result

def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate module result field")
        result[key] = value
    return result

def invalid_constant(_value):
    raise ValueError("non-JSON module result constant")

def module_errors(raw):
    decoder = json.JSONDecoder(object_pairs_hook=unique_object, parse_constant=invalid_constant)
    pending, seen, errors = raw.decode("utf-8").strip(), set(), []
    while pending:
        item, end = decoder.raw_decode(pending)
        if not isinstance(item, dict):
            raise ValueError("invalid module acquisition result")
        path, version, error = item.get("Path"), item.get("Version"), item.get("Error", "")
        if not isinstance(path, str) or not isinstance(version, str) or not isinstance(error, str) or not path or not version or (path, version) in seen:
            raise ValueError("invalid module acquisition identity")
        seen.add((path, version))
        if error:
            escaped = lambda value: "".join("!" + c.lower() if "A" <= c <= "Z" else c for c in value)
            url = "https://proxy.golang.org/" + escaped(path) + "/@v/" + escaped(version) + ".zip"
            prefix = path + "@" + version + ": "
            detail = error[len(prefix):] if error.startswith(prefix) else error
            retryable = re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._~+/-]*', path) and all(part not in ("", ".", "..") for part in path.split("/")) and re.fullmatch(r'v[A-Za-z0-9._~+-]+', version) and re.fullmatch(
                r'read "' + re.escape(url) + r'": stream error: stream ID [1-9][0-9]*; INTERNAL_ERROR; received from peer', detail)
            errors.append(bool(retryable))
        pending = pending[end:].lstrip()
    if not seen:
        raise ValueError("module acquisition omitted its results")
    return errors

def build(cache):
    # Never pass repository, registry or OIDC credentials to either Go process.
    env = {key: os.environ[key] for key in ("PATH", "HOME", "TMPDIR", "GOROOT") if key in os.environ}
    env.update(CGO_ENABLED="0", GOTOOLCHAIN="local", GOENV="off", GOWORK="off",
               GOCACHE=os.path.join(cache, "build"), GOMODCACHE=os.path.join(cache, "modules"))
    deadline = time.monotonic() + 900
    check_deadline(deadline)
    original = manifests()
    # Go may normalize module manifests. Private copies keep the checked-out
    # source untouched; both must remain byte-exact through download and build.
    inputs = os.path.join(cache, "inputs")
    os.mkdir(inputs)
    for name, raw in zip(("go.mod", "go.sum"), original):
        with open(os.path.join(inputs, name), "xb") as destination:
            destination.write(raw)
    modfile = "-modfile=" + os.path.join(inputs, "go.mod")
    for attempt in range(3):
        result, stdout, stderr = run(["go", "mod", "download", "-json", modfile], env, deadline, capture=True)
        if manifests() != original or manifests(inputs) != original:
            raise ValueError("module acquisition changed source manifests")
        errors = module_errors(stdout)
        if result == 0 and not errors and not stderr.strip():
            break
        if result != 1 or not errors or not all(errors) or stderr.strip() or attempt == 2:
            raise ValueError("module acquisition failed")
        print("Retrying public Go module acquisition after an HTTP/2 peer error", file=sys.stderr)
        until = time.monotonic() + attempt + 1
        while time.monotonic() < until:
            check_deadline(deadline)
            time.sleep(min(0.2, max(0, until - time.monotonic())))
    # All module bytes were acquired and checked first. Compilation is attempted
    # once, with proxy and checksum-database network access disabled.
    env.update(GOPROXY="off", GOSUMDB="off")
    os.makedirs(".nvkr/ci-bootstrap", exist_ok=True)
    result, _stdout, _stderr = run(["go", "build", "-mod=readonly", modfile, "-trimpath", "-ldflags", sys.argv[1],
                                  "-o", ".nvkr/ci-bootstrap/nvkr-exec", "./cmd/nvkr"], env, deadline)
    if manifests() != original or manifests(inputs) != original or result != 0:
        raise ValueError("private bootstrap compilation failed or changed source manifests")
    return 0

runner_temp = os.environ.get("RUNNER_TEMP", "")
if not os.path.isabs(runner_temp) or not os.path.isdir(runner_temp) or shutil.which("go") is None:
    print("%[1]s: private bootstrap requires a runner temporary directory and Go", file=sys.stderr)
    sys.exit(%[2]d)
try:
    with tempfile.TemporaryDirectory(prefix="nvkr-ci-bootstrap-", dir=runner_temp) as cache:
        result = build(cache)
except TimeoutError:
    print("%[4]s: private bootstrap acquisition or compilation canceled or timed out", file=sys.stderr)
    result = %[5]d
except (OSError, ValueError, RecursionError):
    print("%[6]s: private bootstrap acquisition, compilation or cache cleanup failed", file=sys.stderr)
    result = %[7]d
if canceled and result != %[5]d:
    print("%[4]s: private bootstrap canceled", file=sys.stderr)
    result = %[5]d
sys.exit(result)
PY`, e.CodeEngineCapability, e.ExitCode(e.New(e.CodeEngineCapability, "")), shellQuote(linker),
		e.CodeTimeout, e.ExitCode(e.New(e.CodeTimeout, "")), e.CodeBuildFailed, e.ExitCode(e.New(e.CodeBuildFailed, "")))
}

func githubCITransferTemplates(plan *Plan, actions githubActionRefs) (map[string][]byte, error) {
	linker, err := generatorLinkerFlags(plan.GeneratorVersion)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, mode := range []string{"upload", "download", "bootstrap-download"} {
		credentialInput := githubCIReadInput
		if mode == "upload" {
			credentialInput = githubCIStorageInput
		}
		inputs := githubYAMLMap(credentialInput, githubActionInput("Dedicated private CI storage credential, scoped only to transfer steps"))
		if mode == "upload" {
			githubYAMLSet(inputs, githubCIAlwaysInput, githubYAMLMap("description", githubYAMLString("Preserve the caller's admitted always-run upload through cancellation"), "required", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "false"}, "default", githubYAMLString("false")))
		}
		fields := []string{"name", "path", "pattern", "merge-multiple", "if-no-files-found", "include-hidden-files", "retention-days", "run-id", "attempt"}
		for _, field := range fields {
			githubYAMLSet(inputs, field, githubYAMLMap("description", githubYAMLString("Original generated transport setting "+field), "required", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "false"}))
		}
		if mode == "download" {
			githubYAMLSet(inputs, githubCIRestoreStageInput, githubYAMLMap("description", githubYAMLString("Exact Plan stage whose carriers are streamed into their final roots"), "required", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "false"}))
		}
		steps := &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq"}
		if mode == "bootstrap-download" {
			steps.Content = append(steps.Content,
				githubYAMLMap("name", githubYAMLString("Provision the pinned Go bootstrap"), "uses", githubYAMLString(actions.SetupGo), "with", githubYAMLMap("go-version-file", githubYAMLString(".go-version"), "cache", githubYAMLString("false"))),
				githubYAMLMap("name", githubYAMLString("Build private transport helper from this checkout"), "shell", githubYAMLString("bash"), "env", githubYAMLMap("GOTOOLCHAIN", githubYAMLString("local")), "run", githubYAMLString(githubCIBootstrapBuildCommand(linker))))
		}
		steps.Content = append(steps.Content, githubYAMLMap("name", githubYAMLString("Create a fresh locator directory"), "id", githubYAMLString("directory"), "shell", githubYAMLString("bash"), "run", githubYAMLString("set -eu\ndirectory=$(mktemp -d \"$RUNNER_TEMP/nvkr-ci-index.XXXXXX\")\nprintf 'path=%s\\n' \"$directory\" >> \"$GITHUB_OUTPUT\"")))
		username := "${{ env.NVKR_CI_STORAGE_READ_USER }}"
		if mode == "upload" {
			username = "${{ env.NVKR_CI_STORAGE_WRITE_USER }}"
		}
		env := githubYAMLMap("NVKR_CI_STORAGE_TOKEN", githubYAMLString("${{ inputs."+credentialInput+" }}"), "NVKR_CI_REPOSITORY_TOKEN", githubYAMLString("${{ github.token }}"), "NVKR_CI_STORAGE_USER", githubYAMLString(username))
		outputs := githubYAMLMap()
		if mode == "upload" {
			githubYAMLSet(steps.Content[0], "if", githubYAMLString(githubCIUploadCondition))
			for _, field := range []struct{ env, input string }{{"NVKR_CI_NAME", "name"}, {"NVKR_CI_PATH", "path"}, {"NVKR_CI_MISSING", "if-no-files-found"}, {"NVKR_CI_HIDDEN", "include-hidden-files"}} {
				githubYAMLSet(env, field.env, githubYAMLString("${{ inputs."+field.input+" }}"))
			}
			githubYAMLSet(env, "NVKR_CI_INDEX", githubYAMLString("${{ steps.directory.outputs.path }}"))
			steps.Content = append(steps.Content, githubYAMLMap("name", githubYAMLString("Admit public metadata or store private payload"), "id", githubYAMLString("store"), "if", githubYAMLString(githubCIStoreCondition), "shell", githubYAMLString("bash"), "env", env, "run", githubYAMLString("set -eu\n./nvkr-exec factory github ci-upload --plan "+shellQuote(githubLayoutFor(plan).planPath)+" --name \"$NVKR_CI_NAME\" --path \"$NVKR_CI_PATH\" --if-no-files-found \"${NVKR_CI_MISSING:-error}\" --include-hidden-files=\"${NVKR_CI_HIDDEN:-false}\" --out \"$NVKR_CI_INDEX\"")))
			steps.Content = append(steps.Content, githubYAMLMap("name", githubYAMLString("Upload validated metadata or private locator"), "id", githubYAMLString("index"), "if", githubYAMLString(githubCIIndexCondition), "uses", githubYAMLString(actions.Upload), "with", githubYAMLMap("name", githubYAMLString("${{ inputs.name }}"), "path", githubYAMLString("${{ steps.store.outputs.locator_path }}"), "include-hidden-files", githubYAMLString("true"), "if-no-files-found", githubYAMLString("error"), "retention-days", githubYAMLString("${{ inputs.retention-days || '7' }}"))))
			for _, field := range []string{"artifact-id", "artifact-url", "artifact-digest"} {
				githubYAMLSet(outputs, field, githubYAMLMap("description", githubYAMLString("Locator index "+field), "value", githubYAMLString("${{ steps.index.outputs."+field+" }}")))
			}
		} else {
			githubYAMLSet(env, "NVKR_CI_INDEX", githubYAMLString("${{ steps.directory.outputs.path }}"))
			githubYAMLSet(env, "NVKR_CI_DESTINATION", githubYAMLString("${{ inputs.path }}"))
			githubYAMLSet(env, "NVKR_CI_RUN", githubYAMLString("${{ inputs.run-id }}"))
			githubYAMLSet(env, "NVKR_CI_ATTEMPT", githubYAMLString("${{ inputs.attempt }}"))
			helper := "./nvkr-exec"
			if mode == "bootstrap-download" {
				helper = "./.nvkr/ci-bootstrap/nvkr-exec"
			}
			locatorEnv := githubYAMLMap("NVKR_CI_REPOSITORY_TOKEN", githubYAMLString("${{ github.token }}"),
				"NVKR_CI_INDEX", githubYAMLString("${{ steps.directory.outputs.path }}"),
				"NVKR_CI_NAME", githubYAMLString("${{ inputs.name }}"), "NVKR_CI_PATTERN", githubYAMLString("${{ inputs.pattern }}"),
				"NVKR_CI_MERGE", githubYAMLString("${{ inputs.merge-multiple || 'false' }}"),
				"NVKR_CI_RUN", githubYAMLString("${{ inputs.run-id }}"), "NVKR_CI_ATTEMPT", githubYAMLString("${{ inputs.attempt }}"))
			locatorCommand := "exec " + helper + " factory github ci-fetch-locators --index-dir \"$NVKR_CI_INDEX\" --name \"$NVKR_CI_NAME\" --pattern \"$NVKR_CI_PATTERN\" --merge-multiple=\"$NVKR_CI_MERGE\" --run-id \"$NVKR_CI_RUN\" --attempt \"$NVKR_CI_ATTEMPT\""
			// The Environment enables reuse only for implicit current-Run reads.
			// Development namespaces and explicit cross-Run consumers stay exact.
			retrySetup := fmt.Sprintf("set -eu\nallow_earlier=%t\nif [ -n \"$NVKR_CI_RUN\" ]; then allow_earlier=false; fi\n", !plan.Environment.GitHubDevelopment)
			retryPolicy := " --allow-earlier-attempts=\"$allow_earlier\""
			locatorCommand = retrySetup + locatorCommand + retryPolicy
			steps.Content = append(steps.Content, githubYAMLMap("name", githubYAMLString("Download metadata and private locators"), "shell", githubYAMLString("bash"), "env", locatorEnv, "run", githubYAMLString(locatorCommand)))
			download := helper + " factory github ci-download --index-dir \"$NVKR_CI_INDEX\" --destination \"${NVKR_CI_DESTINATION:-.}\" --run-id \"$NVKR_CI_RUN\" --attempt \"$NVKR_CI_ATTEMPT\""
			download += retryPolicy
			command := retrySetup
			if mode == "download" {
				githubYAMLSet(env, "NVKR_CI_STAGE", githubYAMLString("${{ inputs."+githubCIRestoreStageInput+" }}"))
				command += "if [ -n \"$NVKR_CI_STAGE\" ]; then\n  exec " + download + " --plan " + shellQuote(githubLayoutFor(plan).planPath) + " --stage \"$NVKR_CI_STAGE\"\nfi\n"
			}
			command += "exec " + download
			steps.Content = append(steps.Content, githubYAMLMap("name", githubYAMLString("Restore metadata and private payloads"), "shell", githubYAMLString("bash"), "env", env, "run", githubYAMLString(command)))
			githubYAMLSet(outputs, "download-path", githubYAMLMap("description", githubYAMLString("Requested private transfer destination"), "value", githubYAMLString("${{ inputs.path }}")))
		}
		document := githubYAMLMap("name", githubYAMLString("Private CI "+mode), "description", githubYAMLString("Generated split transport; Actions artifacts contain validated metadata or private-payload locators."), "inputs", inputs, "outputs", outputs, "runs", githubYAMLMap("using", githubYAMLString("composite"), "steps", steps))
		document.HeadComment = "SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.\nSPDX-License-Identifier: Apache-2.0\n\nGENERATED BY nvkr run generate — DO NOT EDIT"
		raw, err := encodeGitHubYAML(document)
		if err != nil {
			return nil, err
		}
		files[githubCITransferPath(plan, mode)+"/action.yml"] = raw
	}
	return files, nil
}

func githubCIActionDocuments(plan *Plan, workflow []byte, files map[string][]byte) (map[string]*yamlv3.Node, error) {
	documents := map[string]*yamlv3.Node{}
	all := maps.Clone(files)
	all[githubLayoutFor(plan).workflowPath] = workflow
	for name, raw := range all {
		if name != githubLayoutFor(plan).workflowPath && !strings.HasSuffix(name, "/action.yml") {
			continue
		}
		var document yamlv3.Node
		if err := yamlv3.Unmarshal(raw, &document); err != nil || len(document.Content) != 1 {
			return nil, e.New(e.CodePipelineDrift, "private transport requires a valid generated YAML member %s", name)
		}
		resolved, err := githubResolveAliases(&document, map[*yamlv3.Node]bool{})
		if err != nil {
			return nil, err
		}
		documents[name] = resolved
	}
	return documents, nil
}

func githubCIWalkSteps(node *yamlv3.Node, visit func(*yamlv3.Node) error) error {
	if node == nil {
		return nil
	}
	if node.Kind == yamlv3.MappingNode && githubYAMLField(node, "uses") != nil {
		return visit(node)
	}
	for _, child := range node.Content {
		if err := githubCIWalkSteps(child, visit); err != nil {
			return err
		}
	}
	return nil
}

func githubCIJobTransfers(plan *Plan, node *yamlv3.Node, documents map[string]*yamlv3.Node, active map[string]bool) (githubCITransfer, error) {
	var result githubCITransfer
	err := githubCIWalkSteps(node, func(step *yamlv3.Node) error {
		uses := githubYAMLValue(step, "uses")
		if mode := githubCITransferMode(plan, uses); mode != "" {
			result.upload = result.upload || mode == "upload"
			result.download = result.download || mode != "upload"
			return nil
		}
		if !strings.HasPrefix(uses, "./") {
			return nil
		}
		name := strings.TrimPrefix(uses, "./") + "/action.yml"
		if active[name] || documents[name] == nil {
			return e.New(e.CodePipelineDrift, "private transfer references missing or recursive action %s", name)
		}
		active[name] = true
		inner, err := githubCIJobTransfers(plan, documents[name], documents, active)
		delete(active, name)
		result.upload, result.download = result.upload || inner.upload, result.download || inner.download
		return err
	})
	return result, err
}

func githubCIBasePermissions(plan *Plan) (map[string]map[string]string, error) {
	stages, _, err := githubStages(plan)
	if err != nil {
		return nil, err
	}
	permissions := map[string]map[string]string{githubFinalizerJobID: githubFinalizerPermissions(plan)}
	for _, stage := range stages {
		permissions[stage.ID], err = githubStagePermissions(plan, stage)
		if err != nil {
			return nil, err
		}
	}
	return permissions, nil
}

func githubCISetPermissions(job *yamlv3.Node, permissions map[string]string) {
	if permissions == nil {
		githubYAMLRemove(job, "permissions")
		return
	}
	fields := githubYAMLMap()
	for _, key := range sortedKeys(permissions) {
		githubYAMLSet(fields, key, githubYAMLString(permissions[key]))
	}
	githubYAMLSet(job, "permissions", fields)
}

func githubCITransferPermissions(base map[string]string, transfer githubCITransfer) map[string]string {
	// Locator downloads use the authenticated Actions API. Registry access still
	// belongs only to dedicated package credentials, never the repository token.
	if !transfer.download {
		return base
	}
	permissions := maps.Clone(base)
	if permissions == nil {
		permissions = map[string]string{"contents": "read"}
	}
	permissions["actions"] = "read"
	return permissions
}

// Project after logical action sharing. Each provider artifact becomes a locator
// index; no original payload path is forwarded to a provider upload action.
func privatizeGitHubTransfers(plan *Plan, workflow []byte, files map[string][]byte, actions githubActionRefs) ([]byte, map[string][]byte, error) {
	documents, err := githubCIActionDocuments(plan, workflow, files)
	if err != nil {
		return nil, nil, err
	}
	entry := githubLayoutFor(plan).workflowPath
	root := documents[entry].Content[0]
	if githubYAMLField(root, "env") != nil {
		return nil, nil, e.New(e.CodeEngineCapability, "private transport workflow account bindings conflict with an existing environment")
	}
	userEnvironment := githubYAMLMap()
	accounts := githubCIUserEnvironment()
	for _, key := range sortedKeys(accounts) {
		githubYAMLSet(userEnvironment, key, githubYAMLString(accounts[key]))
	}
	githubYAMLSet(root, "env", userEnvironment)
	if err := admitGitHubCIUserEnvironment(documents, entry); err != nil {
		return nil, nil, err
	}
	for name, document := range documents {
		if err := githubCIWalkSteps(document, func(step *yamlv3.Node) error {
			mode := ""
			switch githubYAMLValue(step, "uses") {
			case actions.Upload:
				mode = "upload"
			case actions.Download:
				mode = "download"
				if githubYAMLValue(step, "name") == "Download NVKR runner" {
					mode = "bootstrap-download"
				}
			}
			if mode != "" {
				githubYAMLSet(step, "uses", githubYAMLString("./"+githubCITransferPath(plan, mode)))
				if mode == "upload" && githubYAMLValue(step, "if") == githubAlwaysCondition {
					with := githubYAMLField(step, "with")
					if with == nil {
						with = githubYAMLMap()
						githubYAMLSet(step, "with", with)
					}
					githubYAMLSet(with, githubCIAlwaysInput, githubYAMLString("true"))
				}
			}
			return nil
		}); err != nil {
			return nil, nil, err
		}
		if err := projectGitHubPrivateRestore(plan, name, document, false); err != nil {
			return nil, nil, err
		}
	}
	templates, err := githubCITransferTemplates(plan, actions)
	if err != nil {
		return nil, nil, err
	}
	for name, document := range documents {
		transfer, err := githubCIJobTransfers(plan, document, documents, map[string]bool{})
		if err != nil {
			return nil, nil, err
		}
		if name != entry && (transfer.upload || transfer.download) {
			inputs := githubYAMLField(document.Content[0], "inputs")
			if inputs == nil {
				inputs = githubYAMLMap()
				githubYAMLSet(document.Content[0], "inputs", inputs)
			}
			for _, key := range transfer.inputs() {
				if githubYAMLField(inputs, key) != nil {
					return nil, nil, e.New(e.CodeEngineCapability, "generated action conflicts with private transport input")
				}
				githubYAMLSet(inputs, key, githubActionInput("Dedicated private CI credential, forwarded only to its transfer steps"))
			}
		}
		if err := githubCIWalkSteps(document, func(step *yamlv3.Node) error {
			selected, err := githubCIJobTransfers(plan, step, documents, map[string]bool{})
			if err != nil || (!selected.upload && !selected.download) {
				return err
			}
			with := githubYAMLField(step, "with")
			if with == nil {
				with = githubYAMLMap()
				githubYAMLSet(step, "with", with)
			}
			for _, key := range selected.inputs() {
				githubYAMLSet(with, key, githubYAMLString(githubCICredential(key, name == entry)))
			}
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}
	jobs := githubYAMLField(documents[entry].Content[0], "jobs")
	for index := 1; index < len(jobs.Content); index += 2 {
		job := jobs.Content[index]
		transfer, err := githubCIJobTransfers(plan, job, documents, map[string]bool{})
		if err != nil {
			return nil, nil, err
		}
		var base map[string]string
		if node := githubYAMLField(job, "permissions"); node != nil {
			if err := node.Decode(&base); err != nil {
				return nil, nil, e.Wrap(e.CodePipelineDrift, err, "decode generated permission policy")
			}
		}
		githubCISetPermissions(job, githubCITransferPermissions(base, transfer))
	}
	if err := anchorGitHubPreflight(plan, jobs, actions); err != nil {
		return nil, nil, err
	}
	result := maps.Clone(files)
	for name, document := range documents {
		raw, err := encodeGitHubYAML(document)
		if err != nil {
			return nil, nil, err
		}
		if name == entry {
			workflow = raw
		} else {
			result[name] = raw
		}
	}
	maps.Copy(result, templates)
	return workflow, result, nil
}

// Invert only exact generated private templates and credential/permission wiring
// for the existing logical validator. Final all-member regeneration additionally
// checks the actual executable projection, not just these restored descriptions.
func materializeGitHubPrivateTransfers(plan *Plan, workflow []byte, files map[string][]byte) ([]byte, map[string][]byte, error) {
	if !strings.Contains(string(workflow), githubCIStorageInput) && !strings.Contains(string(workflow), githubCIReadInput) {
		return workflow, files, nil
	}
	actions, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		return nil, nil, err
	}
	templates, err := githubCITransferTemplates(plan, actions)
	if err != nil {
		return nil, nil, err
	}
	for name, expected := range templates {
		if !bytes.Equal(files[name], expected) {
			return nil, nil, e.New(e.CodeEngineCapability, "private transport template %s differs from its admitted implementation", name)
		}
	}
	documents, err := githubCIActionDocuments(plan, workflow, files)
	if err != nil {
		return nil, nil, err
	}
	entry := githubLayoutFor(plan).workflowPath
	if err := admitGitHubCIUserEnvironment(documents, entry); err != nil {
		return nil, nil, err
	}
	githubYAMLRemove(documents[entry].Content[0], "env")
	basePermissions, err := githubCIBasePermissions(plan)
	if err != nil {
		return nil, nil, err
	}
	jobs := githubYAMLField(documents[entry].Content[0], "jobs")
	for index := 0; index < len(jobs.Content); index += 2 {
		id, job := jobs.Content[index].Value, jobs.Content[index+1]
		transfer, err := githubCIJobTransfers(plan, job, documents, map[string]bool{})
		if err != nil {
			return nil, nil, err
		}
		if !transfer.upload && !transfer.download {
			continue
		}
		var actual map[string]string
		if node := githubYAMLField(job, "permissions"); node != nil {
			if err := node.Decode(&actual); err != nil {
				return nil, nil, e.Wrap(e.CodeEngineCapability, err, "decode private transport permissions")
			}
		}
		base := basePermissions[id]
		if !maps.Equal(actual, githubCITransferPermissions(base, transfer)) {
			return nil, nil, e.New(e.CodeEngineCapability, "job %s changes private transport permission scope", id)
		}
		githubCISetPermissions(job, base)
	}
	result := maps.Clone(files)
	for name, document := range documents {
		if _, template := templates[name]; template {
			delete(result, name)
			continue
		}
		if err := projectGitHubPrivateRestore(plan, name, document, true); err != nil {
			return nil, nil, err
		}
		if err := githubCIWalkSteps(document, func(step *yamlv3.Node) error {
			with := githubYAMLField(step, "with")
			mode := githubCITransferMode(plan, githubYAMLValue(step, "uses"))
			always := githubYAMLField(with, githubCIAlwaysInput)
			wantsAlways := mode == "upload" && githubYAMLValue(step, "if") == githubAlwaysCondition
			if wantsAlways {
				if always == nil || always.Kind != yamlv3.ScalarNode || always.Value != "true" {
					return e.New(e.CodeEngineCapability, "always-run private upload omits its exact cancellation binding")
				}
			} else if always != nil {
				return e.New(e.CodeEngineCapability, "private transfer changes its admitted cancellation behavior")
			}
			githubYAMLRemove(with, githubCIAlwaysInput)
			for _, key := range []string{githubCIReadInput, githubCIStorageInput} {
				token := githubYAMLField(with, key)
				if token == nil {
					if (mode == "upload" && key == githubCIStorageInput) || ((mode == "download" || mode == "bootstrap-download") && key == githubCIReadInput) {
						return e.New(e.CodeEngineCapability, "private transfer omits its explicit credential input")
					}
					continue
				}
				if token.Kind != yamlv3.ScalarNode || token.Value != githubCICredential(key, name == entry) {
					return e.New(e.CodeEngineCapability, "private transfer changes its fixed credential binding")
				}
				githubYAMLRemove(with, key)
			}
			if with != nil && len(with.Content) == 0 {
				githubYAMLRemove(step, "with")
			}
			if mode != "" {
				uses := actions.Download
				if mode == "upload" {
					uses = actions.Upload
				}
				githubYAMLSet(step, "uses", githubYAMLString(uses))
			}
			return nil
		}); err != nil {
			return nil, nil, err
		}
		inputs := githubYAMLField(document.Content[0], "inputs")
		if inputs != nil {
			githubYAMLRemove(inputs, githubCIStorageInput)
			githubYAMLRemove(inputs, githubCIReadInput)
		}
		if inputs != nil && len(inputs.Content) == 0 {
			githubYAMLRemove(document.Content[0], "inputs")
		}
		raw, err := encodeGitHubYAML(document)
		if err != nil {
			return nil, nil, err
		}
		if name == entry {
			workflow = raw
		} else {
			result[name] = raw
		}
	}
	return workflow, result, nil
}
