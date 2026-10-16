// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// curlStub answers like the GitHub App endpoints, recording each call's arguments, the headers it read from
// files and its request body. FAIL names a substring of the one URL that must fail.
const curlStub = `set -o pipefail
url= prev= method=GET
for arg in "$@"; do
  case "$prev" in
    -H) case "$arg" in @*) echo "header $(cat "${arg#@}")" >> "$LOG" ;; *) echo "header $arg" >> "$LOG" ;; esac ;;
    --request) method=$arg ;;
    --data) [ "$arg" = @- ] && echo "body $(cat)" >> "$LOG" ;;
  esac
  case "$arg" in https://*) url=$arg ;; esac
  prev=$arg
done
echo "args $*" >> "$LOG"
[ -n "${APP_PRIVATE_KEY:-}" ] && echo "inherited private key" >> "$LOG"
echo "call $method $url" >> "$LOG"
case "$url" in *"$FAIL"*) [ -n "$FAIL" ] && exit 22 ;; esac
case "$url" in
  */app) echo "{\"slug\": \"${SLUG-release-bot}\"}" ;;
  */installation) echo '{"id": 42}' ;;
  */access_tokens) echo '{"token": "ghs_minted"}' ;;
  */users/*) echo "{\"id\": ${BOT_ID-123}}" ;;
esac
`

// runWithCurl runs a rendered step with the stub as curl and returns the call log.
func runWithCurl(t *testing.T, script string, env ...string) (log, output string, err error) {
	t.Helper()
	temp := t.TempDir()
	bin := filepath.Join(temp, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte("#!/bin/bash\n"+curlStub), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(temp, "log")
	output, err = runScript(t, temp, script, append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "LOG=" + logPath,
		"GH_REPO=owner/repo", "GITHUB_API_URL=https://api.example.test"}, env...)...)
	raw, _ := os.ReadFile(logPath)
	return string(raw), output, err
}

func logLines(log, prefix string) []string {
	var lines []string
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, prefix) {
			lines = append(lines, strings.TrimPrefix(line, prefix))
		}
	}
	return lines
}

// The token step signs an App JWT with the private key, finds this repository's installation and mints a
// token for this repository only, with contents, pull requests and workflows. The JWT and the token reach curl
// only through header files, never as arguments, and both are masked.
func TestAppTokenStepMintsARepositoryScopedToken(t *testing.T) {
	requireTools(t, "bash", "openssl", "jq", "tr", "date")
	script := stepScript(t, "pull-request", "Create the App token")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	for _, tc := range []struct {
		name, clientID, key, fail string
		env                       []string
		ok                        bool
		// minted is true when the step fails after minting; it must still hand the token to the revoke step.
		minted bool
	}{
		{name: "mints", clientID: "Iv1.client", key: private, ok: true},
		{name: "missing client ID", key: private},
		{name: "missing private key", clientID: "Iv1.client"},
		{name: "malformed private key", clientID: "Iv1.client", key: "-----BEGIN RSA PRIVATE KEY-----\nbad\n-----END RSA PRIVATE KEY-----"},
		{name: "installation lookup failure", clientID: "Iv1.client", key: private, fail: "/installation"},
		{name: "token request failure", clientID: "Iv1.client", key: private, fail: "/access_tokens"},
		{name: "App lookup failure", clientID: "Iv1.client", key: private, fail: "/app"},
		{name: "bot user lookup failure", clientID: "Iv1.client", key: private, fail: "/users/", minted: true},
		{name: "slug that could reach the output", clientID: "Iv1.client", key: private, env: []string{"SLUG=bot\\ntoken=x"}, minted: true},
		{name: "non-numeric bot user id", clientID: "Iv1.client", key: private, env: []string{`BOT_ID="1x"`}, minted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "output")
			log, output, err := runWithCurl(t, script, append([]string{"APP_CLIENT_ID=" + tc.clientID, "APP_PRIVATE_KEY=" + tc.key, "GITHUB_OUTPUT=" + out, "FAIL=" + tc.fail}, tc.env...)...)
			got, _ := os.ReadFile(out)
			if !tc.ok {
				// A failure after minting writes only the token, so the revoke step still runs; earlier failures write nothing.
				want := ""
				if tc.minted {
					want = "token=ghs_minted\n"
				}
				if err == nil || string(got) != want {
					t.Fatalf("failure wrote %q, want %q: %s", got, want, output)
				}
				if tc.fail == "/installation" && strings.Contains(log, "access_tokens") {
					t.Fatalf("a failed installation lookup went on to request a token: %s", log)
				}
				return
			}
			want := "token=ghs_minted\nbot-name=release-bot[bot]\nbot-email=123+release-bot[bot]@users.noreply.github.com"
			if err != nil || strings.TrimSpace(string(got)) != want || !strings.Contains(output, "::add-mask::ghs_minted") {
				t.Fatalf("token not minted, written or masked: %v %s %s", err, output, got)
			}
			if calls := logLines(log, "call "); len(calls) != 4 || calls[0] != "GET https://api.example.test/app" ||
				calls[1] != "GET https://api.example.test/repos/owner/repo/installation" ||
				calls[2] != "POST https://api.example.test/app/installations/42/access_tokens" ||
				calls[3] != "GET https://api.example.test/users/release-bot%5Bbot%5D" {
				t.Fatalf("unexpected calls: %v", calls)
			}
			// The App calls use the JWT; the bot user lookup uses the minted token.
			var bearers []string
			for _, header := range logLines(log, "header ") {
				if credential, ok := strings.CutPrefix(header, "Authorization: Bearer "); ok {
					bearers = append(bearers, credential)
				}
			}
			if len(bearers) != 4 || bearers[0] != bearers[1] || bearers[1] != bearers[2] || bearers[3] != "ghs_minted" {
				t.Fatalf("unexpected credentials per call: %v", bearers)
			}
			bearer := bearers[0]
			parts := strings.Split(bearer, ".")
			if len(parts) != 3 || !strings.Contains(output, "::add-mask::"+bearer) {
				t.Fatalf("missing or unmasked JWT: %q", bearer)
			}
			for _, args := range logLines(log, "args ") {
				if strings.Contains(args, bearer) || strings.Contains(args, "Bearer") || strings.Contains(args, "ghs_") {
					t.Fatalf("a credential reached a curl argument: %s", args)
				}
			}
			if strings.Contains(log, "BEGIN RSA PRIVATE KEY") || strings.Contains(log, "inherited private key") {
				t.Fatalf("the private key reached curl: %s", log)
			}
			signature, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
				t.Fatalf("the JWT is not an RS256 signature by the App key: %v", err)
			}
			var header, claims map[string]any
			for i, target := range []*map[string]any{&header, &claims} {
				raw, err := base64.RawURLEncoding.DecodeString(parts[i])
				if err != nil || json.Unmarshal(raw, target) != nil {
					t.Fatalf("undecodable JWT part %d: %v", i, err)
				}
			}
			iat, _ := claims["iat"].(float64)
			exp, _ := claims["exp"].(float64)
			if header["alg"] != "RS256" || claims["iss"] != "Iv1.client" || exp-iat != 600 {
				t.Fatalf("unexpected JWT header %v or claims %v", header, claims)
			}
			bodies := logLines(log, "body ")
			if len(bodies) != 1 {
				t.Fatalf("want one token request body: %v", bodies)
			}
			var request struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			if err := json.Unmarshal([]byte(bodies[0]), &request); err != nil {
				t.Fatal(err)
			}
			if len(request.Repositories) != 1 || request.Repositories[0] != "repo" || len(request.Permissions) != 3 ||
				request.Permissions["contents"] != "write" || request.Permissions["pull_requests"] != "write" || request.Permissions["workflows"] != "write" {
				t.Fatalf("the token must be limited to this repository with contents, pull requests and workflows: %s", bodies[0])
			}
		})
	}
}

// The revoke step deletes the installation token through a header file, never an argument.
func TestAppTokenRevokeStepDeletesTheToken(t *testing.T) {
	requireTools(t, "bash")
	script := stepScript(t, "pull-request", "Revoke the App token")
	log, output, err := runWithCurl(t, script, "TOKEN=ghs_minted")
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	if calls := logLines(log, "call "); len(calls) != 1 || calls[0] != "DELETE https://api.example.test/installation/token" {
		t.Fatalf("unexpected calls: %v", calls)
	}
	if !strings.Contains(log, "header Authorization: Bearer ghs_minted") {
		t.Fatalf("the token was not sent: %s", log)
	}
	for _, args := range logLines(log, "args ") {
		if strings.Contains(args, "ghs_minted") {
			t.Fatalf("the token reached a curl argument: %s", args)
		}
	}
}
