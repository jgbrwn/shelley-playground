package server

import (
	"testing"

	"shelley.exe.dev/server/deploy"
)

func TestRedactPrivateSettingsRemovesDeployAPIKey(t *testing.T) {
	settings := map[string]string{
		"theme":             "dark",
		deployAPIKeySetting: "exe1.secret",
	}
	redactPrivateSettings(settings)
	if settings["theme"] != "dark" {
		t.Fatal("non-secret setting was removed")
	}
	if _, ok := settings[deployAPIKeySetting]; ok {
		t.Fatal("deploy API key remained in public settings")
	}
}

func TestWritePendingDeployEventsFlushesEventsAddedBeforeDone(t *testing.T) {
	events := []deploy.Event{
		{Level: "info", Step: "validate", Message: "Validating exe.dev API key…"},
		{Level: "error", Step: "validate", Message: `exe.dev rejected the API key (403): {"error":"command not allowed by token permissions"}`},
	}
	sent := 1 // the info event was written before the run finished
	var written []deploy.Event

	ok := writePendingDeployEvents(events, &sent, func(value any) bool {
		written = append(written, value.(deploy.Event))
		return true
	})
	if !ok {
		t.Fatal("writePendingDeployEvents returned false")
	}
	if sent != len(events) {
		t.Fatalf("sent = %d, want %d", sent, len(events))
	}
	if len(written) != 1 || written[0].Level != "error" || written[0].Message != events[1].Message {
		t.Fatalf("written = %#v, want final API error event", written)
	}
}
