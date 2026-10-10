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

// LocalCommand isolates ordinary developer Git operations. Git itself reads
// developer credential configuration, which is projected only into fetch. It
// never forwards global build callbacks, filters, templates or line conversions.
// The caller owns execution and stop/join of the returned command.
func LocalCommand(ctx context.Context, environment []string, dir string, args ...string) (*exec.Cmd, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "local source Git requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local source Git canceled")
	}
	path, err := SearchPath(environment)
	if err != nil {
		return nil, err
	}
	baseline, err := Baseline(path)
	if err != nil {
		return nil, err
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
				return nil, e.New(e.CodeEngineCapability, "local source Git configuration contains unsupported callbacks, includes or object sources")
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
			data, err = localConfigQuery(ctx, executable, queryEnvironment,
				[]string{"config", "--global", "--includes", "--null", "--get-regexp", `^credential\.(.*\.)?(helper|username|usehttppath)$`})
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
			if !ok || !strings.HasPrefix(key, "credential.") {
				return nil, e.New(e.CodeEngineCapability, "Git returned invalid credential configuration framing")
			}
			child = append(child, "GIT_CONFIG_KEY_"+strconv.Itoa(count)+"="+key, "GIT_CONFIG_VALUE_"+strconv.Itoa(count)+"="+value)
			count++
		}
		// Existing launchers explicitly redirect canonical sources to read-only
		// local repositories. Do not inherit arbitrary GIT_CONFIG_* settings.
		redirects := 0
		if declared := localEnvironmentValue(environment, "GIT_CONFIG_COUNT"); declared != "" {
			redirects, err = strconv.Atoi(declared)
			if err != nil || redirects < 0 || redirects > 16 {
				return nil, e.New(e.CodeEngineCapability, "local source Git requires a bounded explicit repository redirect count")
			}
		}
		for index := 0; index < redirects; index++ {
			key := localEnvironmentValue(environment, "GIT_CONFIG_KEY_"+strconv.Itoa(index))
			local := strings.TrimSuffix(strings.TrimPrefix(key, "url."), ".insteadOf")
			value := localEnvironmentValue(environment, "GIT_CONFIG_VALUE_"+strconv.Itoa(index))
			if !strings.HasPrefix(key, "url.") || !strings.HasSuffix(key, ".insteadOf") || !filepath.IsAbs(local) || filepath.Clean(local) != local || value == "" {
				return nil, e.New(e.CodeEngineCapability, "local source Git accepts only explicit absolute local repository redirects")
			}
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
			case "HOME", "XDG_CONFIG_HOME", "SSH_AUTH_SOCK", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY":
				child = append(child, field)
			}
		}
		prefix = append(prefix, "-c", "fetch.fsckObjects=true")
		args = append([]string{"fetch", "--upload-pack=git-upload-pack", "--no-recurse-submodules"}, args[1:]...)
	}
	child = append(child, "GIT_ALLOW_PROTOCOL=https:http:ssh:git:file", "GIT_ATTR_NOSYSTEM=1", "GIT_TEMPLATE_DIR=")
	cmd := exec.CommandContext(ctx, executable, append(prefix, args...)...)
	cmd.Env = child
	return cmd, nil
}

// UnsupportedConfiguration is the shared Git-owned query used at both local
// and held source boundaries. Hooks/fsmonitor are forcibly disabled separately.
const UnsupportedConfiguration = `^(filter|include|includeif|hook|url)\.|^extensions\.(worktreeconfig|partialclone)$|^remote\..*\.promisor$|^core\.alternaterefscommand$`

func localEnvironmentValue(environment []string, selected string) string {
	for _, field := range environment {
		key, value, _ := strings.Cut(field, "=")
		if key == selected {
			return value
		}
	}
	return ""
}

type localQueryBuffer struct{ data bytes.Buffer }

func (buffer *localQueryBuffer) Write(data []byte) (int, error) {
	if len(data) > (64<<10)-buffer.Len() {
		return 0, e.New(e.CodeEngineCapability, "source Git configuration query exceeds its bound")
	}
	return buffer.data.Write(data)
}

func (buffer *localQueryBuffer) Len() int      { return buffer.data.Len() }
func (buffer *localQueryBuffer) Bytes() []byte { return buffer.data.Bytes() }

func localConfigQuery(ctx context.Context, executable string, environment, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = environment
	var stdout, stderr localQueryBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := ownedcmd.Run(ctx, cmd)
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "source Git configuration query canceled")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && stdout.Len() == 0 && stderr.Len() == 0 {
		return nil, nil
	}
	if err != nil || stderr.Len() != 0 {
		return nil, e.Wrap(e.CodeEngineCapability, err, "source Git configuration query failed")
	}
	return stdout.Bytes(), nil
}
