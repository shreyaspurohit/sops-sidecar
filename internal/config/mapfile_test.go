package config_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shreyaspurohit/sops-sidecar/internal/config"
	"github.com/shreyaspurohit/sops-sidecar/internal/decrypt"
	"github.com/shreyaspurohit/sops-sidecar/internal/writer"
)

func secrets(kv ...string) []decrypt.Secret {
	var out []decrypt.Secret
	for i := 0; i < len(kv); i += 2 {
		out = append(out, decrypt.Secret{Key: kv[i], Value: decrypt.Value(kv[i+1])})
	}
	return out
}

// fileView is the comparable essence of an Entry.
type fileView struct {
	Key, Name, Data string
	Mode            writer.Mode
	UID, GID        int
}

func view(p config.Plan) []fileView {
	var out []fileView
	for _, e := range p.Entries {
		out = append(out, fileView{e.Key, e.File.Name.String(), string(e.File.Data), e.File.Mode, e.File.Owner.UID, e.File.Owner.GID})
	}
	return out
}

func TestParseMapPlan(t *testing.T) {
	keep := writer.KeepID
	tests := []struct {
		name        string
		yaml        string
		secrets     []decrypt.Secret
		want        []fileView
		wantIgnored int
	}{
		{
			name:    "minimal entry uses key as name and built-in defaults",
			yaml:    "files:\n  - key: db\n",
			secrets: secrets("db", "pw"),
			want:    []fileView{{"db", "db", "pw", 0o400, keep, keep}},
		},
		{
			name:    "path overrides name",
			yaml:    "files:\n  - key: db_password\n    path: postgres_password\n",
			secrets: secrets("db_password", "pw"),
			want:    []fileView{{"db_password", "postgres_password", "pw", 0o400, keep, keep}},
		},
		{
			name:    "defaults are inherited",
			yaml:    "defaults: {uid: 70, gid: 71, mode: '0440'}\nfiles:\n  - key: a\n  - key: b\n",
			secrets: secrets("a", "1", "b", "2"),
			want: []fileView{
				{"a", "a", "1", 0o440, 70, 71},
				{"b", "b", "2", 0o440, 70, 71},
			},
		},
		{
			name:    "entry overrides defaults, partially",
			yaml:    "defaults: {uid: 70, gid: 71, mode: '0440'}\nfiles:\n  - key: a\n    uid: 0\n    mode: 0400\n  - key: b\n    gid: 5\n",
			secrets: secrets("a", "1", "b", "2"),
			want: []fileView{
				{"a", "a", "1", 0o400, 0, 71},
				{"b", "b", "2", 0o440, 70, 5},
			},
		},
		{
			name:    "mode as bare number, quoted string, and 3 digits",
			yaml:    "files:\n  - {key: a, mode: 0640}\n  - {key: b, mode: '0600'}\n  - {key: c, mode: 444}\n",
			secrets: secrets("a", "1", "b", "2", "c", "3"),
			want: []fileView{
				{"a", "a", "1", 0o640, keep, keep},
				{"b", "b", "2", 0o600, keep, keep},
				{"c", "c", "3", 0o444, keep, keep},
			},
		},
		{
			name:    "only listed keys are written, rest ignored",
			yaml:    "files:\n  - key: a\n",
			secrets: secrets("a", "1", "b", "2", "c", "3"),
			want:    []fileView{{"a", "a", "1", 0o400, keep, keep}},

			wantIgnored: 2,
		},
		{
			name:    "entries are sorted by file name",
			yaml:    "files:\n  - {key: z, path: a}\n  - {key: a, path: z}\n  - {key: m}\n",
			secrets: secrets("a", "1", "m", "2", "z", "3"),
			want: []fileView{
				{"z", "a", "3", 0o400, keep, keep},
				{"m", "m", "2", 0o400, keep, keep},
				{"a", "z", "1", 0o400, keep, keep},
			},
		},
		{
			name:    "values are byte-exact",
			yaml:    "files:\n  - key: a\n",
			secrets: secrets("a", "x\n\x00\xff"),
			want:    []fileView{{"a", "a", "x\n\x00\xff", 0o400, keep, keep}},
		},
		{
			name:    "comments and anchors in the map file are fine",
			yaml:    "# header\nfiles:\n  - key: a # trailing\n",
			secrets: secrets("a", "1"),
			want:    []fileView{{"a", "a", "1", 0o400, keep, keep}},
		},
		{
			name:    "leading document marker",
			yaml:    "---\nfiles:\n  - key: a\n",
			secrets: secrets("a", "1"),
			want:    []fileView{{"a", "a", "1", 0o400, keep, keep}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := config.ParseMap([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("ParseMap: %v", err)
			}
			plan, err := m.Plan(tt.secrets)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if diff := cmp.Diff(tt.want, view(plan)); diff != "" {
				t.Errorf("entries mismatch (-want +got):\n%s", diff)
			}
			if plan.Ignored != tt.wantIgnored {
				t.Errorf("Ignored = %d, want %d", plan.Ignored, tt.wantIgnored)
			}
		})
	}
}

func TestParseMapErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"empty file", "", "empty"},
		{"only comments", "# nothing\n", "empty"},
		{"empty document", "---\n", "no files"},
		{"empty files list", "files: []\n", "no files"},
		{"files missing", "defaults: {mode: '0400'}\n", "no files"},
		{"unknown top-level field", "files:\n  - key: a\nextra: 1\n", "extra"},
		{"unknown entry field", "files:\n  - key: a\n    owner: 1\n", "owner"},
		{"unknown defaults field", "defaults: {path: x}\nfiles:\n  - key: a\n", "path"},
		{"missing key", "files:\n  - path: a\n", "key is required"},
		{"empty key", "files:\n  - key: ''\n", "key is required"},
		{"duplicate key", "files:\n  - {key: a}\n  - {key: a, path: b}\n", "more than once"},
		{"duplicate path", "files:\n  - {key: a, path: x}\n  - {key: b, path: x}\n", "used by keys"},
		{"duplicate via default path", "files:\n  - {key: a}\n  - {key: b, path: a}\n", "used by keys"},
		{"path with slash", "files:\n  - {key: a, path: sub/file}\n", "path"},
		{"path traversal", "files:\n  - {key: a, path: ../x}\n", "path"},
		{"path is slash", "files:\n  - {key: a, path: /}\n", "path"},
		{"absolute path", "files:\n  - {key: a, path: /etc/passwd}\n", "path"},
		{"path dot dot", "files:\n  - {key: a, path: ..}\n", "path"},
		{"empty path", "files:\n  - {key: a, path: ''}\n", "path"},
		{"reserved path", "files:\n  - {key: a, path: .sops-sidecar-ready}\n", "reserved"},
		{"key as reserved name", "files:\n  - {key: .sops-sidecar-x}\n", "reserved"},
		{"key with slash and no path", "files:\n  - {key: a/b}\n", "path"},
		{"bad mode digits", "files:\n  - {key: a, mode: '0999'}\n", "mode"},
		{"bad mode too long", "files:\n  - {key: a, mode: '04000'}\n", "mode"},
		{"bad mode text", "files:\n  - {key: a, mode: rw}\n", "mode"},
		{"bad mode in defaults", "defaults: {mode: '8'}\nfiles:\n  - key: a\n", "defaults"},
		{"mode is a list", "files:\n  - {key: a, mode: [1]}\n", "mode"},
		{"negative uid", "files:\n  - {key: a, uid: -1}\n", "uid"},
		{"negative gid", "files:\n  - {key: a, gid: -5}\n", "gid"},
		{"negative default uid", "defaults: {uid: -1}\nfiles:\n  - key: a\n", "uid"},
		{"uid not a number", "files:\n  - {key: a, uid: root}\n", "cannot unmarshal"},
		{"two documents", "files:\n  - key: a\n---\nfiles:\n  - key: b\n", "single YAML document"},
		{"second document malformed", "files:\n  - key: a\n---\n: [\n", "single YAML document"},
		{"not yaml", "files: [", "map file"},
		{"top-level list", "- key: a\n", "map file"},
		{"files is a scalar", "files: nope\n", "map file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.ParseMap([]byte(tt.yaml))
			if err == nil {
				t.Fatal("ParseMap succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestPlanWithoutMapFile(t *testing.T) {
	keep := writer.KeepID
	t.Run("every key under its own name", func(t *testing.T) {
		plan, err := config.Map{}.Plan(secrets("b", "2", "a", "1"))
		if err != nil {
			t.Fatal(err)
		}
		want := []fileView{{"a", "a", "1", 0o400, keep, keep}, {"b", "b", "2", 0o400, keep, keep}}
		if diff := cmp.Diff(want, view(plan)); diff != "" {
			t.Errorf("entries mismatch (-want +got):\n%s", diff)
		}
		if plan.Ignored != 0 {
			t.Errorf("Ignored = %d, want 0", plan.Ignored)
		}
	})
	t.Run("no secrets gives an empty plan", func(t *testing.T) {
		plan, err := config.Map{}.Plan(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Entries) != 0 {
			t.Errorf("entries = %v, want none", plan.Entries)
		}
	})
	for _, key := range []string{"a/b", "..", ".sops-sidecar-ready", "x\x00y", ""} {
		t.Run(fmt.Sprintf("invalid key %q names the key not the value", key), func(t *testing.T) {
			_, err := config.Map{}.Plan(secrets("ok", "1", key, canary))
			if err == nil {
				t.Fatal("Plan succeeded, want error")
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", key)) {
				t.Errorf("error %q does not name key %q", err, key)
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("error leaks value: %v", err)
			}
		})
	}
}

func TestPlanMissingKey(t *testing.T) {
	m, err := config.ParseMap([]byte("files:\n  - key: present\n  - key: absent\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Plan(secrets("present", canary))
	if err == nil {
		t.Fatal("Plan succeeded, want error")
	}
	if !strings.Contains(err.Error(), `"absent"`) {
		t.Errorf("error %q does not name the missing key", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Errorf("error leaks value: %v", err)
	}
}

func TestPlanCopiesValues(t *testing.T) {
	m, err := config.ParseMap([]byte("files:\n  - key: a\n"))
	if err != nil {
		t.Fatal(err)
	}
	in := secrets("a", "abc")
	plan, err := m.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	in[0].Value[0] = 'X'
	if got := string(plan.Entries[0].File.Data); got != "abc" {
		t.Errorf("plan data changed with the input: %q", got)
	}
}

func FuzzParseMap(f *testing.F) {
	for _, s := range []string{
		"files:\n  - key: a\n",
		"defaults: {uid: 1, gid: 2, mode: '0440'}\nfiles:\n  - {key: a, path: b, mode: 400}\n",
		"files: []\n",
		"files:\n  - key: a\n---\nfiles:\n  - key: b\n",
		"files:\n  - {key: a, uid: -1}\n",
		"files:\n  - {key: a, path: ../x}\n",
		"",
		"\x00",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := config.ParseMap(data)
		if err != nil {
			return
		}
		if _, err := m.Plan(nil); err == nil {
			t.Fatal("a parsed map file lists at least one key, so Plan(nil) must report it missing")
		}
	})
}

func ExampleMap_Plan() {
	m, err := config.ParseMap([]byte(`
defaults: {mode: "0440"}
files:
  - key: db_password
    path: postgres_password
  - key: api_token
    mode: "0400"
`))
	if err != nil {
		panic(err)
	}
	plan, err := m.Plan(secrets("api_token", "t", "db_password", "p", "unused", "u"))
	if err != nil {
		panic(err)
	}
	for _, e := range plan.Entries {
		fmt.Printf("%s -> %s %s\n", e.Key, e.File.Name, e.File.Mode)
	}
	fmt.Println("ignored:", plan.Ignored)
	// Output:
	// api_token -> api_token 0400
	// db_password -> postgres_password 0440
	// ignored: 1
}
