package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Job struct {
	ID              int64
	Status          string
	CandidatesPath  string
	Domain          string
	Path            string
	Port            int
	UseTLS          bool
	AllowedColos    []string
	StartedAt       time.Time
	FinishedAt      time.Time
	TotalCandidates int
	Processed       int
	SuccessCount    int
	LastError       string
}

type Result struct {
	ID         int64
	IP         string
	Port       int
	Domain     string
	Colo       string
	City       string
	Region     string
	LatencyMS  int
	HTTPStatus int
	Active     bool
	LastError  string
	LastSeenAt time.Time
	CreatedAt  time.Time
}

type ResultFilter struct {
	Colo   string
	City   string
	Active *bool
	Limit  int
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS scan_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  status TEXT NOT NULL,
  candidates_path TEXT NOT NULL,
  domain TEXT NOT NULL,
  path TEXT NOT NULL,
  port INTEGER NOT NULL,
  use_tls INTEGER NOT NULL,
  allowed_colos TEXT NOT NULL,
  started_at DATETIME NOT NULL,
  finished_at DATETIME,
  total_candidates INTEGER NOT NULL DEFAULT 0,
  processed INTEGER NOT NULL DEFAULT 0,
  success_count INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS ip_results (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ip TEXT NOT NULL,
  port INTEGER NOT NULL,
  domain TEXT NOT NULL,
  colo TEXT NOT NULL DEFAULT '',
  city TEXT NOT NULL DEFAULT '',
  region TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER NOT NULL DEFAULT 0,
  http_status INTEGER NOT NULL DEFAULT 0,
  active INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  last_seen_at DATETIME NOT NULL,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL,
  UNIQUE(ip, port, domain)
);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *Store) CreateJob(ctx context.Context, job Job) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO scan_jobs (status, candidates_path, domain, path, port, use_tls, allowed_colos, started_at, total_candidates, processed, success_count, last_error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.Status, job.CandidatesPath, job.Domain, job.Path, job.Port, boolToInt(job.UseTLS),
		strings.Join(job.AllowedColos, ","), job.StartedAt, job.TotalCandidates, job.Processed, job.SuccessCount, job.LastError)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateJobProgress(ctx context.Context, id int64, processed, success, total int) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE scan_jobs SET processed = ?, success_count = ?, total_candidates = ? WHERE id = ?`,
		processed, success, total, id)
	return err
}

func (s *Store) FinishJob(ctx context.Context, id int64, status, lastError string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE scan_jobs SET status = ?, last_error = ?, finished_at = ? WHERE id = ?`,
		status, lastError, time.Now(), id)
	return err
}

func (s *Store) UpsertResult(ctx context.Context, r Result) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO ip_results (ip, port, domain, colo, city, region, latency_ms, http_status, active, last_error, last_seen_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(ip, port, domain) DO UPDATE SET
  colo = excluded.colo,
  city = excluded.city,
  region = excluded.region,
  latency_ms = excluded.latency_ms,
  http_status = excluded.http_status,
  active = excluded.active,
  last_error = excluded.last_error,
  last_seen_at = excluded.last_seen_at,
  updated_at = excluded.updated_at`,
		r.IP, r.Port, r.Domain, r.Colo, r.City, r.Region, r.LatencyMS, r.HTTPStatus,
		boolToInt(r.Active), r.LastError, r.LastSeenAt, now, now)
	return err
}

func (s *Store) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, status, candidates_path, domain, path, port, use_tls, allowed_colos, started_at, finished_at, total_candidates, processed, success_count, last_error
FROM scan_jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		var item Job
		var allowed string
		var finishedAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.Status, &item.CandidatesPath, &item.Domain, &item.Path, &item.Port, &item.UseTLS,
			&allowed, &item.StartedAt, &finishedAt, &item.TotalCandidates, &item.Processed, &item.SuccessCount, &item.LastError); err != nil {
			return nil, err
		}
		if finishedAt.Valid {
			item.FinishedAt = finishedAt.Time
		}
		item.AllowedColos = splitCSV(allowed)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListResults(ctx context.Context, filter ResultFilter) ([]Result, error) {
	args := []any{}
	clauses := []string{"1=1"}
	if filter.Colo != "" {
		clauses = append(clauses, "colo = ?")
		args = append(args, strings.ToUpper(filter.Colo))
	}
	if filter.City != "" {
		clauses = append(clauses, "city = ?")
		args = append(args, filter.City)
	}
	if filter.Active != nil {
		clauses = append(clauses, "active = ?")
		args = append(args, boolToInt(*filter.Active))
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
SELECT id, ip, port, domain, colo, city, region, latency_ms, http_status, active, last_error, last_seen_at, created_at
FROM ip_results WHERE %s
ORDER BY active DESC, latency_ms ASC, updated_at DESC
LIMIT ?`, strings.Join(clauses, " AND "))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Result
	for rows.Next() {
		var item Result
		if err := rows.Scan(&item.ID, &item.IP, &item.Port, &item.Domain, &item.Colo, &item.City, &item.Region,
			&item.LatencyMS, &item.HTTPStatus, &item.Active, &item.LastError, &item.LastSeenAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
