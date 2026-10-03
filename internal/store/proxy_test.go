package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProxyPolicyAndCredential(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	s.DB.Exec(`INSERT INTO users(id,username,password_hash,role) VALUES(1,'alice','x','user'),(2,'bob','x','portal_admin');INSERT INTO groups VALUES(1,'dev');INSERT INTO group_members VALUES(1,1)`)
	u, _ := s.UserByName(ctx, "alice")
	bob, _ := s.UserByName(ctx, "bob")
	rule := ProxyRule{Name: "dev", Scope: "group", SubjectID: 1, Domain: "*.example.com", Ports: "443", Methods: "CONNECT", Effect: "allow", Enabled: true}
	if e = s.SaveProxyRule(ctx, rule); e != nil {
		t.Fatal(e)
	}
	if s.ProxyAllowed(ctx, u, "api.example.com", "443", "CONNECT") {
		t.Fatal("disabled proxy")
	}
	s.SetProxyEnabled(ctx, true)
	if !s.ProxyAllowed(ctx, u, "api.example.com", "443", "CONNECT") || s.ProxyAllowed(ctx, bob, "api.example.com", "443", "CONNECT") {
		t.Fatal("group scope or admin bypass")
	}
	for _, host := range []string{"example.com", "evil-example.com", "example.com.evil"} {
		if s.ProxyAllowed(ctx, u, host, "443", "CONNECT") {
			t.Fatal(host)
		}
	}
	if s.ProxyAllowed(ctx, u, "api.example.com", "80", "CONNECT") || s.ProxyAllowed(ctx, u, "api.example.com", "443", "PUT") {
		t.Fatal("port/method bypass")
	}
	rule.Name = "deny"
	rule.Scope = "user"
	rule.SubjectID = 1
	rule.Domain = "api.example.com"
	rule.Effect = "deny"
	if e = s.SaveProxyRule(ctx, rule); e != nil {
		t.Fatal(e)
	}
	if s.ProxyAllowed(ctx, u, "api.example.com", "443", "CONNECT") {
		t.Fatal("deny precedence")
	}
	s.DeleteProxyRule(ctx, 2)
	s.DB.Exec("DELETE FROM group_members")
	if s.ProxyAllowed(ctx, u, "api.example.com", "443", "CONNECT") {
		t.Fatal("stale membership")
	}
	for _, domain := range []string{"*", "127.0.0.1", "example.com/path", "foo..com", "-foo.com", "例.jp"} {
		rule.Domain = domain
		if s.SaveProxyRule(ctx, rule) == nil {
			t.Fatal("invalid domain", domain)
		}
	}
	tok, e := s.IssueProxyCredential(ctx, 1, "test", 1)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ProxyUser(ctx, "alice", tok); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ProxyUser(ctx, "bob", tok); e == nil {
		t.Fatal("wrong user")
	}
	var stored string
	s.DB.QueryRow("SELECT token_hash FROM proxy_credentials").Scan(&stored)
	if stored == tok {
		t.Fatal("plaintext stored")
	}
	s.DB.Exec("UPDATE users SET enabled=0 WHERE id=1")
	if _, e = s.ProxyUser(ctx, "alice", tok); e == nil {
		t.Fatal("disabled user")
	}
	s.DB.Exec("UPDATE users SET enabled=1 WHERE id=1")
	s.DB.Exec("UPDATE proxy_credentials SET expires=1")
	if _, e = s.ProxyUser(ctx, "alice", tok); e == nil {
		t.Fatal("expired credential")
	}
	s.RevokeProxyCredential(ctx, 1)
	if _, e = s.ProxyUser(ctx, "alice", tok); e == nil {
		t.Fatal("revoked credential")
	}
}
