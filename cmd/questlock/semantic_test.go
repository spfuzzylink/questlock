package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSemanticSetupDoesNotRepullExistingWeights(t *testing.T) {
	var pulls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Write([]byte(`{"models":[{"name":"all-minilm:latest"}]}`))
			return
		}
		pulls.Add(1)
		t.Error("unexpected request", r.URL.Path)
	}))
	defer s.Close()
	if err := ensureSemanticModel(context.Background(), s.URL, "all-minilm"); err != nil {
		t.Fatal(err)
	}
	if pulls.Load() != 0 {
		t.Fatal("existing weights were replaced")
	}
}

func TestSemanticSetupPullsOnlyExplicitMissingModel(t *testing.T) {
	var pulls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Write([]byte(`{"models":[]}`))
		case "/api/pull":
			pulls.Add(1)
			if r.Method != "POST" {
				t.Error("not a POST")
			}
			w.Write([]byte(`{"status":"success"}`))
		default:
			t.Error("unexpected request", r.URL.Path)
		}
	}))
	defer s.Close()
	if err := ensureSemanticModel(context.Background(), s.URL, "all-minilm"); err != nil || pulls.Load() != 1 {
		t.Fatal(err, pulls.Load())
	}
}

func TestSemanticSetupRefusesRemoteURLAndRedirect(t *testing.T) {
	for _, u := range []string{"http://localhost:11434", "http://192.0.2.1:11434", "http://user:secret@127.0.0.1:11434", "http://127.0.0.1:11434/?token=secret", "https://127.0.0.1:11434"} {
		if ensureSemanticModel(context.Background(), u, "all-minilm") == nil {
			t.Fatal("accepted unsafe endpoint", u)
		}
	}
	var target atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { target.Add(1) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer source.Close()
	if ensureSemanticModel(context.Background(), source.URL, "all-minilm") == nil || target.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestSemanticSetupMalformedInventoryDoesNotPull(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"models":null}`, `{"models":{}}`, `{"models":[{}]}`, `{"models":[]`, `{"models":[]} {}`} {
		t.Run(raw, func(t *testing.T) {
			var pulls atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					w.Write([]byte(raw))
					return
				}
				pulls.Add(1)
				w.Write([]byte(`{"status":"success"}`))
			}))
			defer s.Close()
			if err := ensureSemanticModel(context.Background(), s.URL, "all-minilm"); err == nil || pulls.Load() != 0 {
				t.Fatal("malformed inventory authorized a model download", err, pulls.Load())
			}
		})
	}
}
