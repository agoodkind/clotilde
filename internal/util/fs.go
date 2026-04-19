package util

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	DefaultFileMode = 0o644
	DefaultDirMode  = 0o755
)

func ensureDirForFile(filePath string) error {
	return EnsureDir(filepath.Dir(filePath))
}

// EnsureDir creates a directory and all parent directories if they don't exist.
// Returns an error if creation fails.
func EnsureDir(path string) error {
	return os.MkdirAll(path, DefaultDirMode)
}

// FileExists checks if a file exists at the given path.
// Returns true if the file exists, false otherwise.
func FileExists(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Mode().IsRegular()
}

// DirExists checks if a directory exists at the given path.
// Returns true if the directory exists, false otherwise.
func DirExists(path string) bool {
	pathInfo, err := os.Stat(path)
	if err != nil {
		return false
	}
	return pathInfo.IsDir()
}

// ReadJSON reads a JSON file and unmarshals it into the provided interface.
// Returns an error if reading or unmarshaling fails.
func ReadJSON(path string, v any) error {
	data, err := ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// WriteJSON marshals the provided interface to JSON and writes it to a file.
// Creates parent directories if they don't exist.
// Uses indented formatting for readability.
// Returns an error if marshaling or writing fails.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, data)
}

// HomeDir returns the user's home directory path.
// Returns an error if the home directory cannot be determined.
func HomeDir() (string, error) {
	return os.UserHomeDir()
}

// WriteFile writes content to a file, creating parent directories if
// needed. The write is atomic from the reader's perspective: the
// content lands in a sibling .tmp file first and is then renamed onto
// the destination. Concurrent writers can still clobber each other on
// the rename, but no reader ever sees a half-written metadata file.
func WriteFile(path string, content []byte) error {
	if err := ensureDirForFile(path); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, DefaultFileMode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadFile reads the entire contents of a file.
// Returns the file contents and any error encountered.
func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// RemoveAll removes a path and all its contents.
// Returns an error if removal fails.
func RemoveAll(path string) error {
	return os.RemoveAll(path)
}
