package main

import (
	"testing"

	"github.com/EmadMokhtar/wazir/internal/store"
)

func TestCardCmdHasLinkPR(t *testing.T) {
	cmd := newCardCmd()
	for _, sub := range []string{"move", "comment", "link-pr"} {
		found := false
		for _, c := range cmd.Commands() {
			if c.Name() == sub {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("card is missing the %q subcommand", sub)
		}
	}
}

func TestLinkPRInStore(t *testing.T) {
	st := store.NewMemory()
	// A card Wazir already resolved (repo is set) but whose PR predates the linkage.
	st.PutCard("ISSUE_1", store.CardRecord{Repo: "octocat/hello", IssueNumber: 41})

	repo, err := linkPRInStore(st, "ISSUE_1", 43)
	if err != nil {
		t.Fatalf("linkPRInStore: %v", err)
	}
	if repo != "octocat/hello" {
		t.Errorf("repo = %q, want octocat/hello", repo)
	}

	// PRNumber persisted on the record, other fields preserved.
	rec, ok, _ := st.GetCard("ISSUE_1")
	if !ok || rec.PRNumber != 43 || rec.IssueNumber != 41 {
		t.Errorf("record = %+v, want PRNumber=43 IssueNumber=41", rec)
	}
	// PR-index written so a webhook can reverse-map repo#pr -> card.
	id, ok, _ := st.GetPRIndex("octocat/hello", 43)
	if !ok || id != "ISSUE_1" {
		t.Errorf("PR-index = (%q, %v), want (ISSUE_1, true)", id, ok)
	}
}

func TestLinkPRInStoreRejectsUnknownCard(t *testing.T) {
	st := store.NewMemory()
	if _, err := linkPRInStore(st, "NOPE", 1); err == nil {
		t.Fatal("expected an error linking a card with no stored record")
	}
	// A record with no repo is also rejected.
	st.PutCard("ISSUE_2", store.CardRecord{IssueNumber: 7})
	if _, err := linkPRInStore(st, "ISSUE_2", 1); err == nil {
		t.Fatal("expected an error when the card record has no repo")
	}
}
