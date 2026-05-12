package main

import "time"

// rawEventJSON mirrors the JSON shape kargo-tui's ListEventsForStage
// path expects (internal/kargo/events.go: rawEvent). The mock emits these
// directly since the JSON encoder elides timestamps anyway and the TUI
// has its own fallback paths.
type rawEventJSON struct {
	Type           string          `json:"type"`
	Reason         string          `json:"reason"`
	Message        string          `json:"message"`
	Count          int32           `json:"count"`
	FirstTimestamp string          `json:"firstTimestamp,omitempty"`
	LastTimestamp  string          `json:"lastTimestamp,omitempty"`
	Metadata       eventMetaJSON   `json:"metadata"`
	InvolvedObject involvedObjJSON `json:"involvedObject"`
}

type eventMetaJSON struct {
	CreationTimestamp string `json:"creationTimestamp,omitempty"`
}

type involvedObjJSON struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func newEvent(kind, name, reason, msg string, when time.Time) rawEventJSON {
	ts := when.UTC().Format(time.RFC3339)
	return rawEventJSON{
		Type:           "Normal",
		Reason:         reason,
		Message:        msg,
		Count:          1,
		FirstTimestamp: ts,
		LastTimestamp:  ts,
		Metadata:       eventMetaJSON{CreationTimestamp: ts},
		InvolvedObject: involvedObjJSON{Kind: kind, Name: name},
	}
}
