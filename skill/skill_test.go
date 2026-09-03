package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDirSubdirectoryLayout(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "alpha", FileName),
		"---\nname: alpha\ndescription: First skill\n---\n# Steps\n1. Do it\n")
	write(t, filepath.Join(dir, "beta", FileName),
		"---\nname: beta\ndescription: Second skill\n---\nBody here\n")

	skills, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(skills))
	}
	if skills[0].Name != "alpha" || skills[0].Description != "First skill" {
		t.Fatalf("alpha parsed wrong: %+v", skills[0])
	}
	if skills[0].Body != "# Steps\n1. Do it" {
		t.Fatalf("body wrong: %q", skills[0].Body)
	}
}

func TestLoadDirDirectLayout(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, FileName), "Just a body, no frontmatter\n")

	skills, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 {
		t.Fatalf("skills = %d", len(skills))
	}
	if skills[0].Name != filepath.Base(dir) {
		t.Fatalf("missing frontmatter should fall back to dir name, got %q", skills[0].Name)
	}
	if !strings.Contains(skills[0].Body, "Just a body") {
		t.Fatalf("body wrong: %q", skills[0].Body)
	}
}

func TestLoadFileMissing(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "absent", FileName)); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSkillToolProgressiveDisclosure(t *testing.T) {
	s := &Skill{Name: "pdf", Description: "Extract PDFs", Body: "full instructions"}
	st, err := s.Tool()
	if err != nil {
		t.Fatal(err)
	}
	if st.Spec().Function.Description != "Extract PDFs" {
		t.Fatalf("spec description = %q", st.Spec().Function.Description)
	}
	body, err := st.Run(context.Background(), "{}")
	if err != nil {
		t.Fatal(err)
	}
	if body != "full instructions" {
		t.Fatalf("tool must return the full body, got %q", body)
	}
}

func TestSkillToolDefaultDescription(t *testing.T) {
	s := &Skill{Name: "pdf", Body: "b"}
	st, err := s.Tool()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Spec().Function.Description, "pdf") {
		t.Fatalf("default description should name the skill: %q", st.Spec().Function.Description)
	}
}
