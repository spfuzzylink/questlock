// Agent Fence is a small, durable publishing gate for cooperating agent workers.
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
	"syscall"
	"time"

	"github.com/spfuzzylink/agent-fence/internal/httpapi"
	"github.com/spfuzzylink/agent-fence/internal/store"
)

const version = "0.1.0"
const defaultDB = ".agent-fence/fence.db"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-fence:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`Agent Fence — durable artifact publishing with version checks

Usage:
  agent-fence serve [--db PATH] [--listen 127.0.0.1:8080]
  agent-fence token create --agent NAME --scope WORKSPACE [--db PATH]
  agent-fence token revoke --agent NAME [--db PATH]
  agent-fence demo
  agent-fence version

Token creation prints a credential once; save it outside your repository.
The default database is .agent-fence/fence.db. Only the broker owns this path.
The demo uses temporary state and exercises real worker/server process restarts.`)
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
	case "demo":
		if len(args) != 1 {
			return errors.New("demo takes no arguments")
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
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected serve arguments")
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
	fmt.Fprintln(os.Stderr, "Agent Fence listening on http://"+ln.Addr().String())
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

func tokenCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("use token create or token revoke")
	}
	f := flag.NewFlagSet("token "+args[0], flag.ContinueOnError)
	dbPath := f.String("db", defaultDB, "SQLite database path")
	agent := f.String("agent", "", "unique agent name")
	scope := f.String("scope", "", "shared workspace name (create only)")
	if err := f.Parse(args[1:]); err != nil {
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
