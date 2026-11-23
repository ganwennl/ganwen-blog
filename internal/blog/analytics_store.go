package blog

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const analyticsSecretKey = "analytics_secret_v1"
const analyticsRetentionDays = 365
const analyticsEventRetentionDays = 14
const analyticsVisitorRetentionDays = 2
const analyticsDailyLimit = 100000

// Analytics rows contain no raw visitor IDs, IP addresses, user agents or URLs
// containing search/referrer queries. Each dimension counts page views only.
type AnalyticsAggregate struct {
	Date   string `json:"date"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	PostID int64  `json:"post_id"`
	PV     int64  `json:"pv"`
	UV     int64  `json:"uv"`
}
type AnalyticsVisitor struct {
	Date string `json:"date"`
	Hash string `json:"hash"`
}
type AnalyticsEvent struct {
	Date string `json:"date"`
	Hash string `json:"hash"`
}
type AnalyticsBackup struct {
	Aggregates []AnalyticsAggregate `json:"aggregates"`
	Visitors   []AnalyticsVisitor   `json:"visitors"`
	Events     []AnalyticsEvent     `json:"events"`
}

func blogDatabaseTable(name string) bool {
	switch name {
	case "posts", "kv", "translation_jobs", "analytics_aggregates", "analytics_visitors", "analytics_events":
		return true
	}
	return false
}
func (s *Store) analyticsSchema() []string {
	if s.driver == "mysql" {
		return []string{
			"CREATE TABLE IF NOT EXISTS analytics_aggregates (id_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,date CHAR(10) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,label VARCHAR(512) NOT NULL,post_id BIGINT NOT NULL,pv BIGINT NOT NULL,uv BIGINT NOT NULL,KEY analytics_date(date)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin",
			"CREATE TABLE IF NOT EXISTS analytics_visitors (id_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,date CHAR(10) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,KEY analytics_visitor_date(date)) ENGINE=InnoDB",
			"CREATE TABLE IF NOT EXISTS analytics_events (id_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,date CHAR(10) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,KEY analytics_event_date(date)) ENGINE=InnoDB",
		}
	}
	return []string{
		"CREATE TABLE IF NOT EXISTS analytics_aggregates(id_hash TEXT PRIMARY KEY,date TEXT NOT NULL,kind TEXT NOT NULL,label TEXT NOT NULL,post_id INTEGER NOT NULL,pv INTEGER NOT NULL,uv INTEGER NOT NULL)",
		"CREATE INDEX IF NOT EXISTS analytics_date ON analytics_aggregates(date)",
		"CREATE TABLE IF NOT EXISTS analytics_visitors(id_hash TEXT PRIMARY KEY,date TEXT NOT NULL)",
		"CREATE INDEX IF NOT EXISTS analytics_visitor_date ON analytics_visitors(date)",
		"CREATE TABLE IF NOT EXISTS analytics_events(id_hash TEXT PRIMARY KEY,date TEXT NOT NULL)",
		"CREATE INDEX IF NOT EXISTS analytics_event_date ON analytics_events(date)",
	}
}
func analyticsHash(secret []byte, purpose, value string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(purpose + "\x00" + value))
	return hex.EncodeToString(h.Sum(nil))
}
func analyticsAggregateID(row AnalyticsAggregate) string {
	h := sha256.Sum256([]byte(row.Date + "\x00" + row.Kind + "\x00" + row.Label))
	return hex.EncodeToString(h[:])
}
func (s *Store) analyticsInsertIgnore(table string) string {
	base := "INSERT INTO " + table + "(id_hash,date) VALUES(?,?)"
	if s.driver == "mysql" {
		return base + " ON DUPLICATE KEY UPDATE id_hash=id_hash"
	}
	return base + " ON CONFLICT(id_hash) DO NOTHING"
}
func (s *Store) analyticsUpsert() string {
	base := "INSERT INTO analytics_aggregates(id_hash,date,kind,label,post_id,pv,uv) VALUES(?,?,?,?,?,?,?)"
	if s.driver == "mysql" {
		return base + " ON DUPLICATE KEY UPDATE pv=pv+VALUES(pv),uv=uv+VALUES(uv)"
	}
	return base + " ON CONFLICT(id_hash) DO UPDATE SET pv=pv+excluded.pv,uv=uv+excluded.uv"
}
func analyticsSecret(ctx context.Context, tx *sql.Tx, driver string) ([]byte, error) {
	generated := make([]byte, 32)
	if _, err := rand.Read(generated); err != nil {
		return nil, err
	}
	query := "INSERT INTO kv(`key`,value) VALUES(?,?) ON CONFLICT(`key`) DO NOTHING"
	if driver == "mysql" {
		query = "INSERT INTO kv(`key`,value) VALUES(?,?) ON DUPLICATE KEY UPDATE `key`=`key`"
	}
	// The unique key is also a database write lock: separate application/store
	// instances cannot initialize different salts or race quota decisions.
	if _, err := tx.ExecContext(ctx, query, analyticsSecretKey, hex.EncodeToString(generated)); err != nil {
		return nil, err
	}
	var value string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM kv WHERE `key`=?", analyticsSecretKey).Scan(&value); err != nil {
		return nil, err
	}
	secret, err := hex.DecodeString(value)
	if err != nil || len(secret) != 32 {
		return nil, errors.New("invalid analytics secret")
	}
	return secret, nil
}

// RecordAnalytics atomically deduplicates events and visitors before increasing
// daily metrics. All writes share the snapshot/store lock.
func (s *Store) RecordAnalytics(ctx context.Context, now time.Time, eventID, visitorID string, dimensions []AnalyticsAggregate) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	date := now.UTC().Format(time.DateOnly)
	secret, err := analyticsSecret(ctx, tx, s.driver)
	if err != nil {
		return false, err
	}
	// Event hashes have a separate domain and remain stable across midnight;
	// visitor hashes include the UTC day and cannot link visits across days.
	eventHash := analyticsHash(secret, "event", eventID)
	global := AnalyticsAggregate{Date: date, Kind: "total", Label: "all", PV: 1}
	var currentPV int64
	err = tx.QueryRowContext(ctx, "SELECT pv FROM analytics_aggregates WHERE id_hash=?", analyticsAggregateID(global)).Scan(&currentPV)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if currentPV >= analyticsDailyLimit {
		return false, nil
	}
	inserted, err := tx.ExecContext(ctx, s.analyticsInsertIgnore("analytics_events"), eventHash, date)
	if err != nil {
		return false, err
	}
	newEvent, err := inserted.RowsAffected()
	if err != nil {
		return false, err
	}
	if newEvent == 0 {
		return false, nil
	}
	visitorHash := analyticsHash(secret, "visitor:"+date, visitorID)
	inserted, err = tx.ExecContext(ctx, s.analyticsInsertIgnore("analytics_visitors"), visitorHash, date)
	if err != nil {
		return false, err
	}
	newVisitor, err := inserted.RowsAffected()
	if err != nil {
		return false, err
	}
	if newVisitor > 0 {
		global.UV = 1
	}
	for _, row := range append([]AnalyticsAggregate{global}, dimensions...) {
		row.Date, row.PV = date, 1
		if row.Kind != "total" {
			row.UV = 0
		}
		// Cap externally supplied domain cardinality, as well as unusually large
		// sites. The fallback preserves totals without unbounded dimension rows.
		var found int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM analytics_aggregates WHERE id_hash=?", analyticsAggregateID(row)).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM analytics_aggregates WHERE date=? AND kind=?", date, row.Kind).Scan(&count); err != nil {
				return false, err
			}
			if count >= 1000 {
				row.Label, row.PostID = "other", 0
			}
		} else if err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, s.analyticsUpsert(), analyticsAggregateID(row), row.Date, row.Kind, row.Label, row.PostID, row.PV, row.UV); err != nil {
			return false, err
		}
	}
	// Each request removes at most 300 expired rows per table, using a derived
	// subquery accepted by both SQLite and MySQL (no unrestricted table names).
	for _, retention := range []struct {
		table string
		days  int
	}{{"analytics_aggregates", analyticsRetentionDays}, {"analytics_visitors", analyticsVisitorRetentionDays}, {"analytics_events", analyticsEventRetentionDays}} {
		cutoff := now.UTC().AddDate(0, 0, -retention.days+1).Format(time.DateOnly)
		query := fmt.Sprintf("DELETE FROM %s WHERE id_hash IN (SELECT id_hash FROM (SELECT id_hash FROM %s WHERE date<? ORDER BY date LIMIT 300) AS expired)", retention.table, retention.table)
		if _, err = tx.ExecContext(ctx, query, cutoff); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
