package whatsapp

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryStore(t *testing.T) {
	h, err := openHistory(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Hour)
	for i, text := range []string{"un", "deux", "trois"} {
		_ = h.add("41@s.whatsapp.net", Message{ID: string(rune('a' + i)), Text: text, Timestamp: t0.Add(time.Duration(i) * time.Minute), FromMe: i == 1})
	}
	_ = h.add("41@s.whatsapp.net", Message{ID: "a", Text: "dup", Timestamp: t0}) // duplicate ignored
	_ = h.add("999@lid", Message{ID: "z", Text: "via lid", Kind: "image", Timestamp: t0.Add(10 * time.Minute)})

	got, err := h.messages([]string{"41@s.whatsapp.net", "999@lid"}, 3)
	if err != nil || len(got) != 3 || got[0].Text != "deux" || got[2].Render() != "[image] via lid" || !got[0].FromMe {
		t.Fatalf("messages = %+v, %v", got, err)
	}
	acts, _ := h.lastActivity()
	if len(acts) != 2 {
		t.Fatalf("lastActivity = %+v", acts)
	}
	if a, ok := h.oldest([]string{"41@s.whatsapp.net"}); !ok || a.ID != "a" {
		t.Fatalf("oldest = %+v %v", a, ok)
	}
}

func TestFoldAndSort(t *testing.T) {
	if fold("Alice  É") != "alice  e" {
		t.Errorf("fold = %q", fold("Alice  É"))
	}
	now := time.Now()
	c := []Chat{{JID: "b", Name: "Zoé"}, {JID: "a", Name: "Ana", LastActivity: now.Add(-time.Hour)}, {JID: "c", Name: "Bob", LastActivity: now}, {JID: "d", Name: "Éva"}}
	sortChats(c)
	if c[0].JID != "c" || c[1].JID != "a" || c[2].JID != "d" || c[3].JID != "b" {
		t.Errorf("order = %+v", c)
	}
}
