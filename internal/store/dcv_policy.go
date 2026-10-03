package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

type DCVFeature struct {
	Code, Label     string
	Allowed, Locked bool
}
type DCVPolicy struct {
	Revision int64    `json:"revision"`
	Allowed  []string `json:"allowed"`
}

// Explicit catalog: unknown features and aliases such as builtin are never accepted.
func DCVFeatures() []DCVFeature {
	return []DCVFeature{
		{Code: "display", Label: "画面表示"}, {Code: "keyboard", Label: "キーボード入力"},
		{Code: "mouse", Label: "マウス入力"}, {Code: "pointer", Label: "ポインター表示"},
		{Code: "audio-out", Label: "音声出力（EC2 → 端末）"}, {Code: "audio-in", Label: "マイク入力（端末 → EC2）"},
		{Code: "clipboard-copy", Label: "クリップボード持ち出し（EC2 → 端末）"},
		{Code: "clipboard-paste", Label: "クリップボード貼り付け（端末 → EC2）"},
		{Code: "file-download", Label: "ファイルダウンロード（EC2 → 端末）"},
		{Code: "file-upload", Label: "ファイルアップロード（端末 → EC2）"},
		{Code: "screenshot", Label: "スクリーンキャプチャ"}, {Code: "printer", Label: "印刷・PDF持ち出し"},
		{Code: "usb", Label: "USB転送"}, {Code: "smartcard", Label: "スマートカード"},
		{Code: "webcam", Label: "カメラ入力"}, {Code: "gamepad", Label: "ゲームパッド"},
		{Code: "stylus", Label: "スタイラス"}, {Code: "touch", Label: "タッチ入力"},
		{Code: "keyboard-sas", Label: "Ctrl+Alt+Del（対応サーバーのみ）"},
		{Code: "webauthn-redirection", Label: "WebAuthn認証転送"},
		{Code: "extensions-client", Label: "クライアント拡張機能（別途導入が必要）"},
		{Code: "extensions-server", Label: "サーバー拡張機能（別途導入が必要）"},
		{Code: "unsupervised-access", Label: "所有者不在の共同利用（本構成では固定禁止）", Locked: true},
	}
}
func DefaultDCVPolicy() DCVPolicy {
	return DCVPolicy{Revision: 1, Allowed: []string{"display", "keyboard", "mouse", "pointer", "audio-out"}}
}
func decodeDCVPolicy(raw string, revision int64) (DCVPolicy, error) {
	p := DefaultDCVPolicy()
	p.Revision = revision
	if raw != "" {
		if e := json.Unmarshal([]byte(raw), &p.Allowed); e != nil {
			return p, e
		}
	}
	return p, nil
}
func (s *Store) DCVPolicy(ctx context.Context, id string) (DCVPolicy, error) {
	var raw string
	var revision int64
	e := s.DB.QueryRowContext(ctx, "SELECT dcv_policy_json,dcv_policy_revision FROM instances WHERE instance_id=?", id).Scan(&raw, &revision)
	if e != nil {
		return DCVPolicy{}, e
	}
	return decodeDCVPolicy(raw, revision)
}
func (s *Store) SetDCVPolicy(ctx context.Context, admin User, id string, allowed []string) error {
	if e := adminOnly(admin); e != nil {
		return e
	}
	catalog := map[string]bool{}
	for _, f := range DCVFeatures() {
		if !f.Locked {
			catalog[f.Code] = true
		}
	}
	chosen := map[string]bool{}
	for _, code := range allowed {
		if !catalog[code] || chosen[code] {
			return fmt.Errorf("不正なDCV機能です")
		}
		chosen[code] = true
	}
	if !chosen["screenshot"] && chosen["clipboard-copy"] {
		return fmt.Errorf("キャプチャ禁止時はクリップボード持ち出しも禁止してください")
	}
	if chosen["keyboard-sas"] && !chosen["keyboard"] {
		return fmt.Errorf("Ctrl+Alt+Delにはキーボード入力の許可が必要です")
	}
	values := make([]string, 0, len(chosen))
	for code := range chosen {
		values = append(values, code)
	}
	sort.Strings(values)
	raw, _ := json.Marshal(values)
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	result, e := tx.ExecContext(ctx, "UPDATE instances SET dcv_policy_json=?,dcv_policy_revision=dcv_policy_revision+1 WHERE instance_id=?", string(raw), id)
	if e != nil {
		return e
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("対象インスタンスがありません")
	}
	_, e = tx.ExecContext(ctx, "DELETE FROM dcv_tokens WHERE instance_id=(SELECT id FROM instances WHERE instance_id=?)", id)
	if e != nil {
		return e
	}
	return tx.Commit()
}
