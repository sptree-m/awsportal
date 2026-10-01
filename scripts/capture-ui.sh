#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/docs/screenshots"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$OUT"
CSS="$(cat "$ROOT/cmd/awsportal/web/app.css")"
render(){
  local src="$1" out="$2" size="$3"
  { printf '<!doctype html><html lang="ja"><head><meta charset="utf-8"><style>%s</style></head>' "$CSS"; cat "$src"; printf '</html>'; } > "$TMP/page.html"
  google-chrome --headless --no-sandbox --disable-gpu --hide-scrollbars --window-size="$size" --screenshot="$out" "file://$TMP/page.html"
}
cat >"$TMP/dashboard" <<'HTML'
<body><aside><div class="brand"><span class="brand-mark">IP</span><div><strong>Infrastructure</strong><small>Portal</small></div></div><nav><span class="nav-active">Dashboard</span><span>EC2 Instances</span><span>Schedule</span><a>Security / MFA</a><span>Users / Groups</span><span>Audit</span></nav><form class="logout"><button>ログアウト</button></form></aside><main><header class="page-head"><div><p class="eyebrow">INFRASTRUCTURE</p><h1>EC2管理</h1><p class="muted">割り当てられたデスクトップ環境を起動・停止・接続できます。</p></div><div class="account"><strong>admin01</strong><small>portal_admin</small></div></header><section class="panel table-panel"><div class="section-head"><div><h2>インスタンス</h2><p class="muted">DCV接続を主操作として表示しています。</p></div><span class="count">3 instances</span></div><div class="table-wrap"><table><thead><tr><th>名前</th><th>Instance ID</th><th>接続</th><th>電源操作</th></tr></thead><tbody><tr><td><strong>ADAS Development 01</strong></td><td><code>i-0123456789abcdef0</code></td><td><a class="btn primary">DCV 接続</a></td><td><div class="actions"><button>起動</button><button>停止</button></div></td></tr><tr><td><strong>CV Training 02</strong></td><td><code>i-0fedcba9876543210</code></td><td><a class="btn primary">DCV 接続</a></td><td><div class="actions"><button>起動</button><button>停止</button></div></td></tr></tbody></table></div></section><section class="panel"><div class="section-head"><div><h2>スケジュール</h2><p class="muted">平日の自動起動・停止などを設定します。</p></div></div><form class="grid"><input value="i-0123456789abcdef0"><select><option>起動</option></select><input type="time" value="08:00"><input value="1,2,3,4,5"><button class="primary">登録</button></form></section></main></body>
HTML
cat >"$TMP/login" <<'HTML'
<body class="login"><main class="card auth-card"><div class="brand auth-brand"><span class="brand-mark">IP</span><div><strong>Infrastructure</strong><small>Portal</small></div></div><p class="eyebrow">SECURE ACCESS</p><h1>ログイン</h1><p class="muted">社内インフラストラクチャ管理ポータル</p><form><label>ユーザー名<input value="admin01"></label><label>パスワード<input type="password" value="password123"></label><label>認証コード <small class="muted">MFA設定時</small><input placeholder="000000"></label><button class="primary full">ログイン</button></form></main></body>
HTML
render "$TMP/dashboard" "$OUT/dashboard-1366x768.png" "1366,768"
render "$TMP/dashboard" "$OUT/dashboard-1920x1080.png" "1920,1080"
render "$TMP/dashboard" "$OUT/dashboard-2560x1440.png" "2560,1440"
render "$TMP/dashboard" "$OUT/dashboard-3840x2160.png" "3840,2160"
render "$TMP/login" "$OUT/login-1366x768.png" "1366,768"
