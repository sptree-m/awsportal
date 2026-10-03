package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type ProxyRule struct {
	ID, SubjectID                                              int64
	Name, Scope, Subject, Domain, Ports, Methods, Effect, Kind string
	Enabled                                                    bool
}
type ProxyCredential struct {
	ID, UserID, Expires int64
	Username, Label     string
}

const proxySchema = `CREATE TABLE IF NOT EXISTS proxy_rules(id INTEGER PRIMARY KEY,name TEXT NOT NULL,scope TEXT NOT NULL,subject_id INTEGER NOT NULL DEFAULT 0,domain TEXT NOT NULL,ports TEXT NOT NULL,methods TEXT NOT NULL,effect TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS proxy_credentials(id INTEGER PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id),label TEXT NOT NULL,token_hash TEXT UNIQUE NOT NULL,expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS proxy_settings(id INTEGER PRIMARY KEY CHECK(id=1),enabled INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO proxy_settings(id,enabled) VALUES(1,0);`

func (s *Store) ProxyEnabled(ctx context.Context) bool {
	var x bool
	return s.DB.QueryRowContext(ctx, "SELECT enabled FROM proxy_settings WHERE id=1").Scan(&x) == nil && x
}
func (s *Store) SetProxyEnabled(ctx context.Context, enabled bool) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE proxy_settings SET enabled=? WHERE id=1", enabled)
	return e
}
func CanonicalDomain(d string) (string, error) {
	d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
	base := strings.TrimPrefix(d, "*.")
	if len(base) > 253 || len(base) < 1 || net.ParseIP(base) != nil {
		return "", fmt.Errorf("domain required")
	}
	for _, label := range strings.Split(base, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid domain")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("ASCII domain required")
			}
		}
	}
	return d, nil
}
func NormalizeProxyRule(r ProxyRule) (ProxyRule, error) {
	var e error
	r.Name = strings.TrimSpace(r.Name)
	if len(r.Name) < 1 || len(r.Name) > 80 {
		return r, fmt.Errorf("name required")
	}
	if r.Kind == "" {
		r.Kind = "domain"
	}
	switch r.Kind {
	case "domain":
		r.Domain, e = CanonicalDomain(r.Domain)
	case "cidr":
		r.Domain, e = NormalizeCIDR(r.Domain)
	default:
		e = fmt.Errorf("destination kind")
	}
	if e != nil {
		return r, e
	}
	if r.Scope != "all" && r.Scope != "user" && r.Scope != "group" {
		return r, fmt.Errorf("scope")
	}
	if r.Scope == "all" {
		r.SubjectID = 0
	} else if r.SubjectID < 1 {
		return r, fmt.Errorf("subject")
	}
	if r.Effect != "allow" && r.Effect != "deny" {
		return r, fmt.Errorf("effect")
	}
	parts := strings.Split(r.Ports, ",")
	if len(parts) > 16 {
		return r, fmt.Errorf("ports")
	}
	for i, v := range parts {
		p, e := strconv.Atoi(strings.TrimSpace(v))
		if e != nil || p < 1 || p > 65535 {
			return r, fmt.Errorf("ports")
		}
		parts[i] = strconv.Itoa(p)
	}
	r.Ports = strings.Join(parts, ",")
	parts = strings.Split(strings.ToUpper(r.Methods), ",")
	for i, v := range parts {
		v = strings.TrimSpace(v)
		switch v {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT":
		default:
			return r, fmt.Errorf("methods")
		}
		parts[i] = v
	}
	r.Methods = strings.Join(parts, ",")
	return r, nil
}
func (s *Store) SaveProxyRule(ctx context.Context, r ProxyRule) error {
	r, e := NormalizeProxyRule(r)
	if e != nil {
		return e
	}
	if r.Scope != "all" {
		table := "users"
		if r.Scope == "group" {
			table = "groups"
		}
		var n int
		if e = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id=?", r.SubjectID).Scan(&n); e != nil || n != 1 {
			return fmt.Errorf("unknown subject")
		}
	}
	if r.ID == 0 {
		_, e = s.DB.ExecContext(ctx, "INSERT INTO proxy_rules(name,scope,subject_id,domain,ports,methods,effect,enabled,kind) VALUES(?,?,?,?,?,?,?,?,?)", r.Name, r.Scope, r.SubjectID, r.Domain, r.Ports, r.Methods, r.Effect, r.Enabled, r.Kind)
	} else {
		_, e = s.DB.ExecContext(ctx, "UPDATE proxy_rules SET name=?,scope=?,subject_id=?,domain=?,ports=?,methods=?,effect=?,enabled=?,kind=? WHERE id=?", r.Name, r.Scope, r.SubjectID, r.Domain, r.Ports, r.Methods, r.Effect, r.Enabled, r.Kind, r.ID)
	}
	return e
}
func (s *Store) DeleteProxyRule(ctx context.Context, id int64) error {
	_, e := s.DB.ExecContext(ctx, "DELETE FROM proxy_rules WHERE id=?", id)
	return e
}
func (s *Store) ProxyRules(ctx context.Context) ([]ProxyRule, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT p.id,p.name,p.scope,p.subject_id,COALESCE(CASE p.scope WHEN 'user' THEN u.username WHEN 'group' THEN g.name ELSE '全員' END,'削除済み'),p.domain,p.ports,p.methods,p.effect,p.enabled,p.kind FROM proxy_rules p LEFT JOIN users u ON p.scope='user' AND u.id=p.subject_id LEFT JOIN groups g ON p.scope='group' AND g.id=p.subject_id ORDER BY p.id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ProxyRule{}
	for rows.Next() {
		var r ProxyRule
		if e = rows.Scan(&r.ID, &r.Name, &r.Scope, &r.SubjectID, &r.Subject, &r.Domain, &r.Ports, &r.Methods, &r.Effect, &r.Enabled, &r.Kind); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func DomainMatches(pattern, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(host, pattern[1:]) && host != pattern[2:]
	}
	return host == pattern
}
func csvContains(csv, v string) bool {
	for _, x := range strings.Split(csv, ",") {
		if x == v {
			return true
		}
	}
	return false
}
func (s *Store) ProxyAllowed(ctx context.Context, u User, host, port, method string) bool {
	return s.ProxyResolvedAllowed(ctx, u, host, nil, port, method)
}

// Evaluate each resolved IP: an allow for the domain OR IP is needed; either deny wins.
func (s *Store) ProxyResolvedAllowed(ctx context.Context, u User, host string, ips []netip.Addr, port, method string) bool {
	if !s.ProxyEnabled(ctx) {
		return false
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT domain,ports,methods,effect,kind FROM proxy_rules WHERE enabled=1 AND (scope='all' OR (scope='user' AND subject_id=?) OR (scope='group' AND subject_id IN (SELECT group_id FROM group_members WHERE user_id=?)))`, u.ID, u.ID)
	if e != nil {
		return false
	}
	defer rows.Close()
	type rule struct{ target, effect, kind string }
	rules := []rule{}
	for rows.Next() {
		var d, p, m, f, k string
		if rows.Scan(&d, &p, &m, &f, &k) != nil {
			return false
		}
		if csvContains(p, port) && csvContains(m, method) {
			rules = append(rules, rule{d, f, k})
		}
	}
	if rows.Err() != nil {
		return false
	}
	if len(ips) == 0 {
		if ip, e := netip.ParseAddr(host); e == nil {
			ips = []netip.Addr{ip.Unmap()}
		} else {
			ips = []netip.Addr{{}}
		}
	}
	for _, ip := range ips {
		allow := false
		for _, r := range rules {
			match := r.kind == "domain" && DomainMatches(r.target, host)
			if r.kind == "cidr" && ip.IsValid() {
				p, e := netip.ParsePrefix(r.target)
				match = e == nil && p.Contains(ip.Unmap())
			}
			if match {
				if r.effect == "deny" {
					return false
				}
				allow = true
			}
		}
		if !allow {
			return false
		}
	}
	return true
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func (s *Store) IssueProxyCredential(ctx context.Context, uid int64, label string, days int) (string, error) {
	label = strings.TrimSpace(label)
	if len(label) < 1 || len(label) > 80 || days < 1 || days > 90 {
		return "", fmt.Errorf("label / expiry")
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	token := hex.EncodeToString(b)
	_, e := s.DB.ExecContext(ctx, "INSERT INTO proxy_credentials(user_id,label,token_hash,expires) VALUES(?,?,?,?)", uid, label, tokenHash(token), time.Now().Add(time.Duration(days)*24*time.Hour).Unix())
	return token, e
}
func (s *Store) ProxyUser(ctx context.Context, username, token string) (User, error) {
	u, e := s.UserByName(ctx, username)
	if e != nil || !u.Enabled || u.MustChangePassword || s.AccountExpired(u, time.Now()) {
		return User{}, fmt.Errorf("account denied")
	}
	var n int
	e = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM proxy_credentials WHERE user_id=? AND token_hash=? AND expires>?", u.ID, tokenHash(token), time.Now().Unix()).Scan(&n)
	if e != nil || n != 1 {
		return User{}, fmt.Errorf("credential denied")
	}
	return u, nil
}
func (s *Store) ProxyCredentials(ctx context.Context) ([]ProxyCredential, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT p.id,p.user_id,u.username,p.label,p.expires FROM proxy_credentials p JOIN users u ON u.id=p.user_id ORDER BY p.id DESC")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ProxyCredential{}
	for rows.Next() {
		var c ProxyCredential
		if e = rows.Scan(&c.ID, &c.UserID, &c.Username, &c.Label, &c.Expires); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) RevokeProxyCredential(ctx context.Context, id int64) error {
	_, e := s.DB.ExecContext(ctx, "DELETE FROM proxy_credentials WHERE id=?", id)
	return e
}

func (s *Store) HasProxyCIDRRule(ctx context.Context, u User, port, method string) bool {
	rows, e := s.DB.QueryContext(ctx, `SELECT ports,methods FROM proxy_rules WHERE enabled=1 AND kind='cidr' AND effect='allow' AND (scope='all' OR (scope='user' AND subject_id=?) OR (scope='group' AND subject_id IN (SELECT group_id FROM group_members WHERE user_id=?)))`, u.ID, u.ID)
	if e != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var p, m string
		if rows.Scan(&p, &m) != nil {
			return false
		}
		if csvContains(p, port) && csvContains(m, method) {
			return true
		}
	}
	return false
}
