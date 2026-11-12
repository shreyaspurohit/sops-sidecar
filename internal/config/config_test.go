package config_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shreyaspurohit/sops-sidecar/internal/config"
)

// canary stands in for secret data; it must never appear in an error.
const canary = "CANARY-7f3a9c-SECRET"

func lookupFrom(env map[string]string) config.LookupFunc {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestFromEnv(t *testing.T) {
	defaults := config.Config{
		SOPSFile:   config.DefaultSOPSFile,
		AgeKeyFile: config.DefaultAgeKeyFile,
		MapFile:    config.DefaultMapFile,
		OutputDir:  config.DefaultOutputDir,
		LogLevel:   slog.LevelInfo,
	}
	tests := []struct {
		name string
		env  map[string]string
		want config.Config
	}{
		{"nothing set", nil, defaults},
		{"empty values mean defaults", map[string]string{
			config.EnvSOPSFile: "", config.EnvAgeKeyFile: "", config.EnvMapFile: "",
			config.EnvOutputDir: "", config.EnvAllowNonTmpfs: "", config.EnvWipeOnExit: "", config.EnvLogLevel: "",
		}, defaults},
		{"all overridden", map[string]string{
			config.EnvSOPSFile: "/in/s.json", config.EnvAgeKeyFile: "/in/k", config.EnvMapFile: "/in/m.yaml",
			config.EnvOutputDir: "/out", config.EnvAllowNonTmpfs: "true", config.EnvWipeOnExit: "1", config.EnvLogLevel: "debug",
		}, config.Config{
			SOPSFile: "/in/s.json", AgeKeyFile: "/in/k", MapFile: "/in/m.yaml", MapRequired: true,
			OutputDir: "/out", AllowNonTmpfs: true, WipeOnExit: true, LogLevel: slog.LevelDebug,
		}},
		{"explicit map file is required", map[string]string{config.EnvMapFile: config.DefaultMapFile}, func() config.Config {
			c := defaults
			c.MapRequired = true
			return c
		}()},
		{"false booleans", map[string]string{config.EnvAllowNonTmpfs: "false", config.EnvWipeOnExit: "F"}, defaults},
		{"level warn", map[string]string{config.EnvLogLevel: "warn"}, func() config.Config {
			c := defaults
			c.LogLevel = slog.LevelWarn
			return c
		}()},
		{"level error", map[string]string{config.EnvLogLevel: "error"}, func() config.Config {
			c := defaults
			c.LogLevel = slog.LevelError
			return c
		}()},
		{"level is case-insensitive", map[string]string{config.EnvLogLevel: "INFO"}, defaults},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := config.FromEnv(lookupFrom(tt.env))
			if err != nil {
				t.Fatalf("FromEnv: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("config mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFromEnvErrors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantAll []string
	}{
		{"bad bool allow", map[string]string{config.EnvAllowNonTmpfs: "yes please"}, []string{config.EnvAllowNonTmpfs}},
		{"bad bool wipe", map[string]string{config.EnvWipeOnExit: "2"}, []string{config.EnvWipeOnExit}},
		{"bad level", map[string]string{config.EnvLogLevel: "verbose"}, []string{config.EnvLogLevel}},
		{"level offset refused", map[string]string{config.EnvLogLevel: "info+1"}, []string{config.EnvLogLevel}},
		{"relative sops file", map[string]string{config.EnvSOPSFile: "secrets.yaml"}, []string{config.EnvSOPSFile, "absolute"}},
		{"relative age key file", map[string]string{config.EnvAgeKeyFile: "./k"}, []string{config.EnvAgeKeyFile}},
		{"relative map file", map[string]string{config.EnvMapFile: "m.yaml"}, []string{config.EnvMapFile}},
		{"relative output dir", map[string]string{config.EnvOutputDir: "secrets"}, []string{config.EnvOutputDir}},
		{"age key in env", map[string]string{config.EnvAgeKey: canary}, []string{config.EnvAgeKey, "docker inspect", config.EnvAgeKeyFile}},
		{"age key in env but empty", map[string]string{config.EnvAgeKey: ""}, []string{config.EnvAgeKey}},
		{"all problems reported", map[string]string{config.EnvOutputDir: "x", config.EnvLogLevel: "loud"}, []string{config.EnvOutputDir, config.EnvLogLevel}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.FromEnv(lookupFrom(tt.env))
			if err == nil {
				t.Fatal("FromEnv succeeded, want error")
			}
			for _, want := range tt.wantAll {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("error leaks the value of %s: %v", config.EnvAgeKey, err)
			}
		})
	}
}
