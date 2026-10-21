// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package siteidentity owns the NVKE site-host identity probe and source-owned retarget RPC.
// Its descriptive Result does not authorize bootstrap removal or release claims.
package siteidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"time"

	pb "github.com/dsx-ai-factory/nvke/agent/pkg/agent/proto"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// Request describes selected runtime inputs. The caller must authenticate their
// artifact, source and Pod membership before invoking this operation.
type Request struct {
	SchemaVersion       string `json:"schemaVersion"`
	Operation           string `json:"operation"`
	Endpoint            string `json:"endpoint"`
	ExpectedPeer        string `json:"expectedPeer"`
	ExpectedCaller      string `json:"expectedCaller"`
	WorkloadAPI         string `json:"workloadAPI"`
	TrustDomain         string `json:"trustDomain"`
	ServerAddress       string `json:"serverAddress"`
	ServerPort          uint16 `json:"serverPort"`
	TLSJWTEnabled       bool   `json:"tlsJWTEnabled"`
	ExternalMTLSEnabled bool   `json:"externalMTLSEnabled"`
	BudgetSeconds       int    `json:"budgetSeconds"`
}

// PeerProof contains certificate metadata from an authenticated exact-peer RPC.
// It contains neither the certificate nor a key or token.
type PeerProof struct {
	PeerID    string    `json:"peerID"`
	Serial    string    `json:"serial"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	BootID    string    `json:"bootID"`
}

// AgentStatus projects the source-owned status RPC; it is not a bootstrap receipt.
type AgentStatus struct {
	ServerAddress string `json:"serverAddress"`
	ServerPort    uint32 `json:"serverPort"`
	ConfigApplied bool   `json:"configApplied"`
	HasValidSVID  bool   `json:"hasValidSVID"`
	SPIFFEID      string `json:"spiffeID"`
	ExpiresAt     int64  `json:"expiresAt"`
}

// Completion describes the remote mutation's observed lifecycle. It must be
// checked independently from an exit code. Unknown requires dependency retention.
type Completion string

const (
	NotStarted    Completion = "not-started"
	JoinedFailure Completion = "joined-failure"
	JoinedSuccess Completion = "joined-success"
	Unknown       Completion = "unknown"
)

// ErrRemoteCompletionUnknown means the synchronous RPC has no accepted terminal
// response. A joined local helper process does not prove the remote command joined.
var ErrRemoteCompletionUnknown = errors.New("remote operation completion is unknown")

const SchemaVersion = "nvkr.dev/native-site-identity/v1"

// Result is descriptive operational evidence only. Permanent SPIRE policy,
// registration, TPM identity and artifact membership remain the native owner's job.
type Result struct {
	SchemaVersion   string       `json:"schemaVersion"`
	Completion      Completion   `json:"completion"`
	FailureCode     e.Code       `json:"failureCode,omitempty"`
	Operation       string       `json:"operation"`
	CallerID        string       `json:"callerID"`
	Before          PeerProof    `json:"before"`
	Reconfiguration string       `json:"reconfiguration,omitempty"`
	Status          *AgentStatus `json:"status,omitempty"`
	After           *PeerProof   `json:"after,omitempty"`
}

type agentIdentity interface {
	x509svid.Source
	x509bundle.Source
}

func validate(r Request) error {
	if r.SchemaVersion != SchemaVersion {
		return errors.New("unsupported request schema")
	}
	if r.Operation != "probe" && r.Operation != "handoff" {
		return errors.New("operation must be probe or handoff")
	}
	if r.TLSJWTEnabled || r.ExternalMTLSEnabled {
		return errors.New("only the admitted SPIFFE-only profile is supported")
	}
	if r.BudgetSeconds < 30 || r.BudgetSeconds > 300 {
		return errors.New("budget must be 30..300 seconds")
	}
	if r.WorkloadAPI != "unix:///spiffe-workload-api/spire-agent.sock" {
		return errors.New("require the observed controller CSI Workload API socket")
	}
	host, port, err := net.SplitHostPort(r.Endpoint)
	if err != nil {
		return fmt.Errorf("agent endpoint: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Is4() || !ip.IsPrivate() || ip.String() != host {
		return errors.New("agent endpoint must use a canonical private IPv4 address")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != port {
		return errors.New("agent endpoint requires a canonical port")
	}
	td, err := spiffeid.TrustDomainFromString(r.TrustDomain)
	if err != nil {
		return fmt.Errorf("trust domain: %w", err)
	}
	if td.Name() != r.TrustDomain {
		return errors.New("trust domain must be the canonical domain name")
	}
	for _, raw := range []string{r.ExpectedCaller, r.ExpectedPeer} {
		id, err := spiffeid.FromString(raw)
		if err != nil || id.TrustDomain() != td {
			return errors.New("caller and peer must belong to the admitted trust domain")
		}
		if raw == r.ExpectedCaller && !strings.HasPrefix(id.Path(), "/nvke-service/") {
			return errors.New("caller must be a selected NVKE service identity")
		}
		if raw == r.ExpectedPeer && !strings.HasPrefix(id.Path(), "/host/") {
			return errors.New("peer must be a selected NVKE host identity")
		}
	}
	if r.Operation == "probe" {
		return nil
	}
	if r.BudgetSeconds < 240 || r.ServerAddress != host || r.ServerPort == 0 {
		return errors.New("handoff requires its site address, observed SPIRE port and at least 240 seconds")
	}
	return nil
}

// probe opens a new authenticated connection so cached channels cannot stand in
// for a new certificate presentation. SPIFFE verifies the chain and exact URI.
func probeAgent(ctx context.Context, r Request, source agentIdentity) (out PeerProof, resultErr error) {
	conn, err := dial(r, source)
	if err != nil {
		return PeerProof{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, conn.Close()) }()
	var p peer.Peer
	boot, err := pb.NewNodeAgentClient(conn).GetNodeBootInfo(ctx, &pb.GetNodeBootInfoRequest{}, grpc.Peer(&p))
	if err != nil {
		return PeerProof{}, err
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return PeerProof{}, errors.New("authenticated RPC did not provide TLS peer metadata")
	}
	c := tlsInfo.State.PeerCertificates[0]
	if len(c.URIs) != 1 || c.URIs[0].String() != r.ExpectedPeer {
		return PeerProof{}, errors.New("RPC peer metadata differs from its admitted SPIFFE identity")
	}
	if boot.GetBootId() == "" {
		return PeerProof{}, errors.New("agent did not return its boot identity")
	}
	return PeerProof{PeerID: r.ExpectedPeer, Serial: strings.ToLower(c.SerialNumber.Text(16)), NotBefore: c.NotBefore, NotAfter: c.NotAfter, BootID: boot.GetBootId()}, nil
}

func dial(r Request, source agentIdentity) (*grpc.ClientConn, error) {
	expected, err := spiffeid.FromString(r.ExpectedPeer)
	if err != nil {
		return nil, err
	}
	// These are upstream SPIFFE and gRPC constructors, not a new TLS protocol.
	tls := tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeID(expected))
	return grpc.NewClient("passthrough:///"+r.Endpoint, grpc.WithTransportCredentials(credentials.NewTLS(tls)), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<10)))
}

// Run performs only authenticated probe/reconfigure/status/probe. A result cannot
// restore authority or replace the caller's full permanent-server identity proof.
func Run(ctx context.Context, r Request) (ret Result, resultErr error) {
	ret, resultErr = run(ctx, r)
	if resultErr != nil {
		if ret.Completion == JoinedSuccess {
			ret.Completion = JoinedFailure
		}
		var coded *e.Error
		if !errors.As(resultErr, &coded) {
			code := e.CodePreflightFailed
			if errors.Is(resultErr, context.Canceled) || errors.Is(resultErr, context.DeadlineExceeded) {
				code = e.CodeTimeout
			}
			resultErr = e.Wrap(code, resultErr, "site identity operation failed")
		}
		if errors.As(resultErr, &coded) {
			ret.FailureCode = coded.Code
		}
	}
	return ret, resultErr
}

func run(ctx context.Context, r Request) (ret Result, resultErr error) {
	ret.SchemaVersion = SchemaVersion
	ret.Completion = NotStarted
	if ctx == nil {
		return ret, e.New(e.CodeUsage, "site identity requires a context")
	}
	if err := ctx.Err(); err != nil {
		return ret, err
	}
	if err := admitRuntime(ctx); err != nil {
		return ret, err
	}
	ret.Operation = r.Operation
	if err := validate(r); err != nil {
		return ret, e.Wrap(e.CodeUsage, err, "invalid site identity request")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(r.BudgetSeconds)*time.Second)
	defer cancel()
	source, err := workloadapi.NewX509Source(ctx,
		workloadapi.WithClientOptions(workloadapi.WithAddr(r.WorkloadAPI)),
		workloadapi.WithDefaultX509SVIDPicker(callerPicker(r.ExpectedCaller)))
	if err != nil {
		return ret, err
	}
	defer func() { resultErr = errors.Join(resultErr, source.Close()) }()
	svid, err := source.GetX509SVID()
	if err != nil || svid == nil || svid.ID.String() != r.ExpectedCaller {
		return ret, errors.New("workload API did not issue the admitted controller identity")
	}
	ret.CallerID = svid.ID.String()
	probeCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	ret.Before, err = probeAgent(probeCtx, r, source)
	stop()
	if err != nil || r.Operation == "probe" {
		return ret, err
	}
	if err := ctx.Err(); err != nil {
		return ret, err
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < 225*time.Second {
		return ret, errors.New("insufficient budget to dispatch and join source-owned reconfiguration")
	}
	conn, err := dial(r, source)
	if err != nil {
		return ret, err
	}
	dispatchedAt := time.Now()
	client := pb.NewNodeAgentClient(conn)
	completion, outcome, rpcErr := reconfigure(ctx, r, client.ReconfigureSpireAgent, 210*time.Second)
	ret.Completion = completion
	ret.Reconfiguration = outcome
	closeErr := conn.Close()
	if rpcErr != nil {
		return ret, errors.Join(rpcErr, closeErr)
	}
	if closeErr != nil {
		return ret, e.Wrap(e.CodePreflightFailed, closeErr, "close identity RPC")
	}
	if err := ctx.Err(); err != nil {
		return ret, err
	}
	var lastErr error
	for ctx.Err() == nil {
		attempt, stop := context.WithTimeout(ctx, 5*time.Second)
		after, err := probeAgent(attempt, r, source)
		if err == nil {
			ret.Status, err = observedStatus(attempt, r, source)
		}
		stop()
		if err == nil && FreshPeerProof(ret.Before, after, dispatchedAt, time.Now()) {
			ret.After = &after
			ret.Completion = JoinedSuccess
			return ret, nil
		}
		lastErr = err
		if err == nil {
			lastErr = errors.New("fresh workload SVID not yet presented, or boot identity changed")
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return ret, errors.Join(ctx.Err(), lastErr)
}

// FreshPeerProof checks descriptive peer metadata, not endpoint authentication
// or artifact authority. Each caller must authenticate and bind those observations.
// The selected SPIRE agent may backdate a newly issued certificate slightly.
// Accept at most one minute of issuance skew, and require another minute of
// validity at observation. Neither a changed serial nor old valid bytes suffice.
func FreshPeerProof(before, after PeerProof, dispatchedAt, observedAt time.Time) bool {
	return before.PeerID != "" && before.Serial != "" && before.BootID != "" && after.BootID == before.BootID && after.PeerID == before.PeerID && after.Serial != "" && after.Serial != before.Serial &&
		!after.NotBefore.IsZero() && !after.NotAfter.IsZero() && !after.NotBefore.Before(dispatchedAt.Add(-time.Minute)) && !after.NotBefore.After(observedAt) && after.NotAfter.After(observedAt.Add(time.Minute))
}

// The upstream source invokes this picker on every Workload API update. A
// renewed certificate may change; the admitted caller identity must not.
func callerPicker(expected string) func([]*x509svid.SVID) *x509svid.SVID {
	return func(svids []*x509svid.SVID) *x509svid.SVID {
		for _, svid := range svids {
			if svid != nil && svid.ID.String() == expected {
				return svid
			}
		}
		return nil
	}
}

// MaxRequestBytes bounds the complete request, including trailing whitespace.
const MaxRequestBytes = 16 << 10

// DecodeRequest uses the existing canonical document validator before standard
// JSON decoding. No custom JSON or SPIFFE grammar is implemented here.
func DecodeRequest(ctx context.Context, raw []byte) (Request, error) {
	var r Request
	if ctx == nil {
		return r, e.New(e.CodeUsage, "request decoding requires a context")
	}
	if err := ctx.Err(); err != nil {
		return r, e.Wrap(e.CodeTimeout, err, "decode site identity request")
	}
	if len(raw) > MaxRequestBytes {
		return r, e.New(e.CodeUsage, "site identity request exceeds its byte bound")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return r, e.Wrap(e.CodeUsage, err, "invalid site identity document")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, e.Wrap(e.CodeUsage, err, "invalid site identity fields")
	}
	if err := validate(r); err != nil {
		return r, e.Wrap(e.CodeUsage, err, "invalid site identity request")
	}
	return r, nil
}

// reconfigure delegates mutation to NVKE. Its systemd runner may outlive an RPC
// deadline, so only applied/unchanged replies establish remote completion. A
// failed outcome does not distinguish a queued systemd job from a joined failure.
func reconfigure(ctx context.Context, r Request, call func(context.Context, *pb.ReconfigureSpireAgentRequest, ...grpc.CallOption) (*pb.ReconfigureSpireAgentResponse, error), drain time.Duration) (completion Completion, outcome string, resultErr error) {
	defer func() {
		if recover() != nil {
			completion = Unknown
			outcome = ""
			resultErr = e.Wrap(e.CodePreflightFailed, ErrRemoteCompletionUnknown, "remote client panicked after dispatch admission")
		}
	}()
	if err := ctx.Err(); err != nil {
		return NotStarted, "", e.Wrap(e.CodeTimeout, err, "site identity canceled before dispatch")
	}
	critical, stop := context.WithTimeout(context.WithoutCancel(ctx), drain)
	defer stop()
	resp, err := call(critical, &pb.ReconfigureSpireAgentRequest{ServerAddress: r.ServerAddress, ServerPort: uint32(r.ServerPort)})
	if err != nil {
		return Unknown, "", e.Wrap(e.CodePreflightFailed, errors.Join(ErrRemoteCompletionUnknown, critical.Err()), "remote completion unconfirmed; retain dependencies")
	}
	if resp == nil || resp.GetErrorMessage() != "" {
		return Unknown, "", e.Wrap(e.CodeVerifyFailed, ErrRemoteCompletionUnknown, "remote reconfiguration completion unconfirmed; retain dependencies")
	}
	switch resp.GetOutcome() {
	case pb.ReconfigureSpireAgentResponse_OUTCOME_APPLIED:
		return JoinedFailure, "applied", nil
	case pb.ReconfigureSpireAgentResponse_OUTCOME_UNCHANGED:
		return JoinedFailure, "unchanged", nil
	default:
		return Unknown, "", e.Wrap(e.CodeVerifyFailed, ErrRemoteCompletionUnknown, "remote reconfiguration completion unconfirmed; retain dependencies")
	}
}

func observedStatus(ctx context.Context, r Request, source agentIdentity) (_ *AgentStatus, resultErr error) {
	conn, err := dial(r, source)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, conn.Close()) }()
	status, err := pb.NewNodeAgentClient(conn).GetSpireAgentStatus(ctx, &pb.GetSpireAgentStatusRequest{})
	if err != nil {
		return nil, err
	}
	if status == nil || status.GetServerAddress() != r.ServerAddress || status.GetServerPort() != uint32(r.ServerPort) || !status.GetConfigApplied() || !status.GetHasValidSvid() || status.GetSvidSpiffeId() != r.ExpectedPeer || status.GetSvidExpiresAtUnixSeconds() <= time.Now().Add(time.Minute).Unix() {
		return nil, errors.New("source-owned SPIRE status does not confirm the admitted permanent endpoint and host identity")
	}
	return &AgentStatus{ServerAddress: status.GetServerAddress(), ServerPort: status.GetServerPort(), ConfigApplied: status.GetConfigApplied(), HasValidSVID: status.GetHasValidSvid(), SPIFFEID: status.GetSvidSpiffeId(), ExpiresAt: status.GetSvidExpiresAtUnixSeconds()}, nil
}

func admitRuntime(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "site identity requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "site identity canceled before admission")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return e.New(e.CodeEngineCapability, "site identity helper requires Linux ARM64")
	}
	return nil
}
