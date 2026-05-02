package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"goodkind.io/clyde/internal/config"
)

const noticeStateFileName = "notices.json"

var noticeStateMu sync.Mutex

// Claim atomically tests whether the kind/reset window was already
// delivered and, if not, records it as delivered. Returns true when the
// caller "won" the slot and should emit the notice. Returns false when
// the slot was already taken or the file write failed (caller should
// silently skip the injection).
func Claim(kind string, resetsAt time.Time) bool {
	if kind == "" || resetsAt.IsZero() {
		return false
	}
	noticeStateMu.Lock()
	defer noticeStateMu.Unlock()
	claimed, err := claimLocked(kind, resetsAt, false)
	if err != nil {
		adapterHTTPErrorLog.Logger().Warn("adapter.notice.claim_failed",
			"subcomponent", "adapter",
			"kind", kind,
			"err", err.Error(),
		)
		return false
	}
	return claimed
}

// Unclaim removes a previously claimed entry. Use only when the inject
// failed and the notice should be eligible again on the next turn.
func Unclaim(kind string, resetsAt time.Time) {
	if kind == "" || resetsAt.IsZero() {
		return
	}
	noticeStateMu.Lock()
	defer noticeStateMu.Unlock()
	state, err := readNoticeStateLocked()
	if err != nil {
		return
	}
	delete(state, noticeStateKey(kind, resetsAt))
	if err := writeNoticeStateLocked(state); err != nil {
		adapterHTTPErrorLog.Logger().Warn("adapter.notice.unclaim_failed",
			"subcomponent", "adapter",
			"kind", kind,
			"err", err.Error(),
		)
	}
}

// claimLocked is the shared body of Mark and Claim. force=true overwrites
// any existing entry; force=false leaves the existing entry alone and
// returns claimed=false. The bool return is the "I just claimed it" signal.
func claimLocked(kind string, resetsAt time.Time, force bool) (bool, error) {
	state, err := readNoticeStateLocked()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		state = map[string]int64{}
	}

	key := noticeStateKey(kind, resetsAt)
	if _, exists := state[key]; exists && !force {
		return false, nil
	}

	now := adapterClock.Now().UTC()
	pruneNoticeStateLocked(state, now)
	state[key] = now.Unix()
	if err := writeNoticeStateLocked(state); err != nil {
		return false, err
	}
	return true, nil
}

func readNoticeStateLocked() (map[string]int64, error) {
	log := adapterHTTPErrorLog.Logger()
	path := noticeStatePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		log.Warn("adapter.notice.state_read_failed",
			"subcomponent", "adapter",
			"path", path,
			"err", err.Error(),
		)
		return nil, fmt.Errorf("read notice state %s: %w", path, err)
	}
	state := map[string]int64{}
	if err := json.Unmarshal(raw, &state); err != nil {
		log.Warn("adapter.notice.state_unmarshal_failed",
			"subcomponent", "adapter",
			"path", path,
			"raw_bytes", len(raw),
			"err", err.Error(),
		)
		return nil, fmt.Errorf("unmarshal notice state %s: %w", path, err)
	}
	return state, nil
}

func writeNoticeStateLocked(state map[string]int64) error {
	log := adapterHTTPErrorLog.Logger()
	path := noticeStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Warn("adapter.notice.state_mkdir_failed",
			"subcomponent", "adapter",
			"path", filepath.Dir(path),
			"err", err.Error(),
		)
		return fmt.Errorf("mkdir notice state dir %s: %w", filepath.Dir(path), err)
	}
	if len(state) == 0 {
		raw, err := json.Marshal(map[string]int64{})
		if err != nil {
			log.Warn("adapter.notice.state_marshal_failed",
				"subcomponent", "adapter",
				"entry_count", len(state),
				"err", err.Error(),
			)
			return fmt.Errorf("marshal notice state: %w", err)
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			log.Warn("adapter.notice.state_temp_write_failed",
				"subcomponent", "adapter",
				"path", tmp,
				"raw_bytes", len(raw),
				"err", err.Error(),
			)
			return fmt.Errorf("write temp notice state: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			log.Warn("adapter.notice.state_rename_failed",
				"subcomponent", "adapter",
				"tmp_path", tmp,
				"path", path,
				"err", err.Error(),
			)
			return fmt.Errorf("rename notice state: %w", err)
		}
		return nil
	}

	raw, err := json.Marshal(state)
	if err != nil {
		log.Warn("adapter.notice.state_marshal_failed",
			"subcomponent", "adapter",
			"entry_count", len(state),
			"err", err.Error(),
		)
		return fmt.Errorf("marshal notice state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Warn("adapter.notice.state_temp_write_failed",
			"subcomponent", "adapter",
			"path", tmp,
			"raw_bytes", len(raw),
			"err", err.Error(),
		)
		return fmt.Errorf("write temp notice state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Warn("adapter.notice.state_rename_failed",
			"subcomponent", "adapter",
			"tmp_path", tmp,
			"path", path,
			"err", err.Error(),
		)
		return fmt.Errorf("rename notice state: %w", err)
	}
	return nil
}

func pruneNoticeStateLocked(state map[string]int64, now time.Time) {
	if len(state) == 0 {
		return
	}
	if now.IsZero() {
		now = adapterClock.Now().UTC()
	}
	for key := range state {
		_, resetAt, ok := parseNoticeStateKey(key)
		if !ok {
			delete(state, key)
			continue
		}
		if resetAt.Before(now) {
			delete(state, key)
		}
	}
}

func noticeStatePath() string {
	return filepath.Join(config.DefaultStateDir(), noticeStateFileName)
}

func noticeStateKey(kind string, resetsAt time.Time) string {
	resetUnix := resetsAt.UTC().Unix()
	return kind + ":" + strconv.FormatInt(resetUnix, 10)
}

func parseNoticeStateKey(raw string) (string, time.Time, bool) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return "", time.Time{}, false
	}
	if strings.TrimSpace(parts[0]) == "" {
		return "", time.Time{}, false
	}
	resetUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	return parts[0], time.Unix(resetUnix, 0).UTC(), true
}
