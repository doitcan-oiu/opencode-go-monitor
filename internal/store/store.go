// Package store 提供基于 SQLite 的账号、模型与设置持久化。
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无需 CGO），驱动名 "sqlite"
)

// ErrNotFound 表示未找到指定记录。
var ErrNotFound = errors.New("记录不存在")

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
  id           TEXT PRIMARY KEY,
  account      TEXT NOT NULL DEFAULT '',
  password     TEXT NOT NULL DEFAULT '',
  aux_email    TEXT NOT NULL DEFAULT '',
  workspace_id TEXT NOT NULL DEFAULT '',
  auth         TEXT NOT NULL DEFAULT '',
  api_key      TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'pending',
  error        TEXT NOT NULL DEFAULT '',
  report_email TEXT NOT NULL DEFAULT '',
  subscribed   INTEGER NOT NULL DEFAULT 0,
  rolling      TEXT,
  weekly       TEXT,
  monthly      TEXT,
  expires_at   TEXT,
  last_checked TEXT,
  proxy_count  INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS models (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL DEFAULT '',
  protocol   TEXT NOT NULL DEFAULT 'openai',
  endpoint   TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);`

// 针对早期版本数据库的增量迁移（列不存在时才生效，已存在的报错忽略）。
var migrations = []string{
	`ALTER TABLE accounts ADD COLUMN api_key TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE accounts ADD COLUMN proxy_count INTEGER NOT NULL DEFAULT 0`,
}

type Store struct {
	db *sql.DB
}

// Open 打开（或创建）SQLite 数据库并初始化表结构、默认设置与默认模型。
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	for _, m := range migrations {
		_, _ = db.Exec(m) // 列已存在则忽略
	}
	s := &Store{db: db}
	if _, err := s.rawSetting("settings"); err != nil {
		if err := s.SaveSettings(DefaultSettings()); err != nil {
			return nil, err
		}
	}
	if err := s.seedModels(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ---- 账号 CRUD ----

const accountCols = `id, account, password, aux_email, workspace_id, auth, api_key,
	status, error, report_email, subscribed, rolling, weekly, monthly,
	expires_at, last_checked, proxy_count, created_at`

func (s *Store) All() ([]*Account, error) {
	rows, err := s.db.Query(`SELECT ` + accountCols + ` FROM accounts ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Get(id string) (*Account, error) {
	row := s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE id = ?`, id)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) Add(a *Account) error {
	if a.ID == "" {
		a.ID = newID()
	}
	if a.Status == "" {
		a.Status = "pending"
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO accounts (`+accountCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Account, a.Password, a.AuxEmail, a.WorkspaceID, a.Auth, a.APIKey,
		a.Status, a.Error, a.ReportEmail, boolInt(a.Subscribed),
		usageJSON(a.Rolling), usageJSON(a.Weekly), usageJSON(a.Monthly),
		timeStr(a.ExpiresAt), timeStr(a.LastChecked), a.ProxyCount, a.CreatedAt.Format(time.RFC3339))
	return err
}

func (s *Store) Update(id string, fn func(a *Account)) (*Account, error) {
	a, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	fn(a)
	_, err = s.db.Exec(`UPDATE accounts SET
		account=?, password=?, aux_email=?, workspace_id=?, auth=?, api_key=?,
		status=?, error=?, report_email=?, subscribed=?,
		rolling=?, weekly=?, monthly=?, expires_at=?, last_checked=?, proxy_count=?
		WHERE id=?`,
		a.Account, a.Password, a.AuxEmail, a.WorkspaceID, a.Auth, a.APIKey,
		a.Status, a.Error, a.ReportEmail, boolInt(a.Subscribed),
		usageJSON(a.Rolling), usageJSON(a.Weekly), usageJSON(a.Monthly),
		timeStr(a.ExpiresAt), timeStr(a.LastChecked), a.ProxyCount, a.ID)
	return a, err
}

func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// IncrProxyCount 原子地把账号的转发计数 +1。
func (s *Store) IncrProxyCount(id string) error {
	_, err := s.db.Exec(`UPDATE accounts SET proxy_count = proxy_count + 1 WHERE id = ?`, id)
	return err
}

// ---- 模型 CRUD ----

func (s *Store) seedModels() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM models`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, m := range DefaultModels() {
		if err := s.AddModel(m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Models() ([]Model, error) {
	rows, err := s.db.Query(`SELECT id, name, protocol, endpoint FROM models ORDER BY protocol, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.Name, &m.Protocol, &m.Endpoint); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AddModel(m Model) error {
	if m.Protocol != "anthropic" {
		m.Protocol = "openai"
	}
	if m.Endpoint == "" {
		if m.Protocol == "anthropic" {
			m.Endpoint = epAnthropic
		} else {
			m.Endpoint = epOpenAI
		}
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	_, err := s.db.Exec(`INSERT INTO models (id, name, protocol, endpoint, created_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, protocol=excluded.protocol, endpoint=excluded.endpoint`,
		m.ID, m.Name, m.Protocol, m.Endpoint, time.Now().Format(time.RFC3339))
	return err
}

func (s *Store) DeleteModel(id string) error {
	res, err := s.db.Exec(`DELETE FROM models WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 设置 ----

func (s *Store) GetSettings() Settings {
	raw, err := s.rawSetting("settings")
	if err != nil {
		return DefaultSettings()
	}
	out := DefaultSettings()
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func (s *Store) SaveSettings(v Settings) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO settings (key, value) VALUES ('settings', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(data))
	return err
}

func (s *Store) rawSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v, err
}

// ---- 扫描 / 序列化辅助 ----

type scanner interface{ Scan(dest ...any) error }

func scanAccount(row scanner) (*Account, error) {
	var (
		a                              Account
		sub                            int
		rolling, weekly, monthly       sql.NullString
		expiresAt, lastChecked, create sql.NullString
	)
	err := row.Scan(&a.ID, &a.Account, &a.Password, &a.AuxEmail, &a.WorkspaceID, &a.Auth, &a.APIKey,
		&a.Status, &a.Error, &a.ReportEmail, &sub, &rolling, &weekly, &monthly,
		&expiresAt, &lastChecked, &a.ProxyCount, &create)
	if err != nil {
		return nil, err
	}
	a.Subscribed = sub != 0
	a.Rolling = parseUsage(rolling)
	a.Weekly = parseUsage(weekly)
	a.Monthly = parseUsage(monthly)
	a.ExpiresAt = parseTime(expiresAt)
	a.LastChecked = parseTime(lastChecked)
	if create.Valid {
		if t, err := time.Parse(time.RFC3339, create.String); err == nil {
			a.CreatedAt = t
		}
	}
	return &a, nil
}

func usageJSON(u *Usage) any {
	if u == nil {
		return nil
	}
	b, _ := json.Marshal(u)
	return string(b)
}

func parseUsage(ns sql.NullString) *Usage {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	var u Usage
	if json.Unmarshal([]byte(ns.String), &u) != nil {
		return nil
	}
	return &u
}

func timeStr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

func parseTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, ns.String); err == nil {
		return &t
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
