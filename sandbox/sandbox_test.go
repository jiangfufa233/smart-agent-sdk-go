package sandbox

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSanitizeEnvKeepsSafeVarsRedirectsHome(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C",
		"TERM=xterm",
		"SECRET_TOKEN=hunter2",
		"HOME=/root",
		"TMPDIR=/var/tmp",
		"JAVA_HOME=/opt/java",
	}
	got := sanitizeEnv(environ, "/workspace")
	joined := strings.Join(got, "\n")
	for _, want := range []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C", "TERM=xterm"} {
		if !strings.Contains(joined, want) {
			t.Errorf("sanitizeEnv lost %q: %v", want, got)
		}
	}
	if strings.Contains(joined, "SECRET_TOKEN") {
		t.Errorf("sanitizeEnv leaked SECRET_TOKEN: %v", got)
	}
	if !reflect.DeepEqual(got[len(got)-2:], []string{"HOME=/workspace/.home", "TMPDIR=/workspace/.tmp"}) {
		if !strings.Contains(joined, "HOME=/workspace/.home") || !strings.Contains(joined, "TMPDIR=/workspace/.tmp") {
			t.Errorf("sanitizeEnv did not redirect HOME/TMPDIR: %v", got)
		}
	}
}

func TestNewRequiresDir(t *testing.T) {
	if _, err := New(Config{}); err == nil || !strings.Contains(err.Error(), "Dir is required") {
		t.Errorf("New without Dir: got %v, want Dir error", err)
	}
	if _, err := New(Config{Dir: t.TempDir() + "/missing"}); err == nil {
		t.Error("New with missing Dir: expected error")
	}
}

func TestNewDefaultsTimeout(t *testing.T) {
	s, err := New(Config{Dir: t.TempDir(), Lax: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.cfg.Timeout != 30*time.Second {
		t.Errorf("default timeout = %s, want 30s", s.cfg.Timeout)
	}
}

func TestAutoCreatesWorkspace(t *testing.T) {
	dir := t.TempDir() + "/nested/workspace"
	s, err := Auto(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.cfg.Dir != dir {
		t.Errorf("Auto Dir = %q, want %q", s.cfg.Dir, dir)
	}
}

func TestLimitedBufferCaps(t *testing.T) {
	b := &limitedBuffer{max: 8}
	n, err := b.Write([]byte("1234567890"))
	if n != 10 || err != nil {
		t.Fatalf("Write = %d, %v; want 10, nil", n, err)
	}
	if got := b.String(); got != "12345678" {
		t.Errorf("buffer = %q, want truncated %q", got, "12345678")
	}
}

func TestRunSmoke(t *testing.T) {
	s, err := Auto(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	res, err := s.Run(t.Context(), "sh", "-c", "echo hello")
	if err != nil {
		t.Fatalf("Run: %v (stderr: %s)", err, res.Stderr)
	}
	if got := strings.TrimSpace(res.Stdout); got != "hello" {
		t.Errorf("stdout = %q, want hello", got)
	}
}
