package store

import (
	"database/sql"
	"fmt"
)

const acmeAccountCols = `id, name, directory_url, email, account_key_ciphertext,
	registration_uri, eab_key_id, eab_hmac_ciphertext, terms_accepted_unix,
	status, last_error, created_at_unix, updated_at_unix`

func scanACMEAccount(row interface{ Scan(...any) error }) (*ACMEAccount, error) {
	var account ACMEAccount
	if err := row.Scan(
		&account.ID, &account.Name, &account.DirectoryURL, &account.Email,
		&account.AccountKeyCiphertext, &account.RegistrationURI,
		&account.EABKeyID, &account.EABHMACCiphertext, &account.TermsAcceptedUnix,
		&account.Status, &account.LastError, &account.CreatedAtUnix,
		&account.UpdatedAtUnix,
	); err != nil {
		return nil, err
	}
	account.HasAccountKey = account.AccountKeyCiphertext != ""
	account.HasEABHMAC = account.EABHMACCiphertext != ""
	return &account, nil
}

func (s *Store) CreateACMEAccount(account *ACMEAccount) error {
	if account == nil || account.Name == "" || account.DirectoryURL == "" {
		return fmt.Errorf("必须提供 ACME 账号名称和目录地址")
	}
	if account.ID == "" {
		account.ID = newID()
	}
	if account.Status == "" {
		account.Status = "pending"
	}
	now := nowUnix()
	account.CreatedAtUnix = now
	account.UpdatedAtUnix = now
	_, err := s.db.Exec(`
		INSERT INTO acme_accounts (
			id, name, directory_url, email, account_key_ciphertext,
			registration_uri, eab_key_id, eab_hmac_ciphertext,
			terms_accepted_unix, status, last_error, created_at_unix, updated_at_unix
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		account.ID, account.Name, account.DirectoryURL, account.Email,
		account.AccountKeyCiphertext, account.RegistrationURI, account.EABKeyID,
		account.EABHMACCiphertext, account.TermsAcceptedUnix, account.Status,
		account.LastError, now, now,
	)
	if err != nil {
		return fmt.Errorf("创建 ACME 账号失败：%w", err)
	}
	account.HasAccountKey = account.AccountKeyCiphertext != ""
	account.HasEABHMAC = account.EABHMACCiphertext != ""
	return nil
}

func (s *Store) UpdateACMEAccount(account *ACMEAccount) error {
	if account == nil || account.ID == "" {
		return fmt.Errorf("必须提供 ACME 账号 ID")
	}
	account.UpdatedAtUnix = nowUnix()
	res, err := s.db.Exec(`
		UPDATE acme_accounts SET name = ?, directory_url = ?, email = ?,
			account_key_ciphertext = ?, registration_uri = ?, eab_key_id = ?,
			eab_hmac_ciphertext = ?, terms_accepted_unix = ?, status = ?,
			last_error = ?, updated_at_unix = ?
		WHERE id = ?`,
		account.Name, account.DirectoryURL, account.Email,
		account.AccountKeyCiphertext, account.RegistrationURI, account.EABKeyID,
		account.EABHMACCiphertext, account.TermsAcceptedUnix, account.Status,
		account.LastError, account.UpdatedAtUnix, account.ID,
	)
	if err != nil {
		return fmt.Errorf("更新 ACME 账号失败：%w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("ACME 账号不存在：%s", account.ID)
	}
	return nil
}

func (s *Store) GetACMEAccount(id string) (*ACMEAccount, error) {
	account, err := scanACMEAccount(s.db.QueryRow(
		`SELECT `+acmeAccountCols+` FROM acme_accounts WHERE id = ?`, id,
	))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("ACME 账号不存在：%s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("读取 ACME 账号失败：%w", err)
	}
	return account, nil
}

func (s *Store) ListACMEAccounts() ([]ACMEAccount, error) {
	rows, err := s.db.Query(`SELECT ` + acmeAccountCols + ` FROM acme_accounts ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("查询 ACME 账号失败：%w", err)
	}
	defer rows.Close()
	out := []ACMEAccount{}
	for rows.Next() {
		account, err := scanACMEAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *account)
	}
	return out, rows.Err()
}

func (s *Store) DeleteACMEAccount(id string) error {
	res, err := s.db.Exec(`DELETE FROM acme_accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除 ACME 账号失败；请先解除关联证书：%w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("ACME 账号不存在：%s", id)
	}
	return nil
}

const protocolCertificateCols = `id, node_id, managed_domain_id, acme_account_id,
	domains_json, status, agent_key_id, public_key_fingerprint,
	candidate_cert_path, candidate_key_path, active_cert_path, active_key_path,
	cert_pem, serial, fingerprint, not_before_unix, not_after_unix,
	renew_after_unix, revision, retry_count, next_retry_unix, last_error,
	created_at_unix, updated_at_unix`

func scanProtocolCertificate(row interface{ Scan(...any) error }) (*ProtocolCertificate, error) {
	var certificate ProtocolCertificate
	var domainsJSON string
	if err := row.Scan(
		&certificate.ID, &certificate.NodeID, &certificate.ManagedDomainID,
		&certificate.ACMEAccountID, &domainsJSON, &certificate.Status,
		&certificate.AgentKeyID, &certificate.PublicKeyFingerprint,
		&certificate.CandidateCertPath, &certificate.CandidateKeyPath,
		&certificate.ActiveCertPath, &certificate.ActiveKeyPath,
		&certificate.CertPEM, &certificate.Serial, &certificate.Fingerprint,
		&certificate.NotBeforeUnix, &certificate.NotAfterUnix,
		&certificate.RenewAfterUnix, &certificate.Revision,
		&certificate.RetryCount, &certificate.NextRetryUnix,
		&certificate.LastError, &certificate.CreatedAtUnix,
		&certificate.UpdatedAtUnix,
	); err != nil {
		return nil, err
	}
	certificate.Domains = []string{}
	if err := unmarshalJSON(domainsJSON, &certificate.Domains); err != nil {
		return nil, fmt.Errorf("解析协议证书域名失败：%w", err)
	}
	return &certificate, nil
}

func (s *Store) CreateProtocolCertificate(certificate *ProtocolCertificate) error {
	if certificate == nil || certificate.NodeID == "" ||
		certificate.ManagedDomainID == "" || certificate.ACMEAccountID == "" {
		return fmt.Errorf("必须提供节点、托管域名和 ACME 账号")
	}
	var domainNodeID, fqdn string
	if err := s.db.QueryRow(
		`SELECT node_id, fqdn FROM managed_domains WHERE id = ?`,
		certificate.ManagedDomainID,
	).Scan(&domainNodeID, &fqdn); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("托管域名不存在：%s", certificate.ManagedDomainID)
		}
		return err
	}
	if domainNodeID != certificate.NodeID {
		return fmt.Errorf("协议证书节点与托管域名节点不一致")
	}
	if len(certificate.Domains) != 1 || certificate.Domains[0] != fqdn {
		return fmt.Errorf("协议证书域名必须与托管域名完全一致")
	}
	domainsJSON, err := marshalJSON(certificate.Domains)
	if err != nil {
		return err
	}
	if certificate.ID == "" {
		certificate.ID = newID()
	}
	if certificate.Status == "" {
		certificate.Status = "pending"
	}
	now := nowUnix()
	certificate.CreatedAtUnix = now
	certificate.UpdatedAtUnix = now
	_, err = s.db.Exec(`
		INSERT INTO protocol_certificates (
			id, node_id, managed_domain_id, acme_account_id, domains_json,
			status, agent_key_id, public_key_fingerprint, candidate_cert_path,
			candidate_key_path, active_cert_path, active_key_path, cert_pem,
			serial, fingerprint, not_before_unix, not_after_unix,
			renew_after_unix, revision, retry_count, next_retry_unix,
			last_error, created_at_unix, updated_at_unix
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		certificate.ID, certificate.NodeID, certificate.ManagedDomainID,
		certificate.ACMEAccountID, domainsJSON, certificate.Status,
		certificate.AgentKeyID, certificate.PublicKeyFingerprint,
		certificate.CandidateCertPath, certificate.CandidateKeyPath,
		certificate.ActiveCertPath, certificate.ActiveKeyPath, certificate.CertPEM,
		certificate.Serial, certificate.Fingerprint, certificate.NotBeforeUnix,
		certificate.NotAfterUnix, certificate.RenewAfterUnix, certificate.Revision,
		certificate.RetryCount, certificate.NextRetryUnix, certificate.LastError,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("创建协议证书失败：%w", err)
	}
	return nil
}

func (s *Store) UpdateProtocolCertificate(certificate *ProtocolCertificate) error {
	if certificate == nil || certificate.ID == "" {
		return fmt.Errorf("必须提供协议证书 ID")
	}
	domainsJSON, err := marshalJSON(certificate.Domains)
	if err != nil {
		return err
	}
	certificate.UpdatedAtUnix = nowUnix()
	res, err := s.db.Exec(`
		UPDATE protocol_certificates SET domains_json = ?, status = ?,
			agent_key_id = ?, public_key_fingerprint = ?, candidate_cert_path = ?,
			candidate_key_path = ?, active_cert_path = ?, active_key_path = ?,
			cert_pem = ?, serial = ?, fingerprint = ?, not_before_unix = ?,
			not_after_unix = ?, renew_after_unix = ?, revision = ?,
			retry_count = ?, next_retry_unix = ?, last_error = ?, updated_at_unix = ?
		WHERE id = ?`,
		domainsJSON, certificate.Status, certificate.AgentKeyID,
		certificate.PublicKeyFingerprint, certificate.CandidateCertPath,
		certificate.CandidateKeyPath, certificate.ActiveCertPath,
		certificate.ActiveKeyPath, certificate.CertPEM, certificate.Serial,
		certificate.Fingerprint, certificate.NotBeforeUnix,
		certificate.NotAfterUnix, certificate.RenewAfterUnix,
		certificate.Revision, certificate.RetryCount, certificate.NextRetryUnix,
		certificate.LastError, certificate.UpdatedAtUnix, certificate.ID,
	)
	if err != nil {
		return fmt.Errorf("更新协议证书失败：%w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("协议证书不存在：%s", certificate.ID)
	}
	return nil
}

func (s *Store) GetProtocolCertificate(id string) (*ProtocolCertificate, error) {
	certificate, err := scanProtocolCertificate(s.db.QueryRow(
		`SELECT `+protocolCertificateCols+` FROM protocol_certificates WHERE id = ?`, id,
	))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("协议证书不存在：%s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("读取协议证书失败：%w", err)
	}
	return certificate, nil
}

func (s *Store) ListProtocolCertificates() ([]ProtocolCertificate, error) {
	rows, err := s.db.Query(`SELECT ` + protocolCertificateCols + `
		FROM protocol_certificates ORDER BY created_at_unix DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询协议证书失败：%w", err)
	}
	defer rows.Close()
	out := []ProtocolCertificate{}
	for rows.Next() {
		certificate, err := scanProtocolCertificate(rows)
		if err != nil {
			return nil, err
		}
		certificate.CertPEM = ""
		out = append(out, *certificate)
	}
	return out, rows.Err()
}

func (s *Store) ListProtocolCertificatesDue(now int64, limit int) ([]ProtocolCertificate, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+protocolCertificateCols+`
		FROM protocol_certificates
		WHERE (status = 'active' AND renew_after_unix > 0 AND renew_after_unix <= ?)
			OR (status = 'retry_wait' AND next_retry_unix <= ?)
		ORDER BY CASE WHEN status = 'active' THEN renew_after_unix ELSE next_retry_unix END
		LIMIT ?`, now, now, limit)
	if err != nil {
		return nil, fmt.Errorf("查询待签发协议证书失败：%w", err)
	}
	defer rows.Close()
	out := []ProtocolCertificate{}
	for rows.Next() {
		certificate, err := scanProtocolCertificate(rows)
		if err != nil {
			return nil, err
		}
		certificate.CertPEM = ""
		out = append(out, *certificate)
	}
	return out, rows.Err()
}

func (s *Store) PutNodeInboundTLSBinding(binding *NodeInboundTLSBinding) error {
	if binding == nil || binding.NodeID == "" || binding.InboundID == "" {
		return fmt.Errorf("必须提供节点和入站 ID")
	}
	if binding.Mode == "" {
		binding.Mode = "legacy"
	}
	if binding.Mode != "legacy" && binding.Mode != "managed" {
		return fmt.Errorf("TLS 绑定模式无效：%s", binding.Mode)
	}
	if binding.Mode == "managed" &&
		(binding.ManagedDomainID == "" || binding.CertificateID == "") {
		return fmt.Errorf("托管 TLS 必须绑定域名和证书")
	}
	exists, err := s.NodeInboundPairExists(binding.NodeID, binding.InboundID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("节点入站关联不存在：%s/%s", binding.NodeID, binding.InboundID)
	}
	if binding.Mode == "legacy" {
		binding.ManagedDomainID = ""
		binding.CertificateID = ""
	} else {
		var certificateNodeID, certificateDomainID, certificateStatus, domainNodeID string
		err := s.db.QueryRow(`
			SELECT c.node_id, c.managed_domain_id, c.status, d.node_id
			FROM protocol_certificates c
			INNER JOIN managed_domains d ON d.id = c.managed_domain_id
			WHERE c.id = ? AND d.id = ?`,
			binding.CertificateID, binding.ManagedDomainID,
		).Scan(
			&certificateNodeID, &certificateDomainID, &certificateStatus, &domainNodeID,
		)
		if err == sql.ErrNoRows {
			return fmt.Errorf("托管 TLS 的域名和证书不匹配")
		}
		if err != nil {
			return err
		}
		if certificateNodeID != binding.NodeID || domainNodeID != binding.NodeID ||
			certificateDomainID != binding.ManagedDomainID {
			return fmt.Errorf("托管 TLS 的域名或证书不属于该节点")
		}
		if certificateStatus != "active" {
			return fmt.Errorf("托管 TLS 证书尚未生效")
		}
	}
	now := nowUnix()
	if binding.CreatedAtUnix == 0 {
		binding.CreatedAtUnix = now
	}
	binding.UpdatedAtUnix = now
	_, err = s.db.Exec(`
		INSERT INTO node_inbound_tls_bindings (
			node_id, inbound_id, mode, managed_domain_id, certificate_id,
			created_at_unix, updated_at_unix
		) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)
		ON CONFLICT(node_id, inbound_id) DO UPDATE SET
			mode = excluded.mode,
			managed_domain_id = excluded.managed_domain_id,
			certificate_id = excluded.certificate_id,
			updated_at_unix = excluded.updated_at_unix`,
		binding.NodeID, binding.InboundID, binding.Mode, binding.ManagedDomainID,
		binding.CertificateID, binding.CreatedAtUnix, binding.UpdatedAtUnix,
	)
	if err != nil {
		return fmt.Errorf("保存节点入站 TLS 绑定失败：%w", err)
	}
	return nil
}

func (s *Store) GetNodeInboundTLSBinding(nodeID, inboundID string) (*NodeInboundTLSBinding, error) {
	var binding NodeInboundTLSBinding
	var managedDomainID, certificateID sql.NullString
	err := s.db.QueryRow(`
		SELECT node_id, inbound_id, mode, managed_domain_id, certificate_id,
			created_at_unix, updated_at_unix
		FROM node_inbound_tls_bindings WHERE node_id = ? AND inbound_id = ?`,
		nodeID, inboundID,
	).Scan(
		&binding.NodeID, &binding.InboundID, &binding.Mode, &managedDomainID,
		&certificateID, &binding.CreatedAtUnix, &binding.UpdatedAtUnix,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("节点入站 TLS 绑定不存在：%s/%s", nodeID, inboundID)
	}
	if err != nil {
		return nil, fmt.Errorf("读取节点入站 TLS 绑定失败：%w", err)
	}
	binding.ManagedDomainID = managedDomainID.String
	binding.CertificateID = certificateID.String
	return &binding, nil
}

func (s *Store) ListNodeInboundTLSBindings(nodeID string) ([]NodeInboundTLSBinding, error) {
	rows, err := s.db.Query(`
		SELECT node_id, inbound_id, mode, managed_domain_id, certificate_id,
			created_at_unix, updated_at_unix
		FROM node_inbound_tls_bindings WHERE node_id = ?
		ORDER BY inbound_id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("查询节点入站 TLS 绑定失败：%w", err)
	}
	defer rows.Close()
	bindings, err := scanNodeInboundTLSBindings(rows)
	if err != nil {
		return nil, err
	}
	if bindings == nil {
		bindings = []NodeInboundTLSBinding{}
	}
	return bindings, nil
}

// ListAllNodeInboundTLSBindings returns every TLS binding in one query,
// grouped by node ID, for bulk resolution (subscription rendering).
func (s *Store) ListAllNodeInboundTLSBindings() (map[string][]NodeInboundTLSBinding, error) {
	rows, err := s.db.Query(`
		SELECT node_id, inbound_id, mode, managed_domain_id, certificate_id,
			created_at_unix, updated_at_unix
		FROM node_inbound_tls_bindings ORDER BY node_id, inbound_id`)
	if err != nil {
		return nil, fmt.Errorf("查询全部节点入站 TLS 绑定失败：%w", err)
	}
	defer rows.Close()
	out := map[string][]NodeInboundTLSBinding{}
	bindings, err := scanNodeInboundTLSBindings(rows)
	if err != nil {
		return nil, err
	}
	for _, binding := range bindings {
		out[binding.NodeID] = append(out[binding.NodeID], binding)
	}
	return out, nil
}

func scanNodeInboundTLSBindings(rows *sql.Rows) ([]NodeInboundTLSBinding, error) {
	var out []NodeInboundTLSBinding
	for rows.Next() {
		var binding NodeInboundTLSBinding
		var managedDomainID, certificateID sql.NullString
		if err := rows.Scan(
			&binding.NodeID, &binding.InboundID, &binding.Mode,
			&managedDomainID, &certificateID, &binding.CreatedAtUnix,
			&binding.UpdatedAtUnix,
		); err != nil {
			return nil, err
		}
		binding.ManagedDomainID = managedDomainID.String
		binding.CertificateID = certificateID.String
		out = append(out, binding)
	}
	return out, rows.Err()
}

func (s *Store) NodeInboundPairExists(nodeID, inboundID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM node_inbounds WHERE node_id = ? AND inbound_id = ?`,
		nodeID, inboundID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("检查节点入站归属失败：%w", err)
	}
	return count > 0, nil
}

func (s *Store) DeleteProtocolCertificate(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var bindings int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM node_inbound_tls_bindings WHERE certificate_id = ?`, id,
	).Scan(&bindings); err != nil {
		return fmt.Errorf("检查协议证书绑定失败：%w", err)
	}
	if bindings > 0 {
		return fmt.Errorf("删除协议证书失败；请先解除 %d 个 TLS 绑定", bindings)
	}
	var running int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM automation_jobs
		WHERE target_type = 'protocol_certificate' AND target_id = ?
			AND state = 'running'`, id,
	).Scan(&running); err != nil {
		return fmt.Errorf("检查协议证书任务失败：%w", err)
	}
	if running > 0 {
		return fmt.Errorf("协议证书正在签发或续期，请等待任务结束后再删除")
	}
	if _, err := tx.Exec(`
		DELETE FROM automation_jobs
		WHERE target_type = 'protocol_certificate' AND target_id = ?`, id,
	); err != nil {
		return fmt.Errorf("取消协议证书任务失败：%w", err)
	}
	res, err := tx.Exec(`DELETE FROM protocol_certificates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除协议证书失败；请先解除 TLS 绑定：%w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("协议证书不存在：%s", id)
	}
	return tx.Commit()
}
