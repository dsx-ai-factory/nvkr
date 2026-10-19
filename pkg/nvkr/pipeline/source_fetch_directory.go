// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Source checkout and Git writes stay beneath opened directory capabilities.
// These handles confer filesystem access only, not build or evidence authority.
func holdSourceFetchWorkspaceDirectory(ctx context.Context, workspace *os.File, relative string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening source fetch directory")
	}
	if !securepath.WorkspaceRelative(relative) || filepath.Clean(relative) != relative {
		return nil, e.New(e.CodeEngineCapability, "held source fetching requires an exact workspace-relative path")
	}
	directory, _, err := securepath.CreateParent(workspace, filepath.Join(relative, ".nvkr-held-directory"))
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "holding safe source fetch directory")
	}
	return directory, nil
}

func sourceFetchDescriptorPath(fd uintptr) (string, error) {
	switch runtime.GOOS {
	case "linux":
		return fmt.Sprintf("/proc/self/fd/%d/.", fd), nil
	default:
		return "", e.New(e.CodeEngineCapability, "source fetch requires inherited directory descriptors")
	}
}

func sourceFetchGitOut(opts RunOptions, dir string, args ...string) (result string, returnErr error) {
	if opts.fetchDirectory == nil {
		return gitOut(opts.ctx(), dir, args...)
	}
	if len(args) > 0 && args[0] == "fetch" && opts.fetchRepository == "" {
		return "", e.New(e.CodeEngineCapability, "held source fetching requires its exact selected repository")
	}
	defer func() { returnErr = mapCancellation(opts.ctx(), returnErr, "held source Git") }()
	environment := opts.SourceGitEnvironment
	if environment == nil {
		var err error
		environment, err = sourcegit.Baseline(os.Getenv("PATH"))
		if err != nil {
			return "", err
		}
	}
	executable, admitted, err := sourcegit.Admit(opts.fetchRepository, environment)
	if err != nil {
		return "", err
	}
	// Credentials and repository mirror rewriting are needed only for network
	// fetching. Local init/checkout/status cannot receive the source token.
	if len(args) == 0 || args[0] != "fetch" {
		path, err := sourcegit.SearchPath(admitted)
		if err != nil {
			return "", err
		}
		admitted, err = sourcegit.Baseline(path)
		if err != nil {
			return "", err
		}
	}
	path, err := sourceFetchDescriptorPath(3)
	if err != nil {
		return "", err
	}
	command := []string{"-C", path}
	files := []*os.File{opts.fetchDirectory}
	if opts.fetchGitDirectory != nil {
		if err := recheckSourceFetchGitDirectory(opts); err != nil {
			return "", err
		}
		// ExtraFiles starts at FD3: checkout is FD3 and Git metadata is FD4.
		gitPath, err := sourceFetchDescriptorPath(4)
		if err != nil {
			return "", err
		}
		command = append(command, "--git-dir="+gitPath, "--work-tree="+path,
			"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
			"-c", "diff.ignoreSubmodules=all", "-c", "submodule.recurse=false")
		files = append(files, opts.fetchGitDirectory)
	}
	parent := opts.fetchWorkspace
	if parent == nil {
		// Package-private descriptor fixtures have no workspace owner. Public
		// SourceWorkspace fetch always supplies its borrowed held root.
		var err error
		parent, _, err = securepath.OpenDirectory(os.TempDir())
		if err != nil {
			return "", e.Wrap(e.CodeFetchFailed, err, "holding source Git home parent")
		}
		defer parent.Close()
	}
	attempt, err := packerAttemptID()
	if err != nil {
		return "", e.Wrap(e.CodeFetchFailed, err, "allocating private source Git home")
	}
	// Reuse the existing descriptor-relative temporary-directory owner; its
	// directory is empty and remains held until command stop/join and cleanup.
	home, err := createPackerWorkspaceRoot(parent, attempt)
	if err != nil {
		return "", e.Wrap(e.CodeFetchFailed, err, "holding private source Git home")
	}
	defer func() {
		if err := home.cleanup(); err != nil {
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeFetchFailed, err, "cleaning private source Git home"))
		}
	}()
	homePath, err := sourceFetchDescriptorPath(uintptr(3 + len(files)))
	if err != nil {
		return "", err
	}
	files = append(files, home.root)
	// Git parses its own local configuration. Refuse callback/delegation families
	// before status/checkout can execute them; never parse or rewrite Git config.
	if opts.fetchGitDirectory != nil {
		if err := admitSourceFetchGitConfiguration(opts, executable, command, files, homePath, admitted); err != nil {
			return "", err
		}
		if err := recheckSourceFetchGitDirectory(opts); err != nil {
			return "", err
		}
	}
	if len(args) > 0 && args[0] == "fetch" {
		// Repository-local upload-pack and fetch-recurse settings cannot select
		// a different host program or additional repositories for this fetch.
		command = append(command, "fetch", "--upload-pack=git-upload-pack", "--no-recurse-submodules")
		command = append(command, args[1:]...)
	} else {
		command = append(command, args...)
	}
	cmd := execCmd(opts, executable, command...)
	cmd.Env = append(admitted, "HOME="+homePath, "XDG_CONFIG_HOME="+homePath)
	// Also bound transports on non-fetch commands: object reads in a reused
	// partial repository must not activate a configured external helper.
	protocols := "https:file"
	for _, field := range admitted {
		if strings.HasPrefix(field, sourcegit.Token+"=") {
			protocols = "https"
			break
		}
	}
	cmd.Env = append(cmd.Env, "GIT_ALLOW_PROTOCOL="+protocols)
	cmd.ExtraFiles = files
	out, err := outputOwnedCommand(opts.ctx(), cmd)
	if err != nil {
		if opts.ctx().Err() != nil {
			return "", e.Wrap(e.CodeTimeout, opts.ctx().Err(), "source Git execution canceled")
		}
		return "", err
	}
	if opts.fetchGitDirectory != nil {
		if err := recheckSourceFetchGitDirectory(opts); err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(string(out)), nil
}

// This output sink records presence only and rejects the first byte. No config
// names/values are retained, unboundedly buffered, parsed or exposed in errors.
type sourceGitConfigPresence struct{ found bool }

func (output *sourceGitConfigPresence) Write(raw []byte) (int, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	output.found = true
	return 0, e.New(e.CodeEngineCapability, "held source Git configuration query returned unsupported data")
}

func admitSourceFetchGitConfiguration(opts RunOptions, executable string, prefix []string, files []*os.File, homePath string, admitted []string) error {
	configuration, _, err := securepath.OpenChild(opts.fetchGitDirectory, "config", false)
	if os.IsNotExist(err) {
		return nil // Fresh init has no local configuration yet.
	}
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "holding source Git configuration")
	}
	_ = configuration.Close()
	path, err := sourcegit.SearchPath(admitted)
	if err != nil {
		return err
	}
	baseline, err := sourcegit.Baseline(path)
	if err != nil {
		return err
	}
	// --no-includes does not hide delegating include keys: those keys themselves
	// are refused. Worktree/partial-clone configuration is not a held v1 source.
	query := append([]string{"--no-pager"}, prefix...)
	query = append(query, "config", "--local", "--no-includes", "--name-only", "--null", "--get-regexp",
		sourcegit.UnsupportedConfiguration)
	cmd := execCmd(opts, executable, query...)
	cmd.Env = append(baseline, "HOME="+homePath, "XDG_CONFIG_HOME="+homePath, "GIT_ALLOW_PROTOCOL=https:file")
	cmd.ExtraFiles = files
	var stdout, stderr sourceGitConfigPresence
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = runOwnedCommand(opts.ctx(), cmd)
	if opts.ctx().Err() != nil {
		return e.Wrap(e.CodeTimeout, opts.ctx().Err(), "source Git configuration query canceled")
	}
	var exit *exec.ExitError
	if !stdout.found && !stderr.found && errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil // Git's documented no-match status; no data was emitted.
	}
	if stdout.found || stderr.found || err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "held source Git configuration contains unsupported callbacks, includes or object sources")
	}
	return nil
}

func recheckSourceFetchGitDirectory(opts RunOptions) error {
	current, info, err := securepath.OpenChild(opts.fetchDirectory, ".git", true)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "rechecking held source Git directory")
	}
	defer current.Close()
	held, err := opts.fetchGitDirectory.Stat()
	if err != nil || !os.SameFile(info, held) {
		return e.Wrap(e.CodeFetchFailed, err, "source Git directory changed after admission")
	}
	return nil
}

// Authenticate the selected logical outputs against the original directories,
// without creating components or following replacement ancestor links.
func recheckSourceFetchDirectories(node ir.Node, opts RunOptions) error {
	if opts.fetchWorkspace == nil {
		return nil // Ordinary Local and package-private IO fixtures own other paths.
	}
	receipt, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "locating held source receipt")
	}
	for _, selected := range []struct {
		path string
		file *os.File
	}{
		{filepath.Join(opts.SourceRoot, node.Dest), opts.fetchDirectory},
		{filepath.Dir(receipt), opts.fetchReceiptDirectory},
	} {
		if err := recheckSourceFetchDirectory(opts.ctx(), opts.fetchWorkspace, selected.path, selected.file); err != nil {
			return err
		}
	}
	return nil
}

func recheckSourceFetchDirectory(ctx context.Context, workspace *os.File, relative string, held *os.File) error {
	if held == nil || !securepath.WorkspaceRelative(relative) || filepath.Clean(relative) != relative {
		return e.New(e.CodeFetchFailed, "source fetch output lost its held directory binding")
	}
	current, owned := workspace, false
	defer func() {
		if owned {
			_ = current.Close()
		}
	}()
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "source directory recheck canceled")
		}
		child, _, err := securepath.OpenChild(current, component, true)
		if err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "source fetch directory changed during execution")
		}
		if owned {
			_ = current.Close()
		}
		current, owned = child, true
	}
	actual, err := current.Stat()
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "rechecking source fetch directory")
	}
	original, err := held.Stat()
	if err != nil || !os.SameFile(actual, original) {
		return e.Wrap(e.CodeFetchFailed, err, "source fetch directory no longer matches its original owner")
	}
	return nil
}
