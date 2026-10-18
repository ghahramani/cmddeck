package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

var version = "2.1.1"

const appName = "cmddeck"

type Options struct {
	List, All, JSON, Stats, Refresh, NoCache, Version bool
	Search, Config, Category                          string
	hasSearch                                         bool
}

func parseOptions(args []string, stderr io.Writer) (Options, error) {
	var options Options
	flags := flag.NewFlagSet(appName, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&options.List, "list", false, "print results instead of opening the browser")
	flags.BoolVar(&options.List, "l", false, "alias for --list")
	flags.BoolVar(&options.All, "all", false, "also include basic system commands")
	flags.BoolVar(&options.All, "a", false, "alias for --all")
	flags.BoolVar(&options.JSON, "json", false, "export matching commands as JSON")
	flags.BoolVar(&options.Stats, "stats", false, "show inventory and category counts")
	flags.BoolVar(&options.Refresh, "refresh", false, "rescan immediately")
	flags.BoolVar(&options.NoCache, "no-cache", false, "do not read or write the cache")
	flags.BoolVar(&options.Version, "version", false, "show version")
	flags.StringVar(&options.Search, "search", "", "search names, descriptions, packages and categories")
	flags.StringVar(&options.Search, "s", "", "alias for --search")
	flags.StringVar(&options.Config, "config", "", "load a custom JSON config")
	flags.Usage = func() {
		fmt.Fprint(stderr, "Usage: cmddeck [category] [options]\n\nBrowse installed commands by purpose. Basic Unix commands are hidden by default.\n\n")
		flags.PrintDefaults()
		fmt.Fprintln(stderr, "\nExamples: cmddeck | cmddeck monitor | cmddeck --list network | cmddeck --search gpu")
	}
	// Keep common invocations such as 'cmddeck monitor --list' working.
	normalized, positional := []string{}, []string{}
	for i := 0; i < len(args); i++ {
		argument := args[i]
		if argument == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(argument, "-") {
			normalized = append(normalized, argument)
			name, _, hasValue := strings.Cut(strings.TrimLeft(argument, "-"), "=")
			if name == "search" || name == "s" {
				options.hasSearch = true
			}
			if (name == "search" || name == "s" || name == "config") && !hasValue && i+1 < len(args) {
				i++
				normalized = append(normalized, args[i])
			}
		} else {
			positional = append(positional, argument)
		}
	}
	normalized = append(normalized, "--")
	normalized = append(normalized, positional...)
	if err := flags.Parse(normalized); err != nil {
		return options, err
	}
	if flags.NArg() > 1 {
		return options, fmt.Errorf("expected one category or search phrase; quote phrases containing spaces")
	}
	if flags.NArg() == 1 {
		options.Category = flags.Arg(0)
	}
	if options.Category != "" && options.hasSearch {
		return options, fmt.Errorf("use a category/search argument or --search, not both")
	}
	return options, nil
}

func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= width {
		return text
	}
	runes := []rune(text)
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func printCategories(output io.Writer, r *Rules, commands []Command) {
	counts := map[string]int{}
	for _, c := range commands {
		counts[c.Category]++
	}
	for _, category := range r.Categories {
		if counts[category.ID] > 0 {
			fmt.Fprintf(output, "  %-16s %5d  %s\n", category.ID, counts[category.ID], category.Title)
		}
	}
}

func printCommands(output io.Writer, commands []Command, categoryLabels bool) {
	width := 12
	for _, c := range commands {
		if n := utf8.RuneCountInString(c.Name); n > width && n < 46 {
			width = n
		}
	}
	for _, c := range commands {
		label := ""
		if categoryLabels {
			label = " [" + c.Category + "]"
		}
		fmt.Fprintf(output, "  %-*s%s  %s\n", width, cleanText(c.Name), label, cleanText(c.Description))
	}
}

func runCLI(args []string, output, stderr io.Writer) int {
	options, err := parseOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "cmddeck:", err)
		return 2
	}
	if options.Version {
		fmt.Fprintln(output, "cmddeck "+version+" (Go, standalone)")
		return 0
	}
	rules := loadRules()
	config := options.Config
	if config == "" {
		config = defaultConfigPath()
	}
	allCommands, err := inventory(rules, defaultScanOptions(), config, !options.NoCache, options.Refresh)
	if err != nil {
		fmt.Fprintln(stderr, "cmddeck:", err)
		return 2
	}
	commands := make([]Command, 0, len(allCommands))
	hidden := 0
	for _, c := range allCommands {
		if c.Basic {
			hidden++
		}
		if options.All || !c.Basic {
			commands = append(commands, c)
		}
	}
	category, query := "", options.Search
	hasQuery := options.hasSearch
	if options.Category != "" {
		category = rules.categoryMatch(options.Category)
		if category == "" {
			query, hasQuery = options.Category, true
		}
	}
	matches := commands
	if category != "" {
		matches = rules.group(commands, category)
	} else if hasQuery {
		matches = rules.search(commands, query)
	}
	if options.JSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(matches); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if options.Stats {
		fmt.Fprintf(output, "Discovered: %d executable commands in PATH\n", len(allCommands))
		if options.All {
			fmt.Fprintln(output, "Basic system commands included")
		} else {
			fmt.Fprintf(output, "Basic system commands hidden: %d\n", hidden)
		}
		fmt.Fprintf(output, "Showing: %d commands\n", len(matches))
		printCategories(output, rules, matches)
		return 0
	}
	plain := options.List || !isTerminal(os.Stdin.Fd()) || !isTerminal(os.Stdout.Fd())
	if plain {
		if category == "" && !hasQuery {
			printCategories(output, rules, matches)
			fmt.Fprintln(output, "\nRun cmddeck in a terminal to browse, or cmddeck --list <category>.")
		} else {
			if len(matches) == 0 {
				fmt.Fprintln(output, "No matching installed commands.")
				return 1
			}
			if category != "" {
				fmt.Fprintln(output, rules.categories[category].Title)
			} else {
				fmt.Fprintln(output, "Search:", cleanText(query))
			}
			printCommands(output, matches, category == "")
		}
		return 0
	}
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		fmt.Fprintln(stderr, "cmddeck: set TERM to use interactive browsing, or use --list")
		return 2
	}
	if err := browse(rules, commands, category, query, hasQuery); err != nil {
		fmt.Fprintln(stderr, "cmddeck:", err)
		return 1
	}
	return 0
}

func main() { os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr)) }
