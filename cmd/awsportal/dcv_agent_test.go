package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeDCVEC2 struct {
	*fakeEC2
	address string
}

func (e *fakeDCVEC2) DCVAddress(_ context.Context, _ string, public bool) (string, error) {
	if !public || e.address == "" {
		return "", fmt.Errorf("no public address")
	}
	return e.address, nil
}

func TestDCVResolvesAddressAgainAfterEC2Restart(t *testing.T) {
	a, ec2 := newHandlerTestApp(t)
	ctx := context.Background()
	if e := a.db.CreateUser(ctx, "admin", "x", "portal_admin", ""); e != nil {
		t.Fatal(e)
	}
	admin, _ := a.db.UserByName(ctx, "admin")
	_, _ = a.db.DB.Exec(`INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-a','A','ec2-public')`)
	if e := a.db.ConfigureDCV(ctx, admin, "i-a", "ec2-public", "web", strings.Repeat("a", 64)); e != nil {
		t.Fatal(e)
	}
	_ = a.db.DCVHeartbeat(ctx, "i-a", []int64{admin.ID}, "", time.Now())
	resolver := &fakeDCVEC2{fakeEC2: ec2}
	a.ec2 = resolver
	for _, address := range []string{"203.0.113.1", "203.0.113.2", ""} {
		resolver.address = address
		r := requestAs(a, admin, "GET", "/dcv/i-a", nil)
		r.SetPathValue("id", "i-a")
		w := httptest.NewRecorder()
		a.require(a.dcv)(w, r)
		if address == "" {
			if w.Code != 503 {
				t.Fatal(w.Code)
			}
			continue
		}
		if w.Code != 302 || !strings.HasPrefix(w.Header().Get("Location"), "https://"+address+":8443/") {
			t.Fatal(w.Code, w.Header())
		}
	}
}

func TestDCVManagedBrowserConnectionAndMachineAuthentication(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	ctx := context.Background()
	if e := a.db.CreateUser(ctx, "alice", "x", "portal_admin", ""); e != nil {
		t.Fatal(e)
	}
	alice, _ := a.db.UserByName(ctx, "alice")
	_, _ = a.db.DB.Exec(`INSERT INTO instances(instance_id,name,dcv_host) VALUES('i-a','A','dcv.example')`)
	token := strings.Repeat("a", 64)
	if e := a.db.ConfigureDCV(ctx, alice, "i-a", "dcv.example", "web", token); e != nil {
		t.Fatal(e)
	}
	for _, credential := range []string{"", "Bearer wrong", "Bearer " + token} {
		r := httptest.NewRequest("GET", "/api/dcv/agent/state", nil)
		r.Header.Set("Authorization", credential)
		w := httptest.NewRecorder()
		a.dcvAgentState(w, r)
		want := 401
		if credential == "Bearer "+token {
			want = 200
		}
		if w.Code != want {
			t.Fatal(w.Code, want)
		}
	}
	body := `{"ready_users":[1],"error":""}`
	r := httptest.NewRequest("POST", "/api/dcv/agent/heartbeat", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.dcvAgentHeartbeat(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	r = requestAs(a, alice, "GET", "/dcv/i-a", nil)
	r.SetPathValue("id", "i-a")
	w = httptest.NewRecorder()
	a.require(a.dcv)(w, r)
	if w.Code != 302 || !strings.HasPrefix(w.Header().Get("Location"), "https://dcv.example:8443/") {
		t.Fatal(w.Code, w.Header())
	}
	destination, e := url.Parse(w.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	form := url.Values{"sessionId": {destination.Fragment}, "authenticationToken": {destination.Query().Get("authToken")}}.Encode()
	r = httptest.NewRequest("POST", "/api/dcv/agent/auth", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	a.dcvAgentAuth(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<username>awp-u1</username>") {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "/api/dcv/agent/auth", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	a.dcvAgentAuth(w, r)
	if w.Code != 401 {
		t.Fatal("replay allowed")
	}
	_ = a.db.DCVHeartbeat(ctx, "i-a", []int64{alice.ID}, "", time.Now().Add(-time.Hour))
	r = requestAs(a, alice, "GET", "/dcv/i-a", nil)
	r.SetPathValue("id", "i-a")
	w = httptest.NewRecorder()
	a.require(a.dcv)(w, r)
	if w.Code != 503 {
		t.Fatal("unready desktop did not report 503")
	}
	// State response never contains the machine secret or password hash.
	accounts, _ := a.db.DCVAccounts(ctx, "i-a", time.Now())
	raw, _ := json.Marshal(accounts)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "password") {
		t.Fatal("secret leaked")
	}
}
