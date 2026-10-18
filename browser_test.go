//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestBrowserHelperProcess(t *testing.T) {
	if os.Getenv("CMDS_TEST_BROWSER") != "1" {
		return
	}
	os.Exit(runCLI([]string{"--no-cache"}, os.Stdout, os.Stderr))
}

func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	var unlock int32
	if err := ioctl(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		t.Fatal(err)
	}
	var number uint32
	if err := ioctl(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	size := windowSize{Rows: 30, Columns: 120}
	if err := ioctl(slave.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		t.Fatal(err)
	}
	return master, slave
}

func TestBrowserNavigationSearchHelpAndTerminalRestoration(t *testing.T) {
	directory := t.TempDir()
	bin := filepath.Join(directory, "bin")
	marker := filepath.Join(directory, "executed")
	for _, name := range []string{"htop", "nvtop", "top", "qwen", "local-unknown"} {
		writeTestFile(t, filepath.Join(bin, name), "#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+marker+"'\n", 0755)
	}
	master, slave := openPTY(t)
	var original syscall.Termios
	if err := ioctl(slave.Fd(), syscall.TCGETS, unsafe.Pointer(&original)); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestBrowserHelperProcess$")
	child.Env = append(os.Environ(), "CMDS_TEST_BROWSER=1", "PATH="+bin, "TERM=xterm-256color", "XDG_CONFIG_HOME="+directory)
	child.Stdin, child.Stdout, child.Stderr = slave, slave, slave
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	chunks := make(chan []byte, 64)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 32768)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				chunks <- append([]byte{}, buffer[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	waitFor := func(needle string) {
		t.Helper()
		var output bytes.Buffer
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("terminal closed before %q: %q", needle, output.String())
				}
				output.Write(chunk)
				if strings.Contains(output.String(), needle) {
					return
				}
			case <-deadline.C:
				t.Fatalf("terminal did not show %q: %q", needle, output.String())
			}
		}
	}
	send := func(keys string) {
		t.Helper()
		if _, err := io.WriteString(master, keys); err != nil {
			t.Fatal(err)
		}
	}
	waitFor("installed commands")
	send("\r")
	waitFor("COMMANDS")
	send("nvtop\t")
	waitFor("Similar commands")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("browsing ran a command")
	}
	send("b")
	waitFor("Enter Run")
	send("\r")
	waitFor("Ctrl+R Run again")
	arguments, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(arguments)) != "" {
		t.Fatal("Enter did not run immediately without arguments", string(arguments), err)
	}
	send("\x12")
	waitFor("Ctrl+R Run again")
	send("\r")
	waitFor("Enter Run")
	send("\x05")
	waitFor("RUN WITH ARGUMENTS")
	send("\x1b")
	waitFor("Enter Run")
	send("\x05")
	waitFor("RUN WITH ARGUMENTS")
	send("--format \"wide mode\"\r")
	waitFor("Ctrl+R Run again")
	arguments, err = os.ReadFile(marker)
	if err != nil || string(arguments) != "--format\nwide mode\n" {
		t.Fatal("quoted arguments were not passed correctly", string(arguments), err)
	}
	send("\x05")
	waitFor("RUN WITH ARGUMENTS")
	send("\x15\"unfinished\r")
	waitFor("unfinished quote")
	send("\x1b")
	waitFor("Enter Run")
	send("\r")
	waitFor("Ctrl+R Run again")
	arguments, err = os.ReadFile(marker)
	if err != nil || string(arguments) != "--format\nwide mode\n" {
		t.Fatal("saved arguments were not reused", string(arguments), err)
	}
	send("\r")
	waitFor("Enter Run")
	send("\t")
	waitFor("COMMAND DETAILS")
	send("h")
	waitFor("Ctrl+R Run again")
	arguments, err = os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(arguments)) != "--help" {
		t.Fatal("explicit help launch failed", string(arguments), err)
	}
	send("\r")
	waitFor("COMMAND DETAILS")
	send("b")
	waitFor("Enter Run")
	send("\x1b")
	waitFor("htop")
	send("q")
	waitFor("CATEGORIES")
	send("/")
	waitFor("Search all commands")
	send("qwen\t")
	waitFor("COMMAND DETAILS")
	send("\x03")

	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err := <-done:
		exited = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("browser did not exit")
	}
	var restored syscall.Termios
	if err := ioctl(slave.Fd(), syscall.TCGETS, unsafe.Pointer(&restored)); err != nil {
		t.Fatal(err)
	}
	if original != restored {
		t.Fatal("terminal was left in raw mode")
	}
}
