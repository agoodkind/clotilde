package session_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"goodkind.io/clyde/internal/session"
)

var _ = Describe("Session", func() {
	Describe("NewSession", func() {
		It("should create a new session with given name and UUID", func() {
			name := "test-session"
			sessionID := "550e8400-e29b-41d4-a716-446655440000"

			s := session.NewSession(name, sessionID)

			Expect(s.Name).To(Equal(name))
			Expect(s.Metadata.Name).To(Equal(name))
			Expect(s.ProviderID()).To(Equal(session.ProviderClaude))
			Expect(s.Metadata.SessionID).To(Equal(sessionID))
			Expect(s.Metadata.Created).To(BeTemporally("~", time.Now(), time.Second))
			Expect(s.Metadata.LastAccessed).To(BeTemporally("~", time.Now(), time.Second))
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

	Describe("Identity", func() {
		It("defaults legacy metadata to the Claude provider", func() {
			s := &session.Session{
				Name: "legacy",
				Metadata: session.Metadata{
					Name:      "legacy",
					SessionID: "legacy-uuid",
				},
			}

			identity := s.Identity()
			Expect(identity.Current.Provider).To(Equal(session.ProviderClaude))
			Expect(identity.Current.ID).To(Equal("legacy-uuid"))
			Expect(session.IdentityKey(s)).To(Equal("provider:claude:sid:legacy-uuid"))
		})

		It("treats provider namespaces as distinct identity domains", func() {
			claude := &session.Session{
				Name: "claude-session",
				Metadata: session.Metadata{
					Name:      "claude-session",
					Provider:  session.ProviderClaude,
					SessionID: "shared-id",
				},
			}
			other := &session.Session{
				Name: "other-session",
				Metadata: session.Metadata{
					Name:      "other-session",
					Provider:  session.ProviderID("other"),
					SessionID: "shared-id",
				},
			}

			Expect(session.IdentityKey(claude)).ToNot(Equal(session.IdentityKey(other)))
		})

		It("records previous ids through provider-aware rotation", func() {
			s := session.NewSession("rotating", "uuid-1")

			s.RotateIdentity(session.ProviderSessionID{
				Provider: session.ProviderClaude,
				ID:       "uuid-2",
			})
			s.RotateIdentity(session.ProviderSessionID{
				Provider: session.ProviderClaude,
				ID:       "uuid-2",
			})

			Expect(s.Metadata.SessionID).To(Equal("uuid-2"))
			Expect(s.Metadata.PreviousSessionIDs).To(Equal([]string{"uuid-1"}))
			Expect(s.Identity().HasID("uuid-1")).To(BeTrue())
			Expect(s.Identity().HasID("uuid-2")).To(BeTrue())
		})
	})
})
