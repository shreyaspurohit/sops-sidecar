// Package decrypt decrypts a SOPS file in-process with an age identity and
// returns its top-level scalar secrets.
package decrypt

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/getsops/sops/v3"
	"github.com/getsops/sops/v3/aes"
	sopsage "github.com/getsops/sops/v3/age"
	"github.com/getsops/sops/v3/config"
	"github.com/getsops/sops/v3/logging"
	"github.com/getsops/sops/v3/stores/json"
	sopsyaml "github.com/getsops/sops/v3/stores/yaml"
	"go.yaml.in/yaml/v3"
)

// Format is the serialization of a SOPS file.
type Format int

const (
	// FormatYAML is a YAML SOPS file.
	FormatYAML Format = iota
	// FormatJSON is a JSON SOPS file.
	FormatJSON
)

// errTamperedText is the message for ciphertext that cannot be decrypted.
const errTamperedText = "decrypt values: ciphertext is invalid or has been tampered with"

// FormatForPath returns FormatJSON for a path ending in ".json" (any case)
// and FormatYAML otherwise.
func FormatForPath(path string) Format {
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return FormatJSON
	}
	return FormatYAML
}

// Decrypt decrypts the SOPS file in data with ids and returns its top-level
// scalar secrets sorted by key. The file's MAC is always verified. Only age
// keys are used, so no network service is ever contacted. Errors name keys
// but never contain values or text echoed from the file, because the parsers
// involved may repeat their input.
func Decrypt(data []byte, f Format, ids Identities) ([]Secret, error) {
	tree, err := load(data, f)
	if err != nil {
		return nil, err
	}
	if tree.Metadata.MACOnlyEncrypted {
		// That MAC leaves unencrypted values unauthenticated, so anyone who
		// can write the file could inject or change them without the key.
		return nil, errors.New("sops file uses mac_only_encrypted, which leaves unencrypted values unauthenticated; re-encrypt it without that option")
	}
	dataKey, err := unwrapDataKey(tree.Metadata.KeyGroups, ids)
	if err != nil {
		return nil, err
	}
	if err := decryptTree(&tree, dataKey); err != nil {
		return nil, err
	}
	return secrets(tree.Branches)
}

func load(data []byte, f Format) (sops.Tree, error) {
	if f == FormatYAML {
		if err := singleYAMLDocument(data); err != nil {
			return sops.Tree{}, err
		}
	}
	var store sops.Store = sopsyaml.NewStore(&config.YAMLStoreConfig{})
	name := "yaml"
	if f == FormatJSON {
		store = json.NewStore(&config.JSONStoreConfig{})
		name = "json"
	}
	tree, err := store.LoadEncryptedFile(data)
	if err != nil {
		return sops.Tree{}, fmt.Errorf("input is not a valid sops %s file", name)
	}
	return tree, nil
}

// singleYAMLDocument errors unless data holds at most one YAML document. It
// must run before sops parses the file: sops expands aliases in later
// documents without cycle detection, so a cyclic alias there overflows the
// stack, which no recover can catch. Decoding into yaml.Node does not expand
// aliases.
func singleYAMLDocument(data []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return errors.New("input is not a valid sops yaml file")
	}
	if err := dec.Decode(&doc); !errors.Is(err, io.EOF) {
		return errors.New("sops file must hold exactly one YAML document")
	}
	return nil
}

// unwrapDataKey returns the data key from the first age key of the single key
// group that ids can open. Only ids are ever tried: no environment variable or
// home-directory identity is consulted, and no non-age key service is called.
func unwrapDataKey(groups []sops.KeyGroup, ids Identities) ([]byte, error) {
	if len(groups) != 1 {
		return nil, fmt.Errorf("sops file has %d key groups, exactly one is supported", len(groups))
	}
	if len(ids.ids) == 0 {
		return nil, errors.New("no age identities given")
	}
	sawAge := false
	for _, k := range groups[0] {
		key, ok := k.(*sopsage.MasterKey)
		if !ok {
			continue
		}
		sawAge = true
		ids.ids.ApplyToMasterKey(key)
		if dataKey, err := key.Decrypt(); err == nil {
			return dataKey, nil
		}
	}
	if !sawAge {
		return nil, errors.New("sops file has no age key; only age keys are supported")
	}
	return nil, errors.New("none of the age identities can decrypt the sops file's data key")
}

// decryptTree decrypts the tree's values in place and verifies that the MAC
// stored in the file matches the decrypted content. Crafted ciphertext
// metadata (such as a value or the MAC relabeled type:bytes) makes the pinned
// sops cipher panic; that is reported as invalid ciphertext instead of
// crashing the process.
func decryptTree(tree *sops.Tree, dataKey []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New(errTamperedText)
		}
	}()
	cipher := aes.NewCipher()
	mac, err := tree.Decrypt(dataKey, cipher)
	if err != nil {
		return errors.New(errTamperedText)
	}
	want, err := cipher.Decrypt(
		tree.Metadata.MessageAuthenticationCode,
		dataKey,
		tree.Metadata.LastModified.Format(time.RFC3339),
	)
	if err != nil {
		return errors.New("decrypt mac: stored mac is invalid")
	}
	if want != mac {
		return errors.New("mac mismatch: the sops file has been modified")
	}
	return nil
}

// DisableSOPSLogs discards the output of sops' own package loggers. They
// write to stderr outside the sidecar's log level, and some of their messages
// echo file content (such as a comment that fails to decrypt); the sidecar
// reports every failure through its own errors instead.
func DisableSOPSLogs() {
	for _, l := range logging.Loggers {
		l.SetOutput(io.Discard)
	}
}

// secrets turns the decrypted single document into secrets, refusing
// anything but top-level scalars.
func secrets(branches sops.TreeBranches) ([]Secret, error) {
	if len(branches) != 1 {
		return nil, fmt.Errorf("sops file has %d documents, exactly one is supported", len(branches))
	}
	var out []Secret
	seen := make(map[string]struct{})
	for _, item := range branches[0] {
		if _, isComment := item.Key.(sops.Comment); isComment {
			continue
		}
		key, ok := item.Key.(string)
		if !ok {
			return nil, fmt.Errorf("top-level key of type %T is not a string", item.Key)
		}
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("key %q appears more than once", key)
		}
		seen[key] = struct{}{}
		value, err := scalar(key, item.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, Secret{Key: key, Value: value})
	}
	slices.SortFunc(out, func(a, b Secret) int { return cmp.Compare(a.Key, b.Key) })
	return out, nil
}

// scalar converts a string, integer, float, or boolean to its SOPS byte form.
func scalar(key string, v any) (Value, error) {
	switch v.(type) {
	case string, int, float64, bool:
		b, err := sops.ToBytes(v)
		if err != nil {
			return nil, fmt.Errorf("key %q: cannot convert value to bytes", key)
		}
		return Value(b), nil
	case sops.TreeBranch:
		return nil, fmt.Errorf("key %q: value is a nested map; only top-level scalars are supported", key)
	case []any:
		return nil, fmt.Errorf("key %q: value is a list; only top-level scalars are supported", key)
	case nil:
		return nil, fmt.Errorf("key %q: value is null; only top-level scalars are supported", key)
	default:
		return nil, fmt.Errorf("key %q: value of type %T is not a supported scalar", key, v)
	}
}
