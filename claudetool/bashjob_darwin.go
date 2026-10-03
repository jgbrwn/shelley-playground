//go:build darwin

package claudetool

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// processStartTime reads the kernel process start time, which distinguishes
// a persisted job from a later process that reused its PID.
func processStartTime(pid int) (uint64, error) {
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	start := proc.Proc.P_starttime
	return uint64(start.Sec)*1_000_000 + uint64(start.Usec), nil
}

func (j BackgroundJob) currentProcess() bool {
	start, err := processStartTime(j.PID)
	return err == nil && start == j.StartTime
}

// Exited waits for a persisted job using a kqueue process-exit notification.
func (j BackgroundJob) Exited() (<-chan struct{}, error) {
	exited := make(chan struct{})
	if !j.currentProcess() {
		close(exited)
		return exited, nil
	}

	queue, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("create process-exit kqueue: %w", err)
	}
	event := unix.Kevent_t{
		Ident:  uint64(j.PID),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}
	if _, err := unix.Kevent(queue, []unix.Kevent_t{event}, nil, nil); err != nil {
		unix.Close(queue)
		if errors.Is(err, unix.ESRCH) {
			close(exited)
			return exited, nil
		}
		return nil, fmt.Errorf("watch process %d exit: %w", j.PID, err)
	}
	if !j.currentProcess() {
		unix.Close(queue)
		close(exited)
		return exited, nil
	}

	go func() {
		defer close(exited)
		defer unix.Close(queue)
		events := make([]unix.Kevent_t, 1)
		for {
			n, err := unix.Kevent(queue, nil, events, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || n > 0 {
				return
			}
		}
	}()
	return exited, nil
}

// Kill sends SIGTERM to j's process group after checking the leader's start
// time. The wrapper records the command's exit status.
func (j BackgroundJob) Kill() error {
	if !j.currentProcess() {
		return ErrBackgroundJobGone
	}
	err := syscall.Kill(-j.PID, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return ErrBackgroundJobGone
	}
	return err
}
