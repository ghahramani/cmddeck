package main

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	muted         = "2;37"
	borderStyle   = "2;36"
	accent        = "1;36"
	selectedStyle = "1;97;44"
	headingStyle  = "1;37"
)

type surface struct {
	height, width int
	output        strings.Builder
	color         bool
}

func newSurface() *surface {
	h, w := terminalSize()
	s := &surface{height: h, width: w, color: os.Getenv("NO_COLOR") == ""}
	s.output.WriteString("\x1b[H\x1b[2J")
	return s
}
func (s *surface) text(y, x, width int, text, style string) {
	if y < 0 || y >= s.height || x < 0 || x >= s.width || width <= 0 {
		return
	}
	fmt.Fprintf(&s.output, "\x1b[%d;%dH", y+1, x+1)
	if s.color && style != "" {
		fmt.Fprintf(&s.output, "\x1b[%sm", style)
	}
	s.output.WriteString(safeLine(text, min(width, s.width-x)))
	s.output.WriteString("\x1b[0m")
}
func (s *surface) box(y, x, height, width int, title string) {
	if height < 2 || width < 3 {
		return
	}
	s.text(y, x, width, "╭"+strings.Repeat("─", width-2)+"╮", borderStyle)
	for row := y + 1; row < y+height-1; row++ {
		s.text(row, x, 1, "│", borderStyle)
		s.text(row, x+width-1, 1, "│", borderStyle)
	}
	s.text(y+height-1, x, width, "╰"+strings.Repeat("─", width-2)+"╯", borderStyle)
	if title != "" {
		s.text(y, x+2, max(0, width-5), " "+title+" ", accent)
	}
}
func (s *surface) header(title string, total int) {
	s.text(0, 2, s.width-4, "◆ "+strings.ToUpper(appName), accent)
	if s.width > 45 {
		s.text(0, 16, s.width-36, "Your command library", muted)
	}
	count := formatCount(total) + " installed commands"
	if s.width > 70 {
		s.text(0, max(2, s.width-utf8.RuneCountInString(count)-2), len(count), count, muted)
	}
	s.text(1, 2, s.width-4, "All commands  /  "+title, muted)
}
func (s *surface) footer(primary, secondary string) {
	s.text(s.height-2, 2, s.width-4, primary, headingStyle)
	s.text(s.height-1, 2, s.width-4, secondary, muted)
}
func (s *surface) flush()       { fmt.Fprint(os.Stdout, s.output.String()) }
func pickerPage(height int) int { return max(1, height-14) }
func formatCount(value int) string {
	str := fmt.Sprint(value)
	for i := len(str) - 3; i > 0; i -= 3 {
		str = str[:i] + "," + str[i:]
	}
	return str
}
func (b *browser) commandLine(c Command) string {
	text := c.Name
	if args := b.arguments[c.Name]; args != "" {
		text += " " + args
	}
	return "$ " + text
}

func (b *browser) renderPicker(title string, items []listItem, filtered []int, state listState, categories bool) {
	s := newSurface()
	s.header(title, len(b.commands))
	if s.height < 16 || s.width < 50 {
		s.text(4, 2, s.width-4, "Make the terminal larger to browse commands.", headingStyle)
		s.footer("Esc Back  ·  Ctrl+C Quit", "")
		s.flush()
		return
	}
	s.box(3, 1, 3, s.width-2, "")
	hint := "Type to filter commands..."
	if categories {
		hint = "Type to find a category, or / to search all commands..."
	}
	query := "  /  " + state.query
	style := headingStyle
	if state.query == "" && !state.editing {
		query = "  /  " + hint
		style = muted
	} else {
		query += "▏"
	}
	s.text(4, 2, s.width-4, query, style)
	split := s.width >= 105
	leftWidth := s.width - 2
	previewWidth := max(36, min(48, s.width/3))
	if split {
		leftWidth = s.width - previewWidth - 4
	}
	panelHeight := s.height - 11
	panelTitle := "COMMANDS"
	if categories {
		panelTitle = "CATEGORIES"
	}
	s.box(7, 1, panelHeight, leftWidth, panelTitle+"  "+formatCount(len(filtered)))
	nameWidth := max(16, min(28, leftWidth/2-2))
	if categories {
		s.text(8, 4, leftWidth-7, "CATEGORY", muted)
		s.text(8, leftWidth-9, 7, "COUNT", muted)
	} else {
		s.text(8, 4, nameWidth, "COMMAND", muted)
		s.text(8, 5+nameWidth, max(0, leftWidth-nameWidth-7), "DESCRIPTION", muted)
	}
	for i := state.offset; i < min(len(filtered), state.offset+pickerPage(s.height)); i++ {
		item := items[filtered[i]]
		row := 9 + i - state.offset
		style := ""
		marker := "  "
		if i == state.selected {
			style = selectedStyle
			marker = "› "
			s.text(row, 2, leftWidth-2, strings.Repeat(" ", leftWidth-2), style)
		}
		if categories {
			s.text(row, 3, leftWidth-14, marker+item.name, style)
			s.text(row, leftWidth-9, 7, fmt.Sprintf("%6s", formatCount(item.count)), style)
		} else {
			s.text(row, 3, nameWidth+1, marker+truncate(item.name, max(1, nameWidth-1)), style)
			s.text(row, 5+nameWidth, max(0, leftWidth-nameWidth-6), truncate(item.description, max(1, leftWidth-nameWidth-6)), style)
		}
	}
	if len(filtered) == 0 {
		s.text(10, 4, leftWidth-6, "No matches. Esc clears your search.", muted)
	}
	if len(filtered) > 0 {
		item := items[filtered[state.selected]]
		if split {
			b.preview(s, 7, leftWidth+3, panelHeight, previewWidth, item, categories)
		} else {
			preview := item.description
			if item.command != nil {
				preview = b.commandLine(*item.command) + "  ·  " + item.description
			}
			s.text(s.height-3, 2, s.width-4, preview, muted)
		}
	}
	footer := "Enter Run   Ctrl+E Arguments   Tab Details   / Search   Esc Back"
	if categories {
		footer = "Enter Open   / Search all commands   Type to filter   q Quit"
	}
	position := "No matches"
	if len(filtered) > 0 {
		position = fmt.Sprintf("%s / %s", formatCount(state.selected+1), formatCount(len(filtered)))
	}
	secondary := position + "  ·  ↑ ↓ Navigate  ·  Ctrl+C Quit"
	if b.status != "" {
		secondary += "  ·  " + b.status
	}
	s.footer(footer, secondary)
	s.flush()
}

func (b *browser) preview(s *surface, y, x, height, width int, item listItem, category bool) {
	s.box(y, x, height, width, "PREVIEW")
	row := y + 2
	s.text(row, x+2, width-4, item.name, accent)
	row += 2
	for _, line := range wrapText(item.description, width-4) {
		if row >= y+7 {
			break
		}
		s.text(row, x+2, width-4, line, "")
		row++
	}
	row++
	if category {
		s.text(row, x+2, width-4, formatCount(item.count)+" installed commands", muted)
		row += 2
		s.text(row, x+2, width-4, "EXPLORE", muted)
		row++
		for i, c := range b.rules.group(b.commands, item.category) {
			if i >= 6 || row >= y+height-2 {
				break
			}
			s.text(row, x+2, width-4, "  "+c.Name, headingStyle)
			row++
		}
		return
	}
	if item.command == nil {
		return
	}
	command := *item.command
	s.text(row, x+2, width-4, "COMMAND", muted)
	row++
	for _, line := range wrapText(b.commandLine(command), width-4) {
		if row >= y+height-7 {
			break
		}
		s.text(row, x+2, width-4, line, headingStyle)
		row++
	}
	row++
	if row < y+height-6 {
		s.text(row, x+2, width-4, command.Source, muted)
		row++
	}
	if command.Package != "" && row < y+height-6 {
		s.text(row, x+2, width-4, command.Package, muted)
	}
	for i, line := range []string{"Enter   Run now", "Ctrl+E  Edit arguments", "Tab     Command details"} {
		if y+height-4+i > row {
			s.text(y+height-4+i, x+2, width-4, line, accent)
		}
	}
}

func (b *browser) renderDetails(command Command) {
	s := newSurface()
	s.header(b.rules.categories[command.Category].Title+" / "+command.Name, len(b.commands))
	s.box(3, 1, max(3, s.height-7), s.width-2, "COMMAND DETAILS")
	row := 5
	s.text(row, 4, s.width-8, command.Name, accent)
	row += 2
	for _, line := range wrapText(command.Description, max(10, s.width-8)) {
		if row >= s.height-10 {
			break
		}
		s.text(row, 4, s.width-8, line, "")
		row++
	}
	row++
	for _, line := range []string{b.commandLine(command), "", "Path     " + command.Path, "Source   " + command.Source, "Package  " + command.Package} {
		if row < s.height-7 {
			s.text(row, 4, s.width-8, line, muted)
			row++
		}
	}
	row++
	if row < s.height-6 {
		s.text(row, 4, s.width-8, "Similar commands", accent)
		row++
	}
	for _, peer := range b.rules.group(b.commands, command.Category) {
		if peer.Name != command.Name && row < s.height-5 {
			s.text(row, 4, s.width-8, fmt.Sprintf("%-24s %s", peer.Name, peer.Description), "")
			row++
		}
	}
	s.footer("Enter Run   a Arguments   h Help   Esc Back", "Arguments are remembered during this session.")
	s.flush()
}

func (b *browser) renderArguments(command Command, text string, cursor int, message string) {
	s := newSurface()
	s.header("Run "+command.Name, len(b.commands))
	width := max(8, min(88, s.width-4))
	x := max(1, (s.width-width)/2)
	y := max(3, (s.height-14)/2)
	s.box(y, x, 12, width, "RUN WITH ARGUMENTS")
	s.text(y+2, x+3, width-6, command.Name, accent)
	s.text(y+3, x+3, width-6, "Add arguments, then press Enter to run.", muted)
	display := []rune(text)
	visible := max(1, width-11)
	start := max(0, cursor-visible+1)
	before := string(display[start:cursor])
	after := string(display[cursor:])
	s.text(y+5, x+3, width-6, "> "+before+"▏"+after, headingStyle)
	s.text(y+7, x+3, width-6, "$ "+command.Name+" "+text, muted)
	if message != "" {
		s.text(y+9, x+3, width-6, message, "33")
	} else {
		s.text(y+9, x+3, width-6, "Example: --help  or  \"a path with spaces\"", muted)
	}
	s.footer("Enter Run   Esc Cancel   Ctrl+U Clear", "← → Move cursor   Home / End   Backspace / Delete")
	s.flush()
}
