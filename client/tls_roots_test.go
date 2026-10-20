package client_test

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spfuzzylink/questlock/client"
)

func TestExplicitBrokerTLSRootsVerifyPrivateCAWithoutDisablingTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-private" {
			t.Error("missing scoped credential")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"scope":"test","key":"k","version":1,"content":"value","updated_at":"2030-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()
	ordinary, err := client.New(srv.URL, "fixture-private")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ordinary.Get(context.Background(), "k"); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	root := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	trusted, err := client.NewWithTLSRoots(srv.URL, "fixture-private", root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := trusted.Get(context.Background(), "k")
	if err != nil || v.Content != "value" {
		t.Fatal(v, err)
	}
	if _, err = client.NewWithTLSRoots("http://127.0.0.1:8080", "fixture-private", root); err == nil {
		t.Fatal("CA accepted for plaintext")
	}
	if _, err = client.NewWithTLSRoots(srv.URL, "fixture-private", []byte("not a CA")); err == nil {
		t.Fatal("invalid roots accepted")
	}
}
