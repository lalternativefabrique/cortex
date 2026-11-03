// Package sqlite keeps an agent's memory on the machine, in one SQLite file
// searched by words: what cortex remembers with no server behind it.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/lalternativefabrique/cortex/recall"
)

// DefaultPath is where the machine's memory lives, following XDG alongside
// cortex's sessions and profiles.
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "skode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	return filepath.Join(dir, "recall.db"), nil
}

const schema = `
CREATE TABLE IF NOT EXISTS memories (
    id           INTEGER PRIMARY KEY,
    subject      TEXT NOT NULL,
    agent        TEXT NOT NULL,
    conversation TEXT NOT NULL,
    message      TEXT NOT NULL,
    kind         TEXT NOT NULL,
    role         TEXT NOT NULL,
    content      TEXT NOT NULL,
    at           TEXT NOT NULL,
    UNIQUE (subject, agent, conversation, message)
);
CREATE INDEX IF NOT EXISTS memories_scope ON memories (subject, agent, at);
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    content, content='memories', content_rowid='id', tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts (rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts (memories_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;
`

// Store is one machine's memory. It satisfies recall.Store.
type Store struct {
	db *sql.DB
}

// Open opens or creates the memory at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("recall: create directory: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("recall: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("recall: prepare %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Remember(ctx context.Context, e recall.Entry) error {
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO memories (subject, agent, conversation, message, kind, role, content, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (subject, agent, conversation, message) DO NOTHING`,
		e.Subject, e.Agent, e.Conversation, e.Message, string(e.Kind), e.Role, e.Content, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("recall: remember: %w", err)
	}
	return nil
}

func (s *Store) Recall(ctx context.Context, scope recall.Scope, query string, limit int) ([]recall.Entry, error) {
	terms := ftsTerms(query)
	if len(terms) == 0 || limit <= 0 {
		return nil, nil
	}
	found, err := s.search(ctx, scope, strings.Join(terms, " AND "), limit)
	if err == nil && len(found) < limit && len(terms) > 1 {
		var more []recall.Entry
		more, err = s.search(ctx, scope, strings.Join(terms, " OR "), limit)
		found = merge(found, more, limit)
	}
	return found, err
}

// search runs one FTS5 match within the scope, best ranked first.
func (s *Store) search(ctx context.Context, scope recall.Scope, match string, limit int) ([]recall.Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.subject, m.agent, m.conversation, m.message, m.kind, m.role, m.content, m.at
		FROM memories_fts f JOIN memories m ON m.id = f.rowid
		WHERE memories_fts MATCH ? AND m.subject = ? AND m.agent = ?
		ORDER BY bm25(memories_fts), m.at DESC
		LIMIT ?`, match, scope.Subject, scope.Agent, limit)
	if err != nil {
		return nil, fmt.Errorf("recall: search: %w", err)
	}
	defer rows.Close()
	var out []recall.Entry
	for rows.Next() {
		var e recall.Entry
		var kind, at string
		if err := rows.Scan(&e.Subject, &e.Agent, &e.Conversation, &e.Message, &kind, &e.Role, &e.Content, &at); err != nil {
			return nil, fmt.Errorf("recall: read: %w", err)
		}
		e.Kind = recall.Kind(kind)
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Forget(ctx context.Context, scope recall.Scope, conversation string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE subject = ? AND agent = ? AND conversation = ?`,
		scope.Subject, scope.Agent, conversation)
	if err != nil {
		return fmt.Errorf("recall: forget: %w", err)
	}
	return nil
}

// ftsTerms turns free text into FTS5 terms, each quoted and prefix-matched,
// so punctuation and operators in what the model asks never break the
// search. Words under three letters are dropped: articles and pronouns are
// rare in a small memory, which makes them rank above the words that
// matter. Recall asks for every term first, then for any of them.
func ftsTerms(query string) []string {
	words := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	terms := make([]string, 0, len(words))
	for _, w := range words {
		if len([]rune(w)) < 3 {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"*`)
	}
	return terms
}

func merge(first, more []recall.Entry, limit int) []recall.Entry {
	seen := map[string]bool{}
	for _, e := range first {
		seen[e.Conversation+"\x00"+e.Message] = true
	}
	for _, e := range more {
		if len(first) >= limit {
			break
		}
		if key := e.Conversation + "\x00" + e.Message; !seen[key] {
			seen[key] = true
			first = append(first, e)
		}
	}
	return first
}
