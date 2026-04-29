package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	itranscript "goodkind.io/clyde/internal/transcript"
	"goodkind.io/clyde/internal/util"
)

type inspectMessage struct {
	Role      string
	Text      string
	Timestamp time.Time
}

type inspectToolUse struct {
	Name  string
	Count int
}

type inspectStats struct {
	VisibleMessages       int
	VisibleTokensEstimate int
	LastMessageTokens     int
	CompactionCount       int
	LastPreCompactTokens  int
}

type inspectExportStats struct {
	VisibleTokensEstimate int
	VisibleMessages       int
	UserMessages          int
	AssistantMessages     int
	ToolResultMessages    int
	ToolCalls             int
	SystemPrompts         int
	Compactions           int
	TranscriptSizeBytes   int64
}

type daemonDeletedFiles struct {
	Transcript []string
	AgentLogs  []string
}

var daemonModelFamilyRegex = regexp.MustCompile(`claude-(?:\d+-)*(\w+)-\d+`)

type inspectCacheEntry struct {
	Size           int64
	ModUnix        int64
	Model          string
	Stats          inspectStats
	ExportStats    inspectExportStats
	HasModel       bool
	HasStats       bool
	HasExportStats bool
}

var (
	inspectCacheMu sync.Mutex
	inspectCache   = map[string]inspectCacheEntry{}
)

func inspectFingerprint(path string) (size int64, modUnix int64, ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	return info.Size(), info.ModTime().UnixNano(), true
}

func inspectCacheLoad(path string) (inspectCacheEntry, bool) {
	inspectCacheMu.Lock()
	defer inspectCacheMu.Unlock()
	entry, ok := inspectCache[path]
	return entry, ok
}

func inspectCacheStore(path string, entry inspectCacheEntry) {
	inspectCacheMu.Lock()
	inspectCache[path] = entry
	inspectCacheMu.Unlock()
}

func inspectExtractModel(transcriptPath string) string {
	if strings.TrimSpace(transcriptPath) == "" {
		return ""
	}
	size, modUnix, ok := inspectFingerprint(transcriptPath)
	if !ok {
		return ""
	}
	if cached, found := inspectCacheLoad(transcriptPath); found && cached.Size == size && cached.ModUnix == modUnix && cached.HasModel {
		return cached.Model
	}

	type modelEntry struct {
		Type    string `json:"type"`
		Message struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	lastModel := ""
	_ = inspectForEachTailLine(transcriptPath, 128*1024, func(line []byte) {
		var e modelEntry
		if err := json.Unmarshal(line, &e); err == nil && e.Type == "assistant" && e.Message.Model != "" && e.Message.Model != "<synthetic>" {
			lastModel = e.Message.Model
		}
	})
	if lastModel == "" {
		return ""
	}
	if matches := daemonModelFamilyRegex.FindStringSubmatch(lastModel); len(matches) > 1 {
		lastModel = matches[1]
	}
	cacheEntry := inspectCacheEntry{
		Size:     size,
		ModUnix:  modUnix,
		Model:    lastModel,
		HasModel: true,
	}
	if cached, found := inspectCacheLoad(transcriptPath); found && cached.Size == size && cached.ModUnix == modUnix {
		cacheEntry.Stats = cached.Stats
		cacheEntry.HasStats = cached.HasStats
		cacheEntry.ExportStats = cached.ExportStats
		cacheEntry.HasExportStats = cached.HasExportStats
	}
	inspectCacheStore(transcriptPath, cacheEntry)
	return lastModel
}

func inspectRecentMessages(transcriptPath string, n, maxLen int) []inspectMessage {
	all := inspectAllMessages(transcriptPath, maxLen)
	if len(all) > n && n > 0 {
		all = all[len(all)-n:]
	}
	return all
}

func inspectAllMessages(transcriptPath string, maxLen int) []inspectMessage {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	parsed, err := itranscript.Parse(f)
	if err != nil {
		return nil
	}
	turns := itranscript.ShapeConversation(parsed, itranscript.ShapeOptions{
		ConversationOnly: true,
		ToolOnly:         itranscript.ToolOnlyOmit,
		MaxTextRunes:     maxLen,
	})
	out := make([]inspectMessage, 0, len(turns))
	for _, turn := range turns {
		out = append(out, inspectMessage{
			Role:      turn.Role,
			Text:      turn.Text,
			Timestamp: turn.Timestamp,
		})
	}
	return out
}

func inspectStatsFor(transcriptPath string) inspectStats {
	if strings.TrimSpace(transcriptPath) == "" {
		return inspectStats{}
	}
	size, modUnix, ok := inspectFingerprint(transcriptPath)
	if !ok {
		return inspectStats{}
	}
	if cached, found := inspectCacheLoad(transcriptPath); found && cached.Size == size && cached.ModUnix == modUnix && cached.HasStats {
		return cached.Stats
	}

	f, err := os.Open(transcriptPath)
	if err != nil {
		return inspectStats{}
	}
	defer f.Close()

	parsed, err := itranscript.Parse(f)
	if err != nil {
		return inspectStats{}
	}
	turns := itranscript.ShapeConversation(parsed, itranscript.ShapeOptions{
		ConversationOnly: true,
		ToolOnly:         itranscript.ToolOnlyOmit,
	})

	f2, err := os.Open(transcriptPath)
	if err != nil {
		return inspectStats{}
	}
	defer f2.Close()
	scanner := bufio.NewScanner(f2)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	var out inspectStats
	for scanner.Scan() {
		var e struct {
			Type            string `json:"type"`
			Subtype         string `json:"subtype"`
			CompactMetadata struct {
				PreCompactTokenCount int `json:"preCompactTokenCount"`
			} `json:"compactMetadata"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.Type == "system" && e.Subtype == "compact_boundary" {
			out.CompactionCount++
			if e.CompactMetadata.PreCompactTokenCount > 0 {
				out.LastPreCompactTokens = e.CompactMetadata.PreCompactTokenCount
			}
			continue
		}
	}
	for _, turn := range turns {
		if strings.TrimSpace(turn.Text) == "" {
			continue
		}
		tokens := inspectRoughTokens(turn.Text)
		out.VisibleMessages++
		out.VisibleTokensEstimate += tokens
		out.LastMessageTokens = tokens
	}
	entry := inspectCacheEntry{
		Size:     size,
		ModUnix:  modUnix,
		Stats:    out,
		HasStats: true,
	}
	if cached, found := inspectCacheLoad(transcriptPath); found && cached.Size == size && cached.ModUnix == modUnix {
		entry.Model = cached.Model
		entry.HasModel = cached.HasModel
		entry.ExportStats = cached.ExportStats
		entry.HasExportStats = cached.HasExportStats
	}
	inspectCacheStore(transcriptPath, entry)
	return out
}

func inspectExportStatsFor(transcriptPath string) inspectExportStats {
	if strings.TrimSpace(transcriptPath) == "" {
		return inspectExportStats{}
	}
	size, modUnix, ok := inspectFingerprint(transcriptPath)
	if !ok {
		return inspectExportStats{}
	}
	if cached, found := inspectCacheLoad(transcriptPath); found && cached.Size == size && cached.ModUnix == modUnix && cached.HasExportStats {
		return cached.ExportStats
	}

	stats := inspectExportStats{TranscriptSizeBytes: size}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return stats
	}
	defer f.Close()

	messages, err := itranscript.Parse(f)
	if err == nil {
		for _, msg := range messages {
			if strings.TrimSpace(msg.Text) != "" {
				stats.VisibleMessages++
				stats.VisibleTokensEstimate += inspectRoughTokens(msg.Text)
			}
			switch msg.Role {
			case "user":
				stats.UserMessages++
			case "assistant":
				stats.AssistantMessages++
			}
			stats.ToolCalls += len(msg.Tools)
			if msg.Thinking != "" {
				stats.VisibleTokensEstimate += inspectRoughTokens(msg.Thinking)
			}
		}
	}

	raw, err := os.Open(transcriptPath)
	if err == nil {
		defer raw.Close()
		scanner := bufio.NewScanner(raw)
		scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
		for scanner.Scan() {
			var entry inspectExportStatsLine
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue
			}
			if entry.Type == "system" {
				if entry.Subtype == "compact_boundary" {
					stats.Compactions++
				} else {
					stats.SystemPrompts++
				}
				continue
			}
			if entry.Type == "user" && len(entry.Message.Content) > 0 {
				var blocks []inspectExportStatsContentBlock
				if err := json.Unmarshal(entry.Message.Content, &blocks); err == nil {
					for _, block := range blocks {
						if block.Type == "tool_result" {
							stats.ToolResultMessages++
						}
					}
				}
			}
		}
	}

	entry := inspectCacheEntry{
		Size:           size,
		ModUnix:        modUnix,
		ExportStats:    stats,
		HasExportStats: true,
	}
	if cached, found := inspectCacheLoad(transcriptPath); found && cached.Size == size && cached.ModUnix == modUnix {
		entry.Model = cached.Model
		entry.HasModel = cached.HasModel
		entry.Stats = cached.Stats
		entry.HasStats = cached.HasStats
	}
	inspectCacheStore(transcriptPath, entry)
	return stats
}

func inspectToolUseStats(transcriptPath string, topN int) []inspectToolUse {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	counts := map[string]int{}
	for scanner.Scan() {
		var e struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil || e.Type != "assistant" {
			continue
		}
		var msg struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(e.Message, &msg); err != nil {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.Name != "" {
				counts[b.Name]++
			}
		}
	}
	out := make([]inspectToolUse, 0, len(counts))
	for name, count := range counts {
		out = append(out, inspectToolUse{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Name < out[j].Name
		}
		return out[i].Count > out[j].Count
	})
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out
}

func inspectRoughTokens(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	n := len([]rune(text)) / 4
	if n < 1 {
		return 1
	}
	return n
}

type inspectExportStatsLine struct {
	Type    string                    `json:"type"`
	Subtype string                    `json:"subtype"`
	Message inspectExportStatsMessage `json:"message"`
}

type inspectExportStatsMessage struct {
	Content json.RawMessage `json:"content"`
}

type inspectExportStatsContentBlock struct {
	Type string `json:"type"`
}

func inspectForEachTailLine(transcriptPath string, tailSize int, fn func(line []byte)) error {
	if transcriptPath == "" {
		return nil
	}
	file, err := os.Open(transcriptPath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	skipFirstLine := false
	if info.Size() > int64(tailSize) {
		if _, err := file.Seek(info.Size()-int64(tailSize), io.SeekStart); err != nil {
			return err
		}
		check := make([]byte, 1)
		if _, err := file.ReadAt(check, info.Size()-int64(tailSize)-1); err == nil {
			skipFirstLine = check[0] != '\n'
		} else {
			skipFirstLine = true
		}
	}
	reader := bufio.NewReaderSize(file, tailSize)
	if skipFirstLine {
		for {
			_, err := reader.ReadSlice('\n')
			if !errors.Is(err, bufio.ErrBufferFull) {
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return err
				}
				break
			}
		}
	}
	for {
		line, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			for errors.Is(readErr, bufio.ErrBufferFull) {
				_, readErr = reader.ReadSlice('\n')
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			continue
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 {
			fn(line)
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func daemonDeleteSessionData(clydeRoot, sessionID, transcriptPath string) (*daemonDeletedFiles, error) {
	deleted := &daemonDeletedFiles{}
	claudeProjectDir := ""
	if transcriptPath != "" {
		if util.FileExists(transcriptPath) {
			if err := os.Remove(transcriptPath); err != nil {
				return deleted, fmt.Errorf("delete transcript: %w", err)
			}
			deleted.Transcript = append(deleted.Transcript, transcriptPath)
		}
		claudeProjectDir = filepath.Dir(transcriptPath)
	} else {
		projectRoot := filepath.Dir(filepath.Dir(clydeRoot))
		encoded := strings.ReplaceAll(strings.ReplaceAll(projectRoot, "/", "-"), ".", "-")
		home, err := util.HomeDir()
		if err != nil {
			return deleted, err
		}
		claudeProjectDir = filepath.Join(home, ".claude", "projects", encoded)
		path := filepath.Join(claudeProjectDir, sessionID+".jsonl")
		if util.FileExists(path) {
			if err := os.Remove(path); err != nil {
				return deleted, fmt.Errorf("delete transcript: %w", err)
			}
			deleted.Transcript = append(deleted.Transcript, path)
		}
	}
	matches, err := filepath.Glob(filepath.Join(claudeProjectDir, "agent-*.jsonl"))
	if err != nil {
		return deleted, err
	}
	for _, path := range matches {
		contains, err := inspectFileContains(path, sessionID)
		if err != nil || !contains {
			continue
		}
		if err := os.Remove(path); err != nil {
			return deleted, err
		}
		deleted.AgentLogs = append(deleted.AgentLogs, path)
	}
	return deleted, nil
}

func inspectFileContains(path, needle string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), needle) {
			return true, nil
		}
	}
	return false, scanner.Err()
}
