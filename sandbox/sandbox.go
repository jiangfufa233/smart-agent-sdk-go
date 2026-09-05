// Package sandbox runs child processes under an operating-system sandbox.
//
// A Sandbox confines every command it starts: filesystem access is
// restricted to an allowlist of directories, outbound network access can
// be denied, resource limits can be applied, and the whole process tree is
// killed on timeout, context cancellation or Close. What is actually
// enforced depends on the platform and is reported by Capabilities so
// callers never have to guess:
//
//   - Linux: Landlock rulesets (filesystem allowlist, network deny on
//     ABI v4) applied on a dedicated spawn thread, process-group kill,
//     rlimits via the prlimit utility when present.
//   - Windows: a Job Object with kill-on-close (process-tree kill and an
//     optional memory limit). Filesystem and network are not restricted.
//   - Other platforms (macOS, BSD): process-group containment only.
//
// By default construction is fail-closed: if the platform cannot provide
// meaningful enforcement, New returns an error. Set Config.Lax to accept
// degraded containment explicitly. Capabilities, Describe and the audit
// log (via the tool layer) let applications record what level of isolation
// actually applied to each run.
//
// Typical use with the built-in shell tool:
//
//	sb, err := sandbox.Auto("/workspace")
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer sb.Close()
//	res, err := sb.Run(ctx, "ls", "-l")
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxStreamBytes caps how much of a command's stdout or stderr is captured
// in a Result. Further output is discarded.
const maxStreamBytes = 64 << 10

// ErrTimeout is returned by Run when the command exceeded Config.Timeout.
// The process tree has already been killed when it is returned.
var ErrTimeout = errors.New("sandbox: command timed out")

// ResourceLimits caps the resources a command may consume. Zero fields are
// left unset. Limits are enforced through the prlimit utility on Linux and
// reported by Capabilities.Limits; on other platforms they are ignored.
type ResourceLimits struct {
	// CPUSecs caps accumulated CPU time (RLIMIT_CPU).
	CPUSecs int64
	// MemoryMB caps the address space (RLIMIT_AS).
	MemoryMB int64
	// FileSizeMB caps the size of files the command may write (RLIMIT_FSIZE).
	FileSizeMB int64
	// ProcCount caps the number of processes (RLIMIT_NPROC).
	ProcCount int64
}

// Config configures a Sandbox.
type Config struct {
	// Dir is the working directory of every command and (on enforcing
	// platforms) the root that receives write access. It must exist.
	Dir string
	// RWPaths are additional directories granted write access alongside
	// Dir. Ignored on platforms without filesystem enforcement.
	RWPaths []string
	// ROPaths are additional directories granted read and execute access.
	// Ignored on platforms without filesystem enforcement.
	ROPaths []string
	// NoNetwork denies outbound networking (TCP connect and bind) on
	// platforms that support it.
	NoNetwork bool
	// Env is the environment passed to commands. nil sanitizes the parent
	// environment: only PATH, locale and terminal variables are kept, and
	// HOME/TMPDIR are redirected to private directories under Dir. A
	// non-nil value is passed through unchanged.
	Env []string
	// Timeout kills the command's process tree if a Run exceeds it.
	// Zero means 30 seconds.
	Timeout time.Duration
	// Limits applies resource limits where supported.
	Limits ResourceLimits
	// Lax accepts degraded containment instead of failing construction
	// when the platform cannot enforce the requested restrictions. The
	// default is fail-closed.
	Lax bool
}

// Capabilities reports which restrictions a Sandbox actually enforces on
// the current platform. Each field means "this restriction is enforced",
// not merely "it was requested": Network is true when outbound networking
// is denied, Filesystem when the path allowlist is live, Limits when
// resource limits are applied, ProcessTree when the whole tree is killed
// rather than only the direct child.
type Capabilities struct {
	Filesystem  bool
	Network     bool
	Limits      bool
	ProcessTree bool
}

// Result carries the captured output of a command. Stdout and Stderr are
// truncated at 64 KiB each.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExitError reports a command that ran to completion but exited with a
// non-zero status.
type ExitError struct {
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("sandbox: command exited with status %d", e.ExitCode)
}

// Sandbox runs commands under the restrictions of its Config. It is safe
// for concurrent use. A Sandbox holds one dedicated OS thread on Linux and
// should be treated as a long-lived object; see Close.
type Sandbox struct {
	cfg  Config
	caps Capabilities
	plat platform

	mu     sync.Mutex
	active map[*exec.Cmd]struct{}
	closed bool
}

// Auto returns a Sandbox with safe defaults for workspace: Dir receives
// write access, a platform-appropriate set of system directories stays
// readable and executable, outbound networking is denied and the default
// sanitized environment applies. The workspace is created if missing.
func Auto(workspace string) (*Sandbox, error) {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox: create workspace: %w", err)
	}
	return New(Config{
		Dir:       abs,
		ROPaths:   defaultROPaths(),
		NoNetwork: true,
	})
}

// New returns a Sandbox configured by cfg. Construction validates that the
// platform can enforce the request: unless cfg.Lax is set, it fails when
// the required restrictions are unavailable (for example an old Linux
// kernel without Landlock, or any non-enforcing platform).
func New(cfg Config) (*Sandbox, error) {
	if cfg.Dir == "" {
		return nil, errors.New("sandbox: Dir is required")
	}
	abs, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("sandbox: Dir %q is not a directory", abs)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	s := &Sandbox{cfg: cfg, active: make(map[*exec.Cmd]struct{})}
	if err := s.initPlatform(); err != nil {
		return nil, err
	}
	if !cfg.Lax {
		if err := strictError(s.cfg, s.caps); err != nil {
			s.closePlatform()
			return nil, err
		}
	}
	if err := s.startPlatform(); err != nil {
		s.closePlatform()
		return nil, err
	}
	if cfg.Env == nil {
		for _, d := range []string{".home", ".tmp"} {
			_ = os.MkdirAll(filepath.Join(abs, d), 0o700)
		}
	}
	return s, nil
}

// Capabilities reports the restrictions this Sandbox enforces.
func (s *Sandbox) Capabilities() Capabilities { return s.caps }

// Describe returns a human-readable summary of the active containment,
// suitable for audit logs.
func (s *Sandbox) Describe() string { return s.describe() }

// Run executes name with args inside the sandbox and waits for it to
// finish. It returns the captured output and, for a non-zero exit, an
// *ExitError; when the timeout fires, ErrTimeout. A canceled ctx kills the
// process tree and surfaces ctx's own error.
func (s *Sandbox) Run(ctx context.Context, name string, args ...string) (Result, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Result{}, errors.New("sandbox: closed")
	}
	s.mu.Unlock()

	name, args = s.wrapCommand(name, args)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = s.cfg.Dir
	cmd.Env = s.environ()
	cmd.SysProcAttr = s.procAttr()
	// Open the null device on the calling (unrestricted) thread: with a
	// nil Stdin, exec would open /dev/null inside Start — which runs on
	// the Landlock-restricted thread and would fail.
	null, err := os.Open(os.DevNull)
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: open stdin: %w", err)
	}
	cmd.Stdin = null
	stdout := &limitedBuffer{max: maxStreamBytes}
	stderr := &limitedBuffer{max: maxStreamBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	var fired atomic.Bool
	cmd.Cancel = func() error {
		fired.Store(true)
		s.kill(cmd)
		return nil
	}
	cmd.WaitDelay = 3 * time.Second

	// cmd.Process is written by Start on the spawn thread; only track the
	// command (and arm the timeout) after spawn returns so kill/Close never
	// race with Start.
	if err := s.spawn(cmd); err != nil {
		_ = null.Close()
		return Result{}, fmt.Errorf("sandbox: start %s: %w", name, err)
	}
	_ = null.Close()

	s.mu.Lock()
	s.active[cmd] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.active, cmd)
		s.mu.Unlock()
	}()

	timer := time.AfterFunc(s.cfg.Timeout, func() {
		fired.Store(true)
		s.kill(cmd)
	})
	defer timer.Stop()

	waitErr := cmd.Wait()
	s.release(cmd)

	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if fired.Load() {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		return res, fmt.Errorf("%w (after %s, process tree killed)", ErrTimeout, s.cfg.Timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, &ExitError{ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	if waitErr != nil {
		return res, fmt.Errorf("sandbox: wait: %w", waitErr)
	}
	return res, nil
}

// Close kills every command still running under s. Commands started
// afterwards fail. On Linux the dedicated spawn thread stays parked (one
// idle goroutine), so a Sandbox cannot be fully reclaimed: create one per
// agent or tool, not per call.
func (s *Sandbox) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	actives := make([]*exec.Cmd, 0, len(s.active))
	for c := range s.active {
		actives = append(actives, c)
	}
	s.mu.Unlock()
	for _, c := range actives {
		s.kill(c)
	}
	s.closePlatform()
	return nil
}

// environ returns the environment for commands.
func (s *Sandbox) environ() []string {
	if s.cfg.Env != nil {
		return s.cfg.Env
	}
	return sanitizeEnv(os.Environ(), s.cfg.Dir)
}

// sanitizeEnv reduces environ to a safe subset for sandboxed commands and
// redirects home and temp directories into dir. It is a pure function for
// testability.
func sanitizeEnv(environ []string, dir string) []string {
	keep := []string{"PATH=", "LANG=", "LC_", "TERM="}
	home, tmp := "HOME=", "TMPDIR="
	if runtime.GOOS == "windows" {
		keep = append(keep, "SystemRoot=", "SystemDrive=", "ComSpec=", "PATHEXT=")
		home, tmp = "USERPROFILE=", "TEMP="
	}
	var out []string
	for _, kv := range environ {
		for _, p := range keep {
			if strings.HasPrefix(kv, p) {
				out = append(out, kv)
				break
			}
		}
	}
	homeVal := filepath.Join(dir, ".home")
	tmpVal := filepath.Join(dir, ".tmp")
	if runtime.GOOS == "windows" {
		out = append(out, home+"="+homeVal, "TEMP="+tmpVal, "TMP="+tmpVal)
	} else {
		out = append(out, home+homeVal, tmp+tmpVal)
	}
	return out
}

// limitedBuffer is an io.Writer that keeps at most max bytes and silently
// discards the rest, so a chatty command cannot exhaust memory.
type limitedBuffer struct {
	max int
	buf []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	total := len(p)
	if room := b.max - len(b.buf); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf = append(b.buf, p...)
	}
	return total, nil
}

func (b *limitedBuffer) String() string { return string(b.buf) }
