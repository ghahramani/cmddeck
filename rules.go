package main

import (
	_ "embed"
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed rules.json
var ruleData []byte

type Category struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}
type PatternGroup struct {
	Category string   `json:"category"`
	Patterns []string `json:"patterns"`
}
type DescriptionPattern struct {
	Category string `json:"category"`
	Pattern  string `json:"pattern"`
	compiled *regexp.Regexp
}
type Rules struct {
	Categories          []Category           `json:"categories"`
	BasePackages        []string             `json:"base_packages"`
	BaseNames           []string             `json:"base_names"`
	CommandGroups       map[string]string    `json:"command_groups"`
	Exact               map[string]string    `json:"exact"`
	Descriptions        map[string]string    `json:"descriptions"`
	CommandPatterns     []PatternGroup       `json:"command_patterns"`
	PackagePatterns     []PatternGroup       `json:"package_patterns"`
	DescriptionPatterns []DescriptionPattern `json:"description_patterns"`
	categories          map[string]Category
	basePackages        map[string]bool
	baseNames           map[string]bool
}

func loadRules() *Rules {
	var rules Rules
	if err := json.Unmarshal(ruleData, &rules); err != nil {
		panic(err)
	}
	rules.categories = make(map[string]Category)
	rules.basePackages = make(map[string]bool)
	rules.baseNames = make(map[string]bool)
	for _, c := range rules.Categories {
		rules.categories[c.ID] = c
	}
	for _, name := range rules.BasePackages {
		rules.basePackages[name] = true
	}
	for _, name := range rules.BaseNames {
		rules.baseNames[name] = true
	}
	for i := range rules.DescriptionPatterns {
		rules.DescriptionPatterns[i].compiled = regexp.MustCompile("(?i)" + rules.DescriptionPatterns[i].Pattern)
	}
	return &rules
}

func matchPatterns(name string, groups []PatternGroup) string {
	for _, group := range groups {
		for _, pattern := range group.Patterns {
			if ok, _ := filepath.Match(pattern, name); ok {
				return group.Category
			}
		}
	}
	return ""
}

func (r *Rules) classify(name, pkg, description string) (string, string) {
	if category, ok := r.Exact[name]; ok {
		return category, "command rule"
	}
	if category := matchPatterns(name, r.CommandPatterns); category != "" {
		return category, "command family"
	}
	normalized := strings.TrimPrefix(pkg, "lib32-")
	for _, group := range r.PackagePatterns {
		for _, pattern := range group.Patterns {
			a, _ := filepath.Match(pattern, pkg)
			b, _ := filepath.Match(pattern, normalized)
			if a || b {
				return group.Category, "package rule"
			}
		}
	}
	for _, rule := range r.DescriptionPatterns {
		if rule.compiled.MatchString(description) {
			return rule.Category, "description match"
		}
	}
	if strings.HasPrefix(pkg, "python-") {
		return "python", "Python package"
	}
	if strings.HasPrefix(pkg, "perl-") {
		return "languages", "Perl package"
	}
	return "other", "unclassified"
}

func (r *Rules) baseline(name, pkg string) bool {
	_, known := r.Exact[name]
	return !known && (r.basePackages[pkg] || r.baseNames[name])
}

func (r *Rules) categoryMatch(query string) string {
	query = strings.ToLower(strings.TrimSpace(query))
	if _, ok := r.categories[query]; ok {
		return query
	}
	aliases := map[string]string{"monitoring": "monitor", "networking": "network", "gpu monitoring": "monitor",
		"package": "packages", "shell": "terminal", "vm": "virtualization", "kubernetes": "cloud",
		"js": "javascript", "java": "jvm", "c++": "build"}
	if id := aliases[query]; id != "" {
		return id
	}
	found := ""
	for _, c := range r.Categories {
		if strings.Contains(strings.ToLower(c.Title), query) {
			if found != "" {
				return ""
			}
			found = c.ID
		}
	}
	return found
}

func (r *Rules) group(commands []Command, category string) []Command {
	result := []Command{}
	rank := map[string]int{}
	preferred := strings.Fields(r.CommandGroups[category])
	for i, name := range preferred {
		rank[name] = i
	}
	for _, c := range commands {
		if c.Category == category {
			result = append(result, c)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, ok := rank[result[i].Name]
		if !ok {
			a = len(preferred)
		}
		b, ok := rank[result[j].Name]
		if !ok {
			b = len(preferred)
		}
		if a != b {
			return a < b
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}

func (r *Rules) haystack(c Command) string {
	category := r.categories[c.Category]
	return strings.ToLower(strings.Join([]string{c.Name, c.Description, c.Package, c.Source, c.Category, category.Title, category.Description}, " "))
}

func matchesQuery(text, query string) bool {
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

func (r *Rules) search(commands []Command, query string) []Command {
	result := []Command{}
	for _, c := range commands {
		if matchesQuery(r.haystack(c), query) {
			result = append(result, c)
		}
	}
	query = strings.ToLower(query)
	score := func(name string) int {
		name = strings.ToLower(name)
		if name == query {
			return 0
		}
		if strings.HasPrefix(name, query) {
			return 1
		}
		return 2
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := score(result[i].Name), score(result[j].Name)
		if a != b {
			return a < b
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}
