// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"golang.org/x/sys/unix"
)

// nativeHandoff owns the narrowly scoped return channel from site startup.
// Possession of its token only permits submitting bytes; the runtime still
// verifies the resulting kubeconfig with the actual Kubernetes API.
type nativeHandoff struct {
	server      *http.Server
	done        chan error
	result      chan error
	URL, Token  string
	Certificate string
	mu          sync.Mutex
	received    bool
	closed      bool
	root        *os.File
}

func startNativeHandoff(ctx context.Context, gateway, work string) (*nativeHandoff, error) {
	return startNativeHandoffAt(ctx, gateway, "0", work)
}

func startNativeHandoffAt(ctx context.Context, gateway, port, work string) (*nativeHandoff, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	address, err := netip.ParseAddr(gateway)
	if err != nil || !address.Is4() || (!address.IsPrivate() && !address.IsLoopback()) {
		return nil, errors.New("site handoff requires an explicit private or loopback IPv4 bind")
	}
	// The existing path owner checks that uploads cannot escape or replace
	// files in a public or substituted workspace.
	root, info, err := securepath.OpenDirectory(work)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &stat); err != nil || info.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Getuid()) {
		_ = root.Close()
		return nil, errors.New("site handoff requires a private owned workspace")
	}
	keepRoot := false
	defer func() {
		if !keepRoot {
			_ = root.Close()
		}
	}()
	certificate, public, err := nativeHandoffCertificate(address)
	if err != nil {
		return nil, err
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	var listen net.ListenConfig
	listener, err := listen.Listen(ctx, "tcp4", net.JoinHostPort(gateway, port))
	if err != nil {
		return nil, err
	}
	handoff := &nativeHandoff{
		done: make(chan error, 1), result: make(chan error, 1),
		URL:   "https://" + listener.Addr().String() + "/site-kubeconfig",
		Token: hex.EncodeToString(token[:]), Certificate: public,
		root: root,
	}
	handoff.server = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handoff.receive(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	handoff.server.SetKeepAlivesEnabled(false)
	secured := tls.NewListener(listener, &tls.Config{
		Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13,
	})
	go func() { handoff.done <- handoff.server.Serve(secured) }()
	keepRoot = true
	return handoff, nil
}

func (h *nativeHandoff) receive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/site-kubeconfig" || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		http.Error(w, "handoff stopped", http.StatusServiceUnavailable)
		return
	}
	if h.received {
		http.Error(w, "handoff already submitted", http.StatusConflict)
		return
	}
	failed := r.Header.Get("X-NVKE-Startup-Status") == "failed"
	prepared := r.Header.Get("X-NVKE-Startup-Status") == "prepared"
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil || len(raw) == 0 && !failed {
		http.Error(w, "invalid handoff size", http.StatusBadRequest)
		return
	}
	// No credential parsing, endpoint rewrite or success claim here. The
	// original source-owned bytes are created beneath the held private root.
	filename := "site.kubeconfig"
	if prepared {
		filename = "site-prepared.json"
	}
	if failed {
		filename = "site-startup-failure.log"
	}
	fd, err := unix.Openat(int(h.root.Fd()), filename, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err == nil {
		file := os.NewFile(uintptr(fd), "site.kubeconfig")
		_, err = file.Write(raw)
		err = errors.Join(err, file.Sync(), file.Close())
	}
	if err != nil {
		h.received = true
		h.result <- err
		http.Error(w, "handoff storage failed", http.StatusInternalServerError)
		return
	}
	h.received = true
	if failed {
		h.result <- errors.New("source-owned site startup failed; private site-startup-failure.log retained")
		w.WriteHeader(http.StatusAccepted)
		return
	}
	h.result <- nil
	w.WriteHeader(http.StatusCreated)
}

func (h *nativeHandoff) Wait(ctx context.Context) error {
	select {
	case err := <-h.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *nativeHandoff) Stop(ctx context.Context) error {
	if h == nil {
		return nil
	}
	// Shutdown joins handlers. Closing the server on expiry also closes every
	// owned connection; the read bound prevents a stalled upload living on.
	err := h.server.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, h.server.Close())
	}
	select {
	case served := <-h.done:
		if errors.Is(served, http.ErrServerClosed) {
			served = nil
		}
		// receive holds this mutex through its bounded read and final write.
		// Joining it after closing connections also joins forced shutdown.
		joined := make(chan error, 1)
		go func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.closed = true
			joined <- h.root.Close()
		}()
		select {
		case closeErr := <-joined:
			return errors.Join(err, served, closeErr)
		case <-ctx.Done():
			return errors.Join(err, served, ErrUnjoined, ctx.Err())
		}
	case <-ctx.Done():
		return errors.Join(err, ErrUnjoined, ctx.Err())
	}
}

// Use the standard library's certificate generator and TLS implementation.
// This certificate authenticates only this ephemeral private return channel;
// it is separate from the source-owned SPIRE CA on the site node.
func nativeHandoffCertificate(address netip.Addr) (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	description := &x509.Certificate{
		SerialNumber: serial, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		IPAddresses: []net.IP{net.IP(address.AsSlice())},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	raw, err := x509.CreateCertificate(rand.Reader, description, description, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
	private := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey})
	certificate, err := tls.X509KeyPair(public, private)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("site handoff certificate: %w", err)
	}
	return certificate, string(public), nil
}
