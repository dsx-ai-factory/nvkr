// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package factory

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Go raises SIGPIPE for EPIPE on descriptors 1 and 2. A private duplicate above
// that range turns a closed diagnostic pipe into an ordinary ignored write error,
// without changing signal handling for the operation or closing the caller's file.
func githubDiagnosticSink(writer io.Writer) (io.Writer, func()) {
	file, ok := writer.(*os.File)
	if !ok || (file.Fd() != 1 && file.Fd() != 2) {
		return writer, func() {}
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return io.Discard, func() {}
	}
	duplicate := os.NewFile(uintptr(fd), "nkd-diagnostic")
	return duplicate, func() { _ = duplicate.Close() }
}

// Summary paths are provider diagnostics. Do not follow a substituted final
// symlink, block on a FIFO, or write a device if the optional sink is malformed.
func appendGitHubSummary(path, text string) error {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("diagnostic summary is not a regular file")
	}
	_, err = file.WriteString(text)
	return err
}
