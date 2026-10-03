package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSiteSettingsPersistenceAndValidation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "portal.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	defaults, err := s.SiteSettings(ctx)
	if err != nil || defaults != DefaultSiteSettings() {
		t.Fatal(defaults, err)
	}
	admin := User{Role: "portal_admin"}
	v := defaults
	v.BrandTitle = "  研究開発ポータル  "
	v.BrandSubtitle = "社内環境\n利用者向け"
	v.HomeMessage = "お知らせ\n本日の予定"
	v.PortalURL = "https://portal.company.example/"
	v.ProxyURL = "https://[2001:db8::1]:3128"
	if err = s.SaveSiteSettings(ctx, User{Role: "user"}, v); err == nil {
		t.Fatal("non-admin save")
	}
	if err = s.SaveSiteSettings(ctx, admin, v); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.SiteSettings(ctx)
	if err != nil || got.BrandTitle != "研究開発ポータル" || got.HomeMessage != v.HomeMessage || got.PortalURL != "https://portal.company.example" {
		t.Fatal(got, err)
	}
	for _, raw := range []string{"http://portal.example", "javascript:alert(1)", "https://token@portal.example", "https://portal.example/path", "https://portal.example?token=x", "https://portal.example#x", "https://portal.example:", "https://portal.example:70000", "https://portal.example:abc", "https://bad'host.example", "https://portal.example/%2f", "https://portal.example?"} {
		v := got
		v.ProxyURL = raw
		if err = s.SaveSiteSettings(ctx, admin, v); err == nil {
			t.Errorf("accepted unsafe URL: %q", raw)
		}
	}
	for _, mutate := range []func(*SiteSettings){func(v *SiteSettings) { v.BrandTitle = " " }, func(v *SiteSettings) { v.BrandTitle = strings.Repeat("名", 81) }, func(v *SiteSettings) { v.HomeMessage = "x\x00y" }, func(v *SiteSettings) { v.HomeTitle = "two\nlines" }, func(v *SiteSettings) { v.HelpMessage = strings.Repeat("文", 2001) }} {
		v := got
		mutate(&v)
		if err = s.SaveSiteSettings(ctx, admin, v); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
	after, err := s.SiteSettings(ctx)
	if err != nil || after != got {
		t.Fatal("invalid save mutated settings", after, err)
	}
}
