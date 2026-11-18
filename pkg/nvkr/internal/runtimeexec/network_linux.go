// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runtimeexec

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"golang.org/x/sys/unix"
)

type networkChild struct {
	done chan struct{}
	err  error
}

type networkLog struct {
	mu     sync.Mutex
	writer io.Writer
}

func (log *networkLog) Write(p []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.writer.Write(p)
}

func startNetworkChild(ctx context.Context, cancel context.CancelFunc, exits chan<- *networkChild, self *heldfile.File,
	record requestRecord, files []*os.File, stdout, stderr io.Writer,
) *networkChild {
	child := &networkChild{done: make(chan struct{})}
	go func() {
		child.err = executeChild(ctx, self, record, files, stdout, stderr)
		// Publish the first exit before cancellation can cause the peer to exit.
		// A helper failure must never be mistaken for consumer success or timeout.
		exits <- child
		cancel()
		close(child.done)
	}()
	return child
}

// executeOutbound joins both namespace owners before releasing any descriptor.
// A helper exit cancels the consumer; a consumer exit cancels the helper. The
// consumer cannot execute authored code until TAP handoff and helper readiness.
func executeOutbound(ctx context.Context, self *heldfile.File, record requestRecord,
	files []*os.File, stdout, stderr io.Writer,
) error {
	if stderr != nil {
		stderr = &networkLog{writer: stderr}
	}
	dns, err := readHostResolver(ctx)
	if err != nil {
		return err
	}
	control, consumerSocket, err := networkSocketPair()
	if err != nil {
		return err
	}
	defer func() { _ = control.Close() }()
	defer consumerSocket.Close()
	ready, helperSocket, err := networkSocketPair()
	if err != nil {
		return err
	}
	defer func() { _ = ready.Close() }()
	defer helperSocket.Close()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	controlClosed := make(chan struct{})
	stop := context.AfterFunc(runCtx, func() {
		_ = control.Close()
		_ = ready.Close()
		close(controlClosed)
	})
	defer func() {
		if !stop() {
			<-controlClosed
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := errors.Join(control.SetDeadline(deadline), ready.SetDeadline(deadline)); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "bounding outbound readiness")
	}
	record.NetworkFD = nextDescriptor(record)
	consumerFiles := append(append([]*os.File(nil), files...), consumerSocket)
	exits := make(chan *networkChild, 2)
	consumer := startNetworkChild(runCtx, cancel, exits, self, record, consumerFiles, stdout, stderr)
	defer func() { cancel(); <-consumer.done }()
	tap, err := receiveNetworkTAP(control)
	if err != nil {
		return err
	}
	defer tap.Close()
	helperRecord := requestRecord{Mode: "outbound", UID: 65534, GID: 65534, RootFD: 5,
		NetworkFD: 6, ReadyFD: 7, Resolver: dns, Command: outboundCommand(), Environment: outboundEnvironment(), Workdir: "/"}
	helper := startNetworkChild(runCtx, cancel, exits, self, helperRecord, []*os.File{files[0], tap, helperSocket}, io.Discard, stderr)
	defer func() { cancel(); <-helper.done }()
	var signal [1]byte
	if n, err := ready.Read(signal[:]); err != nil || n != 1 || signal[0] != '1' {
		return e.Wrap(e.CodeEngineCapability, err, "outbound helper did not confirm readiness")
	}
	if _, err := control.Write([]byte{'1'}); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "releasing network-admitted consumer")
	}
	select {
	case first := <-exits:
		if first == consumer {
			return consumer.err
		}
		return e.Wrap(e.CodeEngineCapability, first.err, "outbound helper exited before the consumer")
	case <-ctx.Done():
		return e.Wrap(e.CodeTimeout, ctx.Err(), "outbound runtime execution canceled")
	}
}

func readHostResolver(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "outbound resolver observation canceled")
	}
	// Resolver symlinks are normal on the admitted host. Follow only this fixed
	// host configuration path, then require a protected regular file. NONBLOCK
	// prevents a substituted FIFO from hanging admission before that check.
	fd, err := unix.Open("/etc/resolv.conf", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "opening outbound resolver configuration")
	}
	resolver := os.NewFile(uintptr(fd), "outbound-resolver")
	defer resolver.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "observing outbound resolver configuration")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o022 != 0 || stat.Size > 64<<10 {
		return "", e.New(e.CodeEngineCapability, "outbound resolver requires protected regular host configuration (uid=%d mode=%o size=%d)", stat.Uid, stat.Mode, stat.Size)
	}
	if stat.Uid != 0 {
		if err := admitManagedResolver(ctx, stat); err != nil {
			return "", err
		}
	}
	raw, err := io.ReadAll(io.LimitReader(resolver, (64<<10)+1))
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "reading outbound resolver configuration")
	}
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "outbound resolver observation canceled")
	}
	dns := resolverConfiguration(raw)
	if dns == "" {
		return "", e.New(e.CodeEngineCapability, "outbound helper requires bounded numeric resolver addresses")
	}
	return dns, nil
}

func outboundHostAdmission(ctx context.Context) error {
	_, err := readHostResolver(ctx)
	return err
}

func networkSocketPair() (*net.UnixConn, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, e.Wrap(e.CodeEngineCapability, err, "creating private network control sockets")
	}
	parent := os.NewFile(uintptr(fds[0]), "runtime-network-parent")
	child := os.NewFile(uintptr(fds[1]), "runtime-network-child")
	connection, err := net.FileConn(parent)
	closeErr := parent.Close()
	if err != nil || closeErr != nil {
		_ = child.Close()
		if connection != nil {
			_ = connection.Close()
		}
		return nil, nil, e.Wrap(e.CodeEngineCapability, errors.Join(err, closeErr), "holding private network control")
	}
	return connection.(*net.UnixConn), child, nil
}

func receiveNetworkTAP(connection *net.UnixConn) (*os.File, error) {
	var signal [1]byte
	control := make([]byte, unix.CmsgSpace(4))
	n, size, flags, _, err := connection.ReadMsgUnix(signal[:], control)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "receiving private TAP descriptor")
	}
	messages, parseErr := unix.ParseSocketControlMessage(control[:size])
	var fds []int
	for _, message := range messages {
		rights, err := unix.ParseUnixRights(&message)
		parseErr = errors.Join(parseErr, err)
		fds = append(fds, rights...)
	}
	accepted := false
	defer func() {
		if !accepted {
			for _, fd := range fds {
				_ = unix.Close(fd)
			}
		}
	}()
	if parseErr != nil || n != 1 || signal[0] != 'T' || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || len(fds) != 1 {
		return nil, e.New(e.CodeVerifyFailed, "network setup returned an invalid TAP handoff")
	}
	unix.CloseOnExec(fds[0])
	device, err := unix.NewIfreq("tap0")
	if err != nil {
		return nil, entryError(err)
	}
	if err := unix.IoctlIfreq(fds[0], unix.TUNGETIFF, device); err != nil || device.Name() != "tap0" || device.Uint16()&(unix.IFF_TAP|unix.IFF_NO_PI) != unix.IFF_TAP|unix.IFF_NO_PI {
		return nil, e.New(e.CodeVerifyFailed, "network setup did not return its fixed TAP device")
	}
	accepted = true
	return os.NewFile(uintptr(fds[0]), "runtime-private-tap"), nil
}

func connectPrivateNetwork(fd int) error {
	tap, err := createPrivateTAP()
	if err != nil {
		return err
	}
	defer tap.Close()
	if err := unix.Sendmsg(fd, []byte{'T'}, unix.UnixRights(int(tap.Fd())), nil, 0); err != nil {
		return entryError(err)
	}
	var signal [1]byte
	if n, err := unix.Read(fd, signal[:]); err != nil || n != 1 || signal[0] != '1' {
		return e.Wrap(e.CodeEngineCapability, err, "private network owner did not release the consumer")
	}
	return entryError(unix.Close(fd))
}

func createPrivateTAP() (_ *os.File, returnErr error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "opening fixed TAP API device")
	}
	tap := os.NewFile(uintptr(fd), "runtime-private-tap")
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, tap.Close())
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(stat.Rdev) != 10 || unix.Minor(stat.Rdev) != 200 {
		return nil, e.New(e.CodeEngineCapability, "TAP setup requires the fixed TUN character device")
	}
	device, err := unix.NewIfreq("tap0")
	if err != nil {
		return nil, entryError(err)
	}
	device.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, device); err != nil {
		return nil, entryError(err)
	}
	socket, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, entryError(err)
	}
	defer func() { _ = unix.Close(socket) }()
	for _, address := range []struct {
		operation uint
		value     []byte
	}{{unix.SIOCSIFADDR, []byte{10, 0, 2, 100}}, {unix.SIOCSIFNETMASK, []byte{255, 255, 255, 0}}} {
		if err := device.SetInet4Addr(address.value); err != nil {
			return nil, entryError(err)
		}
		if err := unix.IoctlIfreq(socket, address.operation, device); err != nil {
			return nil, entryError(err)
		}
	}
	device.SetUint32(1500)
	if err := unix.IoctlIfreq(socket, unix.SIOCSIFMTU, device); err != nil {
		return nil, entryError(err)
	}
	// This adapter deliberately exposes IPv4 only. Do not leave an automatically
	// configured IPv6 interface beside the fixed route/DNS contract.
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/tap0/disable_ipv6", []byte("1\n"), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, entryError(err)
	}
	device.SetUint16(unix.IFF_UP)
	if err := unix.IoctlIfreq(socket, unix.SIOCSIFFLAGS, device); err != nil {
		return nil, entryError(err)
	}
	if err := unix.IoctlIfreq(socket, unix.SIOCGIFINDEX, device); err != nil {
		return nil, entryError(err)
	}
	if err := privateDefaultRoute(device.Uint32()); err != nil {
		return nil, err
	}
	return tap, nil
}

func privateDefaultRoute(index uint32) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return entryError(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 3}); err != nil {
		return entryError(err)
	}
	// Fixed IPv4 default route; every integer uses the kernel's native byte order.
	message := make([]byte, 44)
	binary.NativeEndian.PutUint32(message[0:4], uint32(len(message)))
	binary.NativeEndian.PutUint16(message[4:6], unix.RTM_NEWROUTE)
	binary.NativeEndian.PutUint16(message[6:8], unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE|unix.NLM_F_EXCL)
	binary.NativeEndian.PutUint32(message[8:12], 1)
	message[16], message[20], message[21], message[23] = unix.AF_INET, unix.RT_TABLE_MAIN, unix.RTPROT_BOOT, unix.RTN_UNICAST
	binary.NativeEndian.PutUint16(message[28:30], 8)
	binary.NativeEndian.PutUint16(message[30:32], unix.RTA_GATEWAY)
	copy(message[32:36], []byte{10, 0, 2, 2})
	binary.NativeEndian.PutUint16(message[36:38], 8)
	binary.NativeEndian.PutUint16(message[38:40], unix.RTA_OIF)
	binary.NativeEndian.PutUint32(message[40:44], index)
	if err := unix.Sendto(fd, message, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return entryError(err)
	}
	var response [4096]byte
	n, from, err := unix.Recvfrom(fd, response[:], 0)
	peer, ok := from.(*unix.SockaddrNetlink)
	if err != nil || !ok || peer.Pid != 0 || n < 20 || binary.NativeEndian.Uint16(response[4:6]) != unix.NLMSG_ERROR ||
		binary.NativeEndian.Uint32(response[8:12]) != 1 || binary.NativeEndian.Uint32(response[16:20]) != 0 {
		return e.Wrap(e.CodeEngineCapability, err, "kernel rejected private runtime default route")
	}
	return nil
}
