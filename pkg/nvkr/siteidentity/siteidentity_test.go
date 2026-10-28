// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package siteidentity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"net/url"
	"runtime"
	"testing"
	"time"

	pb "github.com/dsx-ai-factory/nvke/agent/pkg/agent/proto"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func validRequest() Request {
	return Request{SchemaVersion: SchemaVersion, Operation: "probe", Endpoint: "192.168.70.10:15005", ExpectedPeer: "spiffe://example.test/host/one", ExpectedCaller: "spiffe://example.test/nvke-service/controller", WorkloadAPI: "unix:///spiffe-workload-api/spire-agent.sock", TrustDomain: "example.test", BudgetSeconds: 240}
}

func TestAdmissionBeforeWork(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Skip("runtime admission is Linux ARM64")
	}
	for _, tc := range []struct {
		name string
		edit func(*Request)
	}{
		{"unknown operation", func(r *Request) { r.Operation = "exec" }},
		{"JWT unsupported", func(r *Request) { r.TLSJWTEnabled = true }},
		{"external mTLS unsupported", func(r *Request) { r.ExternalMTLSEnabled = true }},
		{"external endpoint", func(r *Request) { r.Endpoint = "203.0.113.1:15005" }},
		{"hostname endpoint", func(r *Request) { r.Endpoint = "node.example.test:15005" }},
		{"foreign caller", func(r *Request) { r.ExpectedCaller = "spiffe://other.test/service/controller" }},
		{"foreign peer", func(r *Request) { r.ExpectedPeer = "spiffe://other.test/host/one" }},
		{"socket relay", func(r *Request) { r.WorkloadAPI = "tcp://127.0.0.1:8080" }},
		{"unbounded budget", func(r *Request) { r.BudgetSeconds = 301 }},
		{"missing permanent endpoint", func(r *Request) { r.Operation = "handoff" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validRequest()
			tc.edit(&r)
			ret, err := Run(context.Background(), r)
			if err == nil || ret.CallerID != "" {
				t.Fatalf("rejected Request reached workload or RPC work: result=%+v err=%v", ret, err)
			}
		})
	}
}

type testIdentity struct {
	svid   *x509svid.SVID
	bundle *x509bundle.Bundle
}

func (s testIdentity) GetX509SVID() (*x509svid.SVID, error) { return s.svid, nil }
func (s testIdentity) GetX509BundleForTrustDomain(td spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	if s.bundle.TrustDomain() != td {
		return nil, errors.New("unrecognized synthetic trust domain")
	}
	return s.bundle, nil
}

func identities(t *testing.T) (testIdentity, testIdentity) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	td := spiffeid.RequireTrustDomainFromString("example.test")
	bundle := x509bundle.FromX509Authorities(td, []*x509.Certificate{ca})
	makeIdentity := func(raw string, serial int64) testIdentity {
		id := spiffeid.RequireFromString(raw)
		leafKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		u, e := url.Parse(raw)
		if e != nil {
			t.Fatal(e)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), URIs: []*url.URL{u}, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
		b, e := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if e != nil {
			t.Fatal(e)
		}
		leaf, e = x509.ParseCertificate(b)
		if e != nil {
			t.Fatal(e)
		}
		return testIdentity{svid: &x509svid.SVID{ID: id, Certificates: []*x509.Certificate{leaf}, PrivateKey: leafKey}, bundle: bundle}
	}
	return makeIdentity(validRequest().ExpectedCaller, 2), makeIdentity(validRequest().ExpectedPeer, 3)
}

type nodeServer struct {
	pb.UnimplementedNodeAgentServer
	entered chan struct{}
	joined  chan struct{}
	status  *pb.GetSpireAgentStatusResponse
}

func (s *nodeServer) GetSpireAgentStatus(context.Context, *pb.GetSpireAgentStatusRequest) (*pb.GetSpireAgentStatusResponse, error) {
	return s.status, nil
}

func (s *nodeServer) GetNodeBootInfo(ctx context.Context, _ *pb.GetNodeBootInfoRequest) (*pb.GetNodeBootInfoResponse, error) {
	if s.entered != nil {
		close(s.entered)
		<-ctx.Done()
		close(s.joined)
		return nil, status.Error(codes.Canceled, "synthetic cancellation")
	}
	return &pb.GetNodeBootInfoResponse{BootId: "synthetic-boot-id", BootTimeUnixSeconds: 123}, nil
}

func serveNode(t *testing.T, source testIdentity, n *nodeServer) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config := tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeID(spiffeid.RequireFromString(validRequest().ExpectedCaller)))
	s := grpc.NewServer(grpc.Creds(credentials.NewTLS(config)))
	pb.RegisterNodeAgentServer(s, n)
	done := make(chan error, 1)
	go func() { done <- s.Serve(listener) }()
	t.Cleanup(func() { s.Stop(); <-done })
	return listener.Addr().String()
}

func TestGeneratedClientRealMutualTLS(t *testing.T) {
	caller, server := identities(t)
	r := validRequest()
	r.Endpoint = serveNode(t, server, &nodeServer{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := probeAgent(ctx, r, caller)
	if err != nil {
		t.Fatal(err)
	}
	if p.PeerID != r.ExpectedPeer || p.Serial != "3" || p.BootID != "synthetic-boot-id" {
		t.Fatalf("RPC PeerProof differs: %+v", p)
	}
	wrong := r
	wrong.ExpectedPeer = "spiffe://example.test/host/wrong"
	ctxWrong, cancelWrong := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelWrong()
	if _, err := probeAgent(ctxWrong, wrong, caller); err == nil {
		t.Fatal("wrong SPIFFE peer accepted")
	}
	ctxCaller, cancelCaller := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelCaller()
	if _, err := probeAgent(ctxCaller, r, server); err == nil {
		t.Fatal("non-controller caller certificate accepted")
	}
}

func TestSourceOwnedStatusRequiresExactEndpointAndIdentity(t *testing.T) {
	caller, server := identities(t)
	for _, tc := range []struct {
		name  string
		edit  func(*pb.GetSpireAgentStatusResponse)
		valid bool
	}{
		{"confirmed", func(*pb.GetSpireAgentStatusResponse) {}, true},
		{"wrong server", func(s *pb.GetSpireAgentStatusResponse) { s.ServerAddress = "192.168.70.99" }, false},
		{"wrong port", func(s *pb.GetSpireAgentStatusResponse) { s.ServerPort++ }, false},
		{"unapplied config", func(s *pb.GetSpireAgentStatusResponse) { s.ConfigApplied = false }, false},
		{"no valid SVID", func(s *pb.GetSpireAgentStatusResponse) { s.HasValidSvid = false }, false},
		{"different host", func(s *pb.GetSpireAgentStatusResponse) { s.SvidSpiffeId = "spiffe://example.test/host/other" }, false},
		{"expiring SVID", func(s *pb.GetSpireAgentStatusResponse) { s.SvidExpiresAtUnixSeconds = time.Now().Unix() }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validRequest()
			r.ServerAddress, r.ServerPort = "192.168.70.10", 30081
			status := &pb.GetSpireAgentStatusResponse{ServerAddress: r.ServerAddress, ServerPort: uint32(r.ServerPort), ConfigApplied: true, HasValidSvid: true, SvidSpiffeId: r.ExpectedPeer, SvidExpiresAtUnixSeconds: time.Now().Add(time.Hour).Unix()}
			tc.edit(status)
			r.Endpoint = serveNode(t, server, &nodeServer{status: status})
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			observed, err := observedStatus(ctx, r, caller)
			if (err == nil) != tc.valid || tc.valid && (observed == nil || observed.SPIFFEID != r.ExpectedPeer) {
				t.Fatalf("source status=%+v error=%v valid=%v", observed, err, tc.valid)
			}
		})
	}
}

func TestCallerPickerRetainsExactIdentityAcrossUpdates(t *testing.T) {
	expected := spiffeid.RequireFromString(validRequest().ExpectedCaller)
	other := spiffeid.RequireFromString("spiffe://example.test/nvke-service/other")
	old := &x509svid.SVID{ID: expected, Hint: "old synthetic certificate"}
	renewed := &x509svid.SVID{ID: expected, Hint: "renewed synthetic certificate"}
	foreign := &x509svid.SVID{ID: other}
	picker := callerPicker(expected.String())
	// Invoke the same upstream picker across successive updates; a changed list
	// order or removed identity cannot silently switch the TLS caller.
	for _, tc := range []struct {
		name  string
		svids []*x509svid.SVID
		want  *x509svid.SVID
	}{
		{"initial", []*x509svid.SVID{old}, old},
		{"foreign becomes default", []*x509svid.SVID{foreign, old}, old},
		{"same identity renews", []*x509svid.SVID{foreign, renewed}, renewed},
		{"identity removed", []*x509svid.SVID{foreign}, nil},
		{"empty update", nil, nil},
		{"nil entry", []*x509svid.SVID{nil, renewed}, renewed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := picker(tc.svids); got != tc.want {
				t.Fatalf("caller selection changed: got=%p want=%p", got, tc.want)
			}
		})
	}
}

func TestProbeCancellationJoinsServer(t *testing.T) {
	caller, server := identities(t)
	n := &nodeServer{entered: make(chan struct{}), joined: make(chan struct{})}
	r := validRequest()
	r.Endpoint = serveNode(t, server, n)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	joinedProbe := make(chan struct{})
	go func() {
		defer close(joinedProbe)
		_, err := probeAgent(ctx, r, caller)
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-joinedProbe:
		case <-time.After(5 * time.Second):
			t.Error("probe fixture failed to join")
		}
	})
	select {
	case <-n.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("RPC did not enter")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled RPC succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not join")
	}
	select {
	case <-n.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("server RPC did not join")
	}
}

func TestFreshProofRequiresSameBootAndNewCertificate(t *testing.T) {
	now := time.Now()
	before := PeerProof{PeerID: "spiffe://example.test/host/one", Serial: "a", BootID: "boot", NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	fresh := PeerProof{PeerID: before.PeerID, Serial: "b", BootID: before.BootID, NotBefore: now.Add(-10 * time.Second), NotAfter: now.Add(time.Hour)}
	for _, tc := range []struct {
		name string
		edit func(*PeerProof)
		want bool
	}{
		{"renewed", func(*PeerProof) {}, true},
		{"cached certificate", func(p *PeerProof) { p.Serial = before.Serial }, false},
		{"different boot", func(p *PeerProof) { p.BootID = "other" }, false},
		{"different peer", func(p *PeerProof) { p.PeerID = "spiffe://other.test/host/one" }, false},
		{"stale different serial", func(p *PeerProof) { p.NotBefore = now.Add(-2 * time.Hour) }, false},
		{"expired certificate", func(p *PeerProof) { p.NotAfter = now.Add(-time.Second) }, false},
		{"nearly expired certificate", func(p *PeerProof) { p.NotAfter = now.Add(30 * time.Second) }, false},
		{"future issuance", func(p *PeerProof) { p.NotBefore = now.Add(time.Minute) }, false},
		{"missing issuance", func(p *PeerProof) { p.NotBefore = time.Time{} }, false},
		{"missing expiry", func(p *PeerProof) { p.NotAfter = time.Time{} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fresh
			tc.edit(&p)
			if got := FreshPeerProof(before, p, now, now); got != tc.want {
				t.Fatalf("got%v want%v", got, tc.want)
			}
		})
	}
	for _, field := range []string{"peer", "serial", "boot"} {
		t.Run("missing original "+field, func(t *testing.T) {
			original := before
			switch field {
			case "peer":
				original.PeerID = ""
			case "serial":
				original.Serial = ""
			case "boot":
				original.BootID = ""
			}
			if FreshPeerProof(original, fresh, now, now) {
				t.Fatal("incomplete original observation accepted")
			}
		})
	}
}
