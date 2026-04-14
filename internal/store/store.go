// Package store provides an app-level SQLite database for persisting
// account metadata, credentials, cached data, and other state across restarts.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
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
