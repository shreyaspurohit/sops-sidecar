package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/shreyaspurohit/sops-sidecar/internal/decrypt"
	"github.com/shreyaspurohit/sops-sidecar/internal/writer"
)

// ErrNoSecrets is returned for a plan with no files to write. An empty result
// is refused rather than applied: applying it would remove every secret file
// and still report ready.
var ErrNoSecrets = errors.New("no secrets to write: the SOPS file holds no top-level keys")

// Map is a parsed map file. The zero Map means "no map file": every key is
// written under its own name with the built-in defaults.
type Map struct {
	entries []mapEntry
}

// mapEntry is one validated file of the map file with every default applied.
type mapEntry struct {
	key   string
	name  writer.Name
	mode  writer.Mode
	owner writer.Owner
}

// Entry is one file of the plan: the key it comes from and the file to write.
type Entry struct {
	// Key is the SOPS key the file's content comes from.
	Key string
	// File is the file to write; its Data is the key's value.
	File writer.File
}

// Plan is the validated list of files to write.
type Plan struct {
	// Entries are sorted by file name.
	Entries []Entry
	// Ignored counts the keys in the SOPS file that the map file does not list.
	Ignored int
}

// rawMap mirrors the map file's YAML schema before validation.
type rawMap struct {
	Defaults rawSettings `yaml:"defaults"`
	Files    []rawFile   `yaml:"files"`
}

// rawSettings holds the per-file settings that defaults may also provide.
type rawSettings struct {
	UID  *int     `yaml:"uid"`
	GID  *int     `yaml:"gid"`
	Mode *rawMode `yaml:"mode"`
}

type rawFile struct {
	Key      string      `yaml:"key"`
	Path     *string     `yaml:"path"`
	Settings rawSettings `yaml:",inline"`
}

// rawMode keeps a mode as the text written in the file, so that 0400, "0400"
// and 400 mean the same thing.
type rawMode string

// UnmarshalYAML accepts a scalar and keeps its text.
func (m *rawMode) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return errors.New("mode must be a scalar such as 0400")
	}
	*m = rawMode(n.Value)
	return nil
}

// ParseMap parses and validates a map file. The schema is strict: unknown
// fields, a second YAML document, an empty files list, empty or duplicate
// keys, invalid or duplicate file names, invalid modes, and ids outside 0..writer.MaxID are
// all errors.
func ParseMap(data []byte) (Map, error) {
	raw, err := decodeMap(data)
	if err != nil {
		return Map{}, err
	}
	if len(raw.Files) == 0 {
		return Map{}, errors.New("map file lists no files")
	}
	defaults, err := resolveSettings(raw.Defaults, settings{mode: writer.DefaultMode, owner: writer.KeepOwner()})
	if err != nil {
		return Map{}, fmt.Errorf("defaults: %w", err)
	}
	var m Map
	keys := make(map[string]struct{}, len(raw.Files))
	names := make(map[writer.Name]string, len(raw.Files))
	for i, f := range raw.Files {
		e, err := resolveFile(f, defaults)
		if err != nil {
			return Map{}, fmt.Errorf("files[%d]: %w", i, err)
		}
		if _, dup := keys[e.key]; dup {
			return Map{}, fmt.Errorf("files[%d]: key %q is listed more than once", i, e.key)
		}
		if other, dup := names[e.name]; dup {
			return Map{}, fmt.Errorf("files[%d]: file name %q is used by keys %q and %q", i, e.name, other, e.key)
		}
		keys[e.key], names[e.name] = struct{}{}, e.key
		m.entries = append(m.entries, e)
	}
	return m, nil
}

func decodeMap(data []byte) (rawMap, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var raw rawMap
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return rawMap{}, errors.New("map file is empty")
		}
		return rawMap{}, fmt.Errorf("parse map file: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return rawMap{}, errors.New("map file must hold a single YAML document")
	}
	return raw, nil
}

// settings are the resolved mode and owner of a file.
type settings struct {
	mode  writer.Mode
	owner writer.Owner
}

// resolveSettings overlays the values raw sets onto base.
func resolveSettings(raw rawSettings, base settings) (settings, error) {
	out := base
	if raw.Mode != nil {
		m, err := writer.ParseMode(string(*raw.Mode))
		if err != nil {
			return settings{}, err
		}
		out.mode = m
	}
	if err := applyID("uid", raw.UID, &out.owner.UID); err != nil {
		return settings{}, err
	}
	if err := applyID("gid", raw.GID, &out.owner.GID); err != nil {
		return settings{}, err
	}
	return out, nil
}

// applyID stores a set id in dst and rejects ids outside 0..writer.MaxID.
func applyID(name string, id, dst *int) error {
	if id == nil {
		return nil
	}
	if *id < 0 || *id > writer.MaxID {
		return fmt.Errorf("%s must be between 0 and %d", name, writer.MaxID)
	}
	*dst = *id
	return nil
}

func resolveFile(f rawFile, defaults settings) (mapEntry, error) {
	if f.Key == "" {
		return mapEntry{}, errors.New("key is required")
	}
	fileName := f.Key
	if f.Path != nil {
		fileName = *f.Path
	}
	name, err := writer.ParseName(fileName)
	if err != nil {
		return mapEntry{}, fmt.Errorf("key %q: path: %w", f.Key, err)
	}
	s, err := resolveSettings(f.Settings, defaults)
	if err != nil {
		return mapEntry{}, fmt.Errorf("key %q: %w", f.Key, err)
	}
	return mapEntry{key: f.Key, name: name, mode: s.mode, owner: s.owner}, nil
}

// Plan computes the files to write for secrets. With the zero Map every
// secret is written under its key as file name. With a parsed Map only the
// listed keys are written, a listed key that is missing from secrets is an
// error, and unlisted keys are counted in Ignored.
func (m Map) Plan(secrets []decrypt.Secret) (Plan, error) {
	entries := m.entries
	if len(entries) == 0 {
		var err error
		if entries, err = allKeys(secrets); err != nil {
			return Plan{}, err
		}
	}
	byKey := make(map[string]decrypt.Value, len(secrets))
	for _, s := range secrets {
		byKey[s.Key] = s.Value
	}
	plan := Plan{Entries: make([]Entry, 0, len(entries))}
	for _, e := range entries {
		v, ok := byKey[e.key]
		if !ok {
			return Plan{}, fmt.Errorf("key %q is listed in the map file but missing from the SOPS file", e.key)
		}
		plan.Entries = append(plan.Entries, Entry{Key: e.key, File: writer.File{Name: e.name, Data: bytes.Clone(v), Mode: e.mode, Owner: e.owner}})
	}
	if len(plan.Entries) == 0 {
		return Plan{}, ErrNoSecrets
	}
	plan.Ignored = len(secrets) - len(entries)
	slices.SortFunc(plan.Entries, func(a, b Entry) int { return strings.Compare(a.File.Name.String(), b.File.Name.String()) })
	return plan, nil
}

// allKeys lists every secret as an entry with the built-in defaults.
func allKeys(secrets []decrypt.Secret) ([]mapEntry, error) {
	entries := make([]mapEntry, 0, len(secrets))
	for _, s := range secrets {
		name, err := writer.ParseName(s.Key)
		if err != nil {
			return nil, fmt.Errorf("key %q cannot be a file name: %w", s.Key, err)
		}
		entries = append(entries, mapEntry{key: s.Key, name: name, mode: writer.DefaultMode, owner: writer.KeepOwner()})
	}
	return entries, nil
}
