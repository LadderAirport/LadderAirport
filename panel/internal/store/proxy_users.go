package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// --- Proxy users ---

const proxyUserSelectCols = `id, name, note, enabled, secret, created_at_unix, updated_at_unix`

// CreateProxyUser inserts a user. Secret must be set by the caller.
func (s *Store) CreateProxyUser(u *ProxyUser) error {
	if u == nil {
		return fmt.Errorf("用户不能为空")
	}
	u.Name = strings.TrimSpace(u.Name)
	if u.Name == "" {
		return fmt.Errorf("用户名不能为空")
	}
	if strings.TrimSpace(u.Secret) == "" {
		return fmt.Errorf("用户密钥不能为空")
	}
	if u.ID == "" {
		u.ID = newID()
	}
	now := nowUnix()
	if u.CreatedAtUnix == 0 {
		u.CreatedAtUnix = now
	}
	u.UpdatedAtUnix = now
	_, err := s.db.Exec(`
		INSERT INTO proxy_users (id, name, note, enabled, secret, created_at_unix, updated_at_unix)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Name, u.Note, boolToInt(u.Enabled), u.Secret, u.CreatedAtUnix, u.UpdatedAtUnix,
	)
	if err != nil {
		return fmt.Errorf("创建用户失败：%w", err)
	}
	return nil
}

// UpdateProxyUser saves name, note, enabled and secret.
func (s *Store) UpdateProxyUser(u *ProxyUser) error {
	if u == nil || u.ID == "" {
		return fmt.Errorf("必须提供用户 ID")
	}
	u.Name = strings.TrimSpace(u.Name)
	if u.Name == "" {
		return fmt.Errorf("用户名不能为空")
	}
	if strings.TrimSpace(u.Secret) == "" {
		return fmt.Errorf("用户密钥不能为空")
	}
	u.UpdatedAtUnix = nowUnix()
	res, err := s.db.Exec(`
		UPDATE proxy_users SET name = ?, note = ?, enabled = ?, secret = ?, updated_at_unix = ?
		WHERE id = ?`,
		u.Name, u.Note, boolToInt(u.Enabled), u.Secret, u.UpdatedAtUnix, u.ID,
	)
	if err != nil {
		return fmt.Errorf("更新用户失败：%w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("用户不存在：%s", u.ID)
	}
	return nil
}

// DeleteProxyUser removes a user and unbinds its subscriptions (they fall
// back to shared credentials, which admission-only inbounds then reject).
func (s *Store) DeleteProxyUser(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE subscriptions SET user_id = '' WHERE user_id = ?`, id); err != nil {
		return fmt.Errorf("解除订阅与用户的绑定失败：%w", err)
	}
	res, err := tx.Exec(`DELETE FROM proxy_users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除用户失败：%w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("用户不存在：%s", id)
	}
	return tx.Commit()
}

func (s *Store) GetProxyUser(id string) (*ProxyUser, error) {
	row := s.db.QueryRow(`SELECT `+proxyUserSelectCols+` FROM proxy_users WHERE id = ?`, id)
	u, err := scanProxyUser(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("用户不存在：%s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("读取用户失败：%w", err)
	}
	return u, nil
}

func (s *Store) ListProxyUsers() ([]ProxyUser, error) {
	rows, err := s.db.Query(`SELECT ` + proxyUserSelectCols + ` FROM proxy_users ORDER BY created_at_unix ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询用户列表失败：%w", err)
	}
	defer rows.Close()
	out := []ProxyUser{}
	for rows.Next() {
		u, err := scanProxyUser(rows)
		if err != nil {
			return nil, fmt.Errorf("读取用户列表失败：%w", err)
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func scanProxyUser(row interface{ Scan(dest ...any) error }) (*ProxyUser, error) {
	var u ProxyUser
	var enabled int
	if err := row.Scan(&u.ID, &u.Name, &u.Note, &enabled, &u.Secret, &u.CreatedAtUnix, &u.UpdatedAtUnix); err != nil {
		return nil, err
	}
	u.Enabled = enabled != 0
	return &u, nil
}

// HasUserBoundSubscriptions reports whether any subscription is bound to a user.
func (s *Store) HasUserBoundSubscriptions() (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM subscriptions WHERE user_id != ''`).Scan(&n); err != nil {
		return false, fmt.Errorf("查询用户订阅失败：%w", err)
	}
	return n > 0, nil
}

// --- Node admission ---

// ListNodeAdmissions returns admission settings keyed by node ID.
// Nodes without a row are not admission-only.
func (s *Store) ListNodeAdmissions() (map[string]NodeAdmission, error) {
	rows, err := s.db.Query(`SELECT node_id, admission_only, internal_secret FROM node_admission`)
	if err != nil {
		return nil, fmt.Errorf("查询节点准入设置失败：%w", err)
	}
	defer rows.Close()
	out := map[string]NodeAdmission{}
	for rows.Next() {
		var a NodeAdmission
		var only int
		if err := rows.Scan(&a.NodeID, &only, &a.InternalSecret); err != nil {
			return nil, fmt.Errorf("读取节点准入设置失败：%w", err)
		}
		a.AdmissionOnly = only != 0
		out[a.NodeID] = a
	}
	return out, rows.Err()
}

// SetNodeAdmission stores the admission-only flag. newSecret is used only
// when the node has no internal secret yet, so the panel's internal chain
// credential stays stable across toggles.
func (s *Store) SetNodeAdmission(nodeID string, admissionOnly bool, newSecret string) (*NodeAdmission, error) {
	if strings.TrimSpace(newSecret) == "" {
		return nil, fmt.Errorf("内部凭据密钥不能为空")
	}
	if _, err := s.GetNode(nodeID); err != nil {
		return nil, err
	}
	_, err := s.db.Exec(`
		INSERT INTO node_admission (node_id, admission_only, internal_secret) VALUES (?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET admission_only = excluded.admission_only`,
		nodeID, boolToInt(admissionOnly), newSecret,
	)
	if err != nil {
		return nil, fmt.Errorf("保存节点准入设置失败：%w", err)
	}
	all, err := s.ListNodeAdmissions()
	if err != nil {
		return nil, err
	}
	a := all[nodeID]
	return &a, nil
}

// migrateInboundAdmissionToNodes converts v0.17.0 per-inbound admission
// flags: every node with an admission-only inbound attached (standalone or
// as a chain hop) becomes admission-only, failing closed. The old rows are
// then removed; the inbound_admission table is kept only for this migration.
func (s *Store) migrateInboundAdmissionToNodes() error {
	var pending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM inbound_admission`).Scan(&pending); err != nil {
		return fmt.Errorf("检查旧版入站准入设置失败：%w", err)
	}
	if pending == 0 {
		return nil
	}
	rows, err := s.db.Query(`
		SELECT DISTINCT node_id FROM (
			SELECT ni.node_id FROM node_inbounds ni
			JOIN inbound_admission a ON a.inbound_id = ni.inbound_id WHERE a.admission_only = 1
			UNION
			SELECT h.node_id FROM proxy_chain_hops h
			JOIN inbound_admission a ON a.inbound_id = h.inbound_id WHERE a.admission_only = 1
		) ORDER BY node_id`)
	if err != nil {
		return fmt.Errorf("读取旧版入站准入设置失败：%w", err)
	}
	var nodeIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("读取旧版入站准入设置失败：%w", err)
		}
		nodeIDs = append(nodeIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range nodeIDs {
		secret, err := randomHex(32)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO node_admission (node_id, admission_only, internal_secret) VALUES (?, 1, ?)
			ON CONFLICT(node_id) DO UPDATE SET admission_only = 1`, id, secret); err != nil {
			return fmt.Errorf("迁移节点准入设置失败：%w", err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM inbound_admission`); err != nil {
		return fmt.Errorf("清理旧版入站准入设置失败：%w", err)
	}
	return tx.Commit()
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机密钥失败：%w", err)
	}
	return hex.EncodeToString(b), nil
}
