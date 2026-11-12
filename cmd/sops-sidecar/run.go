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
// and the names of the secret files on disk that it manages (written by this
// process or adopted from an earlier run) and has not yet removed.
type sidecar struct {
	cfg     config.Config
	log     *slog.Logger
	dir     *writer.Dir
	marker  health.Marker
	written []writer.Name
}

// runCommand serves until SIGTERM or SIGINT and reloads on SIGHUP. All three
// signals are handled explicitly because the process runs as PID 1, where the
// kernel applies no default signal actions.
func runCommand(lookup config.LookupFunc, stderr io.Writer) int {
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

// serve returns exitFailure if the first load fails; otherwise it reloads on
// every value received from reload and returns the exit code once ctx is done.
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

// openSidecar opens the output dir and prepares it for the first load; on
// error the output dir is closed again.
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

// newSidecar returns the sidecar for dir after preparing dir for the first load.
func newSidecar(cfg config.Config, log *slog.Logger, dir *writer.Dir) (*sidecar, error) {
	marker, err := health.NewMarker(dir)
	if err != nil {
		return nil, err
	}
	written, err := prepareOutputDir(cfg, log, dir, marker)
	if err != nil {
		return nil, err
	}
	return &sidecar{cfg: cfg, log: log, dir: dir, marker: marker, written: written}, nil
}

// prepareOutputDir clears any ready marker a previous run left before any
// check that can fail, so a failed startup is never reported ready. It returns
// the secret files already in dir: the output dir belongs to the sidecar, so
// they are adopted and the first load removes those whose keys are gone.
func prepareOutputDir(cfg config.Config, log *slog.Logger, dir *writer.Dir, marker health.Marker) ([]writer.Name, error) {
	if err := marker.Clear(); err != nil {
		return nil, err
	}
	if err := checkOutputDir(cfg, log, dir); err != nil {
		return nil, err
	}
	if err := dir.RemoveTempFiles(); err != nil {
		return nil, err
	}
	return dir.Names()
}

// checkOutputDir errors unless dir is tmpfs; with AllowNonTmpfs it only warns.
func checkOutputDir(cfg config.Config, log *slog.Logger, dir *writer.Dir) error {
	if cfg.AllowNonTmpfs {
		log.Warn("tmpfs check disabled", "output_dir", cfg.OutputDir,
			"reason", config.EnvAllowNonTmpfs+" is for tests only")
		return nil
	}
	return dir.CheckTmpfs()
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
		return err
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
		return err
	}
	s.log.Info("secrets written", "files", len(plan.Entries), "ignored", plan.Ignored)
	return nil
}

// write writes one planned file and records its name as present on disk.
func (s *sidecar) write(e config.Entry) error {
	if err := s.dir.Write(e.File); err != nil {
		return err
	}
	if !slices.Contains(s.written, e.File.Name) {
		s.written = append(s.written, e.File.Name)
	}
	s.log.Debug("secret written", "key", e.Key, "file", e.File.Name.String(),
		"mode", e.File.Mode, "uid", e.File.Owner.UID, "gid", e.File.Owner.GID)
	return nil
}

// removeStale removes every file written earlier that is absent from next.
// Names stay tracked until their removal succeeds.
func (s *sidecar) removeStale(next []writer.Name) error {
	for _, n := range writer.Stale(s.written, next) {
		if err := s.dir.Remove(n); err != nil {
			return err
		}
		s.written = slices.DeleteFunc(s.written, func(w writer.Name) bool { return w == n })
		s.log.Debug("secret removed", "file", n.String())
	}
	return nil
}

// shutdown keeps the written files unless WipeOnExit is set, in which case it
// clears the ready marker first, so nothing is reported ready while files
// disappear, and then removes them.
func (s *sidecar) shutdown() int {
	s.log.Info("shutting down")
	if !s.cfg.WipeOnExit {
		return exitOK
	}
	if err := s.marker.Clear(); err != nil {
		return logFailure(s.log, "wipe failed", err)
	}
	if err := s.removeStale(nil); err != nil {
		return logFailure(s.log, "wipe failed", err)
	}
	return exitOK
}

// healthCommand reports readiness through its exit code.
func healthCommand(lookup config.LookupFunc, stderr io.Writer) int {
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
func checkCommand(lookup config.LookupFunc, stdout, stderr io.Writer) int {
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
