package util

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestGenerateRandomName(t *testing.T) {
	name := GenerateRandomName()

	// Should be in format "YYYY-MM-DD-adjective-noun"
	datePrefix := time.Now().Format("2006-01-02")
	if !strings.HasPrefix(name, datePrefix+"-") {
		t.Errorf("Expected name to start with '%s-', got '%s'", datePrefix, name)
	}

	// Extract adjective-noun suffix
	suffix := strings.TrimPrefix(name, datePrefix+"-")
	parts := strings.Split(suffix, "-")
	if len(parts) != 2 {
		t.Errorf("Expected adjective-noun suffix, got '%s'", suffix)
	}

	// Should contain valid adjective
	found := false
	for _, adj := range adjectives {
		if adj == parts[0] {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Generated name '%s' has invalid adjective '%s'", name, parts[0])
	}

	// Should contain valid noun
	found = false
	for _, n := range nouns {
		if n == parts[1] {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Generated name '%s' has invalid noun '%s'", name, parts[1])
	}

	// Full format check: YYYY-MM-DD-adjective-noun
	pattern := `^\d{4}-\d{2}-\d{2}-[a-z]+-[a-z]+$`
	if !regexp.MustCompile(pattern).MatchString(name) {
		t.Errorf("Expected name matching '%s', got '%s'", pattern, name)
	}
}

func TestGenerateRandomName_Variety(t *testing.T) {
	// Generate 50 names and ensure we get some variety
	names := make(map[string]bool)
	for i := 0; i < 50; i++ {
		name := GenerateRandomName()
		names[name] = true
	}

	// Should have at least 10 unique names (very conservative check)
	if len(names) < 10 {
		t.Errorf("Expected variety in generated names, got only %d unique names out of 50", len(names))
	}
}

func TestGenerateUniqueRandomName(t *testing.T) {
	datePrefix := time.Now().Format("2006-01-02")
	existing := []string{
		datePrefix + "-happy-fox",
		datePrefix + "-brave-wolf",
		datePrefix + "-clever-bear",
	}

	name := GenerateUniqueRandomName(existing)

	// Should not match any existing name
	for _, existingName := range existing {
		if name == existingName {
			t.Errorf("Generated name '%s' conflicts with existing name '%s'", name, existingName)
		}
	}

	// Should start with date prefix
	if !strings.HasPrefix(name, datePrefix+"-") {
		t.Errorf("Expected name to start with '%s-', got '%s'", datePrefix, name)
	}
}

func TestGenerateUniqueRandomName_FallbackWithNumber(t *testing.T) {
	// Create a scenario where all possible combinations are taken
	// We have 25*25 = 625 combinations
	datePrefix := time.Now().Format("2006-01-02")
	existing := []string{}
	for _, adj := range adjectives {
		for _, noun := range nouns {
			existing = append(existing, datePrefix+"-"+adj+"-"+noun)
		}
	}

	name := GenerateUniqueRandomName(existing)

	// Should have added a number suffix: YYYY-MM-DD-adjective-noun-number
	pattern := `^\d{4}-\d{2}-\d{2}-[a-z]+-[a-z]+-\d+$`
	if !regexp.MustCompile(pattern).MatchString(name) {
		t.Errorf("Expected name with number suffix matching '%s', got '%s'", pattern, name)
	}
}

func TestGenerateUniqueRandomName_Empty(t *testing.T) {
	name := GenerateUniqueRandomName([]string{})

	// Should generate a valid name
	if name == "" {
		t.Error("Expected non-empty name")
	}

	// Should match YYYY-MM-DD-adjective-noun format
	pattern := `^\d{4}-\d{2}-\d{2}-[a-z]+-[a-z]+$`
	if !regexp.MustCompile(pattern).MatchString(name) {
		t.Errorf("Expected name matching '%s', got '%s'", pattern, name)
	}
}
