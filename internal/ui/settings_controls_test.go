package ui

import (
	"testing"
	"time"
)

func TestActivateSelectedConfigControlTogglesBool(t *testing.T) {
	var gotKey, gotValue string
	done := make(chan struct{}, 1)
	a := NewApp(nil, AppCallbacks{
		UpdateConfigControl: func(key, value string) error {
			gotKey = key
			gotValue = value
			done <- struct{}{}
			return nil
		},
	})
	a.configControls = []ConfigControl{{
		Key:   "mitm.enabled_default",
		Label: "MITM capture default",
		Type:  "bool",
		Value: "false",
	}}
	a.activateSelectedConfigControl()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for UpdateConfigControl callback")
	}
	if gotKey != "mitm.enabled_default" {
		t.Fatalf("got key %q", gotKey)
	}
	if gotValue != "true" {
		t.Fatalf("got value %q want true", gotValue)
	}
}

func TestActivateSelectedConfigControlOpensPathEditor(t *testing.T) {
	a := NewApp(nil, AppCallbacks{})
	a.configControls = []ConfigControl{{
		Key:          "mitm.capture_dir",
		Label:        "MITM capture dir",
		Type:         "path",
		Value:        "/tmp/clyde",
		DefaultValue: "/tmp/default",
	}}
	a.activateSelectedConfigControl()
	overlay, ok := a.overlay.(*InputOverlay)
	if !ok {
		t.Fatalf("overlay=%T want *InputOverlay", a.overlay)
	}
	if overlay.Title != "Edit MITM capture dir" {
		t.Fatalf("title=%q", overlay.Title)
	}
	if overlay.Input.Text != "/tmp/clyde" {
		t.Fatalf("text=%q", overlay.Input.Text)
	}
}
