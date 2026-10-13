// Package store 基于 SQLite 的持久化存储（纯 Go 驱动，无需 CGO）。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound 对象不存在
var ErrNotFound = errors.New("store: 对象不存在")

// Store 数据库句柄
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）数据库，并执行迁移。
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS clients (
    cuid TEXT PRIMARY KEY,
    client_id TEXT NOT NULL DEFAULT '',
    mac TEXT NOT NULL DEFAULT '',
    group_id INTEGER NOT NULL DEFAULT 1,
    disabled INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    last_seen INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS objects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS assignments (
    group_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    object_id INTEGER NOT NULL,
    PRIMARY KEY (group_id, kind)
);
CREATE TABLE IF NOT EXISTS commands (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    cuid TEXT NOT NULL,
    cmd_type INTEGER NOT NULL,
    payload BLOB,
    created_at INTEGER NOT NULL,
    delivered INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    cuid TEXT NOT NULL DEFAULT '',
    event INTEGER NOT NULL,
    payload BLOB,
    ts INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT PRIMARY KEY,
    cuid TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
INSERT OR IGNORE INTO groups (id, name, created_at) VALUES (1, '默认分组', 0);
`)
	return err
}

// ---------- 客户端 ----------

// Client 已注册的 ClassIsland 实例
type Client struct {
	CUID      string
	ClientID  string
	MAC       string
	GroupID   int64
	Disabled  bool
	CreatedAt int64
	LastSeen  int64
}

// UpsertClient 注册或更新客户端，返回是否为新注册。
func (s *Store) UpsertClient(cuid, clientID, mac string) (bool, error) {
	now := time.Now().Unix()
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM clients WHERE cuid = ?`, cuid).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		_, err := s.db.Exec(`INSERT INTO clients (cuid, client_id, mac, group_id, disabled, created_at, last_seen)
			VALUES (?, ?, ?, 1, 0, ?, ?)`, cuid, clientID, mac, now, now)
		return true, err
	}
	_, err := s.db.Exec(`UPDATE clients SET client_id = ?, mac = ?, last_seen = ? WHERE cuid = ?`,
		clientID, mac, now, cuid)
	return false, err
}

// TouchClient 更新心跳时间
func (s *Store) TouchClient(cuid string) error {
	_, err := s.db.Exec(`UPDATE clients SET last_seen = ? WHERE cuid = ?`, time.Now().Unix(), cuid)
	return err
}

// GetClient 查询客户端
func (s *Store) GetClient(cuid string) (*Client, error) {
	row := s.db.QueryRow(`SELECT cuid, client_id, mac, group_id, disabled, created_at, last_seen FROM clients WHERE cuid = ?`, cuid)
	c := &Client{}
	var disabled int
	if err := row.Scan(&c.CUID, &c.ClientID, &c.MAC, &c.GroupID, &disabled, &c.CreatedAt, &c.LastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Disabled = disabled != 0
	return c, nil
}

// ListClients 列出客户端
func (s *Store) ListClients() ([]Client, error) {
	rows, err := s.db.Query(`SELECT cuid, client_id, mac, group_id, disabled, created_at, last_seen FROM clients ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		var c Client
		var disabled int
		if err := rows.Scan(&c.CUID, &c.ClientID, &c.MAC, &c.GroupID, &disabled, &c.CreatedAt, &c.LastSeen); err != nil {
			return nil, err
		}
		c.Disabled = disabled != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetClientGroup 修改客户端所属分组
func (s *Store) SetClientGroup(cuid string, groupID int64) error {
	_, err := s.db.Exec(`UPDATE clients SET group_id = ? WHERE cuid = ?`, groupID, cuid)
	return err
}

// DeleteClient 删除客户端及其排队的命令（用于清理退出的、重复的实例）
func (s *Store) DeleteClient(cuid string) error {
	if _, err := s.db.Exec(`DELETE FROM commands WHERE cuid = ?`, cuid); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE cuid = ?`, cuid); err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM clients WHERE cuid = ?`, cuid)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- 档案对象 ----------

// Object 一条档案记录（课表 / 时间表 / 科目 / 策略 / 设置 …）
type Object struct {
	ID        int64
	Kind      string
	Name      string
	Payload   string
	Version   int64
	UpdatedAt int64
}

// ListObjects 列出某类型的全部档案
func (s *Store) ListObjects(kind string) ([]Object, error) {
	rows, err := s.db.Query(`SELECT id, kind, name, payload, version, updated_at FROM objects WHERE kind = ? ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObjects(rows)
}

// GetObject 按 ID 获取
func (s *Store) GetObject(id int64) (*Object, error) {
	row := s.db.QueryRow(`SELECT id, kind, name, payload, version, updated_at FROM objects WHERE id = ?`, id)
	o := &Object{}
	if err := row.Scan(&o.ID, &o.Kind, &o.Name, &o.Payload, &o.Version, &o.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return o, nil
}

// CreateObject 新建档案
func (s *Store) CreateObject(kind, name, payload string) (*Object, error) {
	res, err := s.db.Exec(`INSERT INTO objects (kind, name, payload, version, updated_at) VALUES (?, ?, ?, 1, ?)`,
		kind, name, payload, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetObject(id)
}

// UpdateObjectPayload 更新档案内容并递增版本号（客户端据此判断是否需要重新拉取）
func (s *Store) UpdateObjectPayload(id int64, payload string) (*Object, error) {
	res, err := s.db.Exec(`UPDATE objects SET payload = ?, version = version + 1, updated_at = ? WHERE id = ?`,
		payload, time.Now().Unix(), id)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return nil, ErrNotFound
	}
	return s.GetObject(id)
}

// RenameObject 重命名
func (s *Store) RenameObject(id int64, name string) error {
	_, err := s.db.Exec(`UPDATE objects SET name = ? WHERE id = ?`, name, id)
	return err
}

// DeleteObject 删除档案
func (s *Store) DeleteObject(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM assignments WHERE object_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM objects WHERE id = ?`, id)
	return err
}

// ---------- 分组与分配 ----------

// Group 客户端分组
type Group struct {
	ID        int64
	Name      string
	CreatedAt int64
}

// ListGroups 列出分组
func (s *Store) ListGroups() ([]Group, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM groups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CreateGroup 新建分组
func (s *Store) CreateGroup(name string) (*Group, error) {
	res, err := s.db.Exec(`INSERT INTO groups (name, created_at) VALUES (?, ?)`, name, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Group{ID: id, Name: name}, nil
}

// Assign 把某类型的档案分配给分组
func (s *Store) Assign(groupID, objectID int64, kind string) error {
	_, err := s.db.Exec(`INSERT INTO assignments (group_id, kind, object_id) VALUES (?, ?, ?)
		ON CONFLICT(group_id, kind) DO UPDATE SET object_id = excluded.object_id`, groupID, kind, objectID)
	return err
}

// AssignedObject 取分组在某类型上分配的档案；未分配时回退到该类型的第一个档案。
func (s *Store) AssignedObject(groupID int64, kind string) (*Object, error) {
	var id int64
	err := s.db.QueryRow(`SELECT object_id FROM assignments WHERE group_id = ? AND kind = ?`, groupID, kind).Scan(&id)
	if err != nil {
		// 回退：取该类型第一个档案（空内容时返回 nil，表示不分发）
		row := s.db.QueryRow(`SELECT id FROM objects WHERE kind = ? ORDER BY id LIMIT 1`, kind)
		if err := row.Scan(&id); err != nil {
			return nil, ErrNotFound
		}
	}
	return s.GetObject(id)
}

// ---------- 命令 ----------

// EnqueueCommand 下发命令到客户端（type 取值见 protobuf CommandTypes）
func (s *Store) EnqueueCommand(cuid string, cmdType int, payload []byte) error {
	_, err := s.db.Exec(`INSERT INTO commands (cuid, cmd_type, payload, created_at, delivered) VALUES (?, ?, ?, ?, 0)`,
		cuid, cmdType, payload, time.Now().Unix())
	return err
}

// PendingCommands 取未投递的命令
func (s *Store) PendingCommands(cuid string) ([]struct {
	ID      int64
	Type    int
	Payload []byte
}, error) {
	rows, err := s.db.Query(`SELECT id, cmd_type, payload FROM commands WHERE cuid = ? AND delivered = 0 ORDER BY id`, cuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		ID      int64
		Type    int
		Payload []byte
	}
	for rows.Next() {
		var e struct {
			ID      int64
			Type    int
			Payload []byte
		}
		if err := rows.Scan(&e.ID, &e.Type, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkCommandDelivered 标记命令已投递
func (s *Store) MarkCommandDelivered(id int64) error {
	_, err := s.db.Exec(`UPDATE commands SET delivered = 1 WHERE id = ?`, id)
	return err
}

// ---------- 审计与会话 ----------

// AddAudit 记录审计事件
func (s *Store) AddAudit(cuid string, event int, payload []byte, ts int64) error {
	_, err := s.db.Exec(`INSERT INTO audit_logs (cuid, event, payload, ts) VALUES (?, ?, ?, ?)`, cuid, event, payload, ts)
	return err
}

// ListAudit 最近审计日志
func (s *Store) ListAudit(limit int) ([]struct {
	ID     int64
	CUID   string
	Event  int
	TS     int64
}, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, cuid, event, ts FROM audit_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		ID     int64
		CUID   string
		Event  int
		TS     int64
	}
	for rows.Next() {
		var e struct {
			ID     int64
			CUID   string
			Event  int
			TS     int64
		}
		if err := rows.Scan(&e.ID, &e.CUID, &e.Event, &e.TS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SaveSession 记录握手会话
func (s *Store) SaveSession(sessionID, cuid string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO sessions (session_id, cuid, created_at) VALUES (?, ?, ?)`,
		sessionID, cuid, time.Now().Unix())
	return err
}

// SessionCUID 查询会话所属客户端
func (s *Store) SessionCUID(sessionID string) (string, error) {
	var cuid string
	err := s.db.QueryRow(`SELECT cuid FROM sessions WHERE session_id = ?`, sessionID).Scan(&cuid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return cuid, nil
}

func scanObjects(rows *sql.Rows) ([]Object, error) {
	var out []Object
	for rows.Next() {
		var o Object
		if err := rows.Scan(&o.ID, &o.Kind, &o.Name, &o.Payload, &o.Version, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
