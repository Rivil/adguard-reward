package main

// Tests over the cross-build recipe. nora is linux/amd64 and the binary must
// land there with no libc to depend on and its version stamped in, so these
// check both the recipe text (always) and a real artefact (opt-in).

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is where make must run: the tests live two levels down.
const repoRoot = "../.."

// buildTestEnv opts TestBuildLinux in. `make test-go` exports it; a bare
// `go test ./...` does not, because the test shells out to make and compiles
// a whole second binary.
const buildTestEnv = "ADGUARD_REWARD_BUILD_TEST"

// versionSentinel is stamped through -X main.version and then looked for in
// the artefact's bytes. It is not a plausible git describe output, so finding
// it can only mean the linker flag arrived.
const versionSentinel = "test-0.0.0-gdeadbee"

// makeN returns `make -n <target>`: the recipe as make would run it, with
// every variable expanded and nothing executed.
func makeN(t *testing.T, target string) string {
	t.Helper()
	cmd := exec.Command("make", "-n", target)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %s: %v\n%s", target, err, out)
	}
	return string(out)
}

// goBuildLine is the recipe's `go build` line. Asserting on it rather than on
// the whole output keeps a flag in some other target from satisfying the test.
func goBuildLine(t *testing.T, recipe, target string) string {
	t.Helper()
	for _, line := range strings.Split(recipe, "\n") {
		if strings.Contains(line, "go build") {
			return line
		}
	}
	t.Fatalf("make -n %s has no `go build` line:\n%s", target, recipe)
	return ""
}

func TestMakeBuildLinuxRecipe(t *testing.T) {
	recipe := makeN(t, "build-linux")
	build := goBuildLine(t, recipe, "build-linux")

	// Cross-compilation and no cgo: nora gets a binary with no libc to match.
	for _, want := range []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "-X main.version="} {
		if !strings.Contains(build, want) {
			t.Errorf("build-linux go build line lacks %q:\n%s", want, build)
		}
	}

	// build-web must run first, or the shipped binary embeds whatever dist
	// happened to be lying around.
	pnpm := strings.Index(recipe, "pnpm build")
	if pnpm < 0 {
		t.Fatalf("build-linux does not build the frontend:\n%s", recipe)
	}
	if pnpm > strings.Index(recipe, build) {
		t.Errorf("build-linux compiles before `pnpm build`:\n%s", recipe)
	}

	// The bin-only target is what the build test drives, precisely because
	// pnpm empties web/dist and other packages embed it concurrently.
	if bin := makeN(t, "build-linux-bin"); strings.Contains(bin, "pnpm") {
		t.Errorf("build-linux-bin rebuilds the frontend; the build test cannot use it safely:\n%s", bin)
	}
}

func TestMakefileVersion(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VERSION") && strings.Contains(line, "?=") {
			if !strings.Contains(line, "git describe --tags") {
				t.Errorf("VERSION default is not derived from git: %s", line)
			}
			return
		}
	}
	t.Error("Makefile has no `VERSION ?=` default line")
}

// TestBuildLinux compiles the real artefact and inspects it without ever
// executing it — this machine is darwin, and an ELF that runs here would mean
// the cross-build did not happen.
func TestBuildLinux(t *testing.T) {
	if os.Getenv(buildTestEnv) != "1" {
		t.Skipf("cross-build not checked: set %s=1 (as `make test-go` does) to compile and inspect the linux/amd64 binary", buildTestEnv)
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("cross-build not checked: make is not on PATH (%v)", err)
	}

	out := filepath.Join(t.TempDir(), "adguard-reward-linux-amd64")
	cmd := exec.Command("make", "build-linux-bin", "VERSION="+versionSentinel, "LINUX_BIN="+out)
	cmd.Dir = repoRoot
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make build-linux-bin: %v\n%s", err, b)
	}

	f, err := elf.Open(out)
	if err != nil {
		t.Fatalf("built artefact is not an ELF file: %v", err)
	}
	defer f.Close()

	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_X86_64 {
		t.Errorf("built %v/%v, want ELFCLASS64/EM_X86_64", f.Class, f.Machine)
	}

	// Static: no interpreter to load and nothing to link against. A dynamic
	// binary would fail on a host whose libc differs from the builder's.
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			t.Error("binary has a PT_INTERP header; it is dynamically linked")
		}
	}
	needed, err := f.DynString(elf.DT_NEEDED)
	if err != nil {
		t.Errorf("DynString(DT_NEEDED): %v", err)
	} else if len(needed) != 0 {
		t.Errorf("binary needs shared libraries %v, want none", needed)
	}

	info, err := buildinfo.ReadFile(out)
	if err != nil {
		t.Fatalf("buildinfo.ReadFile: %v", err)
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	for k, want := range map[string]string{"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"} {
		if got := settings[k]; got != want {
			t.Errorf("buildinfo %s = %q, want %q", k, got, want)
		}
	}

	// The stamp is NOT in buildinfo: under -trimpath the toolchain omits the
	// -ldflags setting, so asserting on it would fail on a correct build. The
	// linked-in string is the evidence, and it survives -s -w.
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(versionSentinel)) {
		t.Errorf("binary does not contain %q; -X main.version did not reach the linker", versionSentinel)
	}
}
