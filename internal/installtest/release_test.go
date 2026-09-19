package installtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseGate(t *testing.T) {
	source, e := os.ReadFile("../../scripts/check-release.sh")
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		tag, version, runtime string
		pass                  bool
	}{{"v0.3.0", "0.3.0", "0.3.0", true}, {"v0.3.0-rc1", "0.3.0", "0.3.0", false}, {"v01.3.0", "01.3.0", "01.3.0", false}, {"0.3.0", "0.3.0", "0.3.0", false}, {"v0.2.0", "0.3.0", "0.3.0", false}, {"v0.3.0", "0.3.0", "0.2.0", false}} {
		t.Run(tc.tag+"/"+tc.runtime, func(t *testing.T) {
			dir := t.TempDir()
			for _, path := range []string{"scripts", "internal/version", "dist"} {
				if e := os.MkdirAll(filepath.Join(dir, path), 0755); e != nil {
					t.Fatal(e)
				}
			}
			write(t, filepath.Join(dir, "scripts/check-release.sh"), string(source))
			write(t, filepath.Join(dir, "internal/version/version.go"), "const Version = \""+tc.version+"\"\n")
			write(t, filepath.Join(dir, "dist/codex-thread-bridge-linux-amd64"), "#!/bin/sh\necho "+tc.runtime+"\n")
			out, e := exec.Command("bash", filepath.Join(dir, "scripts/check-release.sh"), tc.tag).CombinedOutput()
			if (e == nil) != tc.pass {
				t.Fatalf("%s %v", out, e)
			}
		})
	}
}
func TestMissingPrerequisites(t *testing.T) {
	for _, missing := range []string{"curl", "checksum"} {
		t.Run(missing, func(t *testing.T) {
			f := setup(t)
			if missing == "curl" {
				if e := os.Remove(filepath.Join(f.bin, "curl")); e != nil {
					t.Fatal(e)
				}
			}
			if missing != "checksum" {
				if real, e := exec.LookPath("shasum"); e == nil {
					_ = os.Symlink(real, filepath.Join(f.bin, "shasum"))
				}
			}
			f.env = append(f.env, "PATH="+f.bin)
			out, e := f.run()
			if e == nil || !strings.Contains(out, "missing:") {
				t.Fatalf("%s %v", out, e)
			}
		})
	}
}
func TestInvalidManifest(t *testing.T) {
	for _, manifest := range []string{"", strings.Repeat("a", 64) + "  other-file\n", "bad  codex-thread-bridge-linux-amd64\n", strings.Repeat(strings.Repeat("0", 64)+"  codex-thread-bridge-linux-amd64\n", 2)} {
		t.Run(manifest[:min(8, len(manifest))], func(t *testing.T) {
			f := setup(t)
			f.existing(t)
			write(t, filepath.Join(f.dir, "SHA256SUMS"), manifest)
			if out, e := f.run(); e == nil {
				t.Fatal(out)
			}
			data, _ := os.ReadFile(f.target())
			if string(data) != "old executable\n" {
				t.Fatal("changed binary")
			}
			f.clean(t)
		})
	}
}
