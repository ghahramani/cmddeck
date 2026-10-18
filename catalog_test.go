package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func testOptions(t *testing.T) ScanOptions {
	t.Helper()
	root := t.TempDir()
	options := ScanOptions{filepath.Join(root, "bin"), filepath.Join(root, "db"), []string{filepath.Join(root, "man")}}
	if err := os.MkdirAll(options.Path, 0755); err != nil {
		t.Fatal(err)
	}
	return options
}

func TestDiscoveryPreservesPATHPrecedenceAndDeduplicatesDirectories(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	writeTestFile(t, filepath.Join(first, "sample"), "#!/bin/sh\nexit 0", 0755)
	writeTestFile(t, filepath.Join(second, "sample"), "#!/bin/sh\nexit 1", 0755)
	writeTestFile(t, filepath.Join(second, "extra"), "#!/bin/sh\nexit 1", 0755)
	writeTestFile(t, filepath.Join(first, "not-executable"), "hello", 0644)
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "absent"), filepath.Join(first, "broken")); err != nil {
		t.Fatal(err)
	}
	directories := pathDirectories(strings.Join([]string{first, alias, second}, ":"))
	if len(directories) != 2 {
		t.Fatalf("directories: %v", directories)
	}
	paths := executablePaths(directories)
	if len(paths) != 2 || paths["sample"] != filepath.Join(first, "sample") {
		t.Fatalf("commands: %v", paths)
	}
}

func TestScannerDoesNotRunTools(t *testing.T) {
	options := testOptions(t)
	marker := filepath.Join(filepath.Dir(options.Path), "executed")
	for _, name := range []string{"htop", "local-unknown", "cat"} {
		writeTestFile(t, filepath.Join(options.Path, name), "#!/bin/sh\ntouch "+marker, 0755)
	}
	commands := scan(loadRules(), options)
	if len(commands) != 3 {
		t.Fatalf("got %d commands", len(commands))
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("scan ran an executable")
	}
	for _, c := range commands {
		if c.Name == "htop" && c.Category != "monitor" {
			t.Fatal(c)
		}
		if c.Name == "local-unknown" && c.Category != "other" {
			t.Fatal(c)
		}
		if c.Name == "cat" && !c.Basic {
			t.Fatal("base command was not marked basic")
		}
	}
}

func TestPacmanMetadataIncludesDependencyCommandsAndSDKSubdirectories(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "library-1")
	writeTestFile(t, filepath.Join(pkg, "desc"), "%NAME%\nlibrary\n\n%DESC%\nImage tools\n\n%REASON%\n1\n", 0644)
	writeTestFile(t, filepath.Join(pkg, "files"), "%FILES%\nusr/bin/\nusr/bin/tool\nusr/bin/core_perl/helper\nopt/cuda/bin/nvcc\nusr/share/unrelated\n", 0644)
	index := packageIndex(root)
	if len(index) != 3 || index["/usr/bin/tool"].Package != "library" || index["/usr/bin/core_perl/helper"].Description != "Image tools" {
		t.Fatalf("index: %v", index)
	}
}

func TestManPageDescriptionPlainAndGzip(t *testing.T) {
	root := t.TempDir()
	text := ".TH EXAMPLE 1\n.SH NAME\nexample \\- inspect images\n.SH OPTIONS\nnever execute me\n"
	writeTestFile(t, filepath.Join(root, "man1/plain.1"), text, 0644)
	file, err := os.Create(filepath.Join(root, "man1/compressed.1.gz"))
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	_, _ = writer.Write([]byte(text))
	_ = writer.Close()
	_ = file.Close()
	for _, name := range []string{"plain", "compressed"} {
		if got := manDescription(name, []string{root}); got != "inspect images" {
			t.Fatalf("%s: %q", name, got)
		}
	}
}

func TestRulesAndBaselineFiltering(t *testing.T) {
	rules := loadRules()
	cases := [][3]string{{"nvtop", "", "monitor"}, {"kimi", "", "ai"}, {"kubectl-ctx", "", "cloud"},
		{"cargo-tauri", "", "rust"}, {"pgmtoppm", "netpbm", "images"}, {"clang-check", "clang", "build"},
		{"pyaml", "python-pyaml", "python"}, {"org.libretro.RetroArch", "", "games"}, {"widget", "qt6-tools", "ui-dev"}}
	for _, c := range cases {
		if category, _ := rules.classify(c[0], c[1], ""); category != c[2] {
			t.Errorf("%s: %s != %s", c[0], category, c[2])
		}
	}
	if !rules.baseline("cat", "coreutils") || !rules.baseline("mount", "util-linux") {
		t.Fatal("standard utilities visible")
	}
	for _, name := range []string{"top", "free", "watch", "ps", "uptime"} {
		if rules.baseline(name, "procps-ng") {
			t.Errorf("useful peer hidden: %s", name)
		}
	}
}

func TestSearchAndGrouping(t *testing.T) {
	rules := loadRules()
	commands := []Command{{Name: "nvtop", Category: "monitor", Description: "GPU process monitor"}, {Name: "htop", Category: "monitor", Description: "CPU processes"}}
	if got := rules.search(commands, "gpu monitor"); len(got) != 2 {
		t.Fatal(got)
	}
	if got := rules.search(commands, "nvtop"); len(got) != 1 || got[0].Name != "nvtop" {
		t.Fatal(got)
	}
	if got := rules.group(commands, "monitor"); got[0].Name != "htop" {
		t.Fatal(got)
	}
	if rules.categoryMatch("monitoring") != "monitor" || rules.categoryMatch("java") != "jvm" {
		t.Fatal("category alias failed")
	}
}

func TestConfigOverridesAndExclusions(t *testing.T) {
	rules := loadRules()
	options := testOptions(t)
	writeTestFile(t, filepath.Join(options.Path, "sample"), "x", 0755)
	writeTestFile(t, filepath.Join(options.Path, "hidden"), "x", 0755)
	config := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, config, `{"exclude":["hidden"],"commands":{"sample":{"category":"ai","description":"My assistant"}}}`, 0644)
	commands, err := inventory(rules, options, config, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Category != "ai" || commands[0].Description != "My assistant" {
		t.Fatal(commands)
	}
}

func TestInvalidConfigGivesErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, input := range []string{`[]`, `null`, `{"exclude":"wrong"}`, `{"commands":[]}`, `{"commands":{"nvtop":{"category":"nonsense"}}}`} {
		writeTestFile(t, path, input, 0644)
		if _, err := loadConfig(path, loadRules()); err == nil {
			t.Errorf("accepted invalid config: %s", input)
		}
	}
}

func TestCacheReuseInvalidationAndCorruptRecovery(t *testing.T) {
	rules := loadRules()
	options := testOptions(t)
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	config := filepath.Join(root, "absent.json")
	writeTestFile(t, filepath.Join(options.Path, "nvtop"), "x", 0755)
	first, err := inventory(rules, options, config, true, false)
	if err != nil {
		t.Fatal(err)
	}
	path := xdgPath("XDG_CACHE_HOME", ".cache", "inventory-go.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cache Cache
	if err = json.Unmarshal(original, &cache); err != nil {
		t.Fatal(err)
	}
	cache.Commands[0].Description = "Cached sentinel"
	writeCache(path, cache)
	second, err := inventory(rules, options, config, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Description != "Cached sentinel" {
		t.Fatal("did not reuse cache")
	}
	writeTestFile(t, filepath.Join(options.Path, "htop"), "x", 0755)
	// Make invalidation independent of filesystem timestamp resolution.
	later := time.Now().Add(time.Second)
	_ = os.Chtimes(options.Path, later, later)
	third, err := inventory(rules, options, config, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(third) != 2 {
		t.Fatal("PATH change did not invalidate cache")
	}
	writeTestFile(t, path, "broken json", 0600)
	recovered, err := inventory(rules, options, config, true, false)
	if err != nil || len(recovered) != 2 {
		t.Fatal("corrupt cache recovery failed", err)
	}
}

func TestNoCacheWritesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-cache")
	t.Setenv("XDG_CACHE_HOME", root)
	_, err := inventory(loadRules(), testOptions(t), filepath.Join(t.TempDir(), "absent.json"), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("no-cache wrote files")
	}
}

func TestArgumentParsingDoesNotEvaluateShellSyntax(t *testing.T) {
	input := `one "two words" '' '$(touch marker)' ';' a\ b`
	want := []string{"one", "two words", "", "$(touch marker)", ";", "a b"}
	got, err := parseArguments(input)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	for _, bad := range []string{`"unfinished`, `trailing\`} {
		if _, err := parseArguments(bad); err == nil {
			t.Fatal("accepted unfinished arguments")
		}
	}
}

func TestCLIJSONSearchAndFlagsAfterCategory(t *testing.T) {
	options := testOptions(t)
	t.Setenv("PATH", options.Path)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"nvtop", "htop", "cat", "local-helper"} {
		writeTestFile(t, filepath.Join(options.Path, name), "x", 0755)
	}
	var out, stderr bytes.Buffer
	code := runCLI([]string{"--no-cache", "--json"}, &out, &stderr)
	var commands []Command
	if code != 0 || json.Unmarshal(out.Bytes(), &commands) != nil || len(commands) != 3 {
		t.Fatal(code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code = runCLI([]string{"monitor", "--list", "--no-cache"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "nvtop") || strings.Contains(out.String(), "local-helper") {
		t.Fatal(code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code = runCLI([]string{"--search", "not-installed", "--list", "--no-cache"}, &out, &stderr); code != 1 {
		t.Fatal("missing search should return 1", code)
	}
}

func TestEmbeddedRulesNeedNoSidecarFiles(t *testing.T) {
	rules := loadRules()
	if len(rules.Categories) < 35 || len(rules.Exact) < 300 {
		t.Fatal("classification rules missing from binary")
	}
	if strings.Contains(cleanText("name\x1b[31m\nnext"), "\x1b") || strings.Contains(safeLine("name\x1b", 80), "\x1b") {
		t.Fatal("terminal controls were not sanitized")
	}
}
