package decrypt_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/getsops/sops/v3"
	"github.com/google/go-cmp/cmp"
	"go.yaml.in/yaml/v3"

	"github.com/shreyaspurohit/sops-sidecar/internal/decrypt"
	"github.com/shreyaspurohit/sops-sidecar/internal/sopstest"
)

// canary stands in for secret data; it must never appear in an error.
const canary = "CANARY-7f3a9c-SECRET"

func item(k string, v any) sops.TreeItem { return sops.TreeItem{Key: k, Value: v} }

func newIDs(t *testing.T) (decrypt.Identities, string) {
	t.Helper()
	keyFile, recipient := sopstest.NewIdentity(t)
	ids, err := decrypt.ParseIdentities(keyFile)
	if err != nil {
		t.Fatalf("ParseIdentities: %v", err)
	}
	return ids, recipient
}

func plain(s []decrypt.Secret) map[string]string {
	m := make(map[string]string, len(s))
	for _, x := range s {
		m[x.Key] = string([]byte(x.Value))
	}
	return m
}

// rewriteSops decodes a YAML SOPS file, lets edit change its "sops" metadata
// map, and encodes it again.
func rewriteSops(t *testing.T, file []byte, edit func(meta map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(file, &doc); err != nil {
		t.Fatal(err)
	}
	meta, ok := doc["sops"].(map[string]any)
	if !ok {
		t.Fatal("no sops metadata")
	}
	edit(meta)
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDecryptValues(t *testing.T) {
	tests := []struct {
		name   string
		branch sops.TreeBranch
		want   map[string]string
	}{
		{"simple strings", sops.TreeBranch{item("b", "two"), item("a", "one")}, map[string]string{"a": "one", "b": "two"}},
		{"no trailing newline", sops.TreeBranch{item("k", "abc")}, map[string]string{"k": "abc"}},
		{"trailing newline kept", sops.TreeBranch{item("k", "abc\n")}, map[string]string{"k": "abc\n"}},
		{"multiple trailing newlines", sops.TreeBranch{item("k", "abc\n\n")}, map[string]string{"k": "abc\n\n"}},
		{"leading and trailing spaces", sops.TreeBranch{item("k", "  abc  ")}, map[string]string{"k": "  abc  "}},
		{"multi-line", sops.TreeBranch{item("k", "line1\nline2\n")}, map[string]string{"k": "line1\nline2\n"}},
		{"unicode", sops.TreeBranch{item("k", "pässwörd ✓ 日本語")}, map[string]string{"k": "pässwörd ✓ 日本語"}},
		{"nul and high bytes", sops.TreeBranch{item("k", "a\x00b\xff\xfe\x80")}, map[string]string{"k": "a\x00b\xff\xfe\x80"}},
		{"empty string", sops.TreeBranch{item("k", "")}, map[string]string{"k": ""}},
		{"integer", sops.TreeBranch{item("n", 42)}, map[string]string{"n": "42"}},
		{"negative integer", sops.TreeBranch{item("n", -7)}, map[string]string{"n": "-7"}},
		{"float", sops.TreeBranch{item("f", 1.5)}, map[string]string{"f": "1.5"}},
		{"bool true", sops.TreeBranch{item("t", true)}, map[string]string{"t": "True"}},
		{"bool false", sops.TreeBranch{item("t", false)}, map[string]string{"t": "False"}},
		{"comments skipped", sops.TreeBranch{sops.TreeItem{Key: sops.Comment{Value: "a note"}}, item("k", "v")}, map[string]string{"k": "v"}},
		{"key needing quoting", sops.TreeBranch{item("with space: colon", "v")}, map[string]string{"with space: colon": "v"}},
	}
	formats := []struct {
		name    string
		format  decrypt.Format
		encrypt func(testing.TB, string, sops.TreeBranch) []byte
	}{
		{"yaml", decrypt.FormatYAML, sopstest.EncryptYAML},
		{"json", decrypt.FormatJSON, sopstest.EncryptJSON},
	}
	for _, ft := range formats {
		for _, tt := range tests {
			t.Run(ft.name+"/"+tt.name, func(t *testing.T) {
				if ft.name == "json" && len(tt.branch) > 0 {
					if _, isComment := tt.branch[0].Key.(sops.Comment); isComment {
						t.Skip("JSON has no comments")
					}
				}
				ids, recipient := newIDs(t)
				file := ft.encrypt(t, recipient, tt.branch)
				got, err := decrypt.Decrypt(file, ft.format, ids)
				if err != nil {
					t.Fatalf("Decrypt: %v", err)
				}
				if diff := cmp.Diff(tt.want, plain(got)); diff != "" {
					t.Errorf("values mismatch (-want +got):\n%s", diff)
				}
			})
		}
	}
}

func TestDecryptSortedByKey(t *testing.T) {
	ids, recipient := newIDs(t)
	file := sopstest.EncryptYAML(t, recipient, sops.TreeBranch{item("c", "3"), item("a", "1"), item("b", "2")})
	got, err := decrypt.Decrypt(file, decrypt.FormatYAML, ids)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, s := range got {
		keys = append(keys, s.Key)
	}
	if diff := cmp.Diff([]string{"a", "b", "c"}, keys); diff != "" {
		t.Errorf("keys not sorted (-want +got):\n%s", diff)
	}
}

func TestDecryptRefusesNonScalars(t *testing.T) {
	tests := []struct {
		name    string
		branch  sops.TreeBranch
		wantKey string
	}{
		{"nested map", sops.TreeBranch{item("ok", "v"), item("db", sops.TreeBranch{item("pw", canary)})}, "db"},
		{"list", sops.TreeBranch{item("hosts", []any{canary, "b"})}, "hosts"},
		{"null", sops.TreeBranch{item("empty", nil)}, "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, recipient := newIDs(t)
			file := sopstest.EncryptYAML(t, recipient, tt.branch)
			_, err := decrypt.Decrypt(file, decrypt.FormatYAML, ids)
			if err == nil {
				t.Fatal("Decrypt succeeded, want error")
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", tt.wantKey)) {
				t.Errorf("error %q does not name key %q", err, tt.wantKey)
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("error leaks value: %v", err)
			}
		})
	}
}

func TestDecryptFailures(t *testing.T) {
	branch := sops.TreeBranch{item("a", canary), item("b", "two")}
	tests := []struct {
		name    string
		format  decrypt.Format
		mutate  func(t *testing.T, file []byte) []byte
		wantMsg string
		wrongID bool
	}{
		{"wrong age key", decrypt.FormatYAML, func(_ *testing.T, f []byte) []byte { return f }, "none of the age identities", true},
		{"tampered ciphertext", decrypt.FormatYAML, func(_ *testing.T, f []byte) []byte {
			return bytes.Replace(f, []byte("a: ENC[AES256_GCM,data:"), []byte("a: ENC[AES256_GCM,data:AAAA"), 1)
		}, "tampered", false},
		{"entry removed breaks mac", decrypt.FormatYAML, func(_ *testing.T, f []byte) []byte {
			var out []string
			for _, l := range strings.Split(string(f), "\n") {
				if !strings.HasPrefix(l, "b: ") {
					out = append(out, l)
				}
			}
			return []byte(strings.Join(out, "\n"))
		}, "mac mismatch", false},
		{"mac replaced", decrypt.FormatYAML, func(t *testing.T, f []byte) []byte {
			return rewriteSops(t, f, func(m map[string]any) { m["mac"] = "ENC[AES256_GCM,data:AAAA,iv:AAAA,tag:AAAA,type:str]" })
		}, "stored mac is invalid", false},
		{"two key groups", decrypt.FormatYAML, func(t *testing.T, f []byte) []byte {
			return rewriteSops(t, f, func(m map[string]any) {
				age := m["age"]
				delete(m, "age")
				m["key_groups"] = []any{map[string]any{"age": age}, map[string]any{"age": age}}
			})
		}, "2 key groups", false},
		{"no age key", decrypt.FormatYAML, func(t *testing.T, f []byte) []byte {
			return rewriteSops(t, f, func(m map[string]any) {
				delete(m, "age")
				m["kms"] = fakeKMSKeys()
			})
		}, "no age key", false},
		{"garbage yaml", decrypt.FormatYAML, func(*testing.T, []byte) []byte { return []byte("a: [" + canary) }, "not a valid sops yaml", false},
		{"plain yaml without sops metadata", decrypt.FormatYAML, func(*testing.T, []byte) []byte { return []byte("a: " + canary + "\n") }, "not a valid sops yaml", false},
		{"garbage json", decrypt.FormatJSON, func(*testing.T, []byte) []byte { return []byte("{" + canary) }, "not a valid sops json", false},
		{"empty input", decrypt.FormatYAML, func(*testing.T, []byte) []byte { return nil }, "not a valid sops yaml", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, recipient := newIDs(t)
			file := sopstest.EncryptYAML(t, recipient, branch)
			if tt.wrongID {
				ids, _ = newIDs(t)
			}
			_, err := decrypt.Decrypt(tt.mutate(t, file), tt.format, ids)
			if err == nil {
				t.Fatal("Decrypt succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not contain %q", err, tt.wantMsg)
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("error leaks canary: %v", err)
			}
			if strings.Contains(err.Error(), "ENC[") {
				t.Errorf("error echoes file content: %v", err)
			}
		})
	}
}

func TestDecryptIgnoresNonAgeKeysInGroup(t *testing.T) {
	ids, recipient := newIDs(t)
	file := sopstest.EncryptYAML(t, recipient, sops.TreeBranch{item("k", "v")})
	file = rewriteSops(t, file, func(m map[string]any) {
		m["kms"] = fakeKMSKeys()
	})
	got, err := decrypt.Decrypt(file, decrypt.FormatYAML, ids)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if diff := cmp.Diff(map[string]string{"k": "v"}, plain(got)); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestDecryptZeroIdentities(t *testing.T) {
	_, recipient := newIDs(t)
	file := sopstest.EncryptYAML(t, recipient, sops.TreeBranch{item("k", "v")})
	if _, err := decrypt.Decrypt(file, decrypt.FormatYAML, decrypt.Identities{}); err == nil {
		t.Fatal("Decrypt with zero Identities succeeded")
	}
}

func TestDecryptIgnoresEnvironmentKeys(t *testing.T) {
	envID, _ := sopstest.NewIdentity(t)
	t.Setenv("SOPS_AGE_KEY", string(envID))
	keyFile, recipient := sopstest.NewIdentity(t)
	file := sopstest.EncryptYAML(t, recipient, sops.TreeBranch{item("k", "v")})
	// the identity from the environment is for a different recipient, so only the explicit one may be used
	ids, err := decrypt.ParseIdentities(envID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decrypt.Decrypt(file, decrypt.FormatYAML, ids); err == nil {
		t.Fatal("Decrypt succeeded with the wrong explicit identity")
	}
	right, err := decrypt.ParseIdentities(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decrypt.Decrypt(file, decrypt.FormatYAML, right); err != nil {
		t.Fatalf("Decrypt with right identity: %v", err)
	}
}

func TestDecryptSecondIdentityWorks(t *testing.T) {
	keyFile, recipient := sopstest.NewIdentity(t)
	other, _ := sopstest.NewIdentity(t)
	ids, err := decrypt.ParseIdentities(append(append([]byte("# two keys\n\n"), other...), keyFile...))
	if err != nil {
		t.Fatal(err)
	}
	file := sopstest.EncryptYAML(t, recipient, sops.TreeBranch{item("k", "v")})
	if _, err := decrypt.Decrypt(file, decrypt.FormatYAML, ids); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
}

func TestParseIdentities(t *testing.T) {
	good, _ := sopstest.NewIdentity(t)
	tests := []struct {
		name    string
		in      []byte
		wantErr bool
	}{
		{"generated key file", good, false},
		{"comments and blank lines", append([]byte("# c\n\n"), good...), false},
		{"empty", nil, true},
		{"only comments", []byte("# nothing\n"), true},
		{"garbage with canary", []byte(canary + "\nAGE-SECRET-KEY-1" + canary + "\n"), true},
		{"truncated secret key", []byte("AGE-SECRET-KEY-1QQQQ\n"), true},
		{"not utf-8", []byte{0xff, 0xfe, '\n'}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decrypt.ParseIdentities(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && (strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "AGE-SECRET-KEY-1Q")) {
				t.Errorf("error leaks key file content: %v", err)
			}
		})
	}
}

func TestFormatForPath(t *testing.T) {
	tests := []struct {
		path string
		want decrypt.Format
	}{
		{"/config/secrets.sops.yaml", decrypt.FormatYAML},
		{"/config/secrets.sops.yml", decrypt.FormatYAML},
		{"/config/secrets.json", decrypt.FormatJSON},
		{"/config/SECRETS.JSON", decrypt.FormatJSON},
		{"/config/secrets.json.bak", decrypt.FormatYAML},
		{"/config/json", decrypt.FormatYAML},
		{"", decrypt.FormatYAML},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := decrypt.FormatForPath(tt.path); got != tt.want {
				t.Errorf("FormatForPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestValueNeverRevealsContent(t *testing.T) {
	ids, recipient := newIDs(t)
	file := sopstest.EncryptYAML(t, recipient, sops.TreeBranch{item("k", canary)})
	secrets, err := decrypt.Decrypt(file, decrypt.FormatYAML, ids)
	if err != nil {
		t.Fatal(err)
	}
	s := secrets[0]
	if string([]byte(s.Value)) != canary {
		t.Fatal("explicit conversion must give the content")
	}
	outputs := map[string]string{
		"value":     fmt.Sprintf("%v %+v %#v %s %q %x %X %d %T", s.Value, s.Value, s.Value, s.Value, s.Value, s.Value, s.Value, s.Value, s.Value),
		"secret":    fmt.Sprintf("%v %+v %#v %s %q %x", s, s, s, s, s, s),
		"secrets":   fmt.Sprintf("%v %+v %#v", secrets, secrets, secrets),
		"pointer":   fmt.Sprintf("%v %+v", &s, &s),
		"identity":  fmt.Sprintf("%v %+v %#v %s %q %x", ids, ids, ids, ids, ids, ids),
		"stringer":  s.Value.String() + s.Value.GoString(),
		"sprint":    fmt.Sprint(s.Value, s),
		"sprintln":  fmt.Sprintln(s.Value, s),
		"slice-key": fmt.Sprintf("%v", map[string]decrypt.Value{"k": s.Value}),
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	logger.Info("msg", "secret", s, "value", s.Value, "ids", ids, slog.Any("any", s))
	logger.Info("msg", slog.Group("g", slog.Any("v", s.Value)))
	outputs["slog-text"] = logs.String()
	logs.Reset()
	jlogger := slog.New(slog.NewJSONHandler(&logs, nil))
	jlogger.Info("msg", "secret", s, "value", s.Value, "ids", ids)
	outputs["slog-json"] = logs.String()
	for name, out := range outputs {
		if strings.Contains(out, canary) {
			t.Errorf("%s output reveals the value: %s", name, out)
		}
		if strings.Contains(out, "AGE-SECRET-KEY") {
			t.Errorf("%s output reveals the identity: %s", name, out)
		}
	}
	if !strings.Contains(outputs["value"], "[REDACTED]") {
		t.Errorf("value output = %q, want a redaction marker", outputs["value"])
	}
}

func TestIdentityNeverRevealed(t *testing.T) {
	keyFile, _ := sopstest.NewIdentity(t)
	ids, err := decrypt.ParseIdentities(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	secretLine := ""
	for _, l := range strings.Split(string(keyFile), "\n") {
		if strings.HasPrefix(l, "AGE-SECRET-KEY-") {
			secretLine = l
		}
	}
	out := fmt.Sprintf("%v|%+v|%#v|%s|%q|%x", ids, ids, ids, ids, ids, ids)
	if strings.Contains(out, secretLine) || strings.Contains(strings.ToLower(out), strings.ToLower(secretLine)) {
		t.Errorf("formatting reveals the identity: %s", out)
	}
}

// fakeKMSKeys is SOPS metadata for a KMS key that can never be reached.
func fakeKMSKeys() []any {
	return []any{map[string]any{
		"arn":         "arn:aws:kms:us-east-1:111122223333:key/00000000-0000-0000-0000-000000000000",
		"enc":         "AAAA",
		"created_at":  "2024-01-01T00:00:00Z",
		"aws_profile": "",
	}}
}
