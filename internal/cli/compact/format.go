package compact

import (
	"fmt"
	"strings"
)

// humanInt renders n with comma thousands separators.
func humanInt(n int) string {
	if n == 0 {
		return "0"
	}
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	var groups []string
	for len(s) > 3 {
		groups = append([]string{s[len(s)-3:]}, groups...)
		s = s[:len(s)-3]
	}
	groups = append([]string{s}, groups...)
	return sign + strings.Join(groups, ",")
}

func shortUUID(u string) string {
	if len(u) > 8 {
		return u[:8] + "..."
	}
	return u
}
