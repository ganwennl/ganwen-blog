package blog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const adminSessionKeyPrefix = "admin_session_v1:"

type storedAdminSession struct {
	OriginHash     string `json:"origin_hash"`
	CredentialHash string `json:"credential_hash"`
	CSRF           string `json:"csrf"`
	ExpiresAt      int64  `json:"expires_at"`
}

// Only a SHA-256 digest of the random bearer token is stored. Every request
// checks the DB so logout also revokes sessions in concurrent app instances.
// The private KV prefix is excluded from content backup exports and restores.
func (s *Store) adminSession(ctx context.Context, tokenHash, originHash, credentialHash string, now time.Time) (adminSession, bool, error) {
	raw, err := s.GetValue(ctx, adminSessionKeyPrefix+tokenHash)
	if errors.Is(err, ErrNotFound) {
		return adminSession{}, false, nil
	}
	if err != nil {
		return adminSession{}, false, err
	}
	var stored storedAdminSession
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return adminSession{}, false, err
	}
	if !sameToken(originHash, stored.OriginHash) || !sameToken(credentialHash, stored.CredentialHash) || stored.ExpiresAt <= now.Unix() || len(stored.CSRF) != 43 {
		return adminSession{}, false, nil
	}
	return adminSession{CSRF: stored.CSRF, Expires: time.Unix(stored.ExpiresAt, 0)}, true, nil
}

func (s *Store) createAdminSession(ctx context.Context, tokenHash, previousHash, originHash, credentialHash string, session adminSession) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Range matching avoids SQL LIKE's underscore wildcards. The bounded session
	// inventory is pruned on successful login without a timer or schema migration.
	rows, err := tx.QueryContext(ctx, "SELECT `key`,value FROM kv WHERE `key`>=? AND `key`<?", adminSessionKeyPrefix, adminSessionKeyPrefix+"~")
	if err != nil {
		return err
	}
	var obsolete []string
	var count int
	now := time.Now().Unix()
	for rows.Next() {
		var key, raw string
		if err = rows.Scan(&key, &raw); err != nil {
			break
		}
		var stored storedAdminSession
		invalid := json.Unmarshal([]byte(raw), &stored) != nil || stored.ExpiresAt <= now
		superseded := stored.OriginHash == originHash && (stored.CredentialHash != credentialHash || key == adminSessionKeyPrefix+previousHash)
		if invalid || superseded {
			obsolete = append(obsolete, key)
		} else {
			count++
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if count >= 1024 {
		return errBusy
	}
	for _, key := range obsolete {
		if _, err = tx.ExecContext(ctx, "DELETE FROM kv WHERE `key`=?", key); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(storedAdminSession{OriginHash: originHash, CredentialHash: credentialHash, CSRF: session.CSRF, ExpiresAt: session.Expires.Unix()})
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO kv(`key`,value) VALUES(?,?)", adminSessionKeyPrefix+tokenHash, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) revokeAdminSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM kv WHERE `key`=?", adminSessionKeyPrefix+tokenHash)
	return err
}
