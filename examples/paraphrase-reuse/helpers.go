package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
)

func loopbackOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return (u.Scheme == "http" || u.Scheme == "https") && ip != nil && ip.IsLoopback() && u.Port() != "" && u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == ""
}

func privateFile(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("token, state and output paths must be absolute and private")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return nil, errors.New("file parent must be an existing private directory")
	}
	if create {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("token must be a private regular file")
	}
	return os.Open(path)
}

func appendJSON(file *os.File, value any) error {
	if err := json.NewEncoder(file).Encode(value); err != nil {
		return err
	}
	return file.Sync()
}
