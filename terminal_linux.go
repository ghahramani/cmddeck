//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
	"unsafe"
)

func ioctl(fd uintptr, request uintptr, pointer unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(pointer))
	if errno != 0 {
		return errno
	}
	return nil
}
func isTerminal(fd uintptr) bool {
	var state syscall.Termios
	return ioctl(fd, syscall.TCGETS, unsafe.Pointer(&state)) == nil
}

type windowSize struct{ Rows, Columns, X, Y uint16 }

func terminalSize() (int, int) {
	var size windowSize
	if ioctl(os.Stdout.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)) != nil || size.Rows == 0 || size.Columns == 0 {
		return 24, 80
	}
	return int(size.Rows), int(size.Columns)
}

type Terminal struct {
	original  syscall.Termios
	active    bool
	alternate bool
	fd        uintptr
}

func openTerminal() (*Terminal, error) {
	terminal := &Terminal{fd: os.Stdin.Fd()}
	if err := ioctl(terminal.fd, syscall.TCGETS, unsafe.Pointer(&terminal.original)); err != nil {
		return nil, err
	}
	if err := terminal.resume(); err != nil {
		return nil, err
	}
	return terminal, nil
}
func (t *Terminal) resume() error {
	if err := t.raw(); err != nil {
		return err
	}
	fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25l\x1b[?7l")
	t.alternate = true
	return nil
}
func (t *Terminal) raw() error {
	raw := t.original
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 1
	if err := ioctl(t.fd, syscall.TCSETS, unsafe.Pointer(&raw)); err != nil {
		return err
	}
	t.active = true
	return nil
}
func (t *Terminal) restore() {
	if !t.active {
		return
	}
	fmt.Fprint(os.Stdout, "\x1b[0m\x1b[?7h\x1b[?25h")
	if t.alternate {
		fmt.Fprint(os.Stdout, "\x1b[?1049l")
		t.alternate = false
	}
	_ = ioctl(t.fd, syscall.TCSETS, unsafe.Pointer(&t.original))
	t.active = false
}

type keyReader struct {
	fd      int
	pending []byte
}

func (k *keyReader) fill() error {
	buffer := make([]byte, 128)
	n, err := syscall.Read(k.fd, buffer)
	if errors.Is(err, syscall.EINTR) {
		return nil
	}
	if err != nil {
		return err
	}
	k.pending = append(k.pending, buffer[:n]...)
	return nil
}
func (k *keyReader) next() (string, error) {
	if len(k.pending) == 0 {
		if err := k.fill(); err != nil {
			return "", err
		}
	}
	if len(k.pending) == 0 {
		return "tick", nil
	}
	if k.pending[0] == 27 {
		if len(k.pending) == 1 {
			if err := k.fill(); err != nil {
				return "", err
			}
		}
		if len(k.pending) == 1 {
			k.pending = nil
			return "escape", nil
		}
		if k.pending[1] != '[' && k.pending[1] != 'O' {
			k.pending = k.pending[1:]
			return "escape", nil
		}
		for attempt := 0; attempt < 2; attempt++ {
			for i := 2; i < len(k.pending); i++ {
				if k.pending[i] >= 0x40 && k.pending[i] <= 0x7e {
					sequence := string(k.pending[:i+1])
					k.pending = k.pending[i+1:]
					keys := map[string]string{"\x1b[A": "up", "\x1b[B": "down", "\x1b[C": "right", "\x1b[D": "left",
						"\x1bOA": "up", "\x1bOB": "down", "\x1bOC": "right", "\x1bOD": "left",
						"\x1b[H": "home", "\x1b[F": "end", "\x1bOH": "home", "\x1bOF": "end",
						"\x1b[1~": "home", "\x1b[4~": "end", "\x1b[5~": "pageup", "\x1b[6~": "pagedown", "\x1b[3~": "delete", "\x1bOP": "help"}
					if key := keys[sequence]; key != "" {
						return key, nil
					}
					return "tick", nil
				}
			}
			if err := k.fill(); err != nil {
				return "", err
			}
		}
		k.pending = nil
		return "escape", nil
	}
	for !utf8.FullRune(k.pending) {
		if err := k.fill(); err != nil {
			return "", err
		}
	}
	char, size := utf8.DecodeRune(k.pending)
	k.pending = k.pending[size:]
	switch char {
	case 3, 4:
		return "quit", nil
	case 8, 127:
		return "backspace", nil
	case 5:
		return "arguments", nil
	case 9:
		return "details", nil
	case 18:
		return "rerun", nil
	case 10, 13:
		return "enter", nil
	case 14:
		return "down", nil
	case 16:
		return "up", nil
	case 21:
		return "clear", nil
	}
	if unicode.IsPrint(char) {
		return string(char), nil
	}
	return "tick", nil
}

func safeLine(text string, width int) string {
	text = strings.Map(func(c rune) rune {
		if unicode.IsPrint(c) {
			return c
		}
		return ' '
	}, text)
	// The inventory is overwhelmingly ASCII. Wide Unicode characters are conservatively
	// bounded so a malicious/odd filename cannot wrap into terminal control rows.
	used := 0
	var result strings.Builder
	for _, c := range text {
		cells := 1
		if c >= 0x1100 && (c <= 0x115f || c >= 0x2329 && c <= 0x232a || c >= 0x2e80 && c <= 0xa4cf || c >= 0xac00 && c <= 0xd7a3 || c >= 0xf900 && c <= 0xfaff || c >= 0xfe10 && c <= 0xfe6f || c >= 0xff00 && c <= 0xff60 || c >= 0x1f000) {
			cells = 2
		}
		if used+cells > width {
			break
		}
		result.WriteRune(c)
		used += cells
	}
	return result.String()
}
