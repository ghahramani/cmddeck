//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Commands get their own terminal so Ctrl+C never reaches an interpreter first.
func commandPTY(original syscall.Termios) (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.File, *os.File, error) { master.Close(); return nil, nil, err }
	var unlock int32
	if err = ioctl(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		return fail(err)
	}
	var number uint32
	if err = ioctl(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		return fail(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fail(err)
	}
	if err = ioctl(slave.Fd(), syscall.TCSETS, unsafe.Pointer(&original)); err != nil {
		slave.Close()
		return fail(err)
	}
	resizeCommandPTY(master)
	return master, slave, nil
}

func resizeCommandPTY(master *os.File) {
	var size windowSize
	if ioctl(os.Stdout.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)) == nil {
		_ = ioctl(master.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size))
	}
}

type commandOutput struct{ muted *atomic.Bool }

func (output commandOutput) Write(data []byte) (int, error) {
	if output.muted.Load() {
		return len(data), nil
	}
	return os.Stdout.Write(data)
}

func (b *browser) runCommand(path string, args []string) error {
	master, slave, err := commandPTY(b.terminal.original)
	if err != nil {
		return err
	}
	defer master.Close()
	defer slave.Close()
	if err = b.terminal.raw(); err != nil {
		return err
	}
	defer b.terminal.restore()
	child := exec.Command(path, args...)
	child.Stdin, child.Stdout, child.Stderr = slave, slave, slave
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err = child.Start(); err != nil {
		return err
	}
	_ = slave.Close()
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	var muted atomic.Bool
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(commandOutput{&muted}, master); close(copied) }()
	finishOutput := func() {
		select {
		case <-copied:
		case <-time.After(150 * time.Millisecond):
			_ = master.Close()
			<-copied
		}
	}
	stop := func() error {
		muted.Store(true)
		b.quitting = true
		var foreground int32
		_ = ioctl(master.Fd(), syscall.TIOCGPGRP, unsafe.Pointer(&foreground))
		signalGroups := func(signal syscall.Signal) {
			if foreground > 1 && int(foreground) != child.Process.Pid {
				_ = syscall.Kill(-int(foreground), signal)
			}
			_ = syscall.Kill(-child.Process.Pid, signal)
		}
		signalGroups(syscall.SIGTERM)
		select {
		case <-waited:
		case <-time.After(300 * time.Millisecond):
			signalGroups(syscall.SIGKILL)
			<-waited
		}
		finishOutput()
		fmt.Fprint(os.Stdout, "\x1b[?1049l\x1b[?1047l\x1b[?47l\x1b[0m\x1b[?25h\x1b[?7h")
		return nil
	}
	forward := func(data []byte) bool {
		if bytes.IndexByte(data, 3) >= 0 {
			return false
		}
		if len(data) > 0 {
			_, _ = master.Write(data)
		}
		return true
	}
	pending := b.reader.pending
	b.reader.pending = nil
	if !forward(pending) {
		return stop()
	}
	buffer := make([]byte, 4096)
	for {
		select {
		case sig := <-b.signals:
			if sig == syscall.SIGWINCH {
				resizeCommandPTY(master)
			} else {
				return stop()
			}
		case err := <-waited:
			finishOutput()
			return err
		default:
		}
		n, err := syscall.Read(int(os.Stdin.Fd()), buffer)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return stop()
		}
		if !forward(buffer[:n]) {
			return stop()
		}
	}
}
