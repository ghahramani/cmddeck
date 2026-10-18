//go:build linux

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestInterruptedCommandHelper(t *testing.T) {
	if os.Getenv("CMDDECK_INTERRUPT_CHILD") != "1" {
		return
	}
	if !isTerminal(os.Stdin.Fd()) || !isTerminal(os.Stdout.Fd()) {
		os.Exit(3)
	}
	_ = os.WriteFile(os.Getenv("CMDDECK_CHILD_PID"), []byte(strconv.Itoa(os.Getpid())), 0600)
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, syscall.SIGINT)
	go func() { <-interrupts; fmt.Fprintln(os.Stderr, "Traceback: KeyboardInterrupt"); os.Exit(130) }()
	fmt.Println("THEME PROMPT")
	choice, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Println("CHOICE", strings.TrimSpace(choice))
	fmt.Println("WAITING FOR INTERRUPT")
	for {
		time.Sleep(time.Second)
	}
}

func TestCtrlCStopsChildAndExitsQuietly(t *testing.T) {
	for _, external := range []bool{false, true} {
		name := "keyboard"
		if external {
			name = "signal"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			pidFile := filepath.Join(root, "child.pid")
			writeTestFile(t, filepath.Join(bin, "nvtop"), "#!/bin/sh\nexec '"+os.Args[0]+"' -test.run=^TestInterruptedCommandHelper$\n", 0755)
			master, slave := openPTY(t)
			var original syscall.Termios
			if err := ioctl(slave.Fd(), syscall.TCGETS, unsafe.Pointer(&original)); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestBrowserHelperProcess$")
			child.Env = append(os.Environ(), "CMDS_TEST_BROWSER=1", "CMDDECK_INTERRUPT_CHILD=1", "CMDDECK_CHILD_PID="+pidFile, "PATH="+bin, "TERM=xterm-256color", "XDG_CONFIG_HOME="+root)
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
			var transcript bytes.Buffer
			waitFor := func(needle string) {
				t.Helper()
				var received bytes.Buffer
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				for {
					select {
					case data, ok := <-chunks:
						if !ok {
							t.Fatalf("terminal closed before %q", needle)
						}
						transcript.Write(data)
						received.Write(data)
						if strings.Contains(received.String(), needle) {
							return
						}
					case <-deadline.C:
						t.Fatalf("missing %q: %s", needle, received.String())
					}
				}
			}
			send := func(text string) {
				t.Helper()
				if _, err := io.WriteString(master, text); err != nil {
					t.Fatal(err)
				}
			}
			waitFor("CATEGORIES")
			send("\r")
			waitFor("Enter Run")
			send("\r")
			waitFor("THEME PROMPT")
			send("2\r")
			waitFor("WAITING FOR INTERRUPT")
			if !strings.Contains(transcript.String(), "CHOICE 2") {
				t.Fatal("command input was not forwarded")
			}
			if external {
				_ = child.Process.Signal(syscall.SIGINT)
			} else {
				send("\x03")
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			select {
			case err := <-done:
				exited = true
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("Ctrl+C did not close cmddeck")
			}
			drain := true
			for drain {
				select {
				case data, ok := <-chunks:
					if !ok {
						drain = false
					} else {
						transcript.Write(data)
					}
				default:
					drain = false
				}
			}
			output := transcript.String()
			for _, bad := range []string{"KeyboardInterrupt", "Traceback", "signal: interrupt", "Ctrl+R Run again"} {
				if strings.Contains(output, bad) {
					t.Errorf("interrupt displayed %q", bad)
				}
			}
			var restored syscall.Termios
			if err := ioctl(slave.Fd(), syscall.TCGETS, unsafe.Pointer(&restored)); err != nil {
				t.Fatal(err)
			}
			if original != restored {
				t.Fatal("terminal was not restored")
			}
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, _ := strconv.Atoi(string(data))
			if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
				t.Fatalf("launched child still exists: %d", pid)
			}
		})
	}
}
