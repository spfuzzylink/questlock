package worker

import (
	"os"
	"path/filepath"
	"strings"
)

// ReadApprovedRequest reuses the MCP worker's pinned, bounded, symlink-refusing
// file boundary for explicit opt-in local queue callers. It discovers no files.
func ReadApprovedRequest(workspace, path string) ([]byte, error) {
	if workspace == "" || path == "" || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return nil, ErrConfiguration
	}
	declared, err := filepath.Abs(workspace)
	if err != nil {
		return nil, ErrConfiguration
	}
	resolved, err := filepath.EvalSymlinks(declared)
	if err != nil {
		return nil, ErrConfiguration
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, ErrConfiguration
	}
	defer root.Close()
	w := worker{root: root, workspace: resolved, declaredWorkspace: declared}
	return w.loadBytes(path)
}
