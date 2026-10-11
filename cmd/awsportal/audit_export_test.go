package main

import (
	"context"
	"github.com/sptree-m/awsportal/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLogoutAuditOnlyForValidSession(t *testing.T) {
	a, _ := newHandlerTestApp(t)
	r := requestAs(a, store.User{Username: "alice"}, http.MethodPost, "/logout", nil)
	a.logout(httptest.NewRecorder(), r)
	// Replaying the removed session must not invent another logout event.
	a.logout(httptest.NewRecorder(), r)
	a.sessions["expired"] = session{User: store.User{Username: "bob"}, Expires: time.Now().Add(-time.Hour)}
	r = httptest.NewRequest(http.MethodPost, "/logout", nil)
	r.AddCookie(&http.Cookie{Name: "awsportal_session", Value: "expired"})
	a.logout(httptest.NewRecorder(), r)
	events, err := a.db.AuditEntries(context.Background(), 200)
	if err != nil || len(events) != 1 || events[0].Action != "logout" || events[0].Actor != "alice" {
		t.Fatalf("unexpected logout audit: %+v %v", events, err)
	}
}
