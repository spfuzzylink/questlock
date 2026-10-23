package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceBuildVersionMatchesReleaseManifest(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if version != strings.TrimSpace(string(manifest)) {
		t.Fatal("source build version differs from VERSION; container and go run must identify the current source")
	}
}

func TestListenAddressRequiresExplicitRemoteOptIn(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "127.0.0.2:0", "[::1]:8080", "[::ffff:127.0.0.1]:8080"} {
		if err := validateListenAddress(address, false); err != nil {
			t.Errorf("literal loopback address %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "192.0.2.1:8080", "[2001:db8::1]:8080", "localhost:8080", "broker.example:8080", "127.1:8080", "127.0.0.1.example:8080"} {
		if err := validateListenAddress(address, false); err == nil || !strings.Contains(err.Error(), "--allow-remote-http") {
			t.Errorf("non-loopback address %q lacked required opt-in error: %v", address, err)
		}
		if err := validateListenAddress(address, true); err != nil {
			t.Errorf("explicit remote address %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{"127.0.0.1", "::1:8080", "127.0.0.1:", "127.0.0.1:http", "127.0.0.1:-1", "127.0.0.1:65536"} {
		for _, allowRemote := range []bool{false, true} {
			if err := validateListenAddress(address, allowRemote); err == nil {
				t.Errorf("invalid listen address %q accepted (allowRemote=%v)", address, allowRemote)
			}
		}
	}
}

func TestRejectedRemoteBindDoesNotCreateDatabase(t *testing.T) {
	dir := t.TempDir()
	for _, address := range []string{"0.0.0.0:0", ":0", "localhost:0"} {
		db := filepath.Join(dir, "unexpected", "state.db")
		err := serve([]string{"--listen", address, "--db", db})
		if err == nil || !strings.Contains(err.Error(), "--allow-remote-http") {
			t.Fatalf("remote bind was not rejected before startup: %v", err)
		}
		if _, err := os.Stat(filepath.Dir(db)); !os.IsNotExist(err) {
			t.Fatalf("rejected bind touched the database directory: %v", err)
		}
	}
}

func TestHelpSucceedsWithoutSideEffects(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"serve", "--help"}, {"serve", "-h"}, {"token", "--help"}, {"token", "create", "--help"}, {"token", "revoke", "-h"}} {
		if err := run(args); err != nil {
			t.Errorf("help command %v failed: %v", args, err)
		}
	}
	if defaultDB != ".questlock/state.db" {
		t.Fatalf("unexpected default database path %q", defaultDB)
	}
}

func TestRejectedStorageStartupDoesNotCreateDatabase(t *testing.T) {
	for _, args := range [][]string{{"--storage-interval", "0s"}, {"--storage-interval", "25h"}, {"--storage-config", filepath.Join(t.TempDir(), "missing.json")}} {
		db := filepath.Join(t.TempDir(), "unexpected", "state.db")
		args = append(args, "--db", db, "--listen", "127.0.0.1:0")
		if err := serve(args); err == nil {
			t.Fatal("invalid storage startup accepted")
		}
		if _, err := os.Stat(filepath.Dir(db)); !os.IsNotExist(err) {
			t.Fatalf("rejected storage settings touched database: %v", err)
		}
	}
}
