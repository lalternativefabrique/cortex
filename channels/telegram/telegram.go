// Package telegram lets a person talk to their agent from Telegram: the bot
// polls the Bot API, so no public address is needed, answers the people
// allowed to talk to it, and delivers what the agent produces on its own.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	DefaultBaseURL = "https://api.telegram.org"
	// pollTimeout is how long one getUpdates waits for a message.
	pollTimeout = 30 * time.Second
	// MaxMessageBytes is Telegram's bound on one message; longer answers are
	// sent in several.
	MaxMessageBytes = 4000
	errorPause      = 5 * time.Second
)

// Ask runs one turn for subject in conversation and returns the answer.
type Ask func(ctx context.Context, subject, conversation, text string) (string, error)

type Config struct {
	Token string
	// Allowed are the Telegram user ids the bot answers. Empty answers
	// no one: a bot token leaks, a user id list does not.
	Allowed []int64
	// Subject names the memory a user talks under. Nil names them tg:<id>.
	Subject func(userID int64) string
	Ask     Ask
	// DeliverChat is where Deliver sends when set; otherwise the chat the
	// first allowed user last wrote from.
	DeliverChat int64
	BaseURL     string
	HTTPClient  *http.Client
}

type Bot struct {
	cfg  Config
	http *http.Client
	base string

	mu       sync.Mutex
	lastChat int64
}

func New(cfg Config) (*Bot, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("telegram: a bot token is required")
	}
	if cfg.Ask == nil {
		return nil, errors.New("telegram: Ask is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Subject == nil {
		cfg.Subject = func(id int64) string { return "tg:" + strconv.FormatInt(id, 10) }
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: pollTimeout + 15*time.Second}
	}
	return &Bot{cfg: cfg, http: hc, base: strings.TrimRight(cfg.BaseURL, "/") + "/bot" + cfg.Token}, nil
}

type update struct {
	ID      int64    `json:"update_id"`
	Message *message `json:"message"`
}

type message struct {
	ID       int64  `json:"message_id"`
	ThreadID int64  `json:"message_thread_id"`
	Text     string `json:"text"`
	From     *struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

// Serve polls for messages until ctx ends. Each message is answered in
// its own goroutine, so a slow turn does not hold the others.
func (b *Bot) Serve(ctx context.Context) error {
	var offset int64
	for {
		updates, err := b.updates(ctx, offset)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			slog.Warn("telegram: updates not read", "error", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(errorPause):
			}
			continue
		}
		for _, u := range updates {
			offset = u.ID + 1
			if u.Message == nil || u.Message.From == nil || strings.TrimSpace(u.Message.Text) == "" {
				continue
			}
			if !b.allowed(u.Message.From.ID) {
				slog.Warn("telegram: message from a user not allowed", "user", u.Message.From.ID)
				continue
			}
			go b.answer(ctx, *u.Message)
		}
	}
}

func (b *Bot) allowed(userID int64) bool {
	for _, id := range b.cfg.Allowed {
		if id == userID {
			return true
		}
	}
	return false
}

// Conversation is the conversation a chat, or a topic in it, talks in.
func Conversation(chatID, threadID int64) string {
	if threadID != 0 {
		return fmt.Sprintf("telegram:%d:%d", chatID, threadID)
	}
	return fmt.Sprintf("telegram:%d", chatID)
}

func (b *Bot) answer(ctx context.Context, m message) {
	if b.cfg.DeliverChat == 0 && len(b.cfg.Allowed) > 0 && m.From.ID == b.cfg.Allowed[0] {
		b.mu.Lock()
		b.lastChat = m.Chat.ID
		b.mu.Unlock()
	}
	_ = b.call(ctx, "sendChatAction", map[string]any{"chat_id": m.Chat.ID, "action": "typing"}, nil)
	answer, err := b.cfg.Ask(ctx, b.cfg.Subject(m.From.ID), Conversation(m.Chat.ID, m.ThreadID), m.Text)
	if err != nil {
		slog.Warn("telegram: turn failed", "chat", m.Chat.ID, "error", err)
		answer = "Je n'ai pas pu répondre : " + err.Error()
	}
	if err := b.send(ctx, m.Chat.ID, m.ThreadID, answer); err != nil {
		slog.Warn("telegram: answer not sent", "chat", m.Chat.ID, "error", err)
	}
}

// Deliver sends text to the person: what a scheduled task produced.
func (b *Bot) Deliver(ctx context.Context, text string) error {
	chat := b.cfg.DeliverChat
	if chat == 0 {
		b.mu.Lock()
		chat = b.lastChat
		b.mu.Unlock()
	}
	if chat == 0 {
		return errors.New("telegram: no chat to deliver to yet; write to the bot once")
	}
	return b.send(ctx, chat, 0, text)
}

func (b *Bot) send(ctx context.Context, chatID, threadID int64, text string) error {
	if strings.TrimSpace(text) == "" {
		text = "(no answer)"
	}
	for _, chunk := range split(text, MaxMessageBytes) {
		params := map[string]any{"chat_id": chatID, "text": chunk}
		if threadID != 0 {
			params["message_thread_id"] = threadID
		}
		if err := b.call(ctx, "sendMessage", params, nil); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) updates(ctx context.Context, offset int64) ([]update, error) {
	var out []update
	err := b.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": int(pollTimeout.Seconds()), "allowed_updates": []string{"message"},
	}, &out)
	return out, err
}

func (b *Bot) call(ctx context.Context, method string, params any, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		if envelope.Description == "" {
			envelope.Description = res.Status
		}
		return fmt.Errorf("telegram %s: %s", method, envelope.Description)
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

// split cuts text into chunks of at most n bytes, on a line or a space
// when one is near, never inside a rune.
func split(text string, n int) []string {
	var out []string
	for len(text) > n {
		cut := n
		if i := strings.LastIndexByte(text[:n], '\n'); i > n/2 {
			cut = i
		} else if i := strings.LastIndexByte(text[:n], ' '); i > n/2 {
			cut = i
		}
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		out = append(out, strings.TrimRight(text[:cut], "\n "))
		text = strings.TrimLeft(text[cut:], "\n ")
	}
	return append(out, text)
}
