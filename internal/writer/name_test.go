package writer_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shreyaspurohit/sops-sidecar/internal/writer"
)

func TestParseName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"plain", "db_password", false},
		{"with dot", "app.key", false},
		{"leading dot", ".hidden", false},
		{"unicode", "pässwörd", false},
		{"max length", strings.Repeat("a", 255), false},
		{"too long", strings.Repeat("a", 256), true},
		{"empty", "", true},
		{"dot", ".", true},
		{"dot dot", "..", true},
		{"slash", "a/b", true},
		{"leading slash", "/etc/passwd", true},
		{"traversal", "../x", true},
		{"backslash", `a\b`, true},
		{"nul", "a\x00b", true},
		{"reserved prefix", writer.ReservedPrefix + "-ready", true},
		{"reserved prefix exact", writer.ReservedPrefix, true},
		{"prefix lookalike", ".sops-sidecarx", true},
		{"shorter than prefix", ".sops", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := writer.ParseName(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseName(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && n.String() != tt.in {
				t.Errorf("String() = %q, want %q", n.String(), tt.in)
			}
			if err != nil && n != (writer.Name{}) {
				t.Errorf("ParseName(%q) returned non-zero Name with error", tt.in)
			}
		})
	}
}

func TestParseReservedName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"marker", writer.ReservedPrefix + "-ready", false},
		{"exact prefix", writer.ReservedPrefix, false},
		{"ordinary name", "secret", true},
		{"empty", "", true},
		{"slash after prefix", writer.ReservedPrefix + "/x", true},
		{"dot dot", "..", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := writer.ParseReservedName(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseReservedName(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && n.String() != tt.in {
				t.Errorf("String() = %q, want %q", n.String(), tt.in)
			}
		})
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		in      string
		want    writer.Mode
		wantErr bool
	}{
		{"0400", 0o400, false},
		{"400", 0o400, false},
		{"0644", 0o644, false},
		{"777", 0o777, false},
		{"0000", 0, false},
		{"000", 0, false},
		{"", 0, true},
		{"0", 0, true},
		{"40", 0, true},
		{"1400", 0, true},
		{"4000", 0, true},
		{"00400", 0, true},
		{"0o400", 0, true},
		{"0800", 0, true},
		{"abc", 0, true},
		{"-400", 0, true},
		{"+400", 0, true},
		{" 400", 0, true},
		{"400 ", 0, true},
		{"0x1ff", 0, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.in), func(t *testing.T) {
			got, err := writer.ParseMode(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseMode(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseMode(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestModeString(t *testing.T) {
	tests := []struct {
		m    writer.Mode
		want string
	}{
		{0o400, "0400"},
		{0o644, "0644"},
		{0, "0000"},
		{0o7, "0007"},
		{writer.DefaultMode, "0400"},
	}
	for _, tt := range tests {
		if got := tt.m.String(); got != tt.want {
			t.Errorf("Mode(%o).String() = %q, want %q", uint32(tt.m), got, tt.want)
		}
	}
}

func TestStale(t *testing.T) {
	mk := func(names ...string) []writer.Name {
		var out []writer.Name
		for _, s := range names {
			n, err := writer.ParseName(s)
			if err != nil {
				t.Fatalf("ParseName(%q): %v", s, err)
			}
			out = append(out, n)
		}
		return out
	}
	tests := []struct {
		name       string
		prev, next []string
		want       []string
	}{
		{"both empty", nil, nil, nil},
		{"nothing previous", nil, []string{"a"}, nil},
		{"all stale", []string{"a", "b"}, nil, []string{"a", "b"}},
		{"none stale", []string{"a", "b"}, []string{"b", "a"}, nil},
		{"some stale keep prev order", []string{"c", "a", "b"}, []string{"a"}, []string{"c", "b"}},
		{"new names ignored", []string{"a"}, []string{"a", "z"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := writer.Stale(mk(tt.prev...), mk(tt.next...))
			if diff := cmp.Diff(mk(tt.want...), got, cmp.Comparer(func(a, b writer.Name) bool { return a == b })); diff != "" {
				t.Errorf("Stale mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func FuzzParseName(f *testing.F) {
	for _, s := range []string{"ok", "", ".", "..", "a/b", `a\b`, "a\x00b", ".sops-sidecar-ready", ".sops-sidecar", "pässwörd"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := writer.ParseName(s)
		if err != nil {
			return
		}
		if n.String() != s {
			t.Fatalf("round trip changed %q to %q", s, n.String())
		}
		if s == "" || s == "." || s == ".." || len(s) > 255 || strings.ContainsAny(s, "/\\\x00") || strings.HasPrefix(s, writer.ReservedPrefix) {
			t.Fatalf("ParseName accepted unsafe name %q", s)
		}
		if _, rerr := writer.ParseReservedName(s); rerr == nil {
			t.Fatalf("name %q accepted by both ParseName and ParseReservedName", s)
		}
	})
}
