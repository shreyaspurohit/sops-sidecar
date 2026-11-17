// Package config parses the environment and the map file into validated
// settings and computes the plan of files to write.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
)

// Environment variable names.
const (
	// EnvSOPSFile names the SOPS file.
	EnvSOPSFile = "SOPS_FILE"
	// EnvAgeKeyFile names the age key file.
	EnvAgeKeyFile = "SOPS_AGE_KEY_FILE"
	// EnvAgeKey is refused if present, even when empty: an age key in an
	// environment variable is visible through `docker inspect`.
	EnvAgeKey = "SOPS_AGE_KEY"
	// EnvMapFile names the map file; setting it makes the map file required.
	EnvMapFile = "SECRETS_MAP"
	// EnvOutputDir names the output dir.
	EnvOutputDir = "OUTPUT_DIR"
	// EnvAllowNonTmpfs disables the tmpfs check of the output dir (tests only).
	EnvAllowNonTmpfs = "ALLOW_NON_TMPFS"
	// EnvWipeOnExit removes written files and the ready marker on shutdown.
	EnvWipeOnExit = "WIPE_ON_EXIT"
	// EnvLogLevel sets the log level: debug, info, warn, or error.
	EnvLogLevel = "LOG_LEVEL"
)

// Defaults for settings the environment leaves unset or empty.
const (
	// DefaultSOPSFile is the SOPS file path when EnvSOPSFile is unset.
	DefaultSOPSFile = "/config/secrets.sops.yaml"
	// DefaultAgeKeyFile is the age key file path when EnvAgeKeyFile is unset.
	DefaultAgeKeyFile = "/config/age.key"
	// DefaultMapFile is the optional map file path when EnvMapFile is unset.
	DefaultMapFile = "/config/secrets.map.yaml"
	// DefaultOutputDir is the output dir when EnvOutputDir is unset.
	DefaultOutputDir = "/secrets"
)

// Config is the validated environment.
type Config struct {
	// SOPSFile is the absolute path of the SOPS file.
	SOPSFile string
	// AgeKeyFile is the absolute path of the age key file.
	AgeKeyFile string
	// MapFile is the absolute path of the map file.
	MapFile string
	// MapRequired is true when EnvMapFile is set: a missing map file is then an error.
	MapRequired bool
	// OutputDir is the absolute path of the output dir.
	OutputDir string
	// AllowNonTmpfs skips the tmpfs check of the output dir.
	AllowNonTmpfs bool
	// WipeOnExit removes written files and the ready marker on shutdown.
	WipeOnExit bool
	// LogLevel is the minimum level logged; values are never logged at any level.
	LogLevel slog.Level
}

// LookupFunc reports the value of the environment variable name and whether
// it is set, with the signature of os.LookupEnv.
type LookupFunc func(name string) (string, bool)

// FromEnv builds a Config from environment variables read through lookup.
// Unset or empty variables take their defaults. All problems are reported
// together. EnvAgeKey is refused outright.
func FromEnv(lookup LookupFunc) (Config, error) {
	r := envReader{lookup: lookup}
	if _, present := lookup(EnvAgeKey); present {
		r.fail(fmt.Errorf("%s is not supported: environment variables are visible in `docker inspect`; mount the age key as a file and set %s", EnvAgeKey, EnvAgeKeyFile))
	}
	c := Config{
		SOPSFile:      r.path(EnvSOPSFile, DefaultSOPSFile),
		AgeKeyFile:    r.path(EnvAgeKeyFile, DefaultAgeKeyFile),
		MapFile:       r.path(EnvMapFile, DefaultMapFile),
		MapRequired:   r.isSet(EnvMapFile),
		OutputDir:     r.path(EnvOutputDir, DefaultOutputDir),
		AllowNonTmpfs: r.boolean(EnvAllowNonTmpfs),
		WipeOnExit:    r.boolean(EnvWipeOnExit),
		LogLevel:      r.level(EnvLogLevel),
	}
	if err := r.err(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// envReader reads typed settings and remembers every problem it meets.
type envReader struct {
	lookup LookupFunc
	errs   []error
}

func (r *envReader) fail(err error) { r.errs = append(r.errs, err) }

func (r *envReader) err() error { return errors.Join(r.errs...) }

// value returns the variable's text and whether it is set to a non-empty value.
func (r *envReader) value(name string) (string, bool) {
	v, ok := r.lookup(name)
	return v, ok && v != ""
}

func (r *envReader) isSet(name string) bool {
	_, ok := r.value(name)
	return ok
}

func (r *envReader) path(name, def string) string {
	v, ok := r.value(name)
	if !ok {
		return def
	}
	if !filepath.IsAbs(v) {
		r.fail(fmt.Errorf("%s=%q must be an absolute path", name, v))
	}
	return v
}

func (r *envReader) boolean(name string) bool {
	v, ok := r.value(name)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail(fmt.Errorf("%s=%q must be a boolean (true or false)", name, v))
	}
	return b
}

func (r *envReader) level(name string) slog.Level {
	v, ok := r.value(name)
	if !ok {
		return slog.LevelInfo
	}
	switch strings.ToLower(v) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	r.fail(fmt.Errorf("%s=%q must be one of debug, info, warn, error", name, v))
	return slog.LevelInfo
}
