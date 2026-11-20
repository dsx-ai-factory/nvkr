// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Static guest fixture for the opt-in Lima mount and forwarding contract.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "expected LISTEN_ADDRESS DIRECTORY")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	server := &http.Server{Addr: os.Args[1], Handler: http.FileServer(http.Dir(os.Args[2])), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second}
	joined := make(chan struct{})
	go func() { <-ctx.Done(); _ = server.Close(); close(joined) }()
	err := server.ListenAndServe()
	stop()
	<-joined
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
