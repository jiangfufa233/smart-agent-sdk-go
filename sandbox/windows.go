//go:build windows

package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// platform holds the Windows-specific sandbox state: one Job Object per
// running command, so terminating one run never touches its siblings.
type platform struct {
	mu   sync.Mutex
	jobs map[*exec.Cmd]windows.Handle
}

func defaultROPaths() []string { return nil }

// initPlatform reports what Job Objects can enforce here. Filesystem and
// network confinement have no Windows equivalent in this package.
func (s *Sandbox) initPlatform() error {
	s.plat.jobs = make(map[*exec.Cmd]windows.Handle)
	s.caps = Capabilities{ProcessTree: true, Limits: s.cfg.Limits.MemoryMB > 0}
	return nil
}

func (s *Sandbox) startPlatform() error { return nil }

// spawn starts the command and immediately assigns it to a fresh Job
// Object with kill-on-close. Without CREATE_SUSPENDED there is a small
// window in which the child could escape before the assignment; the
// fallback in kill covers hosts that refuse the assignment (for example
// when the parent is itself inside a job without breakaway).
func (s *Sandbox) spawn(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil // best-effort: plain process kill still applies
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if s.cfg.Limits.MemoryMB > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
		info.ProcessMemoryLimit = uintptr(s.cfg.Limits.MemoryMB) * 1024 * 1024
	}
	if _, err := windows.SetInformationJobObject(job,
		uint32(windows.JobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	if err != nil {
		windows.CloseHandle(job)
		return nil
	}
	s.plat.mu.Lock()
	s.plat.jobs[cmd] = job
	s.plat.mu.Unlock()
	return nil
}

// wrapCommand passes commands through unchanged.
func (s *Sandbox) wrapCommand(name string, args []string) (string, []string) {
	return name, args
}

// kill terminates the command's Job Object (the whole tree) or, when the
// assignment never happened, the direct child only.
func (s *Sandbox) kill(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	s.plat.mu.Lock()
	job, ok := s.plat.jobs[cmd]
	s.plat.mu.Unlock()
	if ok {
		_ = windows.TerminateJobObject(job, 1)
		return
	}
	_ = cmd.Process.Kill()
}

// release closes the command's Job Object handle; KILL_ON_JOB_CLOSE makes
// this a safety net for anything still running.
func (s *Sandbox) release(cmd *exec.Cmd) {
	s.plat.mu.Lock()
	job, ok := s.plat.jobs[cmd]
	delete(s.plat.jobs, cmd)
	s.plat.mu.Unlock()
	if ok {
		windows.CloseHandle(job)
	}
}

func (s *Sandbox) closePlatform() {}

func (s *Sandbox) describe() string {
	d := "job object tree-kill"
	if s.caps.Limits {
		d += fmt.Sprintf(", memory=%dMB", s.cfg.Limits.MemoryMB)
	}
	return d
}

func (s *Sandbox) procAttr() *syscall.SysProcAttr { return nil }

// strictError fails construction only when Job Objects are entirely
// unavailable, which initPlatform cannot observe; it is kept for
// symmetry and future host-level checks.
func strictError(cfg Config, caps Capabilities) error {
	if !caps.ProcessTree {
		return errors.New("sandbox: job objects unavailable; set Lax=true to proceed")
	}
	return nil
}
