package store

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
)

func TestEgressDraftRevisionAndCIDR(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	s.DB.Exec(`INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-one','one','host')`)
	rules, e := ParseEgressRules("192.0.2.10 tcp 443\n2001:db8::1 udp 100-200")
	if e != nil || len(rules) != 2 || rules[0].CIDR != "192.0.2.10/32" || rules[1].CIDR != "2001:db8::1/128" {
		t.Fatal(rules, e)
	}
	for _, bad := range []string{"bad tcp 443", "0.0.0.0/0 all 0", "10.0.0.0/8 tcp 500-100", "10.0.0.0/8 tcp 65536"} {
		if _, e = ParseEgressRules(bad); e == nil {
			t.Fatal(bad)
		}
	}
	p := EgressPolicy{InstanceID: "i-one", SecurityGroupID: "sg-one", ProxyGroupID: "sg-proxy", ProxyPort: 3128, Rules: rules}
	if e = s.SaveEgress(ctx, p); e != nil {
		t.Fatal(e)
	}
	p, _ = s.Egress(ctx, "i-one")
	if p.Revision != 1 || p.AppliedRevision != 0 {
		t.Fatal(p)
	}
	s.RecordEgressApply(ctx, "i-one", 1, nil)
	s.SaveEgress(ctx, p)
	p, _ = s.Egress(ctx, "i-one")
	if p.Revision != 2 || p.AppliedRevision != 1 {
		t.Fatal(p)
	}
	s.RecordEgressApply(ctx, "i-one", 2, context.DeadlineExceeded)
	p, _ = s.Egress(ctx, "i-one")
	if p.AppliedRevision != 1 || p.LastError == "" {
		t.Fatal("false applied status")
	}
}
func TestProxyDomainAndResolvedCIDR(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(filepath.Join(t.TempDir(), "db"))
	defer s.Close()
	s.Migrate(ctx)
	s.CreateUser(ctx, "alice", "x", "user", "")
	u, _ := s.UserByName(ctx, "alice")
	s.SetProxyEnabled(ctx, true)
	r := ProxyRule{Name: "domain", Scope: "all", Domain: "*.example.com", Ports: "443", Methods: "CONNECT", Effect: "allow", Enabled: true}
	if e := s.SaveProxyRule(ctx, r); e != nil {
		t.Fatal(e)
	}
	r.Name = "IP deny"
	r.Kind = "cidr"
	r.Domain = "8.8.8.0/24"
	r.Effect = "deny"
	if e := s.SaveProxyRule(ctx, r); e != nil {
		t.Fatal(e)
	}
	ips := []netip.Addr{netip.MustParseAddr("8.8.8.8")}
	if s.ProxyResolvedAllowed(ctx, u, "api.example.com", ips, "443", "CONNECT") {
		t.Fatal("IP deny bypass via domain")
	}
	s.DeleteProxyRule(ctx, 2)
	r.Name = "IP allow"
	r.Effect = "allow"
	s.SaveProxyRule(ctx, r)
	if !s.ProxyAllowed(ctx, u, "8.8.8.8", "443", "CONNECT") || !s.ProxyResolvedAllowed(ctx, u, "other.net", ips, "443", "CONNECT") {
		t.Fatal("CIDR allow")
	}
	ips = append(ips, netip.MustParseAddr("9.9.9.9"))
	if s.ProxyResolvedAllowed(ctx, u, "other.net", ips, "443", "CONNECT") {
		t.Fatal("unapproved DNS IP allowed")
	}
}

func TestProxyLegacyMigration(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(filepath.Join(t.TempDir(), "db"))
	defer s.Close()
	if _, e := s.DB.ExecContext(ctx, schema+proxySchema); e != nil {
		t.Fatal(e)
	}
	_, e := s.DB.ExecContext(ctx, `INSERT INTO proxy_rules(name,scope,domain,ports,methods,effect) VALUES('legacy','all','example.com','443','CONNECT','allow')`)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	rules, e := s.ProxyRules(ctx)
	if e != nil || len(rules) != 1 || rules[0].Kind != "domain" || rules[0].Domain != "example.com" {
		t.Fatal(rules, e)
	}
}
