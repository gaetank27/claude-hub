package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// AssistantMessage is one entry of the assistant's conversation thread.
type AssistantMessage struct {
	ID      int64  `json:"id"`
	TS      int64  `json:"ts"` // unix ms
	Channel string `json:"channel"`
	Role    string `json:"role"` // user | assistant | event
	Text    string `json:"text"`
	// Attachments are the files sent with the message (always an array).
	Attachments []AssistantAttachment `json:"attachments"`
	// HeldUntil is the unix ms a message written by the assistant on its own is
	// held back until (night mode); 0 = delivered. Held mirrors HeldUntil > 0.
	HeldUntil int64 `json:"heldUntil"`
	Held      bool  `json:"held"`
	// HeldTool/HeldJID: the WhatsApp contact it would have gone to ("" = none).
	HeldTool string `json:"-"`
	HeldJID  string `json:"-"`
}

// AssistantAttachment is one file sent to the assistant.
type AssistantAttachment struct {
	ID        string `json:"id"`
	MessageID int64  `json:"-"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	Kind      string `json:"kind"` // image | document
	Path      string `json:"-"`    // relative to the assistant project dir
	TS        int64  `json:"ts"`
	Expired   bool   `json:"expired"`
	// Outgoing images (sent BY the assistant): direction "out", pixel size and
	// caption. url is where an authenticated client fetches the bytes.
	Direction string `json:"direction"` // in | out
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Caption   string `json:"caption"`
	URL       string `json:"url"`
}

// AssistantAttachmentURL is the authenticated route serving an attachment.
func AssistantAttachmentURL(id string) string { return "/api/assistant/attachments/" + id }

// AddAssistantMessage appends a message to the thread and returns it with its id.
func (db *DB) AddAssistantMessage(channel, role, text string) (AssistantMessage, error) {
	m := AssistantMessage{TS: time.Now().UnixMilli(), Channel: channel, Role: role, Text: text, Attachments: []AssistantAttachment{}}
	res, err := db.Exec(`INSERT INTO assistant_messages(ts,channel,role,text) VALUES(?,?,?,?)`,
		m.TS, m.Channel, m.Role, m.Text)
	if err != nil {
		return m, err
	}
	m.ID, _ = res.LastInsertId()
	return m, nil
}

// AddHeldAssistantMessage appends an assistant message that is held back until
// the given unix ms instead of being delivered (tool/jid: its WhatsApp target).
func (db *DB) AddHeldAssistantMessage(channel, text string, ts, until int64, tool, jid string) (AssistantMessage, error) {
	m := AssistantMessage{TS: ts, Channel: channel, Role: "assistant", Text: text,
		Attachments: []AssistantAttachment{}, HeldUntil: until, Held: true, HeldTool: tool, HeldJID: jid}
	res, err := db.Exec(`INSERT INTO assistant_messages(ts,channel,role,text,held_until,held_tool,held_jid) VALUES(?,?,?,?,?,?,?)`,
		m.TS, m.Channel, m.Role, m.Text, until, tool, jid)
	if err != nil {
		return m, err
	}
	m.ID, _ = res.LastInsertId()
	return m, nil
}

// SetAssistantMessageTS rewrites a message's timestamp (tests).
func (db *DB) SetAssistantMessageTS(id, ts int64) error {
	_, err := db.Exec(`UPDATE assistant_messages SET ts=? WHERE id=?`, ts, id)
	return err
}

// HeldAssistantMessages lists the messages still held back, oldest first.
func (db *DB) HeldAssistantMessages() ([]AssistantMessage, error) {
	rows, err := db.Query(`SELECT ` + msgCols + ` FROM assistant_messages WHERE held_until>0 ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	rows.Close()
	return out, db.fillHeldAttachments(out)
}

// fillHeldAttachments loads the attachments of messages that are not contiguous.
func (db *DB) fillHeldAttachments(msgs []AssistantMessage) error {
	for i := range msgs {
		rows, err := db.Query(`SELECT `+attCols+` FROM assistant_attachments WHERE message_id=? ORDER BY ts, rowid`, msgs[i].ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			a, err := scanAttachment(rows)
			if err != nil {
				rows.Close()
				return err
			}
			msgs[i].Attachments = append(msgs[i].Attachments, a)
		}
		rows.Close()
	}
	return nil
}

// ReleaseAssistantMessages marks held messages as delivered; it returns how many
// were still held (a message already released is not counted twice).
func (db *DB) ReleaseAssistantMessages(ids []int64) (int, error) {
	n := 0
	for _, id := range ids {
		res, err := db.Exec(`UPDATE assistant_messages SET held_until=0 WHERE id=? AND held_until>0`, id)
		if err != nil {
			return n, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	return n, nil
}

// RescheduleAssistantMessage changes the delivery time of a held message.
func (db *DB) RescheduleAssistantMessage(id, until int64) error {
	_, err := db.Exec(`UPDATE assistant_messages SET held_until=? WHERE id=? AND held_until>0`, until, id)
	return err
}

// LastAssistantUserMessage is the time (unix ms) of the user's latest message to
// the assistant, any channel; 0 when there is none.
func (db *DB) LastAssistantUserMessage() (int64, error) {
	var ts int64
	err := db.QueryRow(`SELECT COALESCE(MAX(ts),0) FROM assistant_messages WHERE role='user'`).Scan(&ts)
	return ts, err
}

const msgCols = `id,ts,channel,role,text,held_until,held_tool,held_jid`

func scanMessages(rows *sql.Rows) ([]AssistantMessage, error) {
	out := []AssistantMessage{}
	for rows.Next() {
		var m AssistantMessage
		if err := rows.Scan(&m.ID, &m.TS, &m.Channel, &m.Role, &m.Text, &m.HeldUntil, &m.HeldTool, &m.HeldJID); err != nil {
			return nil, err
		}
		m.Held = m.HeldUntil > 0
		m.Attachments = []AssistantAttachment{}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListAssistantMessages returns up to limit messages with id > after, oldest
// first; after=0 returns the most recent limit messages.
func (db *DB) ListAssistantMessages(after int64, limit int) ([]AssistantMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT ` + msgCols + ` FROM assistant_messages WHERE id>? ORDER BY id ASC LIMIT ?`
	args := []any{after, limit}
	if after == 0 {
		q = `SELECT ` + msgCols + ` FROM (SELECT ` + msgCols + ` FROM assistant_messages ORDER BY id DESC LIMIT ?) ORDER BY id ASC`
		args = []any{limit}
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	rows.Close()
	return out, db.fillAttachments(out)
}

// fillAttachments loads the attachments of msgs in one query.
func (db *DB) fillAttachments(msgs []AssistantMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	idx := make(map[int64]int, len(msgs))
	for i, m := range msgs {
		idx[m.ID] = i
	}
	rows, err := db.Query(`SELECT `+attCols+` FROM assistant_attachments WHERE message_id>=? AND message_id<=? ORDER BY ts, rowid`,
		msgs[0].ID, msgs[len(msgs)-1].ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return err
		}
		if i, ok := idx[a.MessageID]; ok {
			msgs[i].Attachments = append(msgs[i].Attachments, a)
		}
	}
	return rows.Err()
}

const attCols = `id,message_id,name,mime,size,path,ts,expired,direction,width,height,caption`

func scanAttachment(r interface{ Scan(...any) error }) (AssistantAttachment, error) {
	var a AssistantAttachment
	var exp int
	if err := r.Scan(&a.ID, &a.MessageID, &a.Name, &a.Mime, &a.Size, &a.Path, &a.TS, &exp, &a.Direction, &a.Width, &a.Height, &a.Caption); err != nil {
		return a, err
	}
	a.Expired = exp != 0
	a.URL = AssistantAttachmentURL(a.ID)
	a.Kind = "document"
	if strings.HasPrefix(a.Mime, "image/") {
		a.Kind = "image"
	}
	return a, nil
}

// CreateAssistantAttachment records an uploaded file (not yet linked to a message).
func (db *DB) CreateAssistantAttachment(a AssistantAttachment) error {
	_, err := db.Exec(`INSERT INTO assistant_attachments(id,message_id,name,mime,size,path,ts,direction,width,height,caption) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.MessageID, a.Name, a.Mime, a.Size, a.Path, a.TS, dirOrIn(a.Direction), a.Width, a.Height, a.Caption)
	return err
}

func dirOrIn(d string) string {
	if d == "out" {
		return d
	}
	return "in"
}

// GetAssistantAttachment returns one attachment (sql.ErrNoRows when unknown).
func (db *DB) GetAssistantAttachment(id string) (AssistantAttachment, error) {
	return scanAttachment(db.QueryRow(`SELECT `+attCols+` FROM assistant_attachments WHERE id=?`, id))
}

// LinkAssistantAttachments attaches still-unsent uploads to a message and
// returns them in the order of ids. Unknown or already-sent ids are an error.
func (db *DB) LinkAssistantAttachments(messageID int64, ids []string) ([]AssistantAttachment, error) {
	out := make([]AssistantAttachment, 0, len(ids))
	for _, id := range ids {
		a, err := db.GetAssistantAttachment(id)
		if err != nil || a.MessageID != 0 || a.Expired {
			return nil, fmt.Errorf("unknown attachment %q", id)
		}
		out = append(out, a)
	}
	for i, a := range out {
		if _, err := db.Exec(`UPDATE assistant_attachments SET message_id=? WHERE id=? AND message_id=0`, messageID, a.ID); err != nil {
			return nil, err
		}
		out[i].MessageID = messageID
	}
	return out, nil
}

// ExpirableAssistantAttachments lists the files to purge: sent ones older than
// the retention cutoff and unsent uploads older than orphanCutoff (unix ms).
func (db *DB) ExpirableAssistantAttachments(cutoff, orphanCutoff int64) ([]AssistantAttachment, error) {
	rows, err := db.Query(`SELECT `+attCols+` FROM assistant_attachments WHERE expired=0 AND
		((message_id>0 AND ts<?) OR (message_id=0 AND ts<?))`, cutoff, orphanCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AssistantAttachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ExpireAssistantAttachment marks a purged file (unsent uploads are deleted).
func (db *DB) ExpireAssistantAttachment(a AssistantAttachment) error {
	if a.MessageID == 0 {
		_, err := db.Exec(`DELETE FROM assistant_attachments WHERE id=?`, a.ID)
		return err
	}
	_, err := db.Exec(`UPDATE assistant_attachments SET expired=1 WHERE id=?`, a.ID)
	return err
}

// AllAssistantAttachments lists every non-expired attachment.
func (db *DB) AllAssistantAttachments() ([]AssistantAttachment, error) {
	return db.ExpirableAssistantAttachments(1<<62, 1<<62)
}

// ClearAssistantMessages empties the thread.
func (db *DB) ClearAssistantMessages() error {
	if _, err := db.Exec(`DELETE FROM assistant_attachments`); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM assistant_messages`)
	return err
}

// AssistantTrigger is a place-based trigger.
type AssistantTrigger struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Place     string `json:"place"`
	Event     string `json:"event"` // enter | leave
	Prompt    string `json:"prompt"`
	Enabled   bool   `json:"enabled"`
	LastFired int64  `json:"lastFired"` // unix ms, 0 = never
	CreatedAt int64  `json:"createdAt"`
}

const triggerCols = `id,name,place,event,prompt,enabled,last_fired,created_at`

func (db *DB) CreateAssistantTrigger(t AssistantTrigger) error {
	_, err := db.Exec(`INSERT INTO assistant_triggers(`+triggerCols+`) VALUES(?,?,?,?,?,?,?,?)`,
		t.ID, t.Name, t.Place, t.Event, t.Prompt, b2i(t.Enabled), t.LastFired, t.CreatedAt)
	return err
}

func (db *DB) ListAssistantTriggers() ([]AssistantTrigger, error) {
	rows, err := db.Query(`SELECT ` + triggerCols + ` FROM assistant_triggers ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssistantTrigger{}
	for rows.Next() {
		var t AssistantTrigger
		var en int
		if err := rows.Scan(&t.ID, &t.Name, &t.Place, &t.Event, &t.Prompt, &en, &t.LastFired, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Enabled = en != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

func (db *DB) UpdateAssistantTrigger(t AssistantTrigger) error {
	_, err := db.Exec(`UPDATE assistant_triggers SET name=?,place=?,event=?,prompt=?,enabled=? WHERE id=?`,
		t.Name, t.Place, t.Event, t.Prompt, b2i(t.Enabled), t.ID)
	return err
}

func (db *DB) DeleteAssistantTrigger(id string) error {
	_, err := db.Exec(`DELETE FROM assistant_triggers WHERE id=?`, id)
	return err
}

func (db *DB) MarkAssistantTriggerFired(id string) error {
	_, err := db.Exec(`UPDATE assistant_triggers SET last_fired=? WHERE id=?`, time.Now().UnixMilli(), id)
	return err
}
