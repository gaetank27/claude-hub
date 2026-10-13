package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func init() { register(telegram{}) }

// telegram sends messages through the Telegram Bot API.
type telegram struct{}

func (telegram) Type() string  { return "telegram" }
func (telegram) Label() string { return "Telegram" }

func (telegram) Fields() []Field {
	return []Field{
		{Key: "bot_token", Label: "Bot token", Type: FieldPassword, Secret: true, Required: true,
			Placeholder: "123456:ABC-DEF…"},
		{Key: "chat_id", Label: "Chat ID par défaut", Type: FieldText,
			Placeholder: "ex. 123456789 ou @canal"},
	}
}

func (telegram) Test(ctx context.Context, cfg map[string]string) error {
	if err := missing(cfg, "bot_token"); err != nil {
		return err
	}
	var res struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := tgCall(ctx, cfg["bot_token"], "getMe", nil, &res); err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("telegram rejected the bot token")
	}
	return nil
}

// DetectChat implements ChatDetector: it long-polls getUpdates so the assisted
// setup flow can capture the chat id from the first message the user sends to the
// bot, instead of making them look it up by hand. Stateless — the caller carries
// NextOffset between rounds. A bot with an active webhook can't use getUpdates;
// Telegram's own 409 description is surfaced verbatim so the UI can explain it.
func (telegram) DetectChat(ctx context.Context, cfg map[string]string, offset int64) (DetectResult, error) {
	if err := missing(cfg, "bot_token"); err != nil {
		return DetectResult{}, err
	}
	params := url.Values{
		"timeout":         {"10"}, // long-poll, within httpClient's 15s budget
		"allowed_updates": {`["message"]`},
	}
	if offset > 0 {
		params.Set("offset", strconv.FormatInt(offset, 10))
	}
	var res struct {
		OK     bool `json:"ok"`
		Result []struct {
			UpdateID int64 `json:"update_id"`
			Message  struct {
				Text string `json:"text"`
				Chat struct {
					ID        int64  `json:"id"`
					Title     string `json:"title"`
					Username  string `json:"username"`
					FirstName string `json:"first_name"`
					LastName  string `json:"last_name"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := tgCall(ctx, cfg["bot_token"], "getUpdates", params, &res); err != nil {
		return DetectResult{}, err
	}
	out := DetectResult{NextOffset: offset}
	for _, u := range res.Result {
		if u.UpdateID >= out.NextOffset {
			out.NextOffset = u.UpdateID + 1
		}
		c := u.Message.Chat
		if c.ID == 0 {
			continue
		}
		out.Chats = append(out.Chats, DetectedChat{
			ChatID: c.ID,
			Title:  chatTitle(c.Title, c.FirstName, c.LastName, c.Username),
			Text:   u.Message.Text,
		})
	}
	return out, nil
}

// chatTitle picks a human label for a detected chat: group title, else the
// sender's full name, else the @username.
func chatTitle(title, first, last, username string) string {
	if title != "" {
		return title
	}
	name := strings.TrimSpace(first + " " + last)
	if name != "" {
		return name
	}
	if username != "" {
		return "@" + username
	}
	return ""
}

// TestMessage implements MessageTester: it sends a canned message to the
// configured chat so the user can confirm the bot reaches them end-to-end,
// reusing the same send path as the MCP action.
func (tg telegram) TestMessage(ctx context.Context, cfg map[string]string) (string, error) {
	if strings.TrimSpace(cfg["chat_id"]) == "" {
		return "", fmt.Errorf("configure a chat_id first")
	}
	return tg.Call(ctx, cfg, "telegram_send_message", map[string]any{
		"text": "✅ Test depuis Claude Hub — le connecteur Telegram fonctionne.",
	})
}

func (telegram) Tools() []ToolSpec {
	return []ToolSpec{
		{
			Name:        "telegram_send_message",
			Description: "Send a Telegram message via the configured bot.",
			Args: []ArgSpec{
				{Name: "text", Type: "string", Description: "Message text", Required: true},
				{Name: "chat_id", Type: "string", Description: "Target chat id (defaults to the configured one)"},
			},
		},
		{
			Name: "telegram_send_photo",
			Description: "Send a photo to a Telegram chat. `photo` is either a public http(s) URL " +
				"(Telegram fetches it) or a local file path on the host (uploaded directly).",
			Args: []ArgSpec{
				{Name: "photo", Type: "string", Description: "Public http(s) URL or local file path of the image to send", Required: true},
				{Name: "caption", Type: "string", Description: "Optional caption shown under the photo"},
				{Name: "chat_id", Type: "string", Description: "Target chat id (defaults to the configured one)"},
			},
		},
		{
			Name: "telegram_get_updates",
			Description: "Poll the bot's incoming messages (getUpdates) — the reply channel. Returns JSON " +
				`{"next_offset":N,"messages":[{"update_id","chat_id","text","date"}]}. Pass offset (a prior ` +
				"next_offset) to acknowledge and skip older messages. Fails if a webhook is set on the bot.",
			Args: []ArgSpec{
				{Name: "offset", Type: "string", Description: "Only return updates with update_id >= offset (use a prior next_offset to acknowledge)"},
				{Name: "timeout", Type: "string", Description: "Long-poll seconds (0-10, default 0 = return immediately)"},
			},
		},
	}
}

func (telegram) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	switch action {
	case "telegram_send_message":
		text := argString(args, "text", "")
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("text is required")
		}
		chat := argString(args, "chat_id", cfg["chat_id"])
		if chat == "" {
			return "", fmt.Errorf("no chat_id given and no default configured")
		}
		var res struct {
			OK bool `json:"ok"`
		}
		params := url.Values{"chat_id": {chat}, "text": {text}}
		if err := tgCall(ctx, cfg["bot_token"], "sendMessage", params, &res); err != nil {
			return "", err
		}
		return "Message sent to " + chat, nil
	case "telegram_send_photo":
		photo := strings.TrimSpace(argString(args, "photo", ""))
		if photo == "" {
			return "", fmt.Errorf("photo is required (http(s) URL or local file path)")
		}
		chat := argString(args, "chat_id", cfg["chat_id"])
		if chat == "" {
			return "", fmt.Errorf("no chat_id given and no default configured")
		}
		if err := tgSendPhoto(ctx, cfg["bot_token"], chat, photo, argString(args, "caption", "")); err != nil {
			return "", err
		}
		return "Photo sent to " + chat, nil
	case "telegram_get_updates":
		params := url.Values{"allowed_updates": {`["message"]`}}
		if off := strings.TrimSpace(argString(args, "offset", "")); off != "" {
			params.Set("offset", off)
		}
		if to := strings.TrimSpace(argString(args, "timeout", "")); to != "" {
			params.Set("timeout", to)
		}
		var res struct {
			OK     bool `json:"ok"`
			Result []struct {
				UpdateID int64 `json:"update_id"`
				Message  struct {
					Text string `json:"text"`
					Date int64  `json:"date"`
					Chat struct {
						ID int64 `json:"id"`
					} `json:"chat"`
				} `json:"message"`
			} `json:"result"`
		}
		if err := tgCall(ctx, cfg["bot_token"], "getUpdates", params, &res); err != nil {
			return "", err
		}
		type tgMsg struct {
			UpdateID int64  `json:"update_id"`
			ChatID   int64  `json:"chat_id"`
			Text     string `json:"text"`
			Date     int64  `json:"date"`
		}
		var next int64
		msgs := []tgMsg{}
		for _, u := range res.Result {
			if u.UpdateID+1 > next {
				next = u.UpdateID + 1
			}
			if u.Message.Text == "" {
				continue
			}
			msgs = append(msgs, tgMsg{u.UpdateID, u.Message.Chat.ID, u.Message.Text, u.Message.Date})
		}
		b, err := json.Marshal(map[string]any{"next_offset": next, "messages": msgs})
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

// tgSendPhoto sends a photo via sendPhoto. An http(s) URL is handed to Telegram
// as-is (it fetches the image itself); anything else is treated as a local file
// path on the host and uploaded as multipart/form-data.
func tgSendPhoto(ctx context.Context, token, chat, photo, caption string) error {
	if strings.HasPrefix(photo, "http://") || strings.HasPrefix(photo, "https://") {
		params := url.Values{"chat_id": {chat}, "photo": {photo}}
		if caption != "" {
			params.Set("caption", caption)
		}
		var res struct {
			OK bool `json:"ok"`
		}
		return tgCall(ctx, token, "sendPhoto", params, &res)
	}

	f, err := os.Open(photo)
	if err != nil {
		return fmt.Errorf("open photo: %w", err)
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("chat_id", chat)
	if caption != "" {
		mw.WriteField("caption", caption)
	}
	fw, err := mw.CreateFormFile("photo", filepath.Base(photo))
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	mw.Close()

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := httpClient(false).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Description string `json:"description"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Description != "" {
			return fmt.Errorf("telegram: %s", e.Description)
		}
		return fmt.Errorf("telegram: %s", resp.Status)
	}
	return nil
}

// tgCall invokes a Bot API method and decodes the JSON response, surfacing the
// API's own error description on failure.
func tgCall(ctx context.Context, token, method string, params url.Values, out any) error {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/%s", token, method)
	if params != nil {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient(false).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Description string `json:"description"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Description != "" {
			return fmt.Errorf("telegram: %s", e.Description)
		}
		return fmt.Errorf("telegram: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
