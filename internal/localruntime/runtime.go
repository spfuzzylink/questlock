// Package localruntime owns one private, loopback broker for local onboarding.
// It never constructs a model provider or imports conversation history.
package localruntime

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"golang.org/x/sys/unix"
)

type Options struct{ StateDir, Executable, Version string }
type Handle struct {
	Client       *client.Client
	URL, Version string
}
type Report struct {
	Status             string `json:"status"`
	Version            string `json:"version,omitempty"`
	Loopback           bool   `json:"loopback"`
	CredentialsPrivate bool   `json:"credentials_private"`
}
type connection struct {
	Schema   int    `json:"schema"`
	URL      string `json:"url"`
	Instance string `json:"instance"`
	Admin    string `json:"admin"`
	Version  string `json:"version"`
}

// CheckPath rejects symlink components without changing the supplied path.
func CheckPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return errors.New("an absolute non-root path is required")
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return errors.New("cannot inspect local path")
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("local paths must not contain symlinks")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}

func privateDir(path string) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return errors.New("cannot create private local state directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("local state directory must have mode 0700")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("local state directory must belong to the current user")
	}
	return CheckPath(path)
}

func readPrivate(path string) ([]byte, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "local_private_file")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 65536 {
		return nil, errors.New("local credential or metadata file must be a private regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("local private file belongs to another user")
	}
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return nil, errors.New("local private file is too large")
	}
	return b, nil
}

func writePrivate(path string, b []byte) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".local-runtime-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = CheckPath(path); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func acquire(ctx context.Context, path string, wait bool) (*os.File, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "local_runtime_lock")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, errors.New("local runtime lock must be private")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) {
		f.Close()
		return nil, errors.New("local runtime lock belongs to another user")
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			f.Close()
			return nil, err
		}
		if !wait {
			f.Close()
			return nil, errors.New("local broker is already owned by another process")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, errors.New("local runtime lock timed out")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func readConnection(state string) (connection, error) {
	var c connection
	b, err := readPrivate(filepath.Join(state, "broker.json"))
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || c.Schema != 1 || len(c.Instance) != 64 || len(c.Admin) != 64 || c.Version == "" {
		return c, errors.New("local broker metadata is invalid")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("local broker metadata has trailing content")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return c, errors.New("local broker metadata must identify literal loopback")
	}
	if _, err := hex.DecodeString(c.Instance); err != nil {
		return c, errors.New("local broker identity is invalid")
	}
	if _, err := hex.DecodeString(c.Admin); err != nil {
		return c, errors.New("local broker credential is invalid")
	}
	return c, nil
}

func adminRequest(ctx context.Context, c connection, method, path string) error {
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, nil)
	if err != nil {
		return errors.New("local broker request invalid")
	}
	req.Header.Set("X-Questlock-Local-Admin", c.Admin)
	h := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer h.CloseIdleConnections()
	resp, err := h.Do(req)
	if err != nil {
		return errors.New("local broker is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("local broker did not confirm its identity")
	}
	var got struct {
		Instance string `json:"instance"`
		Version  string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&got) != nil || got.Instance != c.Instance || got.Version != c.Version {
		return errors.New("local broker identity changed")
	}
	return nil
}

func handle(ctx context.Context, state string) (Handle, error) {
	c, err := readConnection(state)
	if err != nil {
		return Handle{}, err
	}
	if err = adminRequest(ctx, c, http.MethodGet, "/_questlock/local/ready"); err != nil {
		return Handle{}, err
	}
	b, err := readPrivate(filepath.Join(state, "worker.token"))
	if err != nil {
		return Handle{}, errors.New("local broker credential is unavailable")
	}
	api, err := client.New(c.URL, strings.TrimSpace(string(b)))
	if err != nil {
		return Handle{}, errors.New("local broker credential is invalid")
	}
	// Authenticated data access proves the token still belongs to this store.
	_, err = api.Get(ctx, "onboarding/v1/ready")
	var apiError *client.Error
	if err != nil && !(errors.As(err, &apiError) && apiError.Status == http.StatusNotFound) {
		return Handle{}, errors.New("local broker token was not accepted")
	}
	return Handle{Client: api, URL: c.URL, Version: c.Version}, nil
}

// Inspect is read-only and never starts services, provisions tokens or calls a model.
func Inspect(ctx context.Context, state string) (Report, error) {
	if err := CheckPath(state); err != nil {
		return Report{Status: "unsafe_path"}, err
	}
	info, err := os.Lstat(state)
	if os.IsNotExist(err) {
		return Report{Status: "not_started"}, nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return Report{Status: "unsafe_permissions"}, errors.New("local state directory must be private")
	}
	h, err := handle(ctx, state)
	if err != nil {
		return Report{Status: "unavailable"}, errors.New("local service is unavailable; inspect private state or start setup")
	}
	return Report{Status: "ready", Version: h.Version, Loopback: true, CredentialsPrivate: true}, nil
}

// Ensure starts one detached local broker, or connects to its authenticated identity.
// The child holds a lifetime owner lock; an unresponsive owner is never replaced.
func Ensure(ctx context.Context, o Options) (Handle, error) {
	if err := privateDir(o.StateDir); err != nil {
		return Handle{}, err
	}
	if !filepath.IsAbs(o.Executable) || o.Version == "" {
		return Handle{}, errors.New("local runtime requires an absolute executable and version")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lock, err := acquire(ctx, filepath.Join(o.StateDir, "start.lock"), true)
	if err != nil {
		return Handle{}, err
	}
	defer lock.Close()
	if h, err := handle(ctx, o.StateDir); err == nil {
		return h, nil
	}
	owner, err := acquire(ctx, filepath.Join(o.StateDir, "owner.lock"), false)
	if err != nil {
		return Handle{}, errors.New("an existing local broker is unresponsive; no second owner was started")
	}
	owner.Close()
	// Invalid private metadata is a stopping condition, not permission to replace it.
	if _, err := readConnection(o.StateDir); err != nil && !os.IsNotExist(err) {
		return Handle{}, err
	}
	logPath := filepath.Join(o.StateDir, "broker.log")
	if err := CheckPath(logPath); err != nil {
		return Handle{}, err
	}
	fd, err := unix.Open(logPath, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return Handle{}, errors.New("cannot open private local broker log")
	}
	log := os.NewFile(uintptr(fd), "local_broker_log")
	defer log.Close()
	info, err := log.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return Handle{}, errors.New("local broker log must be private")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) {
		return Handle{}, errors.New("local broker log belongs to another user")
	}
	cmd := exec.Command(o.Executable, "__local-broker", "--state-dir", o.StateDir)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if err = cmd.Start(); err != nil {
		return Handle{}, errors.New("could not start local broker")
	}
	ended := make(chan error, 1)
	go func() { ended <- cmd.Wait() }()
	for {
		if h, err := handle(ctx, o.StateDir); err == nil {
			return h, nil
		}
		select {
		case <-ctx.Done():
			return Handle{}, errors.New("local broker startup was not confirmed; inspect broker.log before retrying")
		case <-ended:
			return Handle{}, errors.New("local broker stopped during startup; inspect private broker.log")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func randomID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Serve is the detached child entry point. Only this process opens SQLite.
func Serve(ctx context.Context, o Options) error {
	if err := privateDir(o.StateDir); err != nil {
		return err
	}
	owner, err := acquire(ctx, filepath.Join(o.StateDir, "owner.lock"), false)
	if err != nil {
		return err
	}
	defer owner.Close()
	tokenPath := filepath.Join(o.StateDir, "worker.token")
	dbPath := filepath.Join(o.StateDir, "state.db")
	if err := CheckPath(dbPath); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := CheckPath(dbPath + suffix); err != nil {
			return err
		}
	}
	tokenBytes, tokenErr := readPrivate(tokenPath)
	if tokenErr != nil && !os.IsNotExist(tokenErr) {
		return errors.New("local token file is unsafe or unreadable")
	}
	if os.IsNotExist(tokenErr) {
		if _, err := os.Lstat(dbPath); !os.IsNotExist(err) {
			return errors.New("existing local database has no recoverable credential; refusing automatic reprovisioning")
		}
	}
	if tokenErr == nil {
		if _, err := os.Lstat(dbPath); err != nil {
			return errors.New("saved local credential has no database; refusing automatic reprovisioning")
		}
	}
	s, err := store.Open(dbPath)
	if err != nil {
		return errors.New("could not open private local database")
	}
	defer s.Close()
	stopMaintenance, err := s.StartStorageMaintenance(ctx, store.DefaultStorageInterval, admission.SystemSource())
	if err != nil {
		return err
	}
	defer stopMaintenance()
	if err := os.Chmod(dbPath, 0600); err != nil {
		return err
	}
	if os.IsNotExist(tokenErr) {
		token, err := s.CreateToken(ctx, "local-onboarding", "local-onboarding")
		if err != nil {
			return errors.New("could not provision local broker scope")
		}
		if err := writePrivate(tokenPath, []byte(token+"\n")); err != nil {
			return errors.New("could not durably save local credential; no automatic rotation will occur")
		}
	} else if len(strings.TrimSpace(string(tokenBytes))) == 0 {
		return errors.New("local token file is empty")
	}
	if tokenErr == nil {
		if _, err := s.Authenticate(ctx, strings.TrimSpace(string(tokenBytes))); err != nil {
			return errors.New("saved local credential was not accepted by its database")
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return errors.New("could not bind local broker")
	}
	defer ln.Close()
	instance, err := randomID()
	if err != nil {
		return err
	}
	admin, err := randomID()
	if err != nil {
		return err
	}
	c := connection{Schema: 1, URL: "http://" + ln.Addr().String(), Instance: instance, Admin: admin, Version: o.Version}
	api := httpapi.New(s)
	stop := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_questlock/local/") {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Questlock-Local-Admin")), []byte(admin)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if (r.URL.Path != "/_questlock/local/ready" || r.Method != http.MethodGet) && (r.URL.Path != "/_questlock/local/stop" || r.Method != http.MethodPost) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Instance string `json:"instance"`
				Version  string `json:"version"`
			}{instance, o.Version})
			if r.URL.Path == "/_questlock/local/stop" {
				select {
				case stop <- struct{}{}:
				default:
				}
			}
			return
		}
		api.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	b, _ := json.Marshal(c)
	if err := writePrivate(filepath.Join(o.StateDir, "broker.json"), append(b, '\n')); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case <-stop:
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		_ = srv.Close()
		return err
	}
	// Preserve a concurrent replacement rather than deleting another owner's metadata.
	if current, err := readConnection(o.StateDir); err == nil && current.Instance == instance {
		_ = os.Remove(filepath.Join(o.StateDir, "broker.json"))
	}
	return nil
}

func Stop(ctx context.Context, state string) error {
	if err := CheckPath(state); err != nil {
		return err
	}
	c, err := readConnection(state)
	if err != nil {
		return errors.New("no readable managed broker is registered")
	}
	if err := adminRequest(ctx, c, http.MethodGet, "/_questlock/local/ready"); err != nil {
		return err
	}
	if err := adminRequest(ctx, c, http.MethodPost, "/_questlock/local/stop"); err != nil {
		return err
	}
	return nil
}
