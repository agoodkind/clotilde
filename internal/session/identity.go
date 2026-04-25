package session

import "strings"

// IdentityKey returns the logical identity used to collapse stale aliases that
// point at the same underlying Claude session.
func IdentityKey(sess *Session) string {
	if sess == nil {
		return ""
	}
	if id := strings.TrimSpace(sess.Metadata.SessionID); id != "" {
		return "sid:" + id
	}
	return "name:" + sess.Name
}

// PreferIdentityWinner reports whether candidate should replace existing when
// two session rows describe the same logical session.
func PreferIdentityWinner(candidate, existing *Session) bool {
	if candidate == nil {
		return false
	}
	if existing == nil {
		return true
	}
	candidateAuto := LooksLikeAutoAdoptedName(candidate.Name)
	existingAuto := LooksLikeAutoAdoptedName(existing.Name)
	if candidateAuto != existingAuto {
		return !candidateAuto
	}
	if candidate.Metadata.DisplayTitle != "" && existing.Metadata.DisplayTitle == "" {
		return true
	}
	if existing.Metadata.DisplayTitle != "" && candidate.Metadata.DisplayTitle == "" {
		return false
	}
	if candidate.Metadata.LastAccessed.After(existing.Metadata.LastAccessed) {
		return true
	}
	if existing.Metadata.LastAccessed.After(candidate.Metadata.LastAccessed) {
		return false
	}
	if candidate.Metadata.Created.After(existing.Metadata.Created) {
		return true
	}
	if existing.Metadata.Created.After(candidate.Metadata.Created) {
		return false
	}
	return candidate.Name < existing.Name
}

// LooksLikeAutoAdoptedName matches the common workspace-plus-short-id names
// produced by background adoption before a user assigns a friendlier title.
func LooksLikeAutoAdoptedName(name string) bool {
	if len(name) <= 9 {
		return false
	}
	cut := strings.LastIndexByte(name, '-')
	if cut <= 0 || cut >= len(name)-1 {
		return false
	}
	suffix := name[cut+1:]
	if len(suffix) != 8 {
		return false
	}
	for _, ch := range suffix {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
