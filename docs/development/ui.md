# UI・htmx仕様と検証

[最優先のコンセプト](../../README.md#最優先のコンセプト)に従い、プロ仕様UIと超軽量・高速・高いメンテナンス性を両立させます。以下の仕様と検証を機能追加・画面改修でも維持します。

## 表示

- 2026-10-01に承認されたOperations Consoleの外観を維持する。濃紺の常設sidebar、白いworkspace、密な等幅文字、控えめな青い主要操作、緑／赤／橙の状態表示、compact tableと細い罫線を使用する。参照PNGはrelease/design資産として保持する。
- Rounded M+ NM Regular／Boldをローカル配信する。ID・IP・時刻・ログの桁位置を揃える。WOFF2は全glyphを保持し、asset query versionでcacheを更新する。
- 1366×768で主要操作を表示し、1920×1080、2560×1440、3840×2160では情報量を増やす。狭幅でも操作を失わず、表だけを横scrollさせる。
- focus、hover、色と文字による状態表示、reduced-motionに対応する。見栄えだけの画像・animation・巨大な依存を追加しない。

## 通信・画面遷移

- Go SSRと同梱htmxを使う。React／Vue／Bootstrap／Tailwind／jQuery／Chart.js、外部CDN・font・icon等の実行時取得は禁止。CSS／SVG／Canvasでcompact dataを表示し、static assetをcacheする。
- 認証後の画面はsidebar・active item・account・logoutを共通化する。boosted navigationと一覧・cost・detailの更新は対象HTML領域だけを返し、直接URLと履歴復元は全文書を返す。
- 検索はローカル処理。遷移・row swap後も操作listenerを維持し、更新時は検索／状態条件・表示件数・空状態を保持する。detailは権限のある1台だけを取得し、DCVはrunning時だけ有効。
- EC2遷移中だけ1.5秒ごとにpollする。安定時・非表示tabでは停止し、表示復帰で再開する。失敗時は通知して停止し、再試行はnavigate／refreshで行う。
- 実行中はbuttonを無効化し、同時操作を同期する。HTTP／通信／timeoutエラーは読み上げ可能なplain textで表示する。session期限切れはpartial requestでもwindow全体をloginへ戻す。
- dynamic responseはno-store。htmx eval、injected script、history snapshot、inline indicator styleを無効化しCSPを維持する。ログは件数を限定した差分取得とし、表示のために全ファイルを取得しない。APIも必要なfieldだけを返す。

## 各UI変更での必須確認

参照mockupとの整合、sidebar・table密度・文字・状態表示・alignment、4解像度のoverflow、外部resource禁止、初回／idle転送量、poll終了・失敗通知・繰り返し遷移を確認する。機能テストだけで視覚・通信量の退行を容認しない。

```bash
go test -race ./...
bash tests/security.sh
# playwright@1.51.1とChromiumを準備して実行
AWSPORTAL_BROWSER_TEST=1 go test -race ./cmd/awsportal -run TestBrowserConsole -v
```

ブラウザ試験は実template・header・static asset・htmxをhttptest serverで検証し、CIにscreen captureを保存する。EC2／Cost ExplorerはmockでAWSを変更しない。軽量化は転送量・全文書reload回数で評価する。実EC2／DCV／MFA・AWS通信遅延の受入は対象環境で別途行う。
