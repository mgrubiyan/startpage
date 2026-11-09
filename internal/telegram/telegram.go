// Package telegram sends plain-text messages through the Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// maxRunes keeps a message under the API's 4096-character limit, which counts
// UTF-16 code units: emoji take two, so leave room for them.
const maxRunes = 3500

// Bot sends messages to one chat.
type Bot struct {
	client *http.Client
	base   string
	token  string
	chatID string
}

// New returns a Bot that posts to chatID (a numeric id, negative for groups, or
// @channel) with the token from @BotFather.
func New(client *http.Client, token, chatID string) *Bot {
	return &Bot{client: client, base: "https://api.telegram.org", token: token, chatID: chatID}
}

// Send posts text as a plain message: no parse mode, so names and errors in it
// can't break the markup, and no link previews.
//
// The token is part of the request URL, so errors never include the URL.
func (b *Bot) Send(ctx context.Context, text string) error {
	if r := []rune(text); len(r) > maxRunes {
		text = string(r[:maxRunes]) + "…"
	}
	body, err := json.Marshal(struct {
		ChatID    string `json:"chat_id"`
		Text      string `json:"text"`
		NoPreview bool   `json:"disable_web_page_preview"`
	}{b.chatID, text, true})
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/bot"+b.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return errors.New("telegram: bad request URL") // the parse error would quote the token
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // *url.Error prints the full URL, token included
		}
		return fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()

	var reply struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&reply); err != nil {
		return fmt.Errorf("telegram: HTTP %d, unreadable reply", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || !reply.OK {
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, reply.Description)
	}
	return nil
}
