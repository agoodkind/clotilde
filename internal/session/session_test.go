package session_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fgrehm/clotilde/internal/session"
)

var _ = Describe("Session", func() {
	Describe("NewSession", func() {
		It("should create a new session with given name and UUID", func() {
			name := "test-session"
			sessionID := "550e8400-e29b-41d4-a716-446655440000"

			s := session.NewSession(name, sessionID)

			Expect(s.Name).To(Equal(name))
			Expect(s.Metadata.Name).To(Equal(name))
			Expect(s.Metadata.SessionID).To(Equal(sessionID))
			Expect(s.Metadata.Created).To(BeTemporally("~", time.Now(), time.Second))
			Expect(s.Metadata.LastAccessed).To(BeTemporally("~", time.Now(), time.Second))
			Expect(s.Metadata.IsForkedSession).To(BeFalse())
			Expect(s.Metadata.ParentSession).To(BeEmpty())
		})
	})

	Describe("NewIncognitoSession", func() {
		It("should create an incognito session with IsIncognito set to true", func() {
			name := "incognito-session"
			sessionID := "550e8400-e29b-41d4-a716-446655440000"

			s := session.NewIncognitoSession(name, sessionID)

			Expect(s.Name).To(Equal(name))
			Expect(s.Metadata.Name).To(Equal(name))
			Expect(s.Metadata.SessionID).To(Equal(sessionID))
			Expect(s.Metadata.Created).To(BeTemporally("~", time.Now(), time.Second))
			Expect(s.Metadata.LastAccessed).To(BeTemporally("~", time.Now(), time.Second))
			Expect(s.Metadata.IsIncognito).To(BeTrue())
			Expect(s.Metadata.IsForkedSession).To(BeFalse())
			Expect(s.Metadata.ParentSession).To(BeEmpty())
		})
	})

	Describe("UpdateLastAccessed", func() {
		It("should update the lastAccessed timestamp", func() {
			s := session.NewSession("test", "uuid")
			originalTime := s.Metadata.LastAccessed

			time.Sleep(10 * time.Millisecond)
			s.UpdateLastAccessed()

			Expect(s.Metadata.LastAccessed).To(BeTemporally(">", originalTime))
			Expect(s.Metadata.LastAccessed).To(BeTemporally("~", time.Now(), time.Second))
		})
	})
})
