package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

type EgressRule struct {
	CIDR, Protocol string
	From, To       int32
}
type EgressPolicy struct {
	InstanceID, SecurityGroupID, ProxyGroupID string
	ProxyPort                                 int32
	Rules                                     []EgressRule
	Revision, AppliedRevision                 int64
	LastError                                 string
}

const egressSchema = `CREATE TABLE IF NOT EXISTS egress_policies(instance_id TEXT PRIMARY KEY REFERENCES instances(instance_id),security_group_id TEXT NOT NULL,proxy_group_id TEXT NOT NULL,proxy_port INTEGER NOT NULL,rules TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1,applied_revision INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '');`

func NormalizeCIDR(s string) (string, error) {
	s = strings.TrimSpace(s)
	if a, e := netip.ParseAddr(s); e == nil {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()).String(), nil
	}
	p, e := netip.ParsePrefix(s)
	if e != nil || p.Addr().Is4In6() {
		return "", fmt.Errorf("invalid CIDR")
	}
	return p.Masked().String(), nil
}
func ParseEgressRules(raw string) ([]EgressRule, error) {
	out := []EgressRule{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p := strings.Fields(line)
		if len(p) != 3 {
			return nil, fmt.Errorf("CIDR tcp/udp port[-port]")
		}
		cidr, e := NormalizeCIDR(p[0])
		if e != nil {
			return nil, e
		}
		if p[1] != "tcp" && p[1] != "udp" {
			return nil, fmt.Errorf("protocol")
		}
		ports := strings.Split(p[2], "-")
		if len(ports) > 2 {
			return nil, fmt.Errorf("port")
		}
		a, e := strconv.Atoi(ports[0])
		if e != nil || a < 1 || a > 65535 {
			return nil, fmt.Errorf("port")
		}
		b := a
		if len(ports) == 2 {
			b, e = strconv.Atoi(ports[1])
			if e != nil || b < a || b > 65535 {
				return nil, fmt.Errorf("range")
			}
		}
		out = append(out, EgressRule{cidr, p[1], int32(a), int32(b)})
		if len(out) > 50 {
			return nil, fmt.Errorf("maximum 50 rules")
		}
	}
	return out, nil
}
func (p EgressPolicy) RuleText() string {
	out := []string{}
	for _, r := range p.Rules {
		port := fmt.Sprint(r.From)
		if r.To != r.From {
			port += fmt.Sprint("-", r.To)
		}
		out = append(out, r.CIDR+" "+r.Protocol+" "+port)
	}
	return strings.Join(out, "\n")
}
func (s *Store) SaveEgress(ctx context.Context, p EgressPolicy) error {
	if !strings.HasPrefix(p.SecurityGroupID, "sg-") || !strings.HasPrefix(p.ProxyGroupID, "sg-") || p.SecurityGroupID == p.ProxyGroupID || p.ProxyPort < 1 || p.ProxyPort > 65535 {
		return fmt.Errorf("SG / proxy port")
	}
	rules, e := ParseEgressRules(p.RuleText())
	if e != nil {
		return e
	}
	b, e := json.Marshal(rules)
	if e != nil {
		return e
	}
	_, e = s.DB.ExecContext(ctx, `INSERT INTO egress_policies(instance_id,security_group_id,proxy_group_id,proxy_port,rules) VALUES(?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET security_group_id=excluded.security_group_id,proxy_group_id=excluded.proxy_group_id,proxy_port=excluded.proxy_port,rules=excluded.rules,revision=revision+1,last_error=''`, p.InstanceID, p.SecurityGroupID, p.ProxyGroupID, p.ProxyPort, string(b))
	return e
}
func (s *Store) Egress(ctx context.Context, id string) (EgressPolicy, error) {
	p := EgressPolicy{InstanceID: id, ProxyPort: 3128}
	var raw string
	e := s.DB.QueryRowContext(ctx, `SELECT security_group_id,proxy_group_id,proxy_port,rules,revision,applied_revision,last_error FROM egress_policies WHERE instance_id=?`, id).Scan(&p.SecurityGroupID, &p.ProxyGroupID, &p.ProxyPort, &raw, &p.Revision, &p.AppliedRevision, &p.LastError)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &p.Rules)
	}
	return p, e
}
func (s *Store) RecordEgressApply(ctx context.Context, id string, revision int64, err error) error {
	if err != nil {
		_, e := s.DB.ExecContext(ctx, "UPDATE egress_policies SET last_error=? WHERE instance_id=?", err.Error(), id)
		return e
	}
	_, e := s.DB.ExecContext(ctx, "UPDATE egress_policies SET applied_revision=?,last_error='' WHERE instance_id=?", revision, id)
	return e
}
