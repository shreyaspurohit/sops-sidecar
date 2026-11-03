// Package sopstest builds SOPS-encrypted fixtures for tests.
package sopstest

import (
	"crypto/rand"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/getsops/sops/v3"
	"github.com/getsops/sops/v3/aes"
	sopsage "github.com/getsops/sops/v3/age"
	"github.com/getsops/sops/v3/config"
	"github.com/getsops/sops/v3/stores/json"
	"github.com/getsops/sops/v3/stores/yaml"
	"github.com/getsops/sops/v3/version"
)

// dataKeySize is the size in bytes of a SOPS data key.
const dataKeySize = 32

// NewIdentity generates a fresh age identity and returns the text of its key
// file together with its public recipient.
func NewIdentity(t testing.TB) (keyFile []byte, recipient string) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	return []byte("# public key: " + id.Recipient().String() + "\n" + id.String() + "\n"), id.Recipient().String()
}

// EncryptYAML encrypts b for recipient as a YAML SOPS file with a single age
// key group and a valid MAC, the way `sops -e` does.
func EncryptYAML(t testing.TB, recipient string, b sops.TreeBranch) []byte {
	t.Helper()
	return encrypt(t, yaml.NewStore(&config.YAMLStoreConfig{}), recipient, b)
}

// EncryptJSON is EncryptYAML for a JSON SOPS file.
func EncryptJSON(t testing.TB, recipient string, b sops.TreeBranch) []byte {
	t.Helper()
	return encrypt(t, json.NewStore(&config.JSONStoreConfig{}), recipient, b)
}

func encrypt(t testing.TB, store sops.Store, recipient string, b sops.TreeBranch) []byte {
	t.Helper()
	key, err := sopsage.MasterKeyFromRecipient(recipient)
	if err != nil {
		t.Fatalf("age recipient: %v", err)
	}
	tree := sops.Tree{
		Branches: sops.TreeBranches{clone(b)},
		Metadata: sops.Metadata{
			KeyGroups:         []sops.KeyGroup{{key}},
			UnencryptedSuffix: sops.DefaultUnencryptedSuffix,
			Version:           version.Version,
		},
	}
	dataKey := make([]byte, dataKeySize)
	if _, err = rand.Read(dataKey); err != nil {
		t.Fatalf("data key: %v", err)
	}
	if err = key.Encrypt(dataKey); err != nil {
		t.Fatalf("encrypt data key: %v", err)
	}
	sealTree(t, &tree, dataKey)
	out, err := store.EmitEncryptedFile(tree)
	if err != nil {
		t.Fatalf("emit encrypted file: %v", err)
	}
	return out
}

// sealTree encrypts the tree's values and stores the encrypted MAC.
func sealTree(t testing.TB, tree *sops.Tree, dataKey []byte) {
	t.Helper()
	cipher := aes.NewCipher()
	mac, err := tree.Encrypt(dataKey, cipher)
	if err != nil {
		t.Fatalf("encrypt tree: %v", err)
	}
	tree.Metadata.LastModified = time.Now().UTC()
	tree.Metadata.MessageAuthenticationCode, err = cipher.Encrypt(mac, dataKey, tree.Metadata.LastModified.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("encrypt mac: %v", err)
	}
}

// clone returns a deep copy of b, so encryption, which rewrites values in
// place, leaves the original branch unchanged.
func clone(b sops.TreeBranch) sops.TreeBranch {
	out := make(sops.TreeBranch, len(b))
	for i, it := range b {
		out[i] = sops.TreeItem{Key: it.Key, Value: cloneValue(it.Value)}
	}
	return out
}

func cloneValue(v any) any {
	switch v := v.(type) {
	case sops.TreeBranch:
		return clone(v)
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = cloneValue(e)
		}
		return out
	default:
		return v
	}
}
