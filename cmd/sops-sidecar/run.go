package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/shreyaspurohit/sops-sidecar/internal/config"
	"github.com/shreyaspurohit/sops-sidecar/internal/health"
	"github.com/shreyaspurohit/sops-sidecar/internal/writer"
)

// sidecar is the state of one running `run`: the output dir, the ready marker,
// and the names of the files this process has written and not yet removed.
type sidecar struct {
	cfg     config.Config
	log     *slog.Logger
	dir     *writer.Dir
	marker  health.Marker
	written []writer.Name
}

// runCommand loads the configuration, registers the signal handlers, and
// serves until SIGTERM or SIGINT.
func runCommand(lookup func(string) (string, bool), stderr io.Writer) int {
	cfg, err := config.FromEnv(lookup)
	if err != nil {
		return logFailure(newLogger(stderr, slog.LevelInfo), "config failed", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	defer signal.Stop(reload)
	return serve(ctx, cfg, newLogger(stderr, cfg.LogLevel), reload)
}

// serve performs the first load, then reloads on every value received from
// reload until ctx is done. It returns the exit code.
func serve(ctx context.Context, cfg config.Config, log *slog.Logger, reload <-chan os.Signal) int {
	s, err := openSidecar(cfg, log)
	if err != nil {
		return logFailure(log, "startup failed", err)
	}
	defer func() { _ = s.dir.Close() }()
	if err := s.apply(); err != nil {
		return logFailure(log, "load failed", err)
	}
	return s.wait(ctx, reload)
}

// openSidecar opens the output dir and, unless disabled, checks it is tmpfs.
func openSidecar(cfg config.Config, log *slog.Logger) (*sidecar, error) {
	dir, err := writer.Open(cfg.OutputDir)
	if err != nil {
		return nil, err
	}
	s, err := newSidecar(cfg, log, dir)
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	return s, nil
}

func newSidecar(cfg config.Config, log *slog.Logger, dir *writer.Dir) (*sidecar, error) {
	if cfg.AllowNonTmpfs {
		log.Warn("tmpfs check disabled", "output_dir", cfg.OutputDir,
			"reason", config.EnvAllowNonTmpfs+" is for tests only")
	} else if err := dir.CheckTmpfs(writer.ProcMounts); err != nil {
		return nil, err
	}
	marker, err := health.NewMarker(dir)
	if err != nil {
		return nil, err
	}
	return &sidecar{cfg: cfg, log: log, dir: dir, marker: marker}, nil
}

// wait reloads on each reload signal and shuts down when ctx is done.
func (s *sidecar) wait(ctx context.Context, reload <-chan os.Signal) int {
	for {
		select {
		case <-ctx.Done():
			return s.shutdown()
		case <-reload:
			s.log.Info("reload requested")
			if err := s.apply(); err != nil {
				s.log.Error("reload failed", "error", err)
			}
		}
	}
}

// apply runs one load. The ready marker is cleared before anything changes
// and set only after every file is written and stale files are removed, so a
// failure at any point leaves the sidecar not ready.
func (s *sidecar) apply() error {
	if err := s.marker.Clear(); err != nil {
		return fmt.Errorf("clear ready marker: %w", err)
	}
	plan, err := load(s.cfg)
	if err != nil {
		return err
	}
	next := make([]writer.Name, 0, len(plan.Entries))
	for _, e := range plan.Entries {
		if err := s.write(e); err != nil {
			return err
		}
		next = append(next, e.File.Name)
	}
	if err := s.removeStale(next); err != nil {
		return err
	}
	if err := s.marker.Set(); err != nil {
		return fmt.Errorf("set ready marker: %w", err)
	}
	s.log.Info("secrets written", "files", len(plan.Entries), "ignored", plan.Ignored)
	return nil
}

// write writes one planned file and records its name as present on disk.
func (s *sidecar) write(e config.Entry) error {
	if err := s.dir.Write(e.File); err != nil {
		return fmt.Errorf("write %s: %w", e.File.Name, err)
	}
	if !slices.Contains(s.written, e.File.Name) {
		s.written = append(s.written, e.File.Name)
	}
	s.log.Debug("secret written", "key", e.Key, "file", e.File.Name.String())
	return nil
}

// removeStale removes every file written earlier that is absent from next.
// Names stay tracked until their removal succeeds.
func (s *sidecar) removeStale(next []writer.Name) error {
	for _, n := range writer.Stale(s.written, next) {
		if err := s.dir.Remove(n); err != nil {
			return fmt.Errorf("remove %s: %w", n, err)
		}
		s.written = slices.DeleteFunc(s.written, func(w writer.Name) bool { return w == n })
		s.log.Debug("secret removed", "file", n.String())
	}
	return nil
}

// shutdown keeps the written files unless WipeOnExit is set, in which case it
// removes them and the ready marker.
func (s *sidecar) shutdown() int {
	s.log.Info("shutting down")
	if !s.cfg.WipeOnExit {
		return exitOK
	}
	if err := s.removeStale(nil); err != nil {
		return logFailure(s.log, "wipe failed", err)
	}
	if err := s.marker.Clear(); err != nil {
		return logFailure(s.log, "wipe failed", err)
	}
	return exitOK
}

// healthCommand reports readiness through its exit code.
func healthCommand(lookup func(string) (string, bool), stderr io.Writer) int {
	cfg, err := config.FromEnv(lookup)
	if err != nil {
		return logFailure(newLogger(stderr, slog.LevelInfo), "config failed", err)
	}
	if err := health.Check(cfg.OutputDir); err != nil {
		return logFailure(newLogger(stderr, cfg.LogLevel), "not ready", err)
	}
	return exitOK
}

// checkCommand prints the sorted keys that run would write, writing no files.
func checkCommand(lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	cfg, err := config.FromEnv(lookup)
	if err != nil {
		return logFailure(newLogger(stderr, slog.LevelInfo), "config failed", err)
	}
	plan, err := load(cfg)
	if err != nil {
		return logFailure(newLogger(stderr, cfg.LogLevel), "check failed", err)
	}
	keys := make([]string, 0, len(plan.Entries))
	for _, e := range plan.Entries {
		keys = append(keys, e.Key)
	}
	slices.Sort(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintln(stdout, k)
	}
	return exitOK
}
