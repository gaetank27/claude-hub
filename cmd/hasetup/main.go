// Command hasetup is a one-shot helper (run manually, not part of the backend):
// it reads the configured Telegram connector to resolve a default chat_id (via
// getUpdates when none is stored) and registers the standalone "Home Assistant"
// hub project. Safe against the live DB: SQLite WAL + busy_timeout, idempotent.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"system/internal/secretbox"
	"system/internal/store"
)

func main() {
	dataDir := os.Getenv("SYSTEM_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(os.Getenv("HOME"), ".local", "share", "system")
	}
	db, err := store.Open(filepath.Join(dataDir, "system.db"))
	must(err)
	box, err := secretbox.Open(filepath.Join(dataDir, "secret.key"))
	must(err)

	// 1) Resolve the Telegram default chat_id.
	tools, err := db.ListTools()
	must(err)
	var token, chatID string
	for _, t := range tools {
		if t.Type != "telegram" {
			continue
		}
		b, err := box.Decrypt(t.ConfigEnc)
		must(err)
		var cfg map[string]string
		_ = json.Unmarshal(b, &cfg)
		token, chatID = cfg["bot_token"], cfg["chat_id"]
	}
	if chatID == "" && token != "" {
		chatID = fetchChatID(token)
	}
	fmt.Println("CHAT_ID=" + chatID)

	// 2) Register the Home Assistant project (idempotent by slug).
	const slug = "homeassistant"
	if _, err := db.GetProjectBySlug(slug); err == nil {
		fmt.Println("PROJECT=exists")
		return
	}
	p := store.Project{
		ID:        uuid.NewString(),
		Name:      "Home Assistant",
		Slug:      slug,
		Dir:       "/home/claude/projects/homeassistant",
		Category:  "Domotique",
		Comment:   "Automatisations HA : maintenance nocturne (snapshot Proxmox + updates + rollback auto).",
		CreatedAt: time.Now().UTC(),
	}
	must(db.CreateProject(p))
	fmt.Println("PROJECT=created " + p.ID)
}

// fetchChatID returns the most recent chat id that messaged the bot (empty if the
// backlog is empty or a webhook is set). Telegram only retains ~24 h of updates.
func fetchChatID(token string) string {
	resp, err := http.Get("https://api.telegram.org/bot" + token + "/getUpdates")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var r struct {
		Result []struct {
			Message struct {
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
			MyChatMember struct {
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"my_chat_member"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return ""
	}
	for i := len(r.Result) - 1; i >= 0; i-- {
		if id := r.Result[i].Message.Chat.ID; id != 0 {
			return fmt.Sprint(id)
		}
		if id := r.Result[i].MyChatMember.Chat.ID; id != 0 {
			return fmt.Sprint(id)
		}
	}
	return ""
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR:", err)
		os.Exit(1)
	}
}
