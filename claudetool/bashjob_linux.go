//go:build linux

package claudetool

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// processStartTime returns field 22 (starttime) of /proc/<pid>/stat.
func processStartTime(pid int) (uint64, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// comm (field 2) may contain spaces and parentheses; fields after the
	// last ')' start at field 3.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(b[i+1:]))
	if len(fields) < 20 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

// pidfd opens a pidfd for j's wrapper, or fails with ErrBackgroundJobGone.
// The pidfd pins the process identity: if the start time read after opening
// it matches, the pidfd refers to the wrapper.
func (j BackgroundJob) pidfd() (int, error) {
	fd, err := unix.PidfdOpen(j.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return -1, ErrBackgroundJobGone
	}
	if err != nil {
		return -1, fmt.Errorf("pidfd_open %d: %w", j.PID, err)
	}
	if st, err := processStartTime(j.PID); err != nil || st != j.StartTime {
		unix.Close(fd)
		return -1, ErrBackgroundJobGone
	}
	return fd, nil
}

// Exited waits for a persisted job with a pidfd, which pins the process
// identity even if the operating system later reuses its PID.
func (j BackgroundJob) Exited() (<-chan struct{}, error) {
	exited := make(chan struct{})
	fd, err := j.pidfd()
	if errors.Is(err, ErrBackgroundJobGone) {
		close(exited)
		return exited, nil
	}
	if err != nil {
		return nil, err
	}
	go func() {
		defer close(exited)
		defer unix.Close(fd)
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			_, err := unix.Poll(fds, -1)
			if !errors.Is(err, unix.EINTR) {
				return
			}
		}
	}()
	return exited, nil
}

// Kill sends SIGTERM to j's process group after verifying its leader via
// pidfd. The wrapper records the command's exit status.
func (j BackgroundJob) Kill() error {
	fd, err := j.pidfd()
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	err = syscall.Kill(-j.PID, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return ErrBackgroundJobGone
	}
	return err
}
