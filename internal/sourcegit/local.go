// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// LocalCredentialPolicy is static guidance, not a diagnosis of a Git failure.
// Keep it aligned with the fetch-only projection below and ADR-18 section 8.
const LocalCredentialPolicy = "local credential policy: fetch projects global credential.helper, credential.username and credential.useHttpPath, or use an explicit local repository redirect; system helpers, GH/GCM/keyring environment and arbitrary SSH command overrides are not inherited"

// LocalCommand isolates ordinary developer Git operations. Git itself reads
// developer credential configuration, which is projected only into fetch. It
// never forwards global build callbacks, filters, templates or line conversions.
// args starts with the operation, not Git global options. Repository selection
// belongs exclusively to dir; Git still owns each operation's argument grammar.
// The caller owns execution and stop/join of the returned command.
func LocalCommand(ctx context.Context, environment []string, dir string, args ...string) (*exec.Cmd, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "local source Git requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local source Git canceled")
	}
	if len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return nil, e.New(e.CodeEngineCapability, "local source Git requires an operation without caller-supplied global options")
	}
	if dir != "" && (!filepath.IsAbs(dir) || filepath.Clean(dir) != dir || strings.ContainsRune(dir, os.PathListSeparator)) {
		return nil, e.New(e.CodeEngineCapability, "local source Git requires a canonical absolute explicit repository directory without a search-path separator")
	}
	path, err := SearchPath(environment)
	if err != nil {
		return nil, err
	}
	baseline, err := Baseline(path)
	if err != nil {
		return nil, err
	}
	if dir != "" {
		// An explicit source root is not an invitation to discover an enclosing
		// repository. Fresh init is allowed; commands on non-repository child
		// directories fail in Git instead of running ancestor callbacks.
		baseline = append(baseline, "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	}
	executable, err := executable(path)
	if err != nil {
		return nil, err
	}
	prefix := []string{"--no-replace-objects", "--literal-pathspecs", "--no-pager",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false",
		"-c", "core.autocrlf=false", "-c", "core.eol=lf", "-c", "core.attributesFile=" + os.DevNull,
		"-c", "diff.ignoreSubmodules=all", "-c", "submodule.recurse=false",
		"-c", "gc.auto=0", "-c", "maintenance.auto=false"}
	if dir != "" {
		prefix = append(prefix, "-C", dir)
	}
	// Reuse the held source route's callback admission, with Git as parser.
	// Fresh init has no metadata; never query a discovered ancestor repository.
	if dir != "" {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			query := append(append([]string{}, prefix...), "config", "--local", "--no-includes", "--name-only", "--null", "--get-regexp", UnsupportedConfiguration)
			data, err := localConfigQuery(ctx, executable, baseline, query)
			if err != nil {
				return nil, err
			}
			if len(data) != 0 {
				return nil, e.New(e.CodeEngineCapability, "selected source repository contains unsupported Git configuration families: %s", localConfigurationFamilies(data))
			}
			// Do not ask status to inspect nested repositories: their callbacks
			// have not passed this admission. Refuse submodule sources instead of
			// treating ignoreSubmodules=all as evidence of a clean source.
			descriptorPresent := false
			descriptorExecutable := false
			if info, err := os.Lstat(filepath.Join(dir, ".gitmodules")); err == nil {
				if !info.Mode().IsRegular() || info.Size() != 0 {
					return nil, e.New(e.CodeEngineCapability, "selected source repository .gitmodules is unsupported; submodule sources are not admitted")
				}
				descriptorPresent = true
				descriptorExecutable = info.Mode().Perm()&0o111 != 0
			} else if !os.IsNotExist(err) {
				return nil, e.New(e.CodeEngineCapability, "cannot inspect selected source repository submodule descriptor")
			}
			query = append(append([]string{}, prefix...), "ls-files", "--cached", "--format=%(objectmode)", "--", ".gitmodules")
			data, err = localGitQuery(ctx, executable, baseline, query, "submodule descriptor modes", 64<<10, false)
			if err != nil {
				return nil, err
			}
			if descriptorPresent != (len(data) != 0) {
				return nil, e.New(e.CodeEngineCapability, "selected source repository indexed .gitmodules is unsupported; submodule sources are not admitted")
			}
			if descriptorPresent {
				if (!bytes.Equal(data, []byte("100644\n")) && !bytes.Equal(data, []byte("100755\n"))) ||
					descriptorExecutable != bytes.Equal(data, []byte("100755\n")) {
					return nil, e.New(e.CodeEngineCapability, "selected source repository indexed .gitmodules must be a regular tracked empty blob")
				}
				// The maintained OVN fork tracks an inert empty descriptor. Git
				// authenticates its HEAD bytes and both index/worktree comparisons;
				// an empty untracked file or compensating staged edits cannot qualify.
				query = append(append([]string{}, prefix...), "cat-file", "-t", "HEAD:.gitmodules")
				data, err = localGitQuery(ctx, executable, baseline, query, "submodule descriptor type", 64<<10, false)
				if err != nil {
					return nil, err
				}
				if !bytes.Equal(data, []byte("blob\n")) {
					return nil, e.New(e.CodeEngineCapability, "selected source repository .gitmodules must be a tracked empty blob")
				}
				query = append(append([]string{}, prefix...), "cat-file", "-s", "HEAD:.gitmodules")
				data, err = localGitQuery(ctx, executable, baseline, query, "submodule descriptor HEAD", 64<<10, false)
				if err != nil {
					return nil, err
				}
				if !bytes.Equal(data, []byte("0\n")) {
					return nil, e.New(e.CodeEngineCapability, "selected source repository .gitmodules is unsupported; only an authenticated empty descriptor is admitted")
				}
				for _, arguments := range [][]string{{"diff", "--quiet", "--no-ext-diff", "--no-textconv", "--cached", "HEAD", "--", ".gitmodules"}, {"diff", "--quiet", "--no-ext-diff", "--no-textconv", "--", ".gitmodules"}} {
					query = append(append([]string{}, prefix...), arguments...)
					if _, err := localGitQuery(ctx, executable, baseline, query, "submodule descriptor correspondence", 64<<10, false); err != nil {
						return nil, err
					}
				}
			}
			// Git reads its own index and emits only its documented objectmode
			// field, one per line. No paths, objects or nested configuration are
			// read by this adapter. The separate 1 MiB bound accommodates the
			// pipeline's 100,000-entry source-closure limit (seven bytes each).
			query = append(append([]string{}, prefix...), "ls-files", "--cached", "--format=%(objectmode)")
			data, err = localGitQuery(ctx, executable, baseline, query, "index modes", 1<<20, false)
			if err != nil {
				return nil, err
			}
			for _, mode := range bytes.Split(data, []byte{'\n'}) {
				if bytes.Equal(mode, []byte("160000")) {
					return nil, e.New(e.CodeEngineCapability, "selected source repository gitlinks are unsupported; submodule sources are not admitted")
				}
			}
		} else if !os.IsNotExist(err) {
			return nil, e.Wrap(e.CodeEngineCapability, err, "inspecting local source Git metadata")
		}
	}
	child := append([]string{}, baseline...)
	fetch := len(args) > 0 && args[0] == "fetch"
	if fetch {
		// A config query performs no checkout or helper execution. Its closed
		// environment selects only the developer's config location and PATH.
		queryEnvironment := []string{"PATH=" + path, "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
		for _, field := range environment {
			key, _, _ := strings.Cut(field, "=")
			switch key {
			case "HOME", "XDG_CONFIG_HOME":
				queryEnvironment = append(queryEnvironment, field)
			}
		}
		var data []byte
		if localEnvironmentValue(environment, "HOME") != "" || localEnvironmentValue(environment, "XDG_CONFIG_HOME") != "" {
			query := []string{"config", "--global", "--includes", "--null", "--get-regexp", `^credential\.(.*\.)?(helper|username|usehttppath)$`}
			if dir != "" {
				// Conditional credential includes must refer to the selected source,
				// not the launcher's unrelated working directory.
				query = append([]string{"-C", dir}, query...)
				queryEnvironment = append(queryEnvironment, "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
			}
			data, err = localConfigQuery(ctx, executable, queryEnvironment, query)
			if err != nil {
				return nil, err
			}
		}
		count := 1 // Baseline's empty helper resets repository-local helpers.
		// Decode only Git's documented NUL records (key LF value). Git owns
		// syntax, includes and escapes; this is not a Git-config parser.
		for _, record := range bytes.Split(data, []byte{0}) {
			if len(record) == 0 {
				continue
			}
			key, value, ok := strings.Cut(string(record), "\n")
			// Git's valueless boolean form is an implicit true, rather than
			// an empty string (which would mean false when projected).
			if !ok && strings.HasSuffix(key, ".usehttppath") {
				value, ok = "true", true
			}
			if !ok && strings.HasPrefix(key, "credential.") {
				switch {
				case strings.HasSuffix(key, ".helper"):
					return nil, e.New(e.CodeEngineCapability, "global credential.helper requires an explicit value; an explicit empty value resets helpers")
				case strings.HasSuffix(key, ".username"):
					return nil, e.New(e.CodeEngineCapability, "global credential.username requires an explicit value")
				}
			}
			if !ok || !strings.HasPrefix(key, "credential.") {
				return nil, e.New(e.CodeEngineCapability, "Git returned invalid credential configuration framing")
			}
			child = append(child, "GIT_CONFIG_KEY_"+strconv.Itoa(count)+"="+key, "GIT_CONFIG_VALUE_"+strconv.Itoa(count)+"="+value)
			count++
		}
		// Existing launchers explicitly redirect canonical sources to read-only
		// local repositories. Do not inherit arbitrary GIT_CONFIG_* settings.
		redirectEnvironment, err := LocalRedirectEnvironment(environment)
		if err != nil {
			return nil, err
		}
		redirects, _ := strconv.Atoi(localEnvironmentValue(redirectEnvironment, "GIT_CONFIG_COUNT"))
		for index := 0; index < redirects; index++ {
			key := localEnvironmentValue(redirectEnvironment, "GIT_CONFIG_KEY_"+strconv.Itoa(index))
			value := localEnvironmentValue(redirectEnvironment, "GIT_CONFIG_VALUE_"+strconv.Itoa(index))
			child = append(child, "GIT_CONFIG_KEY_"+strconv.Itoa(count)+"="+key,
				"GIT_CONFIG_VALUE_"+strconv.Itoa(count)+"="+value)
			count++
		}
		for index, field := range child {
			if strings.HasPrefix(field, "GIT_CONFIG_COUNT=") {
				child[index] = "GIT_CONFIG_COUNT=" + strconv.Itoa(count)
			}
		}
		// Credential helpers retain their normal account location only in this
		// fetch child. Git still cannot load ambient global/system configuration.
		for _, field := range environment {
			key, _, _ := strings.Cut(field, "=")
			switch key {
			case "HOME", "XDG_CONFIG_HOME", "SSH_AUTH_SOCK",
				"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
				"NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy",
				"GIT_SSL_CAINFO", "SSL_CERT_FILE", "SSL_CERT_DIR":
				child = append(child, field)
			}
		}
		prefix = append(prefix, "-c", "fetch.fsckObjects=true")
		args = append([]string{"fetch", "--upload-pack=git-upload-pack", "--no-recurse-submodules"}, args[1:]...)
	}
	switch args[0] {
	case "clone":
		// Installer copies receive no developer helper or HOME. Keep their
		// object transport and submodule policy as explicit as fetch's.
		prefix = append(prefix, "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true")
		args = append([]string{"clone", "--no-local", "--upload-pack=git-upload-pack", "--no-recurse-submodules"}, args[1:]...)
	case "ls-remote":
		args = append([]string{"ls-remote", "--upload-pack=git-upload-pack"}, args[1:]...)
	}
	// Git owns URL/protocol parsing. Neither authenticated network acquisition
	// nor local installer copying needs unauthenticated cleartext transports.
	child = append(child, "GIT_ALLOW_PROTOCOL=https:ssh:file", "GIT_ATTR_NOSYSTEM=1", "GIT_TEMPLATE_DIR=")
	cmd := exec.CommandContext(ctx, executable, append(prefix, args...)...)
	cmd.Env = child
	return cmd, nil
}

// LocalRedirectEnvironment projects only the already-supported explicit local
// fetch redirects. Launchers may carry these through build preparation without
// forwarding arbitrary Git configuration, callbacks or credentials. Fetch still
// verifies the pinned commit, tree and source closure at its owning boundary.
func LocalRedirectEnvironment(environment []string) ([]string, error) {
	declared := localEnvironmentValue(environment, "GIT_CONFIG_COUNT")
	if declared == "" {
		return nil, nil
	}
	count, err := strconv.Atoi(declared)
	if err != nil || count < 0 || count > 16 {
		return nil, e.New(e.CodeEngineCapability, "local source Git requires a bounded explicit repository redirect count")
	}
	result := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(count)}
	for index := 0; index < count; index++ {
		suffix := strconv.Itoa(index)
		key := localEnvironmentValue(environment, "GIT_CONFIG_KEY_"+suffix)
		value := localEnvironmentValue(environment, "GIT_CONFIG_VALUE_"+suffix)
		family := localConfigurationFamilies([]byte(strings.ToLower(key)))
		const variable = ".insteadOf"
		if !strings.HasPrefix(key, "url.") || len(key) <= len("url.")+len(variable) || !strings.EqualFold(key[len(key)-len(variable):], variable) {
			return nil, e.New(e.CodeEngineCapability, "GIT_CONFIG_KEY_%d (%s) must select an explicit absolute local repository redirect", index, family)
		}
		local := key[len("url.") : len(key)-len(variable)]
		clean := filepath.Clean(local)
		// Maintained workflow templates emit one trailing separator. Preserve
		// Git's exact rewrite base, while still refusing embedded dot segments,
		// repeated separators and URI forms outside the local path contract.
		if !filepath.IsAbs(local) || (local != clean && local != clean+string(filepath.Separator)) {
			return nil, e.New(e.CodeEngineCapability, "GIT_CONFIG_KEY_%d (%s) must use a canonical absolute local repository path", index, family)
		}
		if value == "" {
			return nil, e.New(e.CodeEngineCapability, "GIT_CONFIG_VALUE_%d (GIT_CONFIG_KEY_%d, %s) must provide a nonempty repository redirect source", index, index, family)
		}
		result = append(result, "GIT_CONFIG_KEY_"+suffix+"="+key, "GIT_CONFIG_VALUE_"+suffix+"="+value)
	}
	return result, nil
}

// UnsupportedConfiguration is the shared Git-owned query used at both local
// and held source boundaries. Hooks/fsmonitor are forcibly disabled separately.
const UnsupportedConfiguration = `^(filter|include|includeif|hook|url|http)\.|^extensions\.(worktreeconfig|partialclone)$|^remote\..*\.promisor$|^core\.(alternaterefscommand|sshcommand|gitproxy|askpass|worktree)$`

// localConfigurationFamilies labels Git's already-refused name-only NUL output,
// not configuration syntax or admission policy. Emit only these static section
// labels: key subsections can contain private URLs, paths or arbitrary text.
func localConfigurationFamilies(data []byte) string {
	records := bytes.Split(data, []byte{0})
	var families []string
	for _, family := range []string{"core", "credential", "extensions", "filter", "hook", "http", "include", "includeif", "remote", "safe", "url"} {
		for _, record := range records {
			if bytes.HasPrefix(record, []byte(family+".")) {
				families = append(families, family+".*")
				break
			}
		}
	}
	if len(families) == 0 {
		return "unrecognized"
	}
	return strings.Join(families, ", ")
}

func localEnvironmentValue(environment []string, selected string) string {
	for _, field := range environment {
		key, value, _ := strings.Cut(field, "=")
		if key == selected {
			return value
		}
	}
	return ""
}

type localQueryBuffer struct {
	data  bytes.Buffer
	limit int
}

func (buffer *localQueryBuffer) Write(data []byte) (int, error) {
	if len(data) > buffer.limit-buffer.Len() {
		return 0, e.New(e.CodeEngineCapability, "source Git query exceeds its bound")
	}
	return buffer.data.Write(data)
}

func (buffer *localQueryBuffer) Len() int      { return buffer.data.Len() }
func (buffer *localQueryBuffer) Bytes() []byte { return buffer.data.Bytes() }

func localConfigQuery(ctx context.Context, executable string, environment, args []string) ([]byte, error) {
	return localGitQuery(ctx, executable, environment, args, "configuration", 64<<10, true)
}

func localGitQuery(ctx context.Context, executable string, environment, args []string, description string, stdoutLimit int, allowNoMatch bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = environment
	stdout, stderr := localQueryBuffer{limit: stdoutLimit}, localQueryBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := ownedcmd.Run(ctx, cmd)
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "source Git %s query canceled", description)
	}
	var exit *exec.ExitError
	if allowNoMatch && errors.As(err, &exit) && exit.ExitCode() == 1 && stdout.Len() == 0 && stderr.Len() == 0 {
		return nil, nil
	}
	if err != nil || stderr.Len() != 0 {
		if description == "index modes" || description == "submodule descriptor modes" {
			return nil, e.Wrap(e.CodeEngineCapability, err, "source Git %s query failed; local source admission requires Git 2.38+ with ls-files --format support", description)
		}
		if description == "configuration" {
			return nil, e.Wrap(e.CodeEngineCapability, err, "source Git configuration query failed; %s", LocalCredentialPolicy)
		}
		return nil, e.Wrap(e.CodeEngineCapability, err, "source Git %s query failed", description)
	}
	return stdout.Bytes(), nil
}
