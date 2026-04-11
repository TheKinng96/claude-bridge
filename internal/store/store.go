// Package store provides an app-level SQLite database for persisting
// account metadata, settings, and other CRM state across restarts.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Account represents a connected messaging account (WhatsApp, FB, etc).
type Account struct {
	ID          int64     `json:"id"`
	Channel     string    `json:"channel"`      // "whatsapp" or "facebook"
	JID         string    `json:"jid"`           // unique identifier (e.g. "6281234567890@s.whatsapp.net")
	PhoneNumber string    `json:"phone_number"`
	PushName    string    `json:"push_name"`
	ProfilePic  string    `json:"profile_pic,omitempty"`
	Status      string    `json:"status"` // "connected", "disconnected"
	CreatedAt   time.Time `json:"created_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
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
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL", dbPath))
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
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS accounts (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			channel     TEXT NOT NULL DEFAULT 'whatsapp',
			jid         TEXT NOT NULL UNIQUE,
			phone_number TEXT NOT NULL DEFAULT '',
			push_name   TEXT NOT NULL DEFAULT '',
			profile_pic TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'disconnected',
			created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	return err
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

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}
