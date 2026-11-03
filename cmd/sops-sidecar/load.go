package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/shreyaspurohit/sops-sidecar/internal/config"
	"github.com/shreyaspurohit/sops-sidecar/internal/decrypt"
)

// maxInputBytes is the largest input file that is read; larger files are refused.
const maxInputBytes = 1 << 20

// load reads the inputs named by cfg, decrypts the SOPS file, and returns the
// plan of files to write. It writes nothing and its errors never contain a value.
func load(cfg config.Config) (config.Plan, error) {
	keyFile, err := readInput(cfg.AgeKeyFile)
	if err != nil {
		return config.Plan{}, fmt.Errorf("read age key file: %w", err)
	}
	ids, err := decrypt.ParseIdentities(keyFile)
	if err != nil {
		return config.Plan{}, fmt.Errorf("age key file: %w", err)
	}
	data, err := readInput(cfg.SOPSFile)
	if err != nil {
		return config.Plan{}, fmt.Errorf("read SOPS file: %w", err)
	}
	secrets, err := decrypt.Decrypt(data, decrypt.FormatForPath(cfg.SOPSFile), ids)
	if err != nil {
		return config.Plan{}, fmt.Errorf("decrypt SOPS file: %w", err)
	}
	m, err := loadMap(cfg)
	if err != nil {
		return config.Plan{}, err
	}
	plan, err := m.Plan(secrets)
	if err != nil {
		return config.Plan{}, fmt.Errorf("plan: %w", err)
	}
	return plan, nil
}

// loadMap reads the map file. A missing map file at the default path yields
// the zero Map; a missing map file the user named explicitly is an error.
func loadMap(cfg config.Config) (config.Map, error) {
	data, err := readInput(cfg.MapFile)
	if errors.Is(err, fs.ErrNotExist) && !cfg.MapRequired {
		return config.Map{}, nil
	}
	if err != nil {
		return config.Map{}, fmt.Errorf("read map file: %w", err)
	}
	m, err := config.ParseMap(data)
	if err != nil {
		return config.Map{}, fmt.Errorf("map file: %w", err)
	}
	return m, nil
}

// readInput returns the contents of path, refusing files over maxInputBytes.
func readInput(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxInputBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxInputBytes)
	}
	return data, nil
}
