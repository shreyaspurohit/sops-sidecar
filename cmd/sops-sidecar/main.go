// Command sops-sidecar decrypts one SOPS file with an age key and writes each
// top-level secret as its own file into a tmpfs directory, so neighboring
// containers can mount the secrets read-only.
package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/shreyaspurohit/sops-sidecar/internal/config"
	"github.com/shreyaspurohit/sops-sidecar/internal/decrypt"
)

const (
	exitOK      = 0 // success; health: ready
	exitFailure = 1 // any config, input, decrypt, write, or readiness failure
	exitUsage   = 2 // unknown subcommand or unexpected arguments
)

// Subcommand names.
const (
	cmdRun     = "run"
	cmdHealth  = "health"
	cmdCheck   = "check"
	cmdVersion = "version"
)

// develVersion is reported when the binary carries no module version.
const develVersion = "(devel)"

const usage = `usage: sops-sidecar [run|health|check|version]

  run      decrypt the SOPS file into the output dir, then serve (default);
           SIGHUP reloads, SIGTERM and SIGINT stop
  health   exit 0 if the last load succeeded, 1 otherwise
  check    print the keys that run would write; writes nothing
  version  print the version
`

func main() {
	os.Exit(cli(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

// cli runs one invocation and returns the process exit code. It reads the
// environment only through lookup and writes only to stdout and stderr; sops'
// own loggers are silenced because they can echo file content.
func cli(args []string, lookup config.LookupFunc, stdout, stderr io.Writer) int {
	decrypt.DisableSOPSLogs()
	if len(args) == 0 {
		return runCommand(lookup, stderr)
	}
	if len(args) > 1 {
		return usageError(stderr)
	}
	switch args[0] {
	case cmdRun:
		return runCommand(lookup, stderr)
	case cmdHealth:
		return healthCommand(lookup, stderr)
	case cmdCheck:
		return checkCommand(lookup, stdout, stderr)
	case cmdVersion:
		_, _ = fmt.Fprintf(stdout, "sops-sidecar %s\n", version())
		return exitOK
	default:
		return usageError(stderr)
	}
}

func usageError(stderr io.Writer) int {
	_, _ = fmt.Fprint(stderr, usage)
	return exitUsage
}

// version returns the module version stamped into the binary.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return develVersion
	}
	return info.Main.Version
}

// newLogger returns a JSON logger on w, one object per line, that drops
// records below level.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// logFailure logs err at ERROR under msg and returns exitFailure.
func logFailure(log *slog.Logger, msg string, err error) int {
	log.Error(msg, "error", err)
	return exitFailure
}
