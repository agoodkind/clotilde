package session

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Resolve tier 4 (transparent adoption)", func() {
	var (
		clydeRoot    string
		projectsRoot string
		projDir      string
		store        *FileStore
	)

	const uuid = "22a95bc5-eb24-4302-8f2f-2253bc587cb5"

	BeforeEach(func() {
		clydeRoot = GinkgoT().TempDir()
		projectsRoot = GinkgoT().TempDir()
		projDir = filepath.Join(projectsRoot, "-Users-agoodkind-Sites-tack")
		Expect(os.MkdirAll(projDir, 0o755)).To(Succeed())

		Expect(os.MkdirAll(filepath.Join(clydeRoot, "sessions"), 0o755)).To(Succeed())

		store = &FileStore{
			clydeRoot:      clydeRoot,
			discoveryCache: newDiscoveryCache([]DiscoveryScanner{newClaudeDiscoveryScanner(projectsRoot)}, 0),
		}
	})

	writeTranscript := func(id, customTitle string) {
		body := ""
		if customTitle != "" {
			body += `{"type":"custom-title","customTitle":"` + customTitle + `","sessionId":"` + id + `"}` + "\n"
		}
		body += `{"type":"system","timestamp":"2026-04-12T23:52:12Z","entrypoint":"cli","cwd":"/Users/agoodkind/Sites/tack","sessionId":"` + id + `"}` + "\n"
		path := filepath.Join(projDir, id+".jsonl")
		Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
	}

	It("adopts by sanitized customTitle on tier-1 miss", func() {
		writeTranscript(uuid, "2026-04-12-merry-swan")

		sess, err := store.Resolve("2026-04-12-merry-swan")
		Expect(err).ToNot(HaveOccurred())
		Expect(sess).ToNot(BeNil())
		Expect(sess.Name).To(Equal("2026-04-12-merry-swan"))
		Expect(sess.Metadata.SessionID).To(Equal(uuid))
		Expect(sess.Metadata.DisplayTitle).To(Equal("2026-04-12-merry-swan"))

		metaPath := filepath.Join(clydeRoot, "sessions", "2026-04-12-merry-swan", "metadata.json")
		_, statErr := os.Stat(metaPath)
		Expect(statErr).ToNot(HaveOccurred(), "metadata.json should be written at %s", metaPath)
	})

	It("adopts by bare UUID and uses the sanitized title as Name", func() {
		writeTranscript(uuid, "2026-04-12-merry-swan")

		sess, err := store.Resolve(uuid)
		Expect(err).ToNot(HaveOccurred())
		Expect(sess).ToNot(BeNil())
		Expect(sess.Name).To(Equal("2026-04-12-merry-swan"))
		Expect(sess.Metadata.SessionID).To(Equal(uuid))
	})

	It("falls back to workspace-plus-UUID when customTitle is absent", func() {
		writeTranscript(uuid, "")

		sess, err := store.Resolve(uuid)
		Expect(err).ToNot(HaveOccurred())
		Expect(sess).ToNot(BeNil())
		Expect(sess.Name).To(ContainSubstring("tack"))
		Expect(sess.Name).To(ContainSubstring(uuid[:8]))
		Expect(sess.Metadata.DisplayTitle).To(Equal(""))
	})

	It("falls back to workspace-plus-UUID when customTitle sanitizes to empty", func() {
		writeTranscript(uuid, "🙂🎉")

		sess, err := store.Resolve(uuid)
		Expect(err).ToNot(HaveOccurred())
		Expect(sess).ToNot(BeNil())
		Expect(sess.Name).To(ContainSubstring("tack"))
		Expect(sess.Name).To(ContainSubstring(uuid[:8]))
	})

	It("returns nil on no match without error", func() {
		sess, err := store.Resolve("never-existed")
		Expect(err).ToNot(HaveOccurred())
		Expect(sess).To(BeNil())
	})

	It("is idempotent: second resolve for the same name hits tier 1", func() {
		writeTranscript(uuid, "merry-swan")

		first, err := store.Resolve("merry-swan")
		Expect(err).ToNot(HaveOccurred())
		Expect(first).ToNot(BeNil())

		second, err := store.Resolve("merry-swan")
		Expect(err).ToNot(HaveOccurred())
		Expect(second).ToNot(BeNil())
		Expect(second.Name).To(Equal(first.Name))
		Expect(second.Metadata.SessionID).To(Equal(first.Metadata.SessionID))
	})

	It("skips tier 4 when the store is constructed read-only", func() {
		writeTranscript(uuid, "merry-swan")
		readOnly := &FileStore{clydeRoot: clydeRoot, noAdopt: true}

		sess, err := readOnly.Resolve("merry-swan")
		Expect(err).ToNot(HaveOccurred())
		Expect(sess).To(BeNil(), "read-only store must not adopt even when a matching transcript exists")
	})
})
