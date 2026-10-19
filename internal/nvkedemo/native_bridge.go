// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type nativeBridgeConfig struct {
	prefix        netip.Prefix
	gateway, last netip.Addr
	dhcp          bool
}

func linuxNativeBridge() nativeBridgeConfig {
	return nativeBridgeConfig{netip.MustParsePrefix(GuestCIDR), netip.MustParseAddr(GuestGateway), netip.MustParseAddr("172.30.0.254"), false}
}

// The upstream helper owns vmnet and its packet protocol. This owner passes its
// documented Unix datagram FD and discards received frames without interpreting
// or injecting packets. Holding that interface keeps Apple's gateway available
// before the first QEMU guest and until all guests and services are joined.
type nativeBridgeOwner struct {
	config     nativeBridgeConfig
	process    *OwnedProcess
	connection net.Conn
	done       chan struct{}
}

func startNativeBridge(ctx context.Context, runner Runner, platform, tools string) (_ *nativeBridgeOwner, result error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if platform == "linux" {
		return &nativeBridgeOwner{config: linuxNativeBridge()}, nil
	}
	if platform != "darwin" {
		return nil, errors.New("unsupported native bridge host")
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return nil, err
	}
	for _, fd := range fds {
		unix.CloseOnExec(fd)
	}
	parent, child := os.NewFile(uintptr(fds[0]), "vmnet-parent"), os.NewFile(uintptr(fds[1]), "vmnet-child")
	connection, err := net.FileConn(parent)
	closeErr := parent.Close()
	if err != nil || closeErr != nil {
		_ = child.Close()
		if connection != nil {
			_ = connection.Close()
		}
		return nil, errors.Join(err, closeErr)
	}
	owner := &nativeBridgeOwner{connection: connection, done: make(chan struct{})}
	go func() { defer close(owner.done); _, _ = io.Copy(io.Discard, connection) }()
	defer func() {
		if result != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			result = errors.Join(result, owner.Stop(cleanup))
		}
	}()
	runner.extraFiles = []*os.File{child}
	owner.process, err = runner.start(ctx, []string{filepath.Join(tools, "vmnet-helper"), "--operation-mode=shared", "--fd=3"}, "native-vmnet-interface", true, true)
	closeErr = child.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	budget, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if exited, err := owner.process.Exited(); exited {
			return nil, errors.Join(errors.New("native vmnet interface exited before readiness"), err)
		}
		raw, err := owner.process.stdout.result()
		if err != nil {
			return nil, err
		}
		if json.Valid(raw) {
			owner.config, err = decodeNativeBridge(raw)
			if err != nil {
				return nil, err
			}
			// A real bind is the readiness check; discovery alone does not
			// establish that the host interface has become available.
			var listen net.ListenConfig
			listener, err := listen.Listen(budget, "tcp4", net.JoinHostPort(owner.config.gateway.String(), "0"))
			if err == nil {
				if err := listener.Close(); err != nil {
					return nil, err
				}
				return owner, nil
			}
		}
		if err := pause(budget, 50*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

func decodeNativeBridge(raw []byte) (nativeBridgeConfig, error) {
	// These field names are the public vmnet-helper JSON contract also consumed
	// by the selected NVKE config package. Standard library IP/mask parsing owns
	// address validation; there is no text-log or packet parser here.
	var observed struct {
		Start string `json:"vmnet_start_address"`
		End   string `json:"vmnet_end_address"`
		Mask  string `json:"vmnet_subnet_mask"`
	}
	if err := json.Unmarshal(raw, &observed); err != nil {
		return nativeBridgeConfig{}, err
	}
	gateway, err := netip.ParseAddr(observed.Start)
	if err != nil || !gateway.Is4() || !gateway.IsPrivate() {
		return nativeBridgeConfig{}, errors.New("vmnet requires an observed private IPv4 gateway")
	}
	last, err := netip.ParseAddr(observed.End)
	if err != nil || !last.Is4() || !last.IsPrivate() || last.Compare(gateway) <= 0 {
		return nativeBridgeConfig{}, errors.New("vmnet requires an observed private IPv4 allocation range")
	}
	mask := net.ParseIP(observed.Mask).To4()
	if mask == nil {
		return nativeBridgeConfig{}, errors.New("vmnet returned an invalid IPv4 mask")
	}
	ones, bits := net.IPMask(mask).Size()
	if bits != 32 || ones < 8 || ones > 30 {
		return nativeBridgeConfig{}, errors.New("vmnet returned an unsupported IPv4 mask")
	}
	prefix := netip.PrefixFrom(gateway, ones).Masked()
	if !prefix.Contains(last) || gateway == prefix.Addr() || !prefix.Contains(last.Next()) {
		return nativeBridgeConfig{}, errors.New("vmnet gateway or allocation end is outside the host range")
	}
	return nativeBridgeConfig{prefix, gateway, last, true}, nil
}

func (owner *nativeBridgeOwner) Stop(ctx context.Context) error {
	if owner == nil {
		return nil
	}
	var result error
	if owner.connection != nil {
		result = owner.connection.Close()
		owner.connection = nil
	}
	if owner.process != nil {
		result = errors.Join(result, owner.process.Stop(ctx))
	}
	if owner.done != nil {
		select {
		case <-owner.done:
		case <-ctx.Done():
			return errors.Join(result, ErrUnjoined, ctx.Err())
		}
	}
	return result
}
