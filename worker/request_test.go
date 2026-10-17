package worker

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReadApprovedRequestSharesPinnedBoundary(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{"approved":true}`)
	if e := os.WriteFile(filepath.Join(root, "request.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	b, e := ReadApprovedRequest(root, "request.json")
	if e != nil || !bytes.Equal(b, data) {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join(root, "request.json"), filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"link", "../request.json", filepath.Join(t.TempDir(), "outside.json"), "request.json\x00"} {
		if _, e := ReadApprovedRequest(root, path); e == nil {
			t.Fatal("unsafe request accepted", path)
		}
	}
}
