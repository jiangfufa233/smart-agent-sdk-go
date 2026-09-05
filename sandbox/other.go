//go:build !linux && !windows

package sandbox

import (
	"errors"
	"os/exec"
	"syscall"
)

// platform holds no state on platforms without OS sandboxing support.
type platform struct{}

// defaultROPaths is empty: filesystem rules are not enforced here.
func defaultROPaths() []string { return nil }

// initPlatform reports process-group containment as the only capability.
func (s *Sandbox) initPlatform() error {
	s.caps = Capabilities{ProcessTree: true}
	return nil
}

func (s *Sandbox) startPlatform() error { return nil }

func (s *Sandbox) spawn(cmd *exec.Cmd) error { return cmd.Start() }

// wrapCommand passes commands through unchanged; no limit wrapper exists.
func (s *Sandbox) wrapCommand(name string, args []string) (string, []string) {
	return name, args
}

// kill terminates the command's whole process group.
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

func (s *Sandbox) describe() string { return "process-group containment only" }

func (s *Sandbox) procAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// strictError fails construction: nothing beyond process-group containment
// can be enforced here, so fail-closed users must opt in via Config.Lax.
func strictError(cfg Config, caps Capabilities) error {
	return errors.New("sandbox: no filesystem or network enforcement available on this platform (process-group containment only); set Lax=true to proceed")
}
