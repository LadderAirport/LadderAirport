package store

import (
	"database/sql"
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

// --- Inbound admission ---

// ListInboundAdmissions returns admission settings keyed by inbound ID.
// Inbounds without a row are not admission-only.
func (s *Store) ListInboundAdmissions() (map[string]InboundAdmission, error) {
	rows, err := s.db.Query(`SELECT inbound_id, admission_only, internal_secret FROM inbound_admission`)
	if err != nil {
		return nil, fmt.Errorf("查询入站准入设置失败：%w", err)
	}
	defer rows.Close()
	out := map[string]InboundAdmission{}
	for rows.Next() {
		var a InboundAdmission
		var only int
		if err := rows.Scan(&a.InboundID, &only, &a.InternalSecret); err != nil {
			return nil, fmt.Errorf("读取入站准入设置失败：%w", err)
		}
		a.AdmissionOnly = only != 0
		out[a.InboundID] = a
	}
	return out, rows.Err()
}

// SetInboundAdmission stores the admission-only flag. newSecret is used only
// when the inbound has no internal secret yet, so the panel's internal chain
// credential stays stable across toggles.
func (s *Store) SetInboundAdmission(inboundID string, admissionOnly bool, newSecret string) (*InboundAdmission, error) {
	if strings.TrimSpace(newSecret) == "" {
		return nil, fmt.Errorf("内部凭据密钥不能为空")
	}
	if _, err := s.GetInbound(inboundID); err != nil {
		return nil, err
	}
	_, err := s.db.Exec(`
		INSERT INTO inbound_admission (inbound_id, admission_only, internal_secret) VALUES (?, ?, ?)
		ON CONFLICT(inbound_id) DO UPDATE SET admission_only = excluded.admission_only`,
		inboundID, boolToInt(admissionOnly), newSecret,
	)
	if err != nil {
		return nil, fmt.Errorf("保存入站准入设置失败：%w", err)
	}
	all, err := s.ListInboundAdmissions()
	if err != nil {
		return nil, err
	}
	a := all[inboundID]
	return &a, nil
}
