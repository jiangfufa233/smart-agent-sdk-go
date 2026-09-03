// Package skill loads SKILL.md style skills from disk and exposes them as
// tools, following the progressive-disclosure pattern: the model sees only
// each skill's name and description, and invoking the tool returns the full
// skill body.
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
)

// FileName is the conventional skill manifest file name.
const FileName = "SKILL.md"

// Skill is a parsed SKILL.md manifest.
type Skill struct {
	Name        string
	Description string
	Body        string // markdown instructions returned on invocation
	Path        string
}

// LoadDir loads every skill found under dir. It accepts both a directory
// containing a SKILL.md directly and a directory whose immediate
// subdirectories each contain a SKILL.md.
func LoadDir(dir string) ([]*Skill, error) {
	var skills []*Skill

	if direct := filepath.Join(dir, FileName); fileExists(direct) {
		s, err := LoadFile(direct)
		if err != nil {
			return nil, err
		}
		skills = append(skills, s)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("skill: read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name(), FileName)
		if !fileExists(p) {
			continue
		}
		s, err := LoadFile(p)
		if err != nil {
			return nil, err
		}
		skills = append(skills, s)
	}
	return skills, nil
}

// LoadFile parses a single SKILL.md file with optional `---` delimited
// frontmatter (minimal `key: value` pairs; supports name and description).
func LoadFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("skill: read %s: %w", path, err)
	}
	name, desc, body := parseFrontmatter(string(data))
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	return &Skill{Name: name, Description: desc, Body: body, Path: path}, nil
}

// Tool exposes the skill as a tool that returns its full body.
func (s *Skill) Tool() (tool.Tool, error) {
	body := s.Body
	desc := s.Description
	if desc == "" {
		desc = "Load the instructions of the " + s.Name + " skill."
	}
	return tool.NewFunction(s.Name, desc, func(_ struct{}) (string, error) {
		return body, nil
	})
}

func parseFrontmatter(s string) (name, desc, body string) {
	if !strings.HasPrefix(s, "---") {
		return "", "", strings.TrimSpace(s)
	}
	rest := strings.TrimPrefix(s, "---")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", "", strings.TrimSpace(s)
	}
	header := rest[:end]
	body = strings.TrimLeft(rest[end+len("\n---"):], "-\n ")
	for _, line := range strings.Split(header, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "name":
			name = strings.TrimSpace(v)
		case "description":
			desc = strings.TrimSpace(v)
		}
	}
	return name, desc, strings.TrimSpace(body)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
