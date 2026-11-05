// Package architecture holds invariant tests about the repository itself.
package architecture

import (
	"os"
	"path/filepath"
	"testing"
)

// Skills live once in .agents/skills (agent-agnostic). Claude Code only scans
// .claude/skills, so every skill must have a symlink there pointing back.
func TestEverySkillIsLinkedForClaude(t *testing.T) {
	root := filepath.Join("..", "..")
	skills, err := os.ReadDir(filepath.Join(root, ".agents", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range skills {
		if !s.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, ".agents", "skills", s.Name(), "SKILL.md")); err != nil {
			t.Errorf("%s: missing SKILL.md", s.Name())
		}
		link := filepath.Join(root, ".claude", "skills", s.Name())
		target, err := os.Readlink(link)
		if err != nil {
			t.Errorf("%s: .claude/skills/%s must be a symlink: %v", s.Name(), s.Name(), err)
			continue
		}
		if want := filepath.Join("..", "..", ".agents", "skills", s.Name()); target != want {
			t.Errorf("%s: symlink points to %q, want %q", s.Name(), target, want)
		}
	}
	claude, _ := os.ReadDir(filepath.Join(root, ".claude", "skills"))
	for _, c := range claude {
		if _, err := os.Stat(filepath.Join(root, ".agents", "skills", c.Name())); err != nil {
			t.Errorf(".claude/skills/%s has no canonical skill in .agents/skills", c.Name())
		}
	}
}
