//go:build darwin || linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/systemone"
	"golang.org/x/sys/unix"
)

func TestSystemOneRequestFileRejectsNonregularAndOversizeWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	fifo, link, large := filepath.Join(root, "fifo"), filepath.Join(root, "link"), filepath.Join(root, "large")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(large, bytes.Repeat([]byte("x"), systemone.MaxRequestBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(large, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, root, link, large} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				var output, diagnostics bytes.Buffer
				done <- systemOneCommandIO([]string{"choose", "--request", path, "--python", "/not-used", "--timeout", "1ms"}, &output, &diagnostics)
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("nonregular or oversized input was accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("nonregular input blocked the CLI")
			}
		})
	}
}

func TestSystemOneRequestFileReadsPinnedRegularDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openSystemOneRequest(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		t.Fatal("opened input was not regular")
	}
}
