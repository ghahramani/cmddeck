package main

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
)

type Command struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Package     string `json:"package"`
	Source      string `json:"source"`
	Reason      string `json:"reason"`
	Basic       bool   `json:"basic"`
}
type Owner struct{ Package, Description string }
type Override struct {
	Category    *string `json:"category"`
	Description *string `json:"description"`
}
type Config struct {
	Exclude  []string            `json:"exclude"`
	Commands map[string]Override `json:"commands"`
}
type ScanOptions struct {
	Path, DBRoot string
	ManRoots     []string
}

func cleanText(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

func pacmanFields(text string) map[string][]string {
	fields := map[string][]string{}
	current := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "%") && strings.HasSuffix(line, "%") {
			current = strings.Trim(line, "%")
		} else if line != "" && current != "" {
			fields[current] = append(fields[current], line)
		}
	}
	return fields
}

func packageIndex(root string) map[string]Owner {
	index := map[string]Owner{}
	directories, _ := os.ReadDir(root)
	for _, directory := range directories {
		if !directory.IsDir() {
			continue
		}
		desc, err := os.ReadFile(filepath.Join(root, directory.Name(), "desc"))
		if err != nil {
			continue
		}
		files, err := os.ReadFile(filepath.Join(root, directory.Name(), "files"))
		if err != nil {
			continue
		}
		fields := pacmanFields(string(desc))
		owner := Owner{strings.Join(fields["NAME"], " "), strings.Join(fields["DESC"], " ")}
		for _, name := range pacmanFields(string(files))["FILES"] {
			if !strings.HasSuffix(name, "/") && (strings.HasPrefix(name, "bin/") || strings.HasPrefix(name, "sbin/") || strings.Contains(name, "/bin/") || strings.Contains(name, "/sbin/")) {
				index["/"+name] = owner
			}
		}
	}
	return index
}

func pathDirectories(path string) []string {
	directories := []string{}
	seen := map[string]bool{}
	// Unlike filepath.SplitList, an empty PATH component means the current directory.
	for _, part := range strings.Split(path, string(os.PathListSeparator)) {
		if part == "" {
			part = "."
		}
		absolute, err := filepath.Abs(part)
		if err != nil {
			continue
		}
		real, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			continue
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() || seen[real] {
			continue
		}
		seen[real] = true
		directories = append(directories, absolute)
	}
	return directories
}

func executablePaths(directories []string) map[string]string {
	commands := map[string]string{}
	for _, directory := range directories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if _, seen := commands[entry.Name()]; seen {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || syscall.Access(path, 1) != nil {
				continue
			}
			commands[entry.Name()] = path
		}
	}
	return commands
}

func commandSource(path, pkg string) string {
	if pkg != "" {
		return "pacman"
	}
	path = strings.ToLower(path)
	for _, item := range [][2]string{{"linuxbrew", "Homebrew"}, {"/cellar/", "Homebrew"}, {"/flatpak/", "Flatpak"},
		{"/.cargo/", "Cargo"}, {"/.sdkman/", "SDKMAN"}, {"/flutter/", "Flutter SDK"}, {"/go/bin/", "Go"},
		{"/pipx/", "pipx"}, {"/uv/tools/", "uv"}, {"/node_modules/", "npm"}} {
		if strings.Contains(path, item[0]) {
			return item[1]
		}
	}
	return "local / PATH"
}

var nameSection = regexp.MustCompile(`(?m)^\.SH\s+"?NAME"?\s*\n`)
var nextSection = regexp.MustCompile(`(?m)^\.SH`)
var roffFont = regexp.MustCompile(`\\f(?:\[[^]]*\]|\([^\n]{2}|.)`)
var roffLine = regexp.MustCompile(`(?m)^\.[A-Za-z]+\s*`)
var nameSeparator = regexp.MustCompile(`\s+(?:-|–|—)\s+`)

func manDescription(name string, roots []string) string {
	if strings.ContainsAny(name, "/\x00") {
		return ""
	}
	for _, root := range roots {
		for _, section := range []string{"1", "8", "6"} {
			for _, suffix := range []string{".gz", ""} {
				path := filepath.Join(root, "man"+section, name+"."+section+suffix)
				file, err := os.Open(path)
				if err != nil {
					continue
				}
				var reader io.Reader = file
				var compressed *gzip.Reader
				if suffix == ".gz" {
					compressed, err = gzip.NewReader(file)
					if err != nil {
						file.Close()
						continue
					}
					reader = compressed
				}
				data, err := io.ReadAll(io.LimitReader(reader, 32768))
				if compressed != nil {
					compressed.Close()
				}
				file.Close()
				if err != nil {
					continue
				}
				bounds := nameSection.FindIndex(data)
				if bounds == nil {
					continue
				}
				text := string(data[bounds[1]:])
				if end := nextSection.FindStringIndex(text); end != nil {
					text = text[:end[0]]
				}
				text = roffFont.ReplaceAllString(text, "")
				text = strings.NewReplacer(`\-`, "-", `\&`, "", `\e`, `\`).Replace(text)
				text = cleanText(roffLine.ReplaceAllString(text, ""))
				pieces := nameSeparator.Split(text, 2)
				if len(pieces) == 2 && pieces[1] != "" {
					return truncate(pieces[1], 300)
				}
			}
		}
	}
	return ""
}

func defaultScanOptions() ScanOptions {
	return ScanOptions{os.Getenv("PATH"), "/var/lib/pacman/local", []string{"/usr/share/man", "/usr/local/share/man", "/home/linuxbrew/.linuxbrew/share/man"}}
}

func scan(r *Rules, options ScanOptions) []Command {
	owners := packageIndex(options.DBRoot)
	paths := executablePaths(pathDirectories(options.Path))
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	commands := make([]Command, 0, len(names))
	type classification struct{ category, reason string }
	packages := map[string]classification{}
	for _, name := range names {
		path := paths[name]
		if (name == "apply_patch" || name == "applypatch" || name == "codex-linux-sandbox") && strings.Contains(path, "/.codex/") {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}
		owner, ok := owners[path]
		if !ok {
			owner = owners[resolved]
		}
		description := r.Descriptions[name]
		if description == "" {
			description = manDescription(name, options.ManRoots)
		}
		if description == "" {
			description = owner.Description
		}
		classificationText := owner.Description
		if classificationText == "" {
			classificationText = description
		}
		category, reason := "", ""
		// Shared package classifications are cached; exact command rules still win.
		if exact, ok := r.Exact[name]; ok {
			category, reason = exact, "command rule"
		} else if family := matchPatterns(name, r.CommandPatterns); family != "" {
			category, reason = family, "command family"
		} else if owner.Package != "" {
			value, ok := packages[owner.Package]
			if !ok {
				value.category, value.reason = r.classify("", owner.Package, classificationText)
				packages[owner.Package] = value
			}
			category, reason = value.category, value.reason
		} else {
			category, reason = r.classify(name, "", classificationText)
		}
		basic := r.baseline(name, owner.Package)
		if basic {
			category, reason = "base", "basic system utility"
		}
		if description == "" {
			description = "Local command; no installed description found"
		}
		commands = append(commands, Command{name, path, category, cleanText(description), owner.Package, commandSource(resolved, owner.Package), reason, basic})
	}
	return commands
}

func loadConfig(path string, r *Rules) (Config, error) {
	config := Config{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return config, nil
	}
	if err != nil {
		return config, fmt.Errorf("read config: %w", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "{") {
		return config, fmt.Errorf("config must be a JSON object: %s", path)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("invalid config %s: %w", path, err)
	}
	for name, override := range config.Commands {
		if override.Category != nil {
			if _, ok := r.categories[*override.Category]; !ok {
				return config, fmt.Errorf("unknown category %q for %s", *override.Category, name)
			}
		}
	}
	return config, nil
}

func xdgPath(variable, fallback, name string) string {
	root := os.Getenv(variable)
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, fallback)
	}
	return filepath.Join(root, appName, name)
}

func defaultConfigPath() string {
	current := xdgPath("XDG_CONFIG_HOME", ".config", "config.json")
	if _, err := os.Stat(current); os.IsNotExist(err) {
		legacy := filepath.Join(filepath.Dir(filepath.Dir(current)), "command-catalog", "config.json")
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
	}
	return current
}

func fingerprint(options ScanOptions, config string) string {
	hash := sha256.New()
	hash.Write(ruleData)
	fmt.Fprintln(hash, "go-cache-v2", options.Path)
	paths := append(pathDirectories(options.Path), options.DBRoot, config)
	for _, path := range paths {
		fmt.Fprintln(hash, path)
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintln(hash, info.ModTime().UnixNano(), info.Size())
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type Cache struct {
	Version   int       `json:"version"`
	Signature string    `json:"signature"`
	Created   time.Time `json:"created"`
	Commands  []Command `json:"commands"`
}

func writeCache(path string, cache Cache) {
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), "inventory-*.tmp")
	if err != nil {
		return
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	err = json.NewEncoder(file).Encode(cache)
	closeErr := file.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(temporary, path)
	}
}

func inventory(r *Rules, options ScanOptions, configPath string, cacheEnabled, refresh bool) ([]Command, error) {
	config, err := loadConfig(configPath, r)
	if err != nil {
		return nil, err
	}
	signature := fingerprint(options, configPath)
	cachePath := xdgPath("XDG_CACHE_HOME", ".cache", "inventory-go.json")
	var commands []Command
	if cacheEnabled && !refresh {
		data, err := os.ReadFile(cachePath)
		var cache Cache
		if err == nil && json.Unmarshal(data, &cache) == nil && cache.Version == 2 && cache.Signature == signature &&
			!cache.Created.After(time.Now()) && time.Since(cache.Created) < 6*time.Hour {
			valid := true
			for _, c := range cache.Commands {
				if _, ok := r.categories[c.Category]; !ok {
					valid = false
					break
				}
			}
			if valid && cache.Commands != nil {
				commands = cache.Commands
			}
		}
	}
	if commands == nil {
		commands = scan(r, options)
		if cacheEnabled {
			writeCache(cachePath, Cache{2, signature, time.Now(), commands})
		}
	}
	excluded := map[string]bool{}
	for _, name := range config.Exclude {
		excluded[name] = true
	}
	result := make([]Command, 0, len(commands))
	for _, c := range commands {
		if excluded[c.Name] {
			continue
		}
		if override, ok := config.Commands[c.Name]; ok {
			if override.Category != nil {
				c.Category, c.Reason, c.Basic = *override.Category, "user override", false
			}
			if override.Description != nil {
				c.Description = cleanText(*override.Description)
			}
		}
		result = append(result, c)
	}
	return result, nil
}

// parseArguments handles quoting without evaluating substitutions or shell syntax.
func parseArguments(text string) ([]string, error) {
	args := []string{}
	var token strings.Builder
	quote := rune(0)
	escaped, started := false, false
	for _, c := range text {
		if escaped {
			token.WriteRune(c)
			escaped, started = false, true
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				token.WriteRune(c)
			}
			started = true
			continue
		}
		if c == '\'' || c == '"' {
			quote, started = c, true
			continue
		}
		if unicode.IsSpace(c) {
			if started {
				args = append(args, token.String())
				token.Reset()
				started = false
			}
		} else {
			token.WriteRune(c)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("unfinished quote or escape in arguments")
	}
	if started {
		args = append(args, token.String())
	}
	return args, nil
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}
