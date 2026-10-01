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
<body><aside><div class="brand"><span class="brand-mark">▤</span><div><strong>Infrastructure</strong><small>Portal</small></div></div><nav><span class="nav-active">⌂ Dashboard</span><span>▤ EC2 Instances</span><span>♙ Users</span><span>↔ Proxy</span><span>▧ Audit</span><span>¥ Cost</span><span>⚿ MFA</span></nav></aside><main class="workspace"><header class="topbar"><div><p class="eyebrow">OPERATIONS CONSOLE</p><h1>ダッシュボード</h1></div><div class="account"><strong>admin01</strong><small>portal_admin</small></div></header><section class="metric-grid ops-metrics"><div class="metric"><small>EC2 Instances</small><strong>3</strong><span>Running 2 / Stopped 1</span></div><div class="metric"><small>Running</small><strong>2</strong><span class="ok">● AWS current state</span></div><div class="metric"><small>Transition</small><strong>0</strong><span>Starting / Stopping</span></div><div class="metric"><small>Registered Users</small><strong>12</strong><span>Portal accounts</span></div></section><section class="dashboard-grid"><div class="panel table-panel"><div class="section-head"><div><h2>インスタンス</h2><p class="muted">DCV接続を主操作として表示しています。</p></div><span class="count">3 instances</span></div><div class="table-wrap"><table><thead><tr><th>名前</th><th>Instance ID</th><th>接続</th><th>電源操作</th></tr></thead><tbody><tr><td><strong>ADAS Development 01</strong></td><td><code>i-0123456789abcdef0</code></td><td><a class="btn primary">DCV 接続</a></td><td><div class="actions"><button>起動</button><button>停止</button></div></td></tr><tr><td><strong>CV Training 02</strong></td><td><code>i-0fedcba9876543210</code></td><td><a class="btn primary">DCV 接続</a></td><td><div class="actions"><button>起動</button><button>停止</button></div></td></tr></tbody></table></div></section><div class="panel table-panel"><div class="section-head"><div><h2>Recent Audit</h2><p class="muted">最新の操作</p></div></div><div class="table-wrap"><table><thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Result</th></tr></thead><tbody><tr><td>20:41:03</td><td>admin01</td><td>instance.start</td><td><span class="result-ok">OK</span></td></tr><tr><td>20:35:11</td><td>admin01</td><td>instance.stop</td><td><span class="result-ok">OK</span></td></tr></tbody></table></div></div></section></main></body>
HTML
cat >"$TMP/login" <<'HTML'
<body class="login"><main class="card auth-card"><div class="brand auth-brand"><span class="brand-mark">IP</span><div><strong>Infrastructure</strong><small>Portal</small></div></div><p class="eyebrow">SECURE ACCESS</p><h1>ログイン</h1><p class="muted">社内インフラストラクチャ管理ポータル</p><form><label>ユーザー名<input value="admin01"></label><label>パスワード<input type="password" value="password123"></label><label>認証コード <small class="muted">MFA設定時</small><input placeholder="000000"></label><button class="primary full">ログイン</button></form></main></body>
HTML
render "$TMP/dashboard" "$OUT/dashboard-1366x768.png" "1366,768"
render "$TMP/dashboard" "$OUT/dashboard-1920x1080.png" "1920,1080"
render "$TMP/dashboard" "$OUT/dashboard-2560x1440.png" "2560,1440"
render "$TMP/dashboard" "$OUT/dashboard-3840x2160.png" "3840,2160"
render "$TMP/login" "$OUT/login-1366x768.png" "1366,768"
