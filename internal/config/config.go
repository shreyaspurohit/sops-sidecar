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
	EnvSOPSFile      = "SOPS_FILE"
	EnvAgeKeyFile    = "SOPS_AGE_KEY_FILE"
	EnvAgeKey        = "SOPS_AGE_KEY" // refused if present, even when empty
	EnvMapFile       = "SECRETS_MAP"
	EnvOutputDir     = "OUTPUT_DIR"
	EnvAllowNonTmpfs = "ALLOW_NON_TMPFS"
	EnvWipeOnExit    = "WIPE_ON_EXIT"
	EnvLogLevel      = "LOG_LEVEL"
)

// Defaults for settings the environment leaves unset or empty.
const (
	DefaultSOPSFile   = "/config/secrets.sops.yaml"
	DefaultAgeKeyFile = "/config/age.key"
	DefaultMapFile    = "/config/secrets.map.yaml"
	DefaultOutputDir  = "/secrets"
)

// Config is the validated environment.
type Config struct {
	SOPSFile   string
	AgeKeyFile string
	MapFile    string
	// MapRequired is true when EnvMapFile is set: a missing map file is then an error.
	MapRequired   bool
	OutputDir     string
	AllowNonTmpfs bool
	WipeOnExit    bool
	LogLevel      slog.Level
}

// FromEnv builds a Config from environment variables read through lookup.
// Unset or empty variables take their defaults. All problems are reported
// together. SOPS_AGE_KEY is refused outright: environment variables are
// visible through `docker inspect`, so the key must be mounted as a file.
func FromEnv(lookup func(string) (string, bool)) (Config, error) {
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
	lookup func(string) (string, bool)
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
