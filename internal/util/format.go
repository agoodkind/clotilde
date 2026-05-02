package util

import (
	"fmt"
	"time"
)

// FormatSize converts bytes to human-readable format.
// Examples: 512 -> "512 B", 1536 -> "1.5 KB", 1048576 -> "1 MB"
func FormatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	divisor := int64(unit)
	for i := range 4 {
		if bytes < divisor*unit {
			switch i {
			case 0:
				return fmt.Sprintf("%.1f KB", float64(bytes)/float64(divisor))
			case 1:
				return fmt.Sprintf("%.1f MB", float64(bytes)/float64(divisor))
			case 2:
				return fmt.Sprintf("%.1f GB", float64(bytes)/float64(divisor))
			case 3:
				return fmt.Sprintf("%.1f TB", float64(bytes)/float64(divisor))
			}
		}
		divisor *= unit
	}

	return fmt.Sprintf("%.1f TB", float64(bytes)/float64(divisor/unit))
}

// FormatRelativeTime formats a time as a human-readable relative string.
// Examples: "just now", "5 minutes ago", "2 hours ago", "3 days ago", "2024-01-15"
func FormatRelativeTime(t time.Time) string {
	now := currentTime()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("2006-01-02")
	}
}
