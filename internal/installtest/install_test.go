package installtest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fixture struct {
	dir, home, bin, asset, log, installer string
	env                                   []string
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(text), 0755); e != nil {
		t.Fatal(e)
	}
}
func setup(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	f := &fixture{dir: dir, home: filepath.Join(dir, "home with spaces"), bin: filepath.Join(dir, "commands"), asset: filepath.Join(dir, "asset"), log: filepath.Join(dir, "calls")}
	f.installer, _ = filepath.Abs("../../install.sh")
	for _, d := range []string{f.home, f.bin, filepath.Join(dir, "downloads")} {
		if e := os.MkdirAll(d, 0755); e != nil {
			t.Fatal(e)
		}
	}
	write(t, f.asset, "#!/bin/sh\nprintf '%s\\n' \"${MOCK_BINARY_VERSION:-0.3.0}\"\n")
	bytes, _ := os.ReadFile(f.asset)
	hash := fmt.Sprintf("%x", sha256.Sum256(bytes))
	write(t, filepath.Join(dir, "SHA256SUMS"), hash+"  codex-thread-bridge-linux-amd64\n")
	write(t, filepath.Join(f.bin, "uname"), "#!/bin/sh\nif [ \"$1\" = -s ]; then echo \"${MOCK_OS:-Linux}\"; else echo \"${MOCK_ARCH:-x86_64}\"; fi\n")
	write(t, filepath.Join(f.bin, "curl"), `#!/bin/bash
printf '%s\n' "$*" >> "$MOCK_CURL_LOG"
out=''
url=''
while (($#)); do
 case "$1" in
 --output) out=$2; shift 2 ;;
 --write-out|--proto|--proto-redir) shift 2 ;;
 --*) shift ;;
 *) url=$1; shift ;;
 esac
done
[[ ${MOCK_DOWNLOAD_FAIL:-} != all ]] || exit 22
case "$url" in
 */releases/latest) printf '%s/releases/tag/%s' 'https://github.com/JesseSenior/codex-thread-bridge' "${MOCK_TAG:-v0.3.0}" ;;
 */SHA256SUMS) [[ ${MOCK_DOWNLOAD_FAIL:-} != checksum ]] || exit 22; cp "$MOCK_MANIFEST" "$out" ;;
 */codex-thread-bridge-linux-amd64) cp "$MOCK_ASSET" "$out" ;;
 *) exit 22 ;;
esac
`)
	write(t, filepath.Join(f.bin, "codex"), `#!/bin/sh
printf '%s\n' "$CODEX_HOME" > "$MOCK_CODEX_HOME_LOG"
printf '%s\n' "$@" > "$MOCK_CODEX_LOG"
exit "${MOCK_REGISTER_EXIT:-0}"
`)
	if runtime.GOOS == "darwin" {
		write(t, filepath.Join(f.bin, "mv"), "#!/bin/sh\nif [ \"$1\" = -fT ]; then shift; fi\nexec /bin/mv -f \"$@\"\n")
	}
	f.env = []string{"PATH=" + f.bin + ":" + os.Getenv("PATH"), "HOME=" + f.home, "TMPDIR=" + filepath.Join(dir, "downloads"), "CODEX_HOME=" + filepath.Join(dir, "custom codex"), "MOCK_ASSET=" + f.asset, "MOCK_MANIFEST=" + filepath.Join(dir, "SHA256SUMS"), "MOCK_CURL_LOG=" + f.log, "MOCK_CODEX_LOG=" + filepath.Join(dir, "registration"), "MOCK_CODEX_HOME_LOG=" + filepath.Join(dir, "codex-home")}
	return f
}
func (f *fixture) run(args ...string) (string, error) {
	cmd := exec.Command("bash", append([]string{f.installer}, args...)...)
	cmd.Env = append(os.Environ(), f.env...)
	out, e := cmd.CombinedOutput()
	return string(out), e
}
func (f *fixture) target() string {
	return filepath.Join(f.home, ".local", "bin", "codex-thread-bridge")
}
func (f *fixture) existing(t *testing.T) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(f.target()), 0755); e != nil {
		t.Fatal(e)
	}
	write(t, f.target(), "old executable\n")
}
func (f *fixture) clean(t *testing.T) {
	t.Helper()
	entries, e := os.ReadDir(filepath.Join(f.dir, "downloads"))
	if e != nil || len(entries) != 0 {
		t.Fatalf("temporary files remain: %v %v", entries, e)
	}
	entries, _ = os.ReadDir(filepath.Dir(f.target()))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".codex-thread-bridge.") {
			t.Fatal("staging file remains")
		}
	}
}
func TestSuccessRepeatAndSpaces(t *testing.T) {
	f := setup(t)
	for range 2 {
		out, e := f.run("--socket", "/missing/socket with spaces")
		if e != nil {
			t.Fatalf("%s %v", out, e)
		}
	}
	data, e := os.ReadFile(f.target())
	asset, _ := os.ReadFile(f.asset)
	if e != nil || string(data) != string(asset) {
		t.Fatal("incorrect binary")
	}
	reg, _ := os.ReadFile(filepath.Join(f.dir, "registration"))
	want := "mcp\nadd\ncodex-thread-bridge\n--\n" + f.target() + "\n--socket\n/missing/socket with spaces\n"
	if string(reg) != want {
		t.Fatalf("%q != %q", reg, want)
	}
	home, _ := os.ReadFile(filepath.Join(f.dir, "codex-home"))
	if strings.TrimSpace(string(home)) != filepath.Join(f.dir, "custom codex") {
		t.Fatal("CODEX_HOME lost")
	}
	calls, _ := os.ReadFile(f.log)
	if strings.Count(string(calls), "/releases/latest") != 2 || strings.Count(string(calls), "/download/v0.3.0/") != 4 {
		t.Fatal(string(calls))
	}
	f.clean(t)
}
func TestVersionSelection(t *testing.T) {
	f := setup(t)
	f.env = append(f.env, "MOCK_BINARY_VERSION=0.2.1")
	if out, e := f.run("--version", "v0.2.1"); e != nil {
		t.Fatalf("%s %v", out, e)
	}
	calls, _ := os.ReadFile(f.log)
	if strings.Contains(string(calls), "/latest") || strings.Count(string(calls), "/download/v0.2.1/") != 2 {
		t.Fatal(string(calls))
	}
	reg, _ := os.ReadFile(filepath.Join(f.dir, "registration"))
	if strings.Contains(string(reg), "--socket") {
		t.Fatal("socket override added")
	}
	f.clean(t)
}
func TestFailuresPreserveInstalledBinary(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		manifest  bool
	}{{"download", "MOCK_DOWNLOAD_FAIL=all", false}, {"checksum download", "MOCK_DOWNLOAD_FAIL=checksum", false}, {"checksum", "", true}, {"version", "MOCK_BINARY_VERSION=9.9.9", false}, {"OS", "MOCK_OS=Darwin", false}, {"architecture", "MOCK_ARCH=aarch64", false}, {"unstable latest", "MOCK_TAG=v0.3.0-rc1", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.existing(t)
			if tc.env != "" {
				f.env = append(f.env, tc.env)
			}
			if tc.manifest {
				write(t, filepath.Join(f.dir, "SHA256SUMS"), strings.Repeat("0", 64)+"  codex-thread-bridge-linux-amd64\n")
			}
			if out, e := f.run(); e == nil {
				t.Fatal(out)
			}
			data, _ := os.ReadFile(f.target())
			if string(data) != "old executable\n" {
				t.Fatal("replaced installed executable")
			}
			if _, e := os.Stat(filepath.Join(f.dir, "registration")); !os.IsNotExist(e) {
				t.Fatal("registered after failure")
			}
			f.clean(t)
		})
	}
}
func TestRegistrationFailure(t *testing.T) {
	f := setup(t)
	f.env = append(f.env, "MOCK_REGISTER_EXIT=1")
	out, e := f.run("--socket", "/socket with spaces")
	if e == nil || !strings.Contains(out, "registration failed") || !strings.Contains(out, "codex mcp add") {
		t.Fatalf("%s %v", out, e)
	}
	if _, e = os.Stat(f.target()); e != nil {
		t.Fatal(e)
	}
	f.clean(t)
}
func TestHelpAndBadArguments(t *testing.T) {
	f := setup(t)
	f.env = append(f.env, "MOCK_OS=Darwin")
	if out, e := f.run("--help"); e != nil || !strings.Contains(out, "Usage:") {
		t.Fatalf("%s %v", out, e)
	}
	for _, args := range [][]string{{"--version"}, {"--socket"}, {"--unknown"}, {"--version", "v01.2.3"}, {"--version", "v1.2.3-rc1"}, {"--version", "1.2.3"}} {
		if out, e := f.run(args...); e == nil {
			t.Fatal(out)
		}
	}
}
func TestMissingCodex(t *testing.T) {
	f := setup(t)
	if e := os.Remove(filepath.Join(f.bin, "codex")); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"uname", "curl", "sha256sum", "shasum"} {
		if _, e := os.Stat(filepath.Join(f.bin, name)); e == nil {
			continue
		}
		if real, e := exec.LookPath(name); e == nil {
			if e = os.Symlink(real, filepath.Join(f.bin, name)); e != nil {
				t.Fatal(e)
			}
		}
	}
	f.env = append(f.env, "PATH="+f.bin)
	out, e := f.run()
	if e == nil || !strings.Contains(out, "missing: codex") {
		t.Fatalf("%s %v", out, e)
	}
}
func TestDirectoryTargetRejected(t *testing.T) {
	f := setup(t)
	if e := os.MkdirAll(f.target(), 0755); e != nil {
		t.Fatal(e)
	}
	out, e := f.run()
	if e == nil || !strings.Contains(out, "is a directory") {
		t.Fatalf("%s %v", out, e)
	}
	f.clean(t)
}
