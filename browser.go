package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"unicode/utf8"
)

type listState struct {
	query            string
	selected, offset int
	editing          bool
}
type listItem struct {
	name, description, category string
	count                       int
	command                     *Command
}
type browser struct {
	rules     *Rules
	commands  []Command
	terminal  *Terminal
	reader    keyReader
	signals   chan os.Signal
	positions map[string]listState
	arguments map[string]string
	status    string
	quitting  bool
}

func browse(rules *Rules, commands []Command, category, query string, hasQuery bool) error {
	terminal, err := openTerminal()
	if err != nil {
		return err
	}
	defer terminal.restore()
	b := &browser{rules: rules, commands: commands, terminal: terminal, reader: keyReader{fd: int(os.Stdin.Fd())}, signals: make(chan os.Signal, 8), positions: map[string]listState{}, arguments: map[string]string{}}
	signal.Notify(b.signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGWINCH)
	defer signal.Stop(b.signals)
	return b.run(category, query, hasQuery)
}

func (b *browser) key() (string, error) {
	select {
	case sig := <-b.signals:
		if sig == syscall.SIGWINCH {
			return "resize", nil
		}
		b.quitting = true
		return "quit", nil
	default:
	}
	key, err := b.reader.next()
	if key == "quit" {
		b.quitting = true
	}
	return key, err
}

// pick returns an explicit action so opening a category never executes a command.
func (b *browser) pick(title string, items []listItem, categoryMenu bool, initial listState) (int, string, error) {
	state, ok := b.positions[title]
	if !ok {
		state = initial
	}
	haystacks := make([]string, len(items))
	for i, item := range items {
		if item.command != nil {
			haystacks[i] = b.rules.haystack(*item.command)
		} else {
			haystacks[i] = strings.ToLower(item.name + " " + item.description + " " + item.category)
		}
	}
	dirty := true
	filtered := []int{}
	lastHeight, lastWidth := 0, 0
	for {
		height, width := terminalSize()
		if height != lastHeight || width != lastWidth {
			dirty = true
			lastHeight, lastWidth = height, width
		}
		page := pickerPage(height)
		if dirty {
			filtered = filtered[:0]
			for i, text := range haystacks {
				if matchesQuery(text, state.query) {
					filtered = append(filtered, i)
				}
			}
			state.selected = max(0, min(state.selected, len(filtered)-1))
			if state.selected < state.offset {
				state.offset = state.selected
			}
			if state.selected >= state.offset+page {
				state.offset = state.selected - page + 1
			}
			state.offset = max(0, min(state.offset, max(0, len(filtered)-page)))
			b.renderPicker(title, items, filtered, state, categoryMenu)
			dirty = false
		}
		key, err := b.key()
		if err != nil {
			return -1, "", err
		}
		if key == "tick" {
			continue
		}
		dirty = true
		action := ""
		switch key {
		case "quit":
			return -1, "", nil
		case "up":
			state.selected = max(0, state.selected-1)
		case "down":
			state.selected = min(len(filtered)-1, state.selected+1)
		case "pageup":
			state.selected = max(0, state.selected-page)
		case "pagedown":
			state.selected = min(len(filtered)-1, state.selected+page)
		case "home":
			state.selected = 0
		case "end":
			state.selected = max(0, len(filtered)-1)
		case "enter":
			if categoryMenu {
				action = "open"
			} else {
				action = "run"
			}
		case "right", "details":
			if categoryMenu {
				action = "open"
			} else {
				action = "details"
			}
		case "arguments":
			if !categoryMenu {
				action = "arguments"
			}
		case "help":
			if !categoryMenu {
				action = "help"
			}
		case "escape":
			if state.query != "" {
				state = listState{}
			} else {
				b.positions[title] = state
				return -1, "", nil
			}
		case "left":
			b.positions[title] = state
			return -1, "", nil
		case "clear":
			state.query = ""
			state.selected, state.offset = 0, 0
		case "backspace":
			runes := []rune(state.query)
			if len(runes) > 0 {
				state.query = string(runes[:len(runes)-1])
			}
			state.selected, state.offset = 0, 0
		case "/":
			if categoryMenu {
				b.positions[title] = state
				return -2, "search", nil
			}
			state.editing = true
		case "resize":
		default:
			if key == "q" && !state.editing {
				b.positions[title] = state
				return -1, "", nil
			}
			if utf8.RuneCountInString(key) == 1 {
				state.query += key
				state.selected, state.offset, state.editing = 0, 0, true
			}
		}
		if action != "" && len(filtered) > 0 {
			b.positions[title] = state
			return filtered[state.selected], action, nil
		}
	}
}

func (b *browser) commandList(commands []Command, title, query string, editing bool) error {
	items := make([]listItem, len(commands))
	for i := range commands {
		items[i] = listItem{name: commands[i].Name, description: commands[i].Description, category: commands[i].Category, command: &commands[i]}
	}
	for !b.quitting {
		index, action, err := b.pick(title, items, false, listState{query: query, editing: editing})
		if err != nil {
			return err
		}
		if index < 0 {
			return nil
		}
		switch action {
		case "details":
			err = b.details(commands[index])
		default:
			err = b.launch(commands[index], action)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func wrapText(text string, width int) []string {
	width = max(1, width)
	lines := []string{}
	current := ""
	for _, word := range strings.Fields(text) {
		if current != "" && utf8.RuneCountInString(current)+1+utf8.RuneCountInString(word) > width {
			lines = append(lines, current)
			current = ""
		}
		if current != "" {
			current += " "
		}
		current += word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func (b *browser) details(command Command) error {
	dirty := true
	for !b.quitting {
		if dirty {
			b.renderDetails(command)
			dirty = false
		}
		key, err := b.key()
		if err != nil {
			return err
		}
		if key == "tick" {
			continue
		}
		dirty = true
		switch key {
		case "quit", "escape", "left", "b", "q":
			return nil
		case "enter", "r":
			if err = b.launch(command, "run"); err != nil {
				return err
			}
		case "arguments", "a", "e":
			if err = b.launch(command, "arguments"); err != nil {
				return err
			}
		case "h", "help":
			if err = b.launch(command, "help"); err != nil {
				return err
			}
		}
	}
	return nil
}

// Arguments are edited in the browser, with cancellation and inline validation.
func (b *browser) editArguments(command Command) ([]string, bool, error) {
	text := []rune(b.arguments[command.Name])
	cursor := len(text)
	message := ""
	dirty := true
	for !b.quitting {
		if dirty {
			b.renderArguments(command, string(text), cursor, message)
			dirty = false
		}
		key, err := b.key()
		if err != nil {
			return nil, false, err
		}
		if key == "tick" {
			continue
		}
		dirty = true
		switch key {
		case "quit", "escape":
			return nil, false, nil
		case "enter":
			args, err := parseArguments(string(text))
			if err != nil {
				message = err.Error()
				continue
			}
			b.arguments[command.Name] = string(text)
			return args, true, nil
		case "left":
			cursor = max(0, cursor-1)
		case "right":
			cursor = min(len(text), cursor+1)
		case "home":
			cursor = 0
		case "end":
			cursor = len(text)
		case "clear":
			text = nil
			cursor = 0
		case "backspace":
			if cursor > 0 {
				text = append(text[:cursor-1], text[cursor:]...)
				cursor--
			}
		case "delete":
			if cursor < len(text) {
				text = append(text[:cursor], text[cursor+1:]...)
			}
		default:
			if utf8.RuneCountInString(key) == 1 {
				char := []rune(key)[0]
				text = append(text, 0)
				copy(text[cursor+1:], text[cursor:])
				text[cursor] = char
				cursor++
			}
		}
		if key != "tick" {
			message = ""
		}
	}
	return nil, false, nil
}

func (b *browser) launch(command Command, action string) error {
	args, err := parseArguments(b.arguments[command.Name])
	if err != nil {
		action = "arguments"
	}
	if action == "help" {
		args = []string{"--help"}
	}
	for !b.quitting {
		if action == "arguments" {
			var ok bool
			args, ok, err = b.editArguments(command)
			if err != nil || !ok {
				return err
			}
		}
		b.terminal.restore()
		display := cleanText(command.Name)
		if len(args) > 0 {
			display += " " + strings.Join(args, " ")
		}
		fmt.Fprintf(os.Stdout, "\n\x1b[1;36m▶ %s\x1b[0m\n\n", cleanText(display))
		child := exec.Command(command.Path, args...)
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		runError := child.Run()
		draining := true
		for draining {
			select {
			case sig := <-b.signals:
				if sig != syscall.SIGINT && sig != syscall.SIGWINCH {
					b.quitting = true
				}
			default:
				draining = false
			}
		}
		if b.quitting {
			return nil
		}
		if runError == nil {
			b.status = command.Name + " finished successfully"
			fmt.Fprint(os.Stdout, "\n\x1b[32m✓ Finished\x1b[0m\n")
		} else {
			b.status = command.Name + ": " + runError.Error()
			fmt.Fprintf(os.Stdout, "\n\x1b[33m%s\x1b[0m\n", cleanText(b.status))
		}
		// Keep command output visible while accepting one-key actions.
		if err = b.terminal.raw(); err != nil {
			return err
		}
		fmt.Fprint(os.Stdout, "\r\n\x1b[2mEnter Back  ·  Ctrl+R Run again  ·  Ctrl+E Arguments  ·  Esc Back\x1b[0m\r\n")
		b.reader.pending = nil
		nextAction := ""
		for !b.quitting && nextAction == "" {
			key, keyError := b.key()
			if keyError != nil {
				b.terminal.restore()
				return keyError
			}
			switch key {
			case "enter", "escape", "left":
				nextAction = "back"
			case "rerun":
				nextAction = "run"
			case "arguments":
				nextAction = "arguments"
			case "q":
				b.quitting = true
			}
		}
		if b.quitting {
			b.terminal.restore()
			return nil
		}
		if err = b.terminal.resume(); err != nil {
			return err
		}
		if nextAction == "back" {
			return nil
		}
		action = nextAction
	}
	return nil
}

func (b *browser) run(category, query string, hasQuery bool) error {
	if category != "" {
		return b.commandList(b.rules.group(b.commands, category), b.rules.categories[category].Title, "", false)
	}
	if hasQuery {
		return b.commandList(b.commands, "Search all commands", query, true)
	}
	counts := map[string]int{}
	for _, c := range b.commands {
		counts[c.Category]++
	}
	items := []listItem{}
	for _, c := range b.rules.Categories {
		if counts[c.ID] > 0 {
			items = append(items, listItem{name: c.Title, description: c.Description, category: c.ID, count: counts[c.ID]})
		}
	}
	for !b.quitting {
		index, _, err := b.pick("All commands", items, true, listState{})
		if err != nil {
			return err
		}
		if index == -1 {
			return nil
		}
		if index == -2 {
			err = b.commandList(b.commands, "Search all commands", "", true)
		} else {
			c := items[index]
			err = b.commandList(b.rules.group(b.commands, c.category), c.name, "", false)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
