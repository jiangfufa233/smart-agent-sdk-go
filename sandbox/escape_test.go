package sandbox

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Each Sandbox keeps one dedicated, permanently parked spawn thread
	// (see parkForever); it is a deliberate part of the design.
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jiangfufa233/smart-agent-sdk-go/sandbox.parkForever"))
}

// Helper-process support: the tests below re-execute the test binary
// inside the sandbox with a mode argument; the helper performs the
// sensitive operation and reports the outcome via its exit code.
//
//go:noinline
func TestEscapeHelper(t *testing.T) {
	mode := ""
	for _, a := range os.Args[1:] {
		switch a {
		case "read-etc", "symlink", "dial", "write", "env":
			mode = a
		}
	}
	switch mode {
	case "read-etc":
		if _, err := os.ReadFile("/etc/passwd"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitDenied)
		}
		os.Exit(exitEscaped)
	case "symlink":
		if _, err := os.ReadFile("evil"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitDenied)
		}
		os.Exit(exitEscaped)
	case "dial":
		conn, err := net.Dial("tcp", "127.0.0.1:1")
		if err == nil {
			_ = conn.Close()
			os.Exit(exitEscaped)
		}
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, os.ErrPermission) {
			os.Exit(exitDenied)
		}
		// ECONNREFUSED etc. means the dial reached the loopback stack:
		// the network is not confined.
		os.Exit(exitOtherErr)
	case "write":
		if err := os.WriteFile("out.txt", []byte("written"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitDenied)
		}
		os.Exit(exitEscaped)
	case "env":
		for _, kv := range os.Environ() {
			fmt.Println(kv)
		}
		os.Exit(exitEscaped)
	default:
		t.Skip("helper process, not meant to run directly")
	}
}

const (
	exitEscaped  = 0 // the sandboxed operation succeeded
	exitDenied   = 7 // blocked (expected under confinement)
	exitOtherErr = 9 // failed for another reason
)

func runHelper(t *testing.T, s *Sandbox, mode string) Result {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Run(t.Context(), exe, "-test.run=^TestEscapeHelper$", mode)
	if err != nil {
		var ee *ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("helper %s: %v (stderr: %s)", mode, err, res.Stderr)
		}
	} else {
		res.ExitCode = exitEscaped
	}
	return res
}

// helperSandbox builds a sandbox able to exec the test binary itself: its
// directory must be in the read-only path whitelist.
func helperSandbox(t *testing.T) *Sandbox {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		Dir:       t.TempDir(),
		ROPaths:   append(defaultROPaths(), filepath.Dir(exe)),
		NoNetwork: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestWriteInsideWorkspaceAllowed(t *testing.T) {
	s := helperSandbox(t)
	ws := s.cfg.Dir
	res := runHelper(t, s, "write")
	if res.ExitCode != exitEscaped {
		t.Fatalf("write inside workspace denied: code=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	data, err := os.ReadFile(filepath.Join(ws, "out.txt"))
	if err != nil || string(data) != "written" {
		t.Errorf("out.txt = %q, %v; want %q", data, err, "written")
	}
}

func TestReadEtcDenied(t *testing.T) {
	s := helperSandbox(t)
	if !s.Capabilities().Filesystem {
		t.Skip("Landlock not available on this kernel (5.13+ required)")
	}
	res := runHelper(t, s, "read-etc")
	if res.ExitCode != exitDenied {
		t.Fatalf("reading /etc/passwd escaped the sandbox: code=%d", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "permission denied") {
		t.Errorf("stderr = %q, want permission denied", res.Stderr)
	}
}

func TestSymlinkEscapeDenied(t *testing.T) {
	s := helperSandbox(t)
	if !s.Capabilities().Filesystem {
		t.Skip("Landlock not available on this kernel (5.13+ required)")
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(s.cfg.Dir, "evil")); err != nil {
		t.Fatal(err)
	}
	res := runHelper(t, s, "symlink")
	if res.ExitCode != exitDenied {
		t.Fatalf("symlink escape succeeded: code=%d", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "permission denied") {
		t.Errorf("stderr = %q, want permission denied", res.Stderr)
	}
}

func TestDialDenied(t *testing.T) {
	s := helperSandbox(t)
	if !s.Capabilities().Network {
		t.Skip("network confinement needs Landlock ABI v4 (kernel 6.7+)")
	}
	res := runHelper(t, s, "dial")
	if res.ExitCode != exitDenied {
		t.Fatalf("dial escaped the sandbox: code=%d stderr=%s", res.ExitCode, res.Stderr)
	}
}

func TestEnvSanitizedAtRuntime(t *testing.T) {
	s := helperSandbox(t)
	t.Setenv("SANDBOX_SECRET", "hunter2")
	res := runHelper(t, s, "env")
	for _, kv := range strings.Split(res.Stdout, "\n") {
		switch {
		case strings.HasPrefix(kv, "SANDBOX_SECRET="):
			t.Error("secret leaked into sandbox environment")
		case strings.HasPrefix(kv, "HOME="):
			if !strings.HasSuffix(kv, "/.home") {
				t.Errorf("HOME = %q, want redirected under workspace", kv)
			}
		case strings.HasPrefix(kv, "TMPDIR="):
			if !strings.HasSuffix(kv, "/.tmp") {
				t.Errorf("TMPDIR = %q, want redirected under workspace", kv)
			}
		}
	}
	if !strings.Contains(res.Stdout, "PATH=") {
		t.Error("sanitized env lost PATH")
	}
}

func TestTimeoutKillsTreeNoOrphans(t *testing.T) {
	before := procCmdlines(t)
	ws := t.TempDir()
	s, err := New(Config{
		Dir:       ws,
		Timeout:   300 * time.Millisecond,
		NoNetwork: true,
		ROPaths:   []string{"/usr", "/bin", "/lib", "/lib64"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	const sleeper = "sleep 297"
	start := time.Now()
	_, err = s.Run(t.Context(), "sh", "-c", sleeper+" & "+sleeper)
	if err == nil {
		t.Fatal("sleep unexpectedly completed")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("timeout took %v", d)
	}
	time.Sleep(200 * time.Millisecond) // grace period for reaping
	after := procCmdlines(t)
	for pid, cmd := range after {
		if strings.Contains(cmd, sleeper) && !containsValue(before, cmd) {
			t.Errorf("orphan process survived: pid=%d cmd=%q", pid, cmd)
		}
	}
}

func procCmdlines(t *testing.T) map[int]string {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("cannot scan /proc: %v", err)
	}
	out := make(map[int]string)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		out[pid] = strings.ReplaceAll(string(data), "\x00", " ")
	}
	return out
}

func containsValue(m map[int]string, v string) bool {
	for _, x := range m {
		if x == v {
			return true
		}
	}
	return false
}

func TestDescribeMentionsLandlock(t *testing.T) {
	s := helperSandbox(t)
	if !s.Capabilities().Filesystem {
		t.Skip("Landlock not available on this kernel")
	}
	d := s.Describe()
	if !strings.Contains(d, "landlock abi=") {
		t.Errorf("Describe() = %q", d)
	}
	if s.Capabilities().Network && !strings.Contains(d, "net=deny") {
		t.Errorf("Describe() = %q, want net=deny", d)
	}
}
