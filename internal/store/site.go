package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const siteSchema = `CREATE TABLE IF NOT EXISTS site_settings(id INTEGER PRIMARY KEY CHECK(id=1),settings TEXT NOT NULL);`

type SiteSettings struct {
	BrandTitle    string
	BrandSubtitle string
	HomeTitle     string
	HomeMessage   string
	LoginMessage  string
	HelpMessage   string
	PortalURL     string
	ProxyURL      string
}

func DefaultSiteSettings() SiteSettings {
	return SiteSettings{BrandTitle: "Infrastructure", BrandSubtitle: "Portal", HomeTitle: "ダッシュボード", HomeMessage: "利用できるEC2の状態を確認し、起動・停止・接続を行えます。設定方法は利用者マニュアルをご覧ください。", LoginMessage: "社内インフラストラクチャ管理ポータル", HelpMessage: "接続先、社内CA証明書、専用トークンは管理者に確認してください。"}
}
func (s *Store) SiteSettings(ctx context.Context) (SiteSettings, error) {
	settings := DefaultSiteSettings()
	var raw string
	err := s.DB.QueryRowContext(ctx, "SELECT settings FROM site_settings WHERE id=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	err = json.Unmarshal([]byte(raw), &settings)
	return settings, err
}

var siteHostname = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)

func validateSiteURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return false
	}
	host := u.Hostname()
	if net.ParseIP(host) == nil && (!siteHostname.MatchString(host) || strings.Contains(host, "..") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".")) {
		return false
	}
	if strings.HasSuffix(u.Host, ":") {
		return false
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}
func (s *Store) SaveSiteSettings(ctx context.Context, admin User, v SiteSettings) error {
	if admin.Role != "portal_admin" {
		return errors.New("admin required")
	}
	fields := []struct {
		v         *string
		max       int
		required  bool
		multiline bool
	}{{&v.BrandTitle, 80, true, false}, {&v.BrandSubtitle, 160, false, true}, {&v.HomeTitle, 80, true, false}, {&v.HomeMessage, 4000, false, true}, {&v.LoginMessage, 1000, false, true}, {&v.HelpMessage, 2000, false, true}, {&v.PortalURL, 200, false, false}, {&v.ProxyURL, 200, false, false}}
	for _, f := range fields {
		*f.v = strings.TrimSpace(strings.ReplaceAll(*f.v, "\r\n", "\n"))
		if !utf8.ValidString(*f.v) || utf8.RuneCountInString(*f.v) > f.max || (f.required && *f.v == "") {
			return errors.New("invalid site text")
		}
		for _, r := range *f.v {
			if unicode.IsControl(r) && !(f.multiline && (r == '\n' || r == '\t')) {
				return errors.New("invalid site text")
			}
		}
	}
	if !validateSiteURL(v.PortalURL) || !validateSiteURL(v.ProxyURL) {
		return errors.New("HTTPS origin required; credentials and paths forbidden")
	}
	v.PortalURL = strings.TrimSuffix(v.PortalURL, "/")
	v.ProxyURL = strings.TrimSuffix(v.ProxyURL, "/")
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO site_settings(id,settings) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET settings=excluded.settings`, string(raw))
	return err
}
