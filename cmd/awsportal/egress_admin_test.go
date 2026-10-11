package main

import (
	"context"
	"fmt"
	"github.com/sptree-m/awsportal/internal/store"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type fakeEgressAdmin struct {
	applied int
	fail    bool
}

func (f *fakeEgressAdmin) ApplyEgress(context.Context, store.EgressPolicy) error {
	f.applied++
	if f.fail {
		return fmt.Errorf("test failure")
	}
	return nil
}
func (f *fakeEgressAdmin) InspectEgress(context.Context, store.EgressPolicy) (string, error) {
	return "AWS設定一致", nil
}
func TestEgressAdminAuthorizationRevisionAndFailure(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	f := &fakeEgressAdmin{}
	a.egress = f
	ctx := context.Background()
	a.db.CreateUser(ctx, "admin", "x", "portal_admin", "")
	a.db.CreateUser(ctx, "alice", "x", "user", "")
	admin, _ := a.db.UserByName(ctx, "admin")
	alice, _ := a.db.UserByName(ctx, "alice")
	a.db.DB.Exec(`INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-one','one','host')`)
	post := func(u store.User, v url.Values) *httptest.ResponseRecorder {
		r := requestAs(a, u, "POST", "/admin/egress", strings.NewReader(v.Encode()))
		r.Header.Set("HX-Request", "true")
		r.Header.Set("HX-Target", "egress-live")
		w := httptest.NewRecorder()
		a.require(a.egressChange)(w, r)
		return w
	}
	v := url.Values{"operation": {"save"}, "instance_id": {"i-one"}, "security_group_id": {"sg-one"}, "proxy_group_id": {"sg-proxy"}, "proxy_port": {"3128"}, "rules": {"10.0.0.0/8 tcp 443"}}
	if w := post(alice, v); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := post(admin, v); w.Code != 200 || !strings.Contains(w.Body.String(), "未適用") {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.applied != 0 {
		t.Fatal("save applied")
	}
	v.Set("operation", "apply")
	v.Set("revision", "0")
	if w := post(admin, v); w.Code != 409 {
		t.Fatal(w.Code)
	}
	v.Set("revision", "1")
	f.fail = true
	post(admin, v)
	p, _ := a.db.Egress(ctx, "i-one")
	if p.AppliedRevision != 0 || p.LastError == "" {
		t.Fatal("false success")
	}
	f.fail = false
	post(admin, v)
	p, _ = a.db.Egress(ctx, "i-one")
	if p.AppliedRevision != 1 || p.LastError != "" {
		t.Fatal("not applied")
	}
	v.Set("instance_id", "i-unknown")
	if w := post(admin, v); w.Code != 400 {
		t.Fatal("unknown EC2")
	}
}
