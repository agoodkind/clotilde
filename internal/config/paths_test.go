package config_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/util"
)

var _ = Describe("Path helpers", func() {
	It("should construct sessions directory path", func() {
		root := "/project/.claude/clyde"
		path := config.GetSessionsDir(root)
		Expect(path).To(Equal("/project/.claude/clyde/sessions"))
	})

	It("should construct session directory path", func() {
		root := "/project/.claude/clyde"
		path := config.GetSessionDir(root, "my-session")
		Expect(path).To(Equal("/project/.claude/clyde/sessions/my-session"))
	})
})

var _ = Describe("ProjectRootFromPath", func() {
	var tempDir string

	BeforeEach(func() {
		tempDir = GinkgoT().TempDir()
	})

	It("should find project root with .claude directory", func() {
		claudeDir := filepath.Join(tempDir, ".claude")
		err := util.EnsureDir(claudeDir)
		Expect(err).NotTo(HaveOccurred())

		root := config.ProjectRootFromPath(tempDir)
		Expect(root).To(Equal(tempDir))
	})

	It("should find project root from nested path", func() {
		claudeDir := filepath.Join(tempDir, ".claude")
		err := util.EnsureDir(claudeDir)
		Expect(err).NotTo(HaveOccurred())

		nestedDir := filepath.Join(tempDir, "nested", "deep")
		err = util.EnsureDir(nestedDir)
		Expect(err).NotTo(HaveOccurred())

		root := config.ProjectRootFromPath(nestedDir)
		Expect(root).To(Equal(tempDir))
	})

	It("should return start path if no .claude directory found", func() {
		root := config.ProjectRootFromPath(tempDir)
		Expect(root).To(Equal(tempDir))
	})

	It("should not walk above $HOME to find .claude", func() {
		// Use tempDir as a fake $HOME with .claude/ in it
		GinkgoT().Setenv("HOME", tempDir)

		claudeDir := filepath.Join(tempDir, ".claude")
		err := util.EnsureDir(claudeDir)
		Expect(err).NotTo(HaveOccurred())

		subDir := filepath.Join(tempDir, "projects", "myapp")
		err = util.EnsureDir(subDir)
		Expect(err).NotTo(HaveOccurred())

		// ProjectRootFromPath should NOT treat $HOME as the project root
		// even though ~/.claude/ exists
		root := config.ProjectRootFromPath(subDir)
		Expect(root).To(Equal(subDir))
		Expect(root).NotTo(Equal(tempDir))
	})
})
