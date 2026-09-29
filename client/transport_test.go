package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/protocol"
)

func TestDedicatedTransportIgnoresEnvironmentProxies(t *testing.T) {
	var proxyCalls, directCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(name, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing bearer credential on direct request")
		}
		_ = json.NewEncoder(w).Encode(protocol.Artifact{Key: "result", Content: "direct"})
	}))
	defer server.Close()
	c, err := New("https://broker.example", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok || transport == http.DefaultTransport || transport.Proxy != nil {
		t.Fatal("client must own a dedicated transport with proxies disabled")
	}
	defer transport.CloseIdleConnections()
	certificate := server.Certificate()
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: certificate.DNSNames[0]}
	// Dial the local test server while retaining a non-loopback URL. This makes
	// the test exercise a hostname that environment proxy rules would route.
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "broker.example:443" {
			t.Errorf("unexpected dial target: %q", address)
		}
		directCalls.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "https://"))
	}
	artifact, err := c.Get(context.Background(), "result")
	if err != nil || artifact.Content != "direct" || directCalls.Load() != 1 || proxyCalls.Load() != 0 {
		t.Fatalf("request was not direct: artifact=%+v err=%v direct=%d proxy=%d", artifact, err, directCalls.Load(), proxyCalls.Load())
	}
}

func TestHTTPSVerifiesServerCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.Artifact{})
	}))
	defer server.Close()
	c, err := New(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer c.httpClient.CloseIdleConnections()
	if _, err := c.Get(context.Background(), "result"); err == nil {
		t.Fatal("HTTPS accepted an untrusted server certificate")
	}
}
