package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/observability"
	"github.com/spfuzzylink/questlock/retention"
	"golang.org/x/sys/unix"
)

const storageUsage = `Usage:
  questlock storage config --cold-dir ABS
  questlock storage configure [--db PATH] --config PATH
  questlock storage info [--db PATH]
  questlock storage archive [--db PATH] [--dry-run | --apply]
  questlock storage restore [--db PATH] [--max-items 32]
  questlock storage verify [--db PATH]
  questlock storage history [--db PATH] --scope SCOPE --key KEY --version N
  questlock storage compact [--db PATH]

Archive defaults to a dry run. Use --apply to move eligible content to local
cold storage. History explicitly prints the requested historical content.
Database commands accept --timeout (at most 5m) and optional --langfuse.
Cold history and current identities can grow; retention is not a total disk cap.`

func storageCommand(args []string) error {
	return storageCommandIO(args, os.Stdout, os.Stderr, admission.SystemSource())
}

func storageCommandIO(args []string, output, diagnostics io.Writer, source admission.Source) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(diagnostics, storageUsage)
		return nil
	}
	command := args[0]
	switch command {
	case "config", "configure", "info", "archive", "restore", "verify", "history", "compact":
	default:
		return errors.New("unknown storage command; use storage --help")
	}
	f := flag.NewFlagSet("storage "+command, flag.ContinueOnError)
	f.SetOutput(diagnostics)
	f.Usage = func() { fmt.Fprintln(diagnostics, storageUsage) }
	if command == "config" {
		coldDir := f.String("cold-dir", "", "absolute directory for private local cold storage")
		if err := parseStorageFlags(f, args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return errors.New("invalid storage flags; use storage --help")
		}
		if f.NArg() != 0 || !filepath.IsAbs(*coldDir) {
			return errors.New("storage config requires --cold-dir ABS and accepts flags only")
		}
		config := retention.DefaultConfig(*coldDir)
		if err := config.Validate(); err != nil {
			return errors.New("invalid local cold storage directory")
		}
		return writeModelJSON(output, config)
	}
	dbPath := f.String("db", defaultDB, "local broker SQLite database")
	timeout := f.Duration("timeout", 30*time.Second, "bounded storage operation, at most five minutes")
	langfuse := f.Bool("langfuse", false, "opt in to content-free storage spans using Langfuse environment settings")
	var configPath, scope, key *string
	var version *int64
	var apply, dryRun *bool
	var maxItems *int
	switch command {
	case "configure":
		configPath = f.String("config", "", "strict complete local retention policy JSON")
	case "archive":
		apply = f.Bool("apply", false, "move eligible history to local cold storage")
		dryRun = f.Bool("dry-run", true, "report eligible history without moving content (default)")
	case "restore":
		maxItems = f.Int("max-items", 32, "maximum historical versions to restore, between 1 and 128")
	case "history":
		scope = f.String("scope", "", "exact historical scope")
		key = f.String("key", "", "exact historical artifact key")
		version = f.Int64("version", 0, "exact positive historical version")
	}
	if err := parseStorageFlags(f, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid storage flags; use storage --help")
	}
	if f.NArg() != 0 || *dbPath == "" || *timeout <= 0 || *timeout > 5*time.Minute {
		return errors.New("storage accepts flags only and requires a database path and timeout between zero and five minutes")
	}
	if command == "archive" {
		visitedDryRun := false
		f.Visit(func(value *flag.Flag) { visitedDryRun = visitedDryRun || value.Name == "dry-run" })
		if *apply && visitedDryRun || !*apply && !*dryRun {
			return errors.New("use --apply to archive, or --dry-run to inspect; do not combine them")
		}
	}
	if maxItems != nil && (*maxItems < 1 || *maxItems > 128) {
		return errors.New("storage restore requires --max-items between 1 and 128")
	}
	if command == "history" && (*scope == "" || *key == "" || *version < 1) {
		return errors.New("storage history requires --scope, --key, and a positive --version")
	}
	var config retention.Config
	if configPath != nil {
		if *configPath == "" {
			return errors.New("storage configure requires --config PATH")
		}
		var err error
		config, err = loadRetentionConfig(*configPath)
		if err != nil {
			return err
		}
	}
	exporter, err := langfuseFromEnvironment(*langfuse, 0)
	if err != nil {
		return err
	}
	// Inspection and maintenance never create a new empty broker by accident.
	info, err := os.Lstat(*dbPath)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("storage requires an existing regular broker database")
	}
	s, err := store.Open(*dbPath)
	if err != nil {
		return errors.New("cannot open local broker storage")
	}
	defer s.Close()
	if exporter != nil {
		s.SetStorageObserver(storageObserver(exporter))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var result any
	switch command {
	case "configure":
		err = s.StorageConfigure(ctx, config)
		if err == nil {
			result = map[string]any{"operation": "configure", "status": "complete", "policy": config}
		}
	case "info":
		result, err = s.StorageInfo(ctx)
	case "archive":
		var info store.StorageInfo
		info, err = s.StorageInfo(ctx)
		if err != nil {
			break
		}
		if info.Policy == nil {
			return errors.New("storage retention is not configured; use storage configure")
		}
		now := time.Now().UTC()
		snapshot := admission.Snapshot{}
		if info.Policy.Adaptive && source != nil {
			probeCtx, stop := context.WithTimeout(ctx, 2*time.Second)
			var probeErr error
			snapshot, probeErr = source.Sample(probeCtx)
			stop()
			if probeErr != nil {
				snapshot.Known = false
			}
			now = time.Now().UTC()
		}
		budget := retention.Plan(*info.Policy, snapshot, now)
		result, err = s.StorageArchive(ctx, !*apply, now, budget)
	case "restore":
		result, err = s.StorageRestore(ctx, *maxItems)
	case "verify":
		result, err = s.StorageVerify(ctx)
	case "history":
		result, err = s.StorageHistory(ctx, *scope, *key, *version)
	case "compact":
		err = s.StorageCompact(ctx)
		if err == nil {
			result = store.StorageReport{Operation: "compact", Status: "complete"}
		}
	}
	if err != nil {
		return storageCommandError(err)
	}
	return writeModelJSON(output, result)
}

func parseStorageFlags(f *flag.FlagSet, args []string) error {
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if len(arg) < 2 || arg[0] != '-' || arg == "--" {
			break
		}
		name, _, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		value := f.Lookup(name)
		if value == nil {
			break // The standard parser reports unknown flags and help.
		}
		if seen[name] {
			return errors.New("duplicate storage flag")
		}
		seen[name] = true
		boolean, isBoolean := value.Value.(interface{ IsBoolFlag() bool })
		if !hasValue && (!isBoolean || !boolean.IsBoolFlag()) {
			i++ // A string or numeric flag consumes its next argument verbatim.
		}
	}
	return f.Parse(args)
}

func storageCommandError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("storage operation stopped before confirmation; inspect retained state before retrying")
	}
	var application *store.Error
	if errors.As(err, &application) {
		switch application.Code {
		case "archive_corrupt":
			return errors.New("local archive failed integrity verification; preserve claims and restore a verified archive")
		case "archive_unavailable":
			return errors.New("local archive is unavailable; preserve claims and restore its configured directory")
		case "storage_limit":
			return errors.New("local storage capacity reached; inspect storage info before archiving, compacting or raising the limit")
		case "not_found":
			return errors.New("requested historical artifact was not found")
		case "invalid":
			return errors.New("storage policy or operation is invalid for the retained state")
		}
	}
	return errors.New("storage operation failed; retained state requires inspection before retrying")
}

func loadRetentionConfig(path string) (retention.Config, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return retention.Config{}, errors.New("cannot read local retention policy")
	}
	f := os.NewFile(uintptr(fd), "retention_policy")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 64<<10 {
		return retention.Config{}, errors.New("retention policy must be a regular file of at most 64KiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return retention.Config{}, errors.New("cannot read bounded local retention policy")
	}
	config, err := retention.Decode(data)
	if err != nil {
		return retention.Config{}, errors.New("invalid complete local retention policy JSON")
	}
	return config, nil
}

// Storage telemetry is optional and has no effect on the durable operation,
// returned content or error. The exporter sends one bounded request per event.
func storageObserver(exporter *observability.Langfuse) func(context.Context, retention.Event) {
	return func(parent context.Context, event retention.Event) {
		if exporter == nil || parent == nil || parent.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		_, _ = exporter.ExportStorage(ctx, event)
	}
}
