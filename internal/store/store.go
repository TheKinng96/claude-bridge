// Package store provides an app-level SQLite database for persisting
// account metadata, credentials, cached data, and other state across restarts.
package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	// sqlite-fk driver is registered in sqlitedriver.go — wraps modernc.org/sqlite
	// with automatic PRAGMA foreign_keys = ON on every connection.
)

// Account represents a connected messaging account (WhatsApp, FB, etc).
type Account struct {
	ID          int64     `json:"id"`
	Channel     string    `json:"channel"`      // "whatsapp", "facebook", "instagram", "linkedin", "xiaohongshu"
	JID         string    `json:"jid"`           // unique identifier (e.g. "6281234567890@s.whatsapp.net")
	PhoneNumber string    `json:"phone_number"`
	PushName    string    `json:"push_name"`
	ProfilePic  string    `json:"profile_pic,omitempty"`
	Status      string    `json:"status"` // "connected", "disconnected"
	CreatedAt   time.Time `json:"created_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// Credential stores login credentials for browser-based connectors.
type Credential struct {
	ID        int64     `json:"id"`
	Platform  string    `json:"platform"` // "facebook", "instagram", "linkedin", "xiaohongshu"
	Email     string    `json:"email"`
	Password  string    `json:"password"` // stored locally only — never leaves the machine
	Extra     string    `json:"extra,omitempty"` // JSON blob for platform-specific fields (e.g. page_id)
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CachedContact stores a contact from any platform.
type CachedContact struct {
	ID         int64     `json:"id"`
	Platform   string    `json:"platform"`
	ContactID  string    `json:"contact_id"` // platform-specific unique ID
	Name       string    `json:"name"`
	Username   string    `json:"username,omitempty"`
	AvatarURL  string    `json:"avatar_url,omitempty"`
	ProfileURL string    `json:"profile_url,omitempty"`
	Extra      string    `json:"extra,omitempty"` // JSON blob for platform-specific data
	LastSynced time.Time `json:"last_synced"`
}

// CachedMessage stores a message from any platform.
type CachedMessage struct {
	ID             int64     `json:"id"`
	Platform       string    `json:"platform"`
	ConversationID string    `json:"conversation_id"`
	MessageID      string    `json:"message_id"`
	SenderID       string    `json:"sender_id"`
	SenderName     string    `json:"sender_name"`
	Content        string    `json:"content"`
	MediaURL       string    `json:"media_url,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
	IsOutgoing     bool      `json:"is_outgoing"`
	LastSynced     time.Time `json:"last_synced"`
}

// CachedPost stores a post from any platform.
type CachedPost struct {
	ID         int64     `json:"id"`
	Platform   string    `json:"platform"`
	PostID     string    `json:"post_id"`
	Content    string    `json:"content"`
	MediaURLs  string    `json:"media_urls,omitempty"` // JSON array
	PostURL    string    `json:"post_url,omitempty"`
	PostedAt   time.Time `json:"posted_at"`
	Likes      int       `json:"likes"`
	Comments   int       `json:"comments"`
	Shares     int       `json:"shares"`
	LastSynced time.Time `json:"last_synced"`
}

// CachedComment stores a comment on a post from any platform.
type CachedComment struct {
	ID         int64     `json:"id"`
	Platform   string    `json:"platform"`
	PostID     string    `json:"post_id"`
	CommentID  string    `json:"comment_id"`
	AuthorID   string    `json:"author_id"`
	AuthorName string    `json:"author_name"`
	Content    string    `json:"content"`
	Likes      int       `json:"likes"`
	Timestamp  time.Time `json:"timestamp"`
	LastSynced time.Time `json:"last_synced"`
}

// HealthCheckResult stores the result of a connector health check.
type HealthCheckResult struct {
	ID        int64     `json:"id"`
	Platform  string    `json:"platform"`
	Status    string    `json:"status"` // "ok", "failed"
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// Document represents an ingested knowledge file.
type Document struct {
	ID          int64     `json:"id"`
	Path        string    `json:"path"`
	Filename    string    `json:"filename"`
	FileHash    string    `json:"file_hash"`
	SizeBytes   int64     `json:"size_bytes"`
	MimeType    string    `json:"mime_type"`
	DocType     string    `json:"doc_type,omitempty"`
	Product     string    `json:"product,omitempty"`
	Language    string    `json:"language,omitempty"`
	Audience    string    `json:"audience,omitempty"`  // JSON array
	Summary     string    `json:"summary,omitempty"`
	KeyTerms    string    `json:"key_terms,omitempty"` // JSON array
	RawText     string    `json:"raw_text,omitempty"`
	Status      string    `json:"status"` // pending|extracting|classifying|ready|failed
	Error       string    `json:"error,omitempty"`
	IngestedAt  time.Time `json:"ingested_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// DocumentSearchHit is a single ranked search hit.
type DocumentSearchHit struct {
	Document Document `json:"document"`
	Snippet  string   `json:"snippet"`
	Rank     float64  `json:"rank"`
}

// Contact represents a person we've interacted with on any messaging platform.
type Contact struct {
	ID          int64     `json:"id"`
	JID         string    `json:"jid"`
	Platform    string    `json:"platform"`
	PushName    string    `json:"push_name"`
	FirstSeenAt time.Time `json:"first_seen_at"`
}

// Group is a named segment of contacts (e.g. "VIP", "Leads").
type Group struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	ReplyMode string    `json:"reply_mode"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is the app-level SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the app database at dataDir/app.db.
func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "app.db")
	db, err := sql.Open("sqlite-fk", fmt.Sprintf("file:%s", dbPath))
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	log.Printf("[store] Opened app database: %s", dbPath)
	return s, nil
}

func (s *Store) migrate() error {
	migrations := []string{
		// Original accounts table
		`CREATE TABLE IF NOT EXISTS accounts (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			channel     TEXT NOT NULL DEFAULT 'whatsapp',
			jid         TEXT NOT NULL UNIQUE,
			phone_number TEXT NOT NULL DEFAULT '',
			push_name   TEXT NOT NULL DEFAULT '',
			profile_pic TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'disconnected',
			created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// Credentials for browser-based connectors
		`CREATE TABLE IF NOT EXISTS credentials (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			platform  TEXT NOT NULL UNIQUE,
			email     TEXT NOT NULL DEFAULT '',
			password  TEXT NOT NULL DEFAULT '',
			extra     TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// Cached contacts from all platforms
		`CREATE TABLE IF NOT EXISTS cached_contacts (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			platform   TEXT NOT NULL,
			contact_id TEXT NOT NULL,
			name       TEXT NOT NULL DEFAULT '',
			username   TEXT NOT NULL DEFAULT '',
			avatar_url TEXT NOT NULL DEFAULT '',
			profile_url TEXT NOT NULL DEFAULT '',
			extra      TEXT NOT NULL DEFAULT '{}',
			last_synced DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(platform, contact_id)
		)`,

		// Cached messages from all platforms
		`CREATE TABLE IF NOT EXISTS cached_messages (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			platform        TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			message_id      TEXT NOT NULL,
			sender_id       TEXT NOT NULL DEFAULT '',
			sender_name     TEXT NOT NULL DEFAULT '',
			content         TEXT NOT NULL DEFAULT '',
			media_url       TEXT NOT NULL DEFAULT '',
			timestamp       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			is_outgoing     BOOLEAN NOT NULL DEFAULT 0,
			last_synced     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(platform, message_id)
		)`,

		// Cached posts from all platforms
		`CREATE TABLE IF NOT EXISTS cached_posts (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			platform   TEXT NOT NULL,
			post_id    TEXT NOT NULL,
			content    TEXT NOT NULL DEFAULT '',
			media_urls TEXT NOT NULL DEFAULT '[]',
			post_url   TEXT NOT NULL DEFAULT '',
			posted_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			likes      INTEGER NOT NULL DEFAULT 0,
			comments   INTEGER NOT NULL DEFAULT 0,
			shares     INTEGER NOT NULL DEFAULT 0,
			last_synced DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(platform, post_id)
		)`,

		// Cached comments on posts
		`CREATE TABLE IF NOT EXISTS cached_comments (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			platform    TEXT NOT NULL,
			post_id     TEXT NOT NULL,
			comment_id  TEXT NOT NULL,
			author_id   TEXT NOT NULL DEFAULT '',
			author_name TEXT NOT NULL DEFAULT '',
			content     TEXT NOT NULL DEFAULT '',
			likes       INTEGER NOT NULL DEFAULT 0,
			timestamp   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_synced DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(platform, comment_id)
		)`,

		// Health check results
		`CREATE TABLE IF NOT EXISTS health_checks (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			platform   TEXT NOT NULL,
			status     TEXT NOT NULL DEFAULT 'ok',
			error      TEXT NOT NULL DEFAULT '',
			checked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// Indexes for common queries
		`CREATE INDEX IF NOT EXISTS idx_cached_contacts_platform ON cached_contacts(platform)`,
		`CREATE INDEX IF NOT EXISTS idx_cached_messages_platform ON cached_messages(platform, conversation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_cached_messages_timestamp ON cached_messages(platform, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_cached_posts_platform ON cached_posts(platform, posted_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_cached_comments_post ON cached_comments(platform, post_id, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_health_checks_platform ON health_checks(platform, checked_at DESC)`,

		// Knowledge ingestion: documents table + FTS5 virtual table + sync triggers.
		`CREATE TABLE IF NOT EXISTS documents (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			path         TEXT NOT NULL UNIQUE,
			filename     TEXT NOT NULL,
			file_hash    TEXT NOT NULL,
			size_bytes   INTEGER NOT NULL,
			mime_type    TEXT NOT NULL,
			doc_type     TEXT NOT NULL DEFAULT '',
			product      TEXT NOT NULL DEFAULT '',
			language     TEXT NOT NULL DEFAULT '',
			audience     TEXT NOT NULL DEFAULT '[]',
			summary      TEXT NOT NULL DEFAULT '',
			key_terms    TEXT NOT NULL DEFAULT '[]',
			raw_text     TEXT NOT NULL DEFAULT '',
			status       TEXT NOT NULL DEFAULT 'pending',
			error        TEXT NOT NULL DEFAULT '',
			ingested_at  DATETIME,
			updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_status   ON documents(status)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_type     ON documents(doc_type)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_language ON documents(language)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_hash     ON documents(file_hash)`,

		`CREATE VIRTUAL TABLE IF NOT EXISTS documents_fts USING fts5(
			filename, doc_type, product, language, summary, key_terms, raw_text,
			content='documents', content_rowid='id',
			tokenize='unicode61'
		)`,

		`CREATE TRIGGER IF NOT EXISTS documents_ai AFTER INSERT ON documents BEGIN
			INSERT INTO documents_fts(rowid, filename, doc_type, product, language, summary, key_terms, raw_text)
			VALUES (new.id, new.filename, new.doc_type, new.product, new.language, new.summary, new.key_terms, new.raw_text);
		END`,
		`CREATE TRIGGER IF NOT EXISTS documents_ad AFTER DELETE ON documents BEGIN
			INSERT INTO documents_fts(documents_fts, rowid, filename, doc_type, product, language, summary, key_terms, raw_text)
			VALUES ('delete', old.id, old.filename, old.doc_type, old.product, old.language, old.summary, old.key_terms, old.raw_text);
		END`,
		`CREATE TRIGGER IF NOT EXISTS documents_au AFTER UPDATE ON documents BEGIN
			INSERT INTO documents_fts(documents_fts, rowid, filename, doc_type, product, language, summary, key_terms, raw_text)
			VALUES ('delete', old.id, old.filename, old.doc_type, old.product, old.language, old.summary, old.key_terms, old.raw_text);
			INSERT INTO documents_fts(rowid, filename, doc_type, product, language, summary, key_terms, raw_text)
			VALUES (new.id, new.filename, new.doc_type, new.product, new.language, new.summary, new.key_terms, new.raw_text);
		END`,

		`CREATE TABLE IF NOT EXISTS contacts (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			jid           TEXT NOT NULL UNIQUE,
			platform      TEXT NOT NULL DEFAULT 'whatsapp',
			push_name     TEXT NOT NULL DEFAULT '',
			first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS groups (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT NOT NULL,
			type       TEXT NOT NULL CHECK(type IN ('manual','auto')),
			reply_mode TEXT NOT NULL CHECK(reply_mode IN ('auto','review','off')),
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS contact_groups (
			contact_id  INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
			group_id    INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
			source      TEXT NOT NULL CHECK(source IN ('manual','auto')),
			assigned_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (contact_id, group_id)
		)`,

		`CREATE TABLE IF NOT EXISTS pending_replies (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			contact_jid    TEXT NOT NULL,
			account_jid    TEXT NOT NULL,
			platform       TEXT NOT NULL DEFAULT 'whatsapp',
			incoming_msg   TEXT NOT NULL,
			proposed_reply TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'pending'
			               CHECK(status IN ('pending','approved','rejected','sent')),
			created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			reviewed_at    DATETIME
		)`,

		`CREATE TABLE IF NOT EXISTS magic_tokens (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at DATETIME NOT NULL,
			used_at    DATETIME
		)`,
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
		}
	}

	// Additive column migrations — silently skip if column already exists.
	for _, m := range []string{
		`ALTER TABLE documents ADD COLUMN embedding BLOB`,
	} {
		if _, err := s.db.Exec(m); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
				return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
			}
		}
	}

	return nil
}

// DB returns the underlying database for advanced queries.
func (s *Store) DB() *sql.DB {
	return s.db
}

// UpsertAccount inserts or updates an account by JID.
func (s *Store) UpsertAccount(ctx context.Context, a *Account) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO accounts (channel, jid, phone_number, push_name, profile_pic, status, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			phone_number = excluded.phone_number,
			push_name    = excluded.push_name,
			profile_pic  = CASE WHEN excluded.profile_pic != '' THEN excluded.profile_pic ELSE accounts.profile_pic END,
			status       = excluded.status,
			last_seen_at = excluded.last_seen_at
	`, a.Channel, a.JID, a.PhoneNumber, a.PushName, a.ProfilePic, a.Status, a.LastSeenAt)
	return err
}

// SetStatus updates the status of an account.
func (s *Store) SetStatus(ctx context.Context, jid, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE accounts SET status = ?, last_seen_at = ? WHERE jid = ?
	`, status, time.Now(), jid)
	return err
}

// SetProfilePic updates the profile picture URL of an account.
func (s *Store) SetProfilePic(ctx context.Context, jid, url string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE accounts SET profile_pic = ? WHERE jid = ?
	`, url, jid)
	return err
}

// GetAccounts returns all accounts for a given channel.
func (s *Store) GetAccounts(ctx context.Context, channel string) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, channel, jid, phone_number, push_name, profile_pic, status, created_at, last_seen_at
		FROM accounts WHERE channel = ? ORDER BY created_at ASC
	`, channel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Channel, &a.JID, &a.PhoneNumber, &a.PushName, &a.ProfilePic, &a.Status, &a.CreatedAt, &a.LastSeenAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// GetAccount returns a single account by JID.
func (s *Store) GetAccount(ctx context.Context, jid string) (*Account, error) {
	var a Account
	err := s.db.QueryRowContext(ctx, `
		SELECT id, channel, jid, phone_number, push_name, profile_pic, status, created_at, last_seen_at
		FROM accounts WHERE jid = ?
	`, jid).Scan(&a.ID, &a.Channel, &a.JID, &a.PhoneNumber, &a.PushName, &a.ProfilePic, &a.Status, &a.CreatedAt, &a.LastSeenAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// DeleteAccount removes an account by JID.
func (s *Store) DeleteAccount(ctx context.Context, jid string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE jid = ?`, jid)
	return err
}

// ---------------------------------------------------------------------------
// Credentials (for browser-based connectors)
// ---------------------------------------------------------------------------

// SaveCredential stores or updates credentials for a platform.
func (s *Store) SaveCredential(ctx context.Context, c *Credential) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO credentials (platform, email, password, extra, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(platform) DO UPDATE SET
			email = excluded.email,
			password = excluded.password,
			extra = excluded.extra,
			updated_at = excluded.updated_at
	`, c.Platform, c.Email, c.Password, c.Extra, time.Now())
	return err
}

// GetCredential returns the credential for a platform.
func (s *Store) GetCredential(ctx context.Context, platform string) (*Credential, error) {
	var c Credential
	err := s.db.QueryRowContext(ctx, `
		SELECT id, platform, email, password, extra, created_at, updated_at
		FROM credentials WHERE platform = ?
	`, platform).Scan(&c.ID, &c.Platform, &c.Email, &c.Password, &c.Extra, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// DeleteCredential removes credentials for a platform.
func (s *Store) DeleteCredential(ctx context.Context, platform string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE platform = ?`, platform)
	return err
}

// ---------------------------------------------------------------------------
// Cached Contacts
// ---------------------------------------------------------------------------

// UpsertCachedContact inserts or updates a cached contact.
func (s *Store) UpsertCachedContact(ctx context.Context, c *CachedContact) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cached_contacts (platform, contact_id, name, username, avatar_url, profile_url, extra, last_synced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform, contact_id) DO UPDATE SET
			name = excluded.name,
			username = excluded.username,
			avatar_url = excluded.avatar_url,
			profile_url = excluded.profile_url,
			extra = excluded.extra,
			last_synced = excluded.last_synced
	`, c.Platform, c.ContactID, c.Name, c.Username, c.AvatarURL, c.ProfileURL, c.Extra, time.Now())
	return err
}

// GetCachedContacts returns cached contacts for a platform, with optional search.
func (s *Store) GetCachedContacts(ctx context.Context, platform, query string, limit int) ([]CachedContact, time.Time, error) {
	var lastSynced time.Time
	var args []interface{}

	q := `SELECT id, platform, contact_id, name, username, avatar_url, profile_url, extra, last_synced
		FROM cached_contacts WHERE platform = ?`
	args = append(args, platform)

	if query != "" {
		q += ` AND (name LIKE ? OR username LIKE ?)`
		like := "%" + query + "%"
		args = append(args, like, like)
	}

	q += ` ORDER BY name ASC`

	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, lastSynced, err
	}
	defer rows.Close()

	var contacts []CachedContact
	for rows.Next() {
		var c CachedContact
		if err := rows.Scan(&c.ID, &c.Platform, &c.ContactID, &c.Name, &c.Username, &c.AvatarURL, &c.ProfileURL, &c.Extra, &c.LastSynced); err != nil {
			return nil, lastSynced, err
		}
		if c.LastSynced.After(lastSynced) {
			lastSynced = c.LastSynced
		}
		contacts = append(contacts, c)
	}
	return contacts, lastSynced, nil
}

// ClearCachedContacts removes all cached contacts for a platform.
func (s *Store) ClearCachedContacts(ctx context.Context, platform string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cached_contacts WHERE platform = ?`, platform)
	return err
}

// ---------------------------------------------------------------------------
// Cached Messages
// ---------------------------------------------------------------------------

// UpsertCachedMessage inserts or updates a cached message.
func (s *Store) UpsertCachedMessage(ctx context.Context, m *CachedMessage) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cached_messages (platform, conversation_id, message_id, sender_id, sender_name, content, media_url, timestamp, is_outgoing, last_synced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform, message_id) DO UPDATE SET
			sender_name = excluded.sender_name,
			content = excluded.content,
			media_url = excluded.media_url,
			last_synced = excluded.last_synced
	`, m.Platform, m.ConversationID, m.MessageID, m.SenderID, m.SenderName, m.Content, m.MediaURL, m.Timestamp, m.IsOutgoing, time.Now())
	return err
}

// GetCachedMessages returns cached messages for a platform/conversation.
func (s *Store) GetCachedMessages(ctx context.Context, platform, conversationID string, limit int) ([]CachedMessage, time.Time, error) {
	var lastSynced time.Time
	var args []interface{}

	q := `SELECT id, platform, conversation_id, message_id, sender_id, sender_name, content, media_url, timestamp, is_outgoing, last_synced
		FROM cached_messages WHERE platform = ?`
	args = append(args, platform)

	if conversationID != "" {
		q += ` AND conversation_id = ?`
		args = append(args, conversationID)
	}

	q += ` ORDER BY timestamp DESC`

	if limit <= 0 {
		limit = 50
	}
	q += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, lastSynced, err
	}
	defer rows.Close()

	var messages []CachedMessage
	for rows.Next() {
		var m CachedMessage
		if err := rows.Scan(&m.ID, &m.Platform, &m.ConversationID, &m.MessageID, &m.SenderID, &m.SenderName, &m.Content, &m.MediaURL, &m.Timestamp, &m.IsOutgoing, &m.LastSynced); err != nil {
			return nil, lastSynced, err
		}
		if m.LastSynced.After(lastSynced) {
			lastSynced = m.LastSynced
		}
		messages = append(messages, m)
	}
	return messages, lastSynced, nil
}

// ---------------------------------------------------------------------------
// Cached Posts
// ---------------------------------------------------------------------------

// UpsertCachedPost inserts or updates a cached post.
func (s *Store) UpsertCachedPost(ctx context.Context, p *CachedPost) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cached_posts (platform, post_id, content, media_urls, post_url, posted_at, likes, comments, shares, last_synced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform, post_id) DO UPDATE SET
			content = excluded.content,
			media_urls = excluded.media_urls,
			post_url = excluded.post_url,
			likes = excluded.likes,
			comments = excluded.comments,
			shares = excluded.shares,
			last_synced = excluded.last_synced
	`, p.Platform, p.PostID, p.Content, p.MediaURLs, p.PostURL, p.PostedAt, p.Likes, p.Comments, p.Shares, time.Now())
	return err
}

// GetCachedPosts returns cached posts for a platform.
func (s *Store) GetCachedPosts(ctx context.Context, platform string, limit int) ([]CachedPost, time.Time, error) {
	var lastSynced time.Time

	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, platform, post_id, content, media_urls, post_url, posted_at, likes, comments, shares, last_synced
		FROM cached_posts WHERE platform = ? ORDER BY posted_at DESC LIMIT ?
	`, platform, limit)
	if err != nil {
		return nil, lastSynced, err
	}
	defer rows.Close()

	var posts []CachedPost
	for rows.Next() {
		var p CachedPost
		if err := rows.Scan(&p.ID, &p.Platform, &p.PostID, &p.Content, &p.MediaURLs, &p.PostURL, &p.PostedAt, &p.Likes, &p.Comments, &p.Shares, &p.LastSynced); err != nil {
			return nil, lastSynced, err
		}
		if p.LastSynced.After(lastSynced) {
			lastSynced = p.LastSynced
		}
		posts = append(posts, p)
	}
	return posts, lastSynced, nil
}

// ---------------------------------------------------------------------------
// Cached Comments
// ---------------------------------------------------------------------------

// UpsertCachedComment inserts or updates a cached comment.
func (s *Store) UpsertCachedComment(ctx context.Context, c *CachedComment) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cached_comments (platform, post_id, comment_id, author_id, author_name, content, likes, timestamp, last_synced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform, comment_id) DO UPDATE SET
			author_name = excluded.author_name,
			content = excluded.content,
			likes = excluded.likes,
			last_synced = excluded.last_synced
	`, c.Platform, c.PostID, c.CommentID, c.AuthorID, c.AuthorName, c.Content, c.Likes, c.Timestamp, time.Now())
	return err
}

// GetCachedComments returns cached comments for a post.
func (s *Store) GetCachedComments(ctx context.Context, platform, postID string, limit int) ([]CachedComment, time.Time, error) {
	var lastSynced time.Time

	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, platform, post_id, comment_id, author_id, author_name, content, likes, timestamp, last_synced
		FROM cached_comments WHERE platform = ? AND post_id = ? ORDER BY timestamp DESC LIMIT ?
	`, platform, postID, limit)
	if err != nil {
		return nil, lastSynced, err
	}
	defer rows.Close()

	var comments []CachedComment
	for rows.Next() {
		var c CachedComment
		if err := rows.Scan(&c.ID, &c.Platform, &c.PostID, &c.CommentID, &c.AuthorID, &c.AuthorName, &c.Content, &c.Likes, &c.Timestamp, &c.LastSynced); err != nil {
			return nil, lastSynced, err
		}
		if c.LastSynced.After(lastSynced) {
			lastSynced = c.LastSynced
		}
		comments = append(comments, c)
	}
	return comments, lastSynced, nil
}

// ---------------------------------------------------------------------------
// Health Checks
// ---------------------------------------------------------------------------

// SaveHealthCheck stores a health check result.
func (s *Store) SaveHealthCheck(ctx context.Context, h *HealthCheckResult) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO health_checks (platform, status, error, checked_at)
		VALUES (?, ?, ?, ?)
	`, h.Platform, h.Status, h.Error, time.Now())
	return err
}

// GetLatestHealthCheck returns the most recent health check for a platform.
func (s *Store) GetLatestHealthCheck(ctx context.Context, platform string) (*HealthCheckResult, error) {
	var h HealthCheckResult
	err := s.db.QueryRowContext(ctx, `
		SELECT id, platform, status, error, checked_at
		FROM health_checks WHERE platform = ? ORDER BY checked_at DESC LIMIT 1
	`, platform).Scan(&h.ID, &h.Platform, &h.Status, &h.Error, &h.CheckedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// ---------------------------------------------------------------------------
// Documents (knowledge ingestion)
// ---------------------------------------------------------------------------

// UpsertDocument inserts a new row for path, or updates an existing row matched by path.
// Returns the resulting document id.
func (s *Store) UpsertDocument(ctx context.Context, d *Document) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO documents (path, filename, file_hash, size_bytes, mime_type, status, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			filename   = excluded.filename,
			file_hash  = excluded.file_hash,
			size_bytes = excluded.size_bytes,
			mime_type  = excluded.mime_type,
			status     = excluded.status,
			error      = '',
			updated_at = excluded.updated_at
	`, d.Path, d.Filename, d.FileHash, d.SizeBytes, d.MimeType, d.Status, time.Now())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if id != 0 {
		d.ID = id
		return id, nil
	}
	// ON CONFLICT updated — fetch id
	row := s.db.QueryRowContext(ctx, `SELECT id FROM documents WHERE path = ?`, d.Path)
	if err := row.Scan(&id); err != nil {
		return 0, err
	}
	d.ID = id
	return id, nil
}

// SetDocumentStatus updates status and error for a document.
func (s *Store) SetDocumentStatus(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE documents SET status = ?, error = ?, updated_at = ? WHERE id = ?
	`, status, errMsg, time.Now(), id)
	return err
}

// UpdateDocumentMetadata writes classifier output + raw text and marks status=ready.
func (s *Store) UpdateDocumentMetadata(ctx context.Context, id int64, d *Document) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE documents SET
			doc_type    = ?,
			product     = ?,
			language    = ?,
			audience    = ?,
			summary     = ?,
			key_terms   = ?,
			raw_text    = ?,
			status      = 'ready',
			error       = '',
			ingested_at = ?,
			updated_at  = ?
		WHERE id = ?
	`, d.DocType, d.Product, d.Language, d.Audience, d.Summary, d.KeyTerms, d.RawText,
		time.Now(), time.Now(), id)
	return err
}

// RenameDocument updates path and filename in place (used when a file is renamed with same hash).
func (s *Store) RenameDocument(ctx context.Context, id int64, newPath, newFilename string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE documents SET path = ?, filename = ?, updated_at = ? WHERE id = ?
	`, newPath, newFilename, time.Now(), id)
	return err
}

// GetDocument returns a document by id, or nil if not found.
func (s *Store) GetDocument(ctx context.Context, id int64) (*Document, error) {
	return s.scanDocumentRow(s.db.QueryRowContext(ctx, `
		SELECT id, path, filename, file_hash, size_bytes, mime_type,
		       doc_type, product, language, audience, summary, key_terms, raw_text,
		       status, error, COALESCE(ingested_at, ''), updated_at, created_at
		FROM documents WHERE id = ?
	`, id))
}

// GetDocumentByPath returns a document by its file path, or nil if not found.
func (s *Store) GetDocumentByPath(ctx context.Context, path string) (*Document, error) {
	return s.scanDocumentRow(s.db.QueryRowContext(ctx, `
		SELECT id, path, filename, file_hash, size_bytes, mime_type,
		       doc_type, product, language, audience, summary, key_terms, raw_text,
		       status, error, COALESCE(ingested_at, ''), updated_at, created_at
		FROM documents WHERE path = ?
	`, path))
}

// GetDocumentByHash returns a document by file_hash (any path), nil if not found.
func (s *Store) GetDocumentByHash(ctx context.Context, hash string) (*Document, error) {
	return s.scanDocumentRow(s.db.QueryRowContext(ctx, `
		SELECT id, path, filename, file_hash, size_bytes, mime_type,
		       doc_type, product, language, audience, summary, key_terms, raw_text,
		       status, error, COALESCE(ingested_at, ''), updated_at, created_at
		FROM documents WHERE file_hash = ? LIMIT 1
	`, hash))
}

// DocumentFilter narrows ListDocuments results.
type DocumentFilter struct {
	DocType  string
	Product  string
	Language string
	Status   string
	Limit    int
}

// ListDocuments returns documents matching filter, newest first.
func (s *Store) ListDocuments(ctx context.Context, f DocumentFilter) ([]Document, error) {
	q := `SELECT id, path, filename, file_hash, size_bytes, mime_type,
	             doc_type, product, language, audience, summary, key_terms, raw_text,
	             status, error, COALESCE(ingested_at, ''), updated_at, created_at
	      FROM documents WHERE 1=1`
	var args []interface{}
	if f.DocType != "" {
		q += ` AND doc_type = ?`
		args = append(args, f.DocType)
	}
	if f.Product != "" {
		q += ` AND product = ?`
		args = append(args, f.Product)
	}
	if f.Language != "" {
		q += ` AND language = ?`
		args = append(args, f.Language)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	q += ` ORDER BY updated_at DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		d, err := scanDocumentFromRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// SearchDocuments runs an FTS5 query and returns ranked hits with snippets.
func (s *Store) SearchDocuments(ctx context.Context, query, language, docType string, limit int) ([]DocumentSearchHit, error) {
	if limit <= 0 {
		limit = 20
	}
	// bm25 returns lower=better; negate so higher=better for downstream use.
	q := `SELECT d.id, d.path, d.filename, d.file_hash, d.size_bytes, d.mime_type,
	             d.doc_type, d.product, d.language, d.audience, d.summary, d.key_terms, d.raw_text,
	             d.status, d.error, COALESCE(d.ingested_at, ''), d.updated_at, d.created_at,
	             snippet(documents_fts, -1, '<mark>', '</mark>', '…', 20) AS snip,
	             -bm25(documents_fts) AS score
	      FROM documents_fts
	      JOIN documents d ON d.id = documents_fts.rowid
	      WHERE documents_fts MATCH ?`
	args := []interface{}{query}
	if language != "" {
		q += ` AND d.language = ?`
		args = append(args, language)
	}
	if docType != "" {
		q += ` AND d.doc_type = ?`
		args = append(args, docType)
	}
	q += ` ORDER BY score DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []DocumentSearchHit
	for rows.Next() {
		var d Document
		var ingestedStr string
		var snip string
		var score float64
		if err := rows.Scan(
			&d.ID, &d.Path, &d.Filename, &d.FileHash, &d.SizeBytes, &d.MimeType,
			&d.DocType, &d.Product, &d.Language, &d.Audience, &d.Summary, &d.KeyTerms, &d.RawText,
			&d.Status, &d.Error, &ingestedStr, &d.UpdatedAt, &d.CreatedAt,
			&snip, &score,
		); err != nil {
			return nil, err
		}
		if t, err := time.Parse("2006-01-02 15:04:05", ingestedStr); err == nil {
			d.IngestedAt = t
		}
		hits = append(hits, DocumentSearchHit{Document: d, Snippet: snip, Rank: score})
	}
	return hits, rows.Err()
}

// DeleteDocument removes a document by id (cascades to FTS via trigger).
func (s *Store) DeleteDocument(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, id)
	return err
}

// DeleteDocumentByPath removes a document by path.
func (s *Store) DeleteDocumentByPath(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM documents WHERE path = ?`, path)
	return err
}

// DocumentStats returns aggregate counts grouped by status.
func (s *Store) DocumentStats(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM documents GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{"total": 0, "pending": 0, "extracting": 0, "classifying": 0, "ready": 0, "failed": 0}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
		out["total"] += n
	}
	return out, rows.Err()
}

// ReadyDocumentPaths returns a set of paths where status='ready' (used to skip re-scan on startup).
func (s *Store) ReadyDocumentPaths(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM documents WHERE status = 'ready'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// AllDocumentPaths returns every path currently in the table (used by startup scan for pruning).
func (s *Store) AllDocumentPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM documents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// HasEmbedding reports whether the document already has an embedding stored.
func (s *Store) HasEmbedding(ctx context.Context, id int64) bool {
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents WHERE id = ? AND embedding IS NOT NULL`, id).Scan(&n)
	return n > 0
}

// SetDocumentEmbedding stores a float32 vector for a document.
func (s *Store) SetDocumentEmbedding(ctx context.Context, id int64, vec []float32) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE documents SET embedding = ? WHERE id = ?`,
		float32SliceToBytes(vec), id,
	)
	return err
}

// EmbeddingStats returns total ready docs and how many have embeddings.
func (s *Store) EmbeddingStats(ctx context.Context) (total, withEmbedding int, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(CASE WHEN embedding IS NOT NULL THEN 1 END) FROM documents WHERE status = 'ready'`)
	err = row.Scan(&total, &withEmbedding)
	return
}

// VectorSearch loads all embeddings into memory and returns the top-limit most similar docs.
// Falls back gracefully — returns empty slice (not error) if no embeddings exist yet.
func (s *Store) VectorSearch(ctx context.Context, queryVec []float32, limit int) ([]DocumentSearchHit, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, path, filename, doc_type, product, language, summary, embedding
		FROM documents
		WHERE status = 'ready' AND embedding IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		hit   DocumentSearchHit
		score float32
	}
	var candidates []candidate

	for rows.Next() {
		var d Document
		var embBytes []byte
		if err := rows.Scan(&d.ID, &d.Path, &d.Filename, &d.DocType, &d.Product, &d.Language, &d.Summary, &embBytes); err != nil {
			continue
		}
		if len(embBytes) == 0 {
			continue
		}
		vec := bytesToFloat32Slice(embBytes)
		score := cosineSimilarity(queryVec, vec)
		candidates = append(candidates, candidate{
			hit:   DocumentSearchHit{Document: d},
			score: score,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]DocumentSearchHit, len(candidates))
	for i, c := range candidates {
		c.hit.Rank = float64(c.score)
		out[i] = c.hit
	}
	return out, nil
}

// --- Contact CRUD ---

func (s *Store) UpsertContact(ctx context.Context, jid, platform, pushName string) (*Contact, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO contacts (jid, platform, push_name) VALUES (?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET push_name=excluded.push_name`,
		jid, platform, pushName)
	if err != nil {
		return nil, err
	}
	return s.GetContact(ctx, jid)
}

func (s *Store) GetContact(ctx context.Context, jid string) (*Contact, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, jid, platform, push_name, first_seen_at FROM contacts WHERE jid=?`, jid)
	var c Contact
	if err := row.Scan(&c.ID, &c.JID, &c.Platform, &c.PushName, &c.FirstSeenAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func (s *Store) ListContacts(ctx context.Context) ([]Contact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, jid, platform, push_name, first_seen_at FROM contacts ORDER BY first_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.JID, &c.Platform, &c.PushName, &c.FirstSeenAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- Group CRUD ---

func (s *Store) CreateGroup(ctx context.Context, name, groupType, replyMode string) (*Group, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO groups (name, type, reply_mode) VALUES (?, ?, ?)`,
		name, groupType, replyMode)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetGroup(ctx, id)
}

func (s *Store) GetGroup(ctx context.Context, id int64) (*Group, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, type, reply_mode, created_at FROM groups WHERE id=?`, id)
	var g Group
	if err := row.Scan(&g.ID, &g.Name, &g.Type, &g.ReplyMode, &g.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &g, nil
}

func (s *Store) UpdateGroup(ctx context.Context, id int64, name, replyMode string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE groups SET name=?, reply_mode=? WHERE id=?`, name, replyMode, id)
	return err
}

func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM groups WHERE id=?`, id)
	return err
}

func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, type, reply_mode, created_at FROM groups ORDER BY type DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Type, &g.ReplyMode, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) AssignContactToGroup(ctx context.Context, contactID, groupID int64, source string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO contact_groups (contact_id, group_id, source) VALUES (?, ?, ?)`,
		contactID, groupID, source)
	return err
}

func (s *Store) RemoveContactFromGroup(ctx context.Context, contactID, groupID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM contact_groups WHERE contact_id=? AND group_id=?`, contactID, groupID)
	return err
}

// GetContactGroups returns groups for a contact, manual groups first.
func (s *Store) GetContactGroups(ctx context.Context, contactID int64) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT g.id, g.name, g.type, g.reply_mode, g.created_at
		 FROM groups g
		 JOIN contact_groups cg ON cg.group_id = g.id
		 WHERE cg.contact_id = ?
		 ORDER BY CASE cg.source WHEN 'manual' THEN 0 ELSE 1 END, g.name ASC`,
		contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Type, &g.ReplyMode, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) ListContactsWithGroups(ctx context.Context) ([]Contact, map[int64][]Group, error) {
	contacts, err := s.ListContacts(ctx)
	if err != nil {
		return nil, nil, err
	}
	groupsByContact := make(map[int64][]Group)
	for _, c := range contacts {
		groups, err := s.GetContactGroups(ctx, c.ID)
		if err != nil {
			return nil, nil, err
		}
		groupsByContact[c.ID] = groups
	}
	return contacts, groupsByContact, nil
}

func (s *Store) ListGroupContacts(ctx context.Context, groupID int64) ([]Contact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.jid, c.platform, c.push_name, c.first_seen_at
		 FROM contacts c
		 JOIN contact_groups cg ON cg.contact_id = c.id
		 WHERE cg.group_id = ?
		 ORDER BY c.push_name ASC`,
		groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.JID, &c.Platform, &c.PushName, &c.FirstSeenAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func float32SliceToBytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func bytesToFloat32Slice(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

func (s *Store) scanDocumentRow(row *sql.Row) (*Document, error) {
	var d Document
	var ingestedStr string
	err := row.Scan(
		&d.ID, &d.Path, &d.Filename, &d.FileHash, &d.SizeBytes, &d.MimeType,
		&d.DocType, &d.Product, &d.Language, &d.Audience, &d.Summary, &d.KeyTerms, &d.RawText,
		&d.Status, &d.Error, &ingestedStr, &d.UpdatedAt, &d.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t, err := time.Parse("2006-01-02 15:04:05", ingestedStr); err == nil {
		d.IngestedAt = t
	}
	return &d, nil
}

func scanDocumentFromRows(rows *sql.Rows) (*Document, error) {
	var d Document
	var ingestedStr string
	if err := rows.Scan(
		&d.ID, &d.Path, &d.Filename, &d.FileHash, &d.SizeBytes, &d.MimeType,
		&d.DocType, &d.Product, &d.Language, &d.Audience, &d.Summary, &d.KeyTerms, &d.RawText,
		&d.Status, &d.Error, &ingestedStr, &d.UpdatedAt, &d.CreatedAt,
	); err != nil {
		return nil, err
	}
	if t, err := time.Parse("2006-01-02 15:04:05", ingestedStr); err == nil {
		d.IngestedAt = t
	}
	return &d, nil
}
