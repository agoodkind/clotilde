package claude_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"goodkind.io/clyde/internal/providers/claude"
)

var _ = Describe("Paths", func() {
	Describe("ProjectDir", func() {
		It("should encode project path correctly", func() {
			clydeRoot := "/home/user/project/.claude/clyde"
			encoded := claude.ProjectDir(clydeRoot)
			Expect(encoded).To(Equal("-home-user-project"))
		})

		It("should handle paths with dots", func() {
			clydeRoot := "/home/user/my.project/.claude/clyde"
			encoded := claude.ProjectDir(clydeRoot)
			Expect(encoded).To(Equal("-home-user-my-project"))
		})

		It("should handle nested paths", func() {
			clydeRoot := "/home/user/projects/foo/bar/.claude/clyde"
			encoded := claude.ProjectDir(clydeRoot)
			Expect(encoded).To(Equal("-home-user-projects-foo-bar"))
		})
	})

	Describe("TranscriptPath", func() {
		It("should generate correct transcript path", func() {
			homeDir := "/home/user"
			clydeRoot := "/home/user/project/.claude/clyde"
			sessionID := "550e8400-e29b-41d4-a716-446655440000"

			path := claude.TranscriptPath(homeDir, clydeRoot, sessionID)
			expected := filepath.Join(homeDir, ".claude", "projects", "-home-user-project", sessionID+".jsonl")
			Expect(path).To(Equal(expected))
		})
	})
})
