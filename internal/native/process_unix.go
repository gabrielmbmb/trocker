//go:build linux || darwin

package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/moby/term"
	"golang.org/x/sys/unix"
)

type process struct {
	command       *exec.Cmd
	mu            sync.Mutex
	finished      bool
	terminal      int
	foreground    int
	terminalState *term.State
}

func prepareProcess(command *exec.Cmd) (*process, error) {
	p := &process{command: command, terminal: -1}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if input, ok := command.Stdin.(*os.File); ok {
		fd, isTerminal := term.GetFdInfo(input)
		if isTerminal {
			group, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
			if err != nil {
				return nil, fmt.Errorf("cannot inspect native command terminal")
			}
			if group != unix.Getpgrp() {
				return nil, fmt.Errorf("native command must be launched from the foreground terminal")
			}
			state, err := term.SaveState(fd)
			if err != nil {
				return nil, fmt.Errorf("cannot inspect native command terminal settings")
			}
			p.terminalState = state
			p.terminal, p.foreground = int(fd), group
			command.SysProcAttr.Foreground = true
			command.SysProcAttr.Ctty = int(fd)
		}
	}
	return p, nil
}

func (p *process) restore() error {
	if p.terminal < 0 {
		return nil
	}
	ignored := signal.Ignored(syscall.SIGTTOU)
	signal.Ignore(syscall.SIGTTOU)
	if !ignored {
		defer signal.Reset(syscall.SIGTTOU)
	}
	foregroundErr := unix.IoctlSetPointerInt(p.terminal, unix.TIOCSPGRP, p.foreground)
	stateErr := term.RestoreTerminal(uintptr(p.terminal), p.terminalState)
	if foregroundErr != nil || stateErr != nil {
		return fmt.Errorf("cannot restore foreground terminal and settings")
	}
	return nil
}

// Observe exit without reaping the leader. Its PID/group identity cannot be
// reused until remaining group members are killed and Cmd.Wait reaps it.
func (p *process) wait() error {
	observed := awaitExit(p.command.Process.Pid)
	p.mu.Lock()
	killed := p.killLocked()
	p.finished = true
	p.mu.Unlock()
	waitErr := p.command.Wait()
	if cleanupErr := errors.Join(observed, killed); cleanupErr != nil {
		return &executionError{cause: cleanupErr}
	}
	return waitErr
}

func (p *process) signal(ctx context.Context) error {
	selected := syscall.SIGTERM
	if cause, ok := errors.AsType[*signalCause](context.Cause(ctx)); ok {
		if received, ok := cause.signal.(syscall.Signal); ok {
			selected = received
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return nil
	}
	return groupSignal(p.command.Process.Pid, selected)
}

func (p *process) kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killLocked()
}

func (p *process) killLocked() error {
	if p.finished {
		return nil
	}
	return groupSignal(p.command.Process.Pid, syscall.SIGKILL)
}

func groupSignal(pid int, signal syscall.Signal) error {
	err := normalizeGroupError(pid, unix.Kill(-pid, signal))
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot signal native process group: %w", err)
	}
	return nil
}

func processExitCode(err *exec.ExitError) int {
	if status, ok := err.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return err.ExitCode()
}

func signalExitCode(signal os.Signal) int {
	if value, ok := signal.(syscall.Signal); ok {
		return 128 + int(value)
	}
	return 1
}
func forwardedSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
