package whatsapp

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

// historyDB persists every message the connector sees (sent, received, and the
// history the phone pushes) in <DataDir>/whatsapp/<toolID>.history.db, so
// whatsapp_get_messages survives restarts. Chats are keyed by a canonical JID
// (the phone-number form when a @lid maps to one).
type historyDB struct{ db *sql.DB }

const historySchema = `
CREATE TABLE IF NOT EXISTS wa_messages (
    chat        TEXT NOT NULL,
    id          TEXT NOT NULL,
    sender      TEXT NOT NULL DEFAULT '',
    sender_name TEXT NOT NULL DEFAULT '',
    from_me     INTEGER NOT NULL DEFAULT 0,
    ts          INTEGER NOT NULL,   -- unix ms
    text        TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL DEFAULT 'text',
    PRIMARY KEY (chat, id)
);
CREATE INDEX IF NOT EXISTS wa_messages_chat_ts ON wa_messages(chat, ts);
CREATE INDEX IF NOT EXISTS wa_messages_ts ON wa_messages(ts);
`

func openHistory(path string) (*historyDB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(historySchema); err != nil {
		db.Close()
		return nil, err
	}
	return &historyDB{db: db}, nil
}

// add stores one message; a message already known (same chat + id) is kept.
func (h *historyDB) add(chat string, m Message) error {
	if m.ID == "" {
		m.ID = fmt.Sprintf("local-%d", m.Timestamp.UnixNano())
	}
	if m.Kind == "" {
		m.Kind = "text"
	}
	fromMe := 0
	if m.FromMe {
		fromMe = 1
	}
	_, err := h.db.Exec(`INSERT OR IGNORE INTO wa_messages(chat,id,sender,sender_name,from_me,ts,text,kind) VALUES(?,?,?,?,?,?,?,?)`,
		chat, m.ID, m.From, m.SenderName, fromMe, m.Timestamp.UnixMilli(), m.Text, m.Kind)
	return err
}

// messages returns the last limit messages of the given chat keys, oldest first.
func (h *historyDB) messages(chats []string, limit int) ([]Message, error) {
	if len(chats) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(chats)), ",")
	args := make([]any, 0, len(chats)+1)
	for _, c := range chats {
		args = append(args, c)
	}
	args = append(args, limit)
	rows, err := h.db.Query(`SELECT chat,id,sender,sender_name,from_me,ts,text,kind FROM wa_messages
		WHERE chat IN (`+ph+`) ORDER BY ts DESC, rowid DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var fromMe int
		var ts int64
		if err := rows.Scan(&m.Chat, &m.ID, &m.From, &m.SenderName, &fromMe, &ts, &m.Text, &m.Kind); err != nil {
			return nil, err
		}
		m.FromMe, m.Timestamp = fromMe != 0, time.UnixMilli(ts)
		out = append(out, m)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// oldest returns the oldest stored message across the chat keys (the anchor of
// an on-demand history request).
func (h *historyDB) oldest(chats []string) (Message, bool) {
	ph := strings.TrimSuffix(strings.Repeat("?,", len(chats)), ",")
	args := make([]any, len(chats))
	for i, c := range chats {
		args[i] = c
	}
	var m Message
	var fromMe int
	var ts int64
	err := h.db.QueryRow(`SELECT chat,id,from_me,ts FROM wa_messages WHERE chat IN (`+ph+`) AND id NOT LIKE 'local-%' ORDER BY ts ASC LIMIT 1`, args...).
		Scan(&m.Chat, &m.ID, &fromMe, &ts)
	if err != nil {
		return Message{}, false
	}
	m.FromMe, m.Timestamp = fromMe != 0, time.UnixMilli(ts)
	return m, true
}

// activity is the latest message of one stored chat.
type activity struct {
	chat string
	ts   time.Time
	text string
	kind string
}

// lastActivity returns the most recent message of every stored chat.
func (h *historyDB) lastActivity() ([]activity, error) {
	rows, err := h.db.Query(`SELECT m.chat, m.ts, m.text, m.kind FROM wa_messages m
		JOIN (SELECT chat, MAX(ts) AS mx FROM wa_messages GROUP BY chat) l ON l.chat=m.chat AND l.mx=m.ts
		GROUP BY m.chat`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []activity
	for rows.Next() {
		var a activity
		var ts int64
		if err := rows.Scan(&a.chat, &ts, &a.text, &a.kind); err != nil {
			return nil, err
		}
		a.ts = time.UnixMilli(ts)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (h *historyDB) count() int {
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM wa_messages`).Scan(&n)
	return n
}

// importLegacy loads the old <toolID>.messages.json buffer into an empty store.
func (h *historyDB) importLegacy(path string, canon func(string) string) int {
	if h.count() > 0 {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	old := map[string][]Message{}
	if json.Unmarshal(data, &old) != nil {
		return 0
	}
	n := 0
	for chat, msgs := range old {
		for _, m := range msgs {
			if m.Text == "" {
				continue
			}
			if h.add(canon(chat), m) == nil {
				n++
			}
		}
	}
	_ = os.Rename(path, path+".imported")
	return n
}

// describe returns the text and kind of a message without downloading media.
// kind is "" when the message carries nothing worth storing (reactions,
// protocol notices…).
func describe(msg *waE2E.Message) (text, kind string) {
	switch {
	case msg == nil:
		return "", ""
	case msg.GetConversation() != "":
		return msg.GetConversation(), "text"
	case msg.GetExtendedTextMessage() != nil:
		return msg.GetExtendedTextMessage().GetText(), "text"
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetCaption(), "image"
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetCaption(), "video"
	case msg.GetAudioMessage() != nil:
		if msg.GetAudioMessage().GetPTT() {
			return "", "voice"
		}
		return "", "audio"
	case msg.GetDocumentMessage() != nil:
		d := msg.GetDocumentMessage()
		return strings.TrimSpace(d.GetFileName() + " " + d.GetCaption()), "document"
	case msg.GetStickerMessage() != nil:
		return "", "sticker"
	case msg.GetLocationMessage() != nil:
		return msg.GetLocationMessage().GetName(), "location"
	case msg.GetContactMessage() != nil:
		return msg.GetContactMessage().GetDisplayName(), "contact"
	}
	return "", ""
}

// render is how a message appears in tool output: "[image] caption".
func (m Message) Render() string {
	if m.Kind == "" || m.Kind == "text" {
		return m.Text
	}
	if m.Text == "" || strings.HasPrefix(m.Text, "[") {
		return "[" + m.Kind + "] " + strings.TrimSpace(m.Text)
	}
	return "[" + m.Kind + "] " + m.Text
}

// fold lower-cases and strips accents for name searches.
func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sortChats orders chats by latest activity, then by name for the silent ones.
func sortChats(c []Chat) {
	sort.SliceStable(c, func(i, j int) bool {
		a, b := c[i], c[j]
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.After(b.LastActivity)
		}
		if an, bn := fold(a.Name), fold(b.Name); an != bn {
			return an < bn
		}
		return a.JID < b.JID
	})
}
