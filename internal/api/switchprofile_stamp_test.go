package api

import (
	"encoding/json"
	"testing"

	"meshcraft/internal/portrules"
)

// The merge half of the follow-link (the evaluation half lives in
// portrules): what applySwitchSpecToDevice writes into `devices.ports` must
// carry `profile_id` for rule-referenced profiles, clear it for inline rules,
// and never touch what it never rewrote (labels, unmatched ports).

// TestMergeStampsTheFollowedProfile : a port matched by a profile-referencing
// rule gets the follow-link, and keeps its label — the merge stays a merge.
func TestMergeStampsTheFollowedProfile(t *testing.T) {
	var existing = json.RawMessage(
		`{"GE1":{"label":"PC room 2","mode":"trunk","pvid":1,"profile_id":"old"},
		"GE9":{"label":"AP roof","profile_id":"old"}}`)
	var out, err = mergePortConfigs(existing, []portrules.Assignment{
		{Port: "GE1",
			Config:        portrules.Config{Mode: "access", PVID: 20},
			PortProfileID: "p-bureaux"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ge1 map[string]json.RawMessage
	if json.Unmarshal(out["GE1"], &ge1) != nil {
		t.Fatalf("unreadable merged port: %s", out["GE1"])
	}
	if string(ge1["profile_id"]) != `"p-bureaux"` {
		t.Errorf("follow-link not stamped: %s", ge1["profile_id"])
	}
	if string(ge1["label"]) != `"PC room 2"` {
		t.Errorf("the merge must not eat the label: %s", ge1["label"])
	}
	// GE9 was matched by NO rule: absent from the write, its old link — even
	// one that predates this apply — is not this gesture's to touch.
	var _, touched = out["GE9"]
	if touched {
		t.Error("an unmatched port must not be rewritten")
	}
}

// TestMergeClearsTheFollowOnInlineRule : an inline rule wins a port that used
// to follow a group. Its values now come from the rule; keeping the old
// reference would make the panel claim a provenance the values no longer
// have — same reason a manual VLAN edit detaches.
func TestMergeClearsTheFollowOnInlineRule(t *testing.T) {
	var existing = json.RawMessage(`{"GE2":{"mode":"access","pvid":10,"profile_id":"p-bureaux"}}`)
	var out, err = mergePortConfigs(existing, []portrules.Assignment{
		{Port: "GE2", Config: portrules.Config{Mode: "trunk", PVID: 1, TaggedAll: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ge2 map[string]json.RawMessage
	if json.Unmarshal(out["GE2"], &ge2) != nil {
		t.Fatalf("unreadable merged port: %s", out["GE2"])
	}
	if string(ge2["profile_id"]) != "null" {
		t.Errorf("an inline rule must clear the follow-link: %s", ge2["profile_id"])
	}
}
