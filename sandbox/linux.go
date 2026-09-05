//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// platform holds the Linux-specific sandbox state.
type platform struct {
	// rulesetFD is the open Landlock ruleset between initPlatform and
	// startPlatform; -1 when Landlock is unavailable.
	rulesetFD int
	abi       int
	startCh   chan startReq
	prlimit   string
	skipped   []string // paths whose rules could not be added
}

type startReq struct {
	cmd   *exec.Cmd
	errCh chan error
}

// defaultROPaths lists directories a command needs to start and link at
// all (shell, dynamic loader, locale and terminfo data live here).
func defaultROPaths() []string {
	return []string{"/usr", "/bin", "/lib", "/lib64", "/etc/ld.so.cache"}
}

// fsROBits grants read and execute access for read-only directories.
const fsROBits = unix.LANDLOCK_ACCESS_FS_EXECUTE |
	unix.LANDLOCK_ACCESS_FS_READ_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_DIR

// landlockRulesetAttr and landlockPathBeneathAttr mirror the kernel ABI
// layouts exactly (x/sys's generated structs do not: their parent_fd is
// int32 instead of the kernel's __s64). The ruleset struct is passed with
// an explicit size: 8 bytes when only filesystem rights are handled, 16
// when network rights are added, so older kernels that know a shorter
// struct do not reject it with E2BIG.
type landlockRulesetAttr struct {
	HandledAccessFS  uint64
	HandledAccessNet uint64
}

type landlockPathBeneathAttr struct {
	AllowedAccess uint64
	ParentFD      int64
}

// landlockABI reports the kernel's Landlock ABI version, or -1 when
// Landlock is unavailable.
func landlockABI() int {
	v, _, errno := unix.RawSyscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0,
		unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return -1
	}
	return int(v)
}

// fsHandledBits returns every filesystem access right this kernel ABI
// understands; rights above the ABI stay unhandled (allowed) by design.
// IOCTL_DEV (ABI 5) is intentionally left out: device ioctls stay allowed.
func fsHandledBits(abi int) uint64 {
	bits := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		bits |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		bits |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return bits
}

func limitsRequested(l ResourceLimits) bool {
	return l.CPUSecs > 0 || l.MemoryMB > 0 || l.FileSizeMB > 0 || l.ProcCount > 0
}

// initPlatform probes Landlock, builds the ruleset and detects prlimit.
// It does not restrict anything yet: the ruleset is applied on the
// dedicated spawn thread in startPlatform, so that commands inherit the
// restriction while the rest of the host process stays unaffected.
func (s *Sandbox) initPlatform() error {
	s.caps = Capabilities{ProcessTree: true}
	s.plat.rulesetFD = -1
	s.plat.prlimit, _ = exec.LookPath("prlimit")
	s.caps.Limits = s.plat.prlimit != "" && limitsRequested(s.cfg.Limits)

	abi := landlockABI()
	if abi < 1 {
		return nil
	}
	fd, err := s.buildRuleset(abi)
	if err != nil {
		return nil // Landlock present but unusable; caps stay false.
	}
	s.plat.rulesetFD = fd
	s.plat.abi = abi
	s.caps.Filesystem = true
	s.caps.Network = s.cfg.NoNetwork && abi >= 4
	return nil
}

// fileScopedBits are the Landlock rights a non-directory fd may grant:
// PATH_BENEATH rules on files reject directory-only rights such as
// READ_DIR, so file paths in the whitelists are masked to these.
const fileScopedBits = unix.LANDLOCK_ACCESS_FS_EXECUTE |
	unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_FILE |
	unix.LANDLOCK_ACCESS_FS_TRUNCATE

// buildRuleset creates the ruleset with every handled right and grants:
// read+execute on ROPaths, full filesystem access on Dir and RWPaths. No
// network rule is ever added, so handled network rights are denied.
func (s *Sandbox) buildRuleset(abi int) (int, error) {
	fsBits := fsHandledBits(abi)
	var attr landlockRulesetAttr
	attr.HandledAccessFS = fsBits
	size := unsafe.Sizeof(attr.HandledAccessFS)
	net := s.cfg.NoNetwork && abi >= 4
	if net {
		attr.HandledAccessNet = unix.LANDLOCK_ACCESS_NET_BIND_TCP |
			unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
		size = unsafe.Sizeof(attr)
	}
	fd, _, errno := unix.RawSyscall6(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), size, 0, 0, 0, 0)
	if errno != 0 {
		return -1, errno
	}
	rulesetFD := int(fd)

	add := func(path string, access uint64) {
		dfd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			s.plat.skipped = append(s.plat.skipped, path)
			return
		}
		defer func() { _ = unix.Close(dfd) }()
		var st unix.Stat_t
		if err := unix.Fstat(dfd, &st); err != nil {
			s.plat.skipped = append(s.plat.skipped, path)
			return
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			access &= fileScopedBits
		}
		pa := landlockPathBeneathAttr{AllowedAccess: access, ParentFD: int64(dfd)}
		_, _, errno := unix.RawSyscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rulesetFD),
			unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&pa)), 0, 0, 0)
		if errno != 0 {
			s.plat.skipped = append(s.plat.skipped, path)
		}
	}
	for _, p := range s.cfg.ROPaths {
		if p != "" {
			add(p, fsROBits)
		}
	}
	for _, p := range append([]string{s.cfg.Dir}, s.cfg.RWPaths...) {
		if p != "" {
			add(p, fsBits)
		}
	}
	return rulesetFD, nil
}

// startPlatform applies the ruleset on the dedicated spawn thread. Once a
// thread carries a Landlock domain it can never be released back to the
// scheduler, so the thread is parked forever; the Sandbox should be
// long-lived (see Close).
func (s *Sandbox) startPlatform() error {
	if s.plat.rulesetFD < 0 {
		return nil
	}
	ch := make(chan startReq)
	s.plat.startCh = ch
	fd := s.plat.rulesetFD
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			ready <- err
			return
		}
		if _, _, errno := unix.RawSyscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(fd), 0, 0); errno != 0 {
			ready <- errno
			return
		}
		ready <- nil
		parkForever(ch)
	}()
	if err := <-ready; err != nil {
		_ = unix.Close(fd)
		s.plat.rulesetFD = -1
		s.plat.startCh = nil
		s.caps.Filesystem = false
		s.caps.Network = false
		if !s.cfg.Lax {
			return fmt.Errorf("sandbox: apply landlock: %w", err)
		}
		return nil
	}
	_ = unix.Close(fd) // the ruleset is applied; the fd is no longer needed
	s.plat.rulesetFD = -1
	return nil
}

// parkForever starts commands on the calling (landlock-restricted) thread.
// It never exits: its thread cannot be returned to the scheduler.
func parkForever(ch <-chan startReq) {
	for req := range ch {
		req.errCh <- req.cmd.Start()
	}
}

// spawn runs cmd.Start on the restricted thread when Landlock is active,
// so the child inherits the ruleset. Without Landlock it starts directly.
func (s *Sandbox) spawn(cmd *exec.Cmd) error {
	if s.plat.startCh == nil {
		return cmd.Start()
	}
	req := startReq{cmd: cmd, errCh: make(chan error, 1)}
	s.plat.startCh <- req
	return <-req.errCh
}

// wrapCommand routes the command through prlimit when limits are set.
func (s *Sandbox) wrapCommand(name string, args []string) (string, []string) {
	if s.plat.prlimit == "" || !limitsRequested(s.cfg.Limits) {
		return name, args
	}
	wrapped := []string{s.plat.prlimit}
	if s.cfg.Limits.CPUSecs > 0 {
		wrapped = append(wrapped, fmt.Sprintf("--cpu=%d", s.cfg.Limits.CPUSecs))
	}
	if s.cfg.Limits.MemoryMB > 0 {
		wrapped = append(wrapped, fmt.Sprintf("--as=%d", s.cfg.Limits.MemoryMB*1024*1024))
	}
	if s.cfg.Limits.FileSizeMB > 0 {
		wrapped = append(wrapped, fmt.Sprintf("--fsize=%d", s.cfg.Limits.FileSizeMB*1024*1024))
	}
	if s.cfg.Limits.ProcCount > 0 {
		wrapped = append(wrapped, fmt.Sprintf("--nproc=%d", s.cfg.Limits.ProcCount))
	}
	wrapped = append(wrapped, "--", name)
	return s.plat.prlimit, append(wrapped, args...)
}

// kill terminates the command's whole process group (Setpgid makes the
// child its own group leader, so grandchildren die with it).
func (s *Sandbox) kill(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

func (s *Sandbox) closePlatform() {}

// release has nothing to clean up without per-run handles.
func (s *Sandbox) release(*exec.Cmd) {}

func (s *Sandbox) describe() string {
	if !s.caps.Filesystem {
		return "process-group containment only"
	}
	d := fmt.Sprintf("landlock abi=%d rw=[%s] ro=[%s]", s.plat.abi, s.cfg.Dir,
		strings.Join(s.cfg.ROPaths, ","))
	if s.caps.Network {
		d += " net=deny"
	}
	if s.caps.Limits {
		d += " limits=prlimit"
	}
	if len(s.plat.skipped) > 0 {
		d += " skipped=[" + strings.Join(s.plat.skipped, ",") + "]"
	}
	return d
}

func (s *Sandbox) procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// strictError fails construction when Landlock cannot deliver the requested
// restrictions.
func strictError(cfg Config, caps Capabilities) error {
	if !caps.Filesystem {
		return errors.New("sandbox: landlock unavailable (kernel >= 5.13 required); set Lax=true to proceed without filesystem confinement")
	}
	if cfg.NoNetwork && !caps.Network {
		return errors.New("sandbox: denying network requires landlock ABI v4 (kernel >= 6.7); set Lax=true or NoNetwork=false")
	}
	return nil
}
