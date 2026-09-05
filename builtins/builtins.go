// Package builtins provides first-party tools with sandboxing, approval
// and auditing built in. Unlike arbitrary function tools, the execution
// point lives inside the SDK: every command and file access is confined
// by a [sandbox.Sandbox] and every call is automatically captured by the
// audit layer, because the tool arguments themselves are the command and
// path being acted on.
//
// Construction is fail-closed: NewShellTool refuses to register an
// unconfined shell, so a missing sandbox is a startup error rather than a
// silent downgrade:
//
//	sb, err := sandbox.Auto("/workspace")
//	if err != nil {
//		log.Fatal(err)
//	}
//	shell, err := builtins.NewShellTool(builtins.ShellConfig{
//		Workspace: "/workspace",
//		Sandbox:   sb,
//	})
//	// agent.Tools = append(agent.Tools, shell)
//	// agent.Tools = append(agent.Tools, builtins.NewFileTool(
//	// 	builtins.FileConfig{Roots: []string{"/workspace"}}))
//
// Human approval for individual commands is layered on top with
// [tool.WithPolicy], the same mechanism every other tool uses.
package builtins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/jiangfufa233/smart-agent-sdk-go/sandbox"
	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
)

// maxEchoBytes caps how much of a command's output the model sees.
const maxEchoBytes = 16 << 10

// defaultMaxFileBytes is the default file size accepted by the file tool.
const defaultMaxFileBytes = 256 << 10

// DefaultDenyRules returns the deny patterns applied by ShellConfig when
// Deny is nil. They anchor on destructive targets — recursive or forced
// removal, filesystem reformatting, piping downloads into a shell,
// privilege escalation, world-writable root, writes into /etc, raw device
// writes and host power operations. They are a guardrail for obvious
// mistakes, not a security boundary; the sandbox is the boundary.
func DefaultDenyRules() []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(`^\s*rm\s+(-{1,2}[\w-]+\s+)*(-\w*[rf]|--(recursive|force))\b`),
		regexp.MustCompile(`\bmkfs(\.\w+)?\b`),
		regexp.MustCompile(`\b(curl|wget)\b[^|;&]*\|\s*(ba|z|da|k)?sh\b`),
		regexp.MustCompile(`(^|[\s;&|])sudo\s`),
		regexp.MustCompile(`\bchmod\s+(-R\s+)?777\s+/(?:\s|$)`),
		regexp.MustCompile(`(^|[\s;&|])(>|>>)\s*/etc/`),
		regexp.MustCompile(`\bdd\b[^|;&]*\bof=/dev/`),
		regexp.MustCompile(`^\s*(shutdown|reboot|halt|poweroff)\b`),
	}
}

// shellArgs is the shell tool's JSON parameter schema.
type shellArgs struct {
	Command string `json:"command" desc:"shell command to run"`
}

// fileArgs is the file tool's JSON parameter schema.
type fileArgs struct {
	Path string `json:"path" desc:"file path, absolute or relative to the first root"`
}

// ShellConfig configures NewShellTool.
type ShellConfig struct {
	// Workspace is the working directory for commands. It is required.
	Workspace string
	// Sandbox executes every command. It is required: the shell tool
	// refuses to run without confinement. Use sandbox.Auto(Workspace)
	// for safe defaults.
	Sandbox *sandbox.Sandbox
	// Deny lists command patterns that are rejected before execution
	// with a *tool.AuthorizationError. nil applies DefaultDenyRules;
	// an empty slice disables the check.
	Deny []*regexp.Regexp
}

// NewShellTool returns a tool named "shell" that runs commands inside
// cfg.Sandbox with cfg.Workspace as the working directory. It fails when
// the sandbox cannot enforce meaningful containment on this platform.
func NewShellTool(cfg ShellConfig) (*tool.FunctionTool, error) {
	if cfg.Workspace == "" {
		return nil, errors.New("builtins: ShellConfig.Workspace is required")
	}
	if cfg.Sandbox == nil {
		return nil, errors.New("builtins: ShellConfig.Sandbox is required (use sandbox.Auto); refusing unconfined shell")
	}
	caps := cfg.Sandbox.Capabilities()
	if runtime.GOOS == "linux" {
		if !caps.Filesystem {
			return nil, errors.New("builtins: sandbox does not enforce filesystem confinement; refusing to register shell (set Config.Lax knowingly)")
		}
	} else if !caps.ProcessTree {
		return nil, errors.New("builtins: sandbox does not enforce process-tree containment; refusing to register shell")
	}
	deny := cfg.Deny
	if deny == nil {
		deny = DefaultDenyRules()
	}
	shell, prefix := "sh", []string{"-c"}
	if runtime.GOOS == "windows" {
		shell, prefix = "cmd", []string{"/C"}
	}
	return tool.NewFunction("shell",
		"Run a shell command inside the workspace sandbox. Only the workspace "+
			"is writable and network access is denied; dangerous commands are "+
			"rejected. Returns stdout, or an error carrying the exit status and "+
			"stderr so the command can be adjusted.",
		func(ctx context.Context, a shellArgs) (string, error) {
			for _, re := range deny {
				if re.MatchString(a.Command) {
					return "", &tool.AuthorizationError{
						Tool: "shell",
						Err:  fmt.Errorf("command matches deny rule %q", re.String()),
					}
				}
			}
			res, err := cfg.Sandbox.Run(ctx, shell, append(prefix, a.Command)...)
			if err != nil {
				var ee *sandbox.ExitError
				if errors.As(err, &ee) {
					return "", fmt.Errorf("exit status %d: %s", ee.ExitCode, truncate(ee.Stderr))
				}
				if errors.Is(err, sandbox.ErrTimeout) {
					return "", errors.New("command timed out; the process tree was killed")
				}
				return "", err
			}
			out := truncate(res.Stdout)
			if se := truncate(res.Stderr); se != "" {
				if out != "" {
					out += "\n"
				}
				out += "[stderr]\n" + se
			}
			if out == "" {
				out = "(no output)"
			}
			return out, nil
		})
}

// FileConfig configures NewFileTool.
type FileConfig struct {
	// Roots are the directories files may be read from. Required.
	Roots []string
	// MaxBytes caps the size of readable files; zero means 256 KiB.
	MaxBytes int64
}

// NewFileTool returns a read-only tool named "read_file". Paths must stay
// inside cfg.Roots after lexical cleaning and symlink resolution; binary
// and oversized files are refused.
func NewFileTool(cfg FileConfig) (*tool.FunctionTool, error) {
	if len(cfg.Roots) == 0 {
		return nil, errors.New("builtins: FileConfig.Roots is required")
	}
	roots := make([]string, 0, len(cfg.Roots))
	for _, r := range cfg.Roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("builtins: root %q: %w", r, err)
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("builtins: root %q is not a directory", r)
		}
		roots = append(roots, abs)
	}
	max := cfg.MaxBytes
	if max <= 0 {
		max = defaultMaxFileBytes
	}
	escape := func(p string) error {
		return fmt.Errorf("path escapes workspace roots: %s", p)
	}
	return tool.NewFunction("read_file",
		"Read a text file from the workspace. The path must stay inside the "+
			"configured roots; relative paths resolve against the first root. "+
			"Binary or oversized files are refused.",
		func(ctx context.Context, a fileArgs) (string, error) {
			if a.Path == "" {
				return "", errors.New("path is empty")
			}
			var found string
			var fallbackErr error
			for _, root := range roots {
				full := a.Path
				if !filepath.IsAbs(full) {
					full = filepath.Join(root, full)
				}
				if !within(root, full) {
					continue
				}
				real, err := filepath.EvalSymlinks(full)
				if err != nil {
					if fallbackErr == nil {
						fallbackErr = err
					}
					continue
				}
				realRoot, err := filepath.EvalSymlinks(root)
				if err != nil {
					continue
				}
				if !within(realRoot, real) {
					return "", escape(a.Path) // symlink escape: fail loudly
				}
				found = real
				break
			}
			if found == "" {
				if fallbackErr != nil {
					return "", fmt.Errorf("read_file: %v", fallbackErr)
				}
				return "", escape(a.Path)
			}
			fi, err := os.Stat(found)
			if err != nil {
				return "", fmt.Errorf("read_file: %v", err)
			}
			if fi.IsDir() {
				return "", fmt.Errorf("path is a directory: %s", a.Path)
			}
			if fi.Size() > max {
				return "", fmt.Errorf("file exceeds %d bytes: %s", max, a.Path)
			}
			data, err := os.ReadFile(found)
			if err != nil {
				return "", fmt.Errorf("read_file: %v", err)
			}
			probe := data
			if len(probe) > 8192 {
				probe = probe[:8192]
			}
			if bytes.IndexByte(probe, 0) >= 0 {
				return "", fmt.Errorf("binary file refused: %s", a.Path)
			}
			return truncate(string(data)), nil
		})
}

// within reports whether path p lies inside root (both absolute).
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// truncate caps s at maxEchoBytes with a marker.
func truncate(s string) string {
	if len(s) <= maxEchoBytes {
		return s
	}
	return s[:maxEchoBytes] + "\n[truncated]"
}
