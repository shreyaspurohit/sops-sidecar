package writer_test

import (
	"testing"

	"github.com/shreyaspurohit/sops-sidecar/internal/writer"
)

const sampleMounts = `rootfs / rootfs rw 0 0
overlay / overlay rw,relatime 0 0
proc /proc proc rw,nosuid 0 0
tmpfs /secrets tmpfs rw,nosuid,size=1024k 0 0
ext4 /secrets-disk ext4 rw 0 0
tmpfs /run/my\040secrets tmpfs rw 0 0
tmpfs /tab\011dir tmpfs rw 0 0
tmpfs /back\134slash tmpfs rw 0 0
ext4 /secrets/deep ext4 rw 0 0
`

func TestMountType(t *testing.T) {
	tests := []struct {
		name    string
		mounts  string
		dir     string
		want    string
		wantErr bool
	}{
		{"exact mount point", sampleMounts, "/secrets", "tmpfs", false},
		{"below mount point", sampleMounts, "/secrets/sub", "tmpfs", false},
		{"deeper mount wins", sampleMounts, "/secrets/deep/x", "ext4", false},
		{"prefix is not a path prefix", sampleMounts, "/secrets-other", "overlay", false},
		{"sibling with longer name", sampleMounts, "/secrets-disk", "ext4", false},
		{"root matches everything", sampleMounts, "/etc", "overlay", false},
		{"root itself", sampleMounts, "/", "overlay", false},
		{"space escape", sampleMounts, "/run/my secrets", "tmpfs", false},
		{"tab escape", sampleMounts, "/tab\tdir/x", "tmpfs", false},
		{"backslash escape", sampleMounts, `/back\slash`, "tmpfs", false},
		{"last of equal mount points wins", "a / ext4 rw 0 0\nb / tmpfs rw 0 0\n", "/x", "tmpfs", false},
		{"no trailing newline", "tmpfs /s tmpfs rw 0 0", "/s", "tmpfs", false},
		{"blank lines ignored", "\n\ntmpfs /s tmpfs rw 0 0\n\n", "/s/a", "tmpfs", false},
		{"invalid escape kept literally", `tmpfs /a\999b tmpfs rw 0 0`, `/a\999b`, "tmpfs", false},
		{"truncated escape kept literally", `tmpfs /a\04 tmpfs rw 0 0`, `/a\04`, "tmpfs", false},
		{"no matching mount", "tmpfs /other tmpfs rw 0 0\n", "/secrets", "", true},
		{"empty table", "", "/secrets", "", true},
		{"malformed line", "tmpfs /secrets\n", "/secrets", "", true},
		{"relative dir", sampleMounts, "secrets", "", true},
		{"unclean dir", sampleMounts, "/secrets/../x", "", true},
		{"trailing slash dir", sampleMounts, "/secrets/", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := writer.MountType([]byte(tt.mounts), tt.dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("MountType error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("MountType = %q, want %q", got, tt.want)
			}
		})
	}
}
