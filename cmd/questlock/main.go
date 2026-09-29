// Questlock is a small, durable publishing gate for cooperating agent workers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
)

var version = "0.1.0" // Release builds set this from VERSION using -ldflags.
const defaultDB = ".questlock/state.db"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "questlock:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`Questlock — durable artifact publishing with version checks

Usage:
  questlock serve [--db PATH] [--listen 127.0.0.1:8080] [--allow-remote-http]
  questlock token create --agent NAME --scope WORKSPACE [--db PATH]
  questlock token revoke --agent NAME [--db PATH]
  questlock quest
  questlock version

Token creation prints a credential once; save it outside your repository.
The default database is .questlock/state.db. Only the broker owns this path.
HTTP binds must use a literal loopback IP unless --allow-remote-http is explicit.
Remote HTTP requires a trusted network boundary and TLS termination for clients.
The quest uses temporary state and exercises real worker/server process restarts.
The demo command remains available as an alias for quest.`)
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage()
		return nil
	case "version":
		fmt.Println(version)
		return nil
	case "serve":
		return serve(args[1:])
	case "token":
		return tokenCommand(args[1:])
	case "quest", "demo":
		if len(args) != 1 {
			return fmt.Errorf("%s takes no arguments", args[0])
		}
		return demo()
	case "_demo-worker":
		return demoWorker(args[1:])
	default:
		return fmt.Errorf("unknown command %q; use --help", args[0])
	}
}

func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	dbPath := f.String("db", defaultDB, "SQLite database path")
	listen := f.String("listen", "127.0.0.1:8080", "HTTP bind address")
	allowRemoteHTTP := f.Bool("allow-remote-http", false, "allow non-loopback HTTP behind a trusted network boundary and TLS proxy")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected serve arguments")
	}
	if err := validateListenAddress(*listen, *allowRemoteHTTP); err != nil {
		return err
	}
	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: httpapi.New(s), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(os.Stderr, "Questlock listening on http://"+ln.Addr().String())
	if host, _, err := net.SplitHostPort(ln.Addr().String()); err == nil {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			fmt.Fprintln(os.Stderr, "Non-loopback bind: use a loopback port mapping or a trusted TLS proxy.")
		}
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			return err
		}
		return nil
	}
}

// validateListenAddress runs before the database is opened or a socket is bound.
// Requiring a literal IP avoids relying on mutable hostname resolution for the
// default local-only boundary. Explicit remote mode may use hostnames.
func validateListenAddress(address string, allowRemoteHTTP bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("listen address must be host:port (use [::1]:8080 for IPv6)")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("listen port must be an integer between 0 and 65535")
	}
	ip := net.ParseIP(host)
	if !allowRemoteHTTP && (ip == nil || !ip.IsLoopback()) {
		return errors.New("non-loopback or hostname HTTP binds require --allow-remote-http; use 127.0.0.1 or [::1] for local access")
	}
	return nil
}

func tokenCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("use token create or token revoke")
	}
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		usage()
		return nil
	}
	f := flag.NewFlagSet("token "+args[0], flag.ContinueOnError)
	dbPath := f.String("db", defaultDB, "SQLite database path")
	agent := f.String("agent", "", "unique agent name")
	scope := f.String("scope", "", "shared workspace name (create only)")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || *agent == "" {
		return errors.New("--agent is required; no positional arguments are allowed")
	}
	if args[0] != "create" && args[0] != "revoke" {
		return errors.New("use token create or token revoke")
	}
	if args[0] == "create" && *scope == "" {
		return errors.New("--scope is required for token create")
	}
	if args[0] == "revoke" && *scope != "" {
		return errors.New("token revoke does not take --scope")
	}
	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	if args[0] == "revoke" {
		if err := s.RevokeAgent(ctx, *agent); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Revoked agent:", *agent)
		return nil
	}
	token, err := s.CreateToken(ctx, *agent, *scope)
	if err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}
