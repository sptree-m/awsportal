# 利用者マニュアルとポータル表示設定

利用者・管理者は、ログイン後のメニュー「利用者マニュアル」（`/manual`）からプロキシとGitミラーの手順を確認できる。外部ドキュメントサービスへ接続する必要はなく、リリースバイナリに同梱する。

## 利用者マニュアル

- HTTPSプロキシの接続先、専用トークンの受領、curlによる接続確認、社内CA、他のツールへの設定、許可方式の説明。
- Git Mirrors画面でのID確認、ポータルからのクライアント取得、Pythonによる0600設定ファイルの作成、sync/clone/fetch、作業ツリー更新、自動タスクの例。
- プロキシとミラーのトークンを区別し、読み取りのみの場合と同期要求できる場合の手順を説明。
- 接続・認証・証明書・初回同期・失敗ジョブなどの対処方法。

マニュアルに秘密情報は保存しない。トークンの実際の発行と権限割り当ては、従来のProxy/Git Mirrors管理画面で行う。未設定の接続先は例示と明示し、管理者に確認した値への置換を案内する。

## 管理者マニュアル

Portal Adminには `/manual` の目次と本文に次の手順を表示する。一般UserとGroup Adminには、アンカー付きURLを指定しても管理者向け本文を返さない。

- `/manual#admin-provision-guide`：外部CLI/IaCでのEC2作成、起動時の種類選択と既存EC2変更の違い、Portal登録・DCV認証・割り当て、既存Shared自動作成の導入順序とスイッチ。
- `/manual#admin-golden-guide`：Golden AMIの構築・実機検証、アーキテクチャ、秘密情報の分離、Region/account配布とKMS、数値LT版、NEXT→STABLE、worker承認設定の一致、更新・復旧・保持費用。

`/admin/two-stage` のGolden AMI欄からも管理者マニュアルへ移動できる。詳しい構築手順は同じreleaseの[インスタンス管理](instances.md#ポータル外でのec2作成と種類選択)、[環境運用](environments.md#golden-amiの作成配布更新)、[組織ネットワーク](../deployment/organization-network.md)、[DCV](dcv.md)に集約する。マニュアル追加はAWSリソースの作成や未実装機能の有効化を行うものではない。

## ログイン前の証明書配布とhosts設定

ログイン画面のフォーム下に「事前準備：証明書・hosts設定」を表示する。設定済みなら公開CA証明書と端末準備ZIPをログイン前に取得できる。ZIPには `ca.crt`、接続先とCA SHA256を記録した `client-setup.json`、Windows用 `setup-windows.ps1`、macOS/Linux用 `setup-unix.py`、READMEを含む。秘密鍵やログイン資格情報は配布しない。未設定なら管理者への案内だけを表示する。

### 管理者の配布設定

1. HTTPSリバースプロキシとSAN付きサーバー証明書を[構築手順](../deployment/setup.md#ローカルhttpsと初回端末準備)に従って用意する。ここで配布するのはサーバー証明書を発行した公開CAで、サーバー秘密鍵は別管理。
2. 公開CAをPortalホストの `/etc/awsportal/client-preparation/ca.crt` に配置する。有効期間内のCA証明書1枚をPEMで保存する。秘密鍵・複数証明書・余分なPEMは起動時に拒否する。
3. `/etc/awsportal/client-preparation/hosts.json` に、利用端末から到達可能な接続先を1〜32件記録する。次のIP/名前は例示。実IP、サーバー証明書のSANと一致する実ホスト名へ置き換える。Portal、Proxy、必要ならDCVホストを含める。端末上でサーバーが稼働するときだけ127.0.0.1等を指定する。

   ```json
   {"hosts":[{"name":"portal.company.example","ip":"192.0.2.10"},{"name":"proxy.company.example","ip":"192.0.2.11"}]}
   ```

4. ファイルと親ディレクトリはroot所有・Portalサービスから読取可能とし、一般Userへ書込権限を与えない。systemdの `[Service]` に次を追加してdaemon-reload後にPortalを再起動する。片方だけの設定、無効なCA、不正な名前/IP・重複は起動エラーになる。

   ```ini
   Environment=AWSPORTAL_PREPARATION_CA_FILE=/etc/awsportal/client-preparation/ca.crt
   Environment=AWSPORTAL_PREPARATION_HOSTS_FILE=/etc/awsportal/client-preparation/hosts.json
   ```

5. 設定済み管理端末でログイン画面を開き、表示された名前/IPとCA SHA256を確認する。`/preparation/client-setup.zip` をダウンロードし、初回利用者には既存の信頼済み社内配布経路から渡す。CAフィンガープリントと承認IP/名前は別の管理経路でも通知する。未設定端末で証明書検証を無効にして最初のCAを取得させない。

配布内容は起動時に読込み、更新時はPortalを再起動する。URLは匿名閲覧可能なため、CAと接続先は利用者へ公開できる内容だけを置く。配布応答は `no-store`。両変数未設定ではダウンロードは404になる。表示設定のPortalURL/ProxyURLは説明用であり、hosts配布設定やサーバーTLSを自動変更しない。

### 利用端末の実行と復旧

ZIPを同じフォルダーへ展開し、管理者から別途受領したSHA256を指定する。既定はプレビューだけで、CA・hostsの変更は行わない。

```powershell
# Windows PowerShell 5.1以降。管理者のスクリプト署名・実行ポリシーに従う
.\setup-windows.ps1 -ExpectedFingerprint 管理者から受領したSHA256
# 管理者として開いたPowerShellで適用
.\setup-windows.ps1 -ExpectedFingerprint 管理者から受領したSHA256 -Apply
```

```sh
# macOS / Linux。Python 3とopensslが必要
python3 setup-unix.py --expected-fingerprint 管理者から受領したSHA256
sudo python3 setup-unix.py --expected-fingerprint 管理者から受領したSHA256 --apply
```

WindowsはLocalMachine Root、macOSはSystem keychain、LinuxはUbuntu/DebianまたはRHEL/FedoraのシステムCAストアへ登録する。登録するCAのDER SHA256を実行引数・設定ファイルと照合し、有効期間とCA属性を確認する。hostsは指定した名前だけを更新し、別名・他のエントリーを保持する。適用前に日時付きhostsバックアップを作る。Windows/macOSはDNSキャッシュも更新する。実行時のネットワークダウンロードは行わない。

適用後にブラウザーを再起動してSANと一致するHTTPSホスト名へ接続する。Firefoxの設定、Java、コンテナなど独立CAストアを使うアプリは別途登録が必要な場合がある。hostsは名前解決だけで、通信経路、SG、Firewall、TLSサーバーを設定しない。

復旧は出力されたhostsバックアップを確認して必要な対応だけを戻し、CAはSHA256で特定して信頼ストアから削除する。Linuxは `awsportal-<SHA256先頭16桁>.crt` を削除して `update-ca-certificates` または `update-ca-trust extract` を再実行する。CA更新は旧CAを自動削除しない。管理者が移行終了を確認して旧CAを削除する。CA登録後にhosts適用が失敗する等の部分失敗では、変更状況とバックアップを確認する。

## 表示設定

Portal Adminのみ「表示設定」（`/admin/site`）で変更できる。一般ユーザーには編集メニューを表示せず、GET/POSTも403で拒否する。匿名アクセスはログインへ移動する。

| 項目 | 表示先 | 最大文字数 |
|---|---|---|
| 左上のタイトル | 全画面のサイト名・ブラウザタイトル | 80、必須 |
| タイトル下の説明 | サイト名の下、改行可 | 160 |
| トップページの見出し | ホームの見出し | 80、必須 |
| トップページのメッセージ | ホームのお知らせ、改行可 | 4000 |
| ログイン画面の案内 | ログイン前にも表示、改行可 | 1000 |
| 問い合わせ・補足メッセージ | 利用者マニュアルの先頭、改行可 | 2000 |
| ポータル・プロキシのHTTPS URL | マニュアルのコマンド例 | 各200 |

表示設定はSQLiteの`site_settings`へ保存し、次回画面表示から適用する。既存DBへの移行はテーブル追加だけで、ユーザー・権限・インスタンス等を変更しない。未保存なら従来のサイト名を使う。DB再オープン・サービス再起動後も設定を維持する。DBのバックアップに含めて管理する。

トップページのメッセージは空欄なら非表示。説明・問い合わせ案内も空欄にできる。HTMLやJavaScriptは実行せず、入力した文字をHTMLエスケープして表示する。画像・テーマ・任意HTMLの編集機能は対象外。

接続先URLはHTTPSのホストと任意のポートに限定する。認証情報・パス・クエリ・フラグメントは拒否する。接続先の登録は**利用者向け手順の表示設定**であり、TLS証明書、サーバー待受、DNS、ネットワーク許可は変更しない。実運用では利用者EC2から到達できる接続先を指定する。

保存時に操作を監査ログへ記録する。ログイン画面へ表示する文言は公開範囲を考慮して設定する。

## 検証

- 管理者のみ保存できること、一般ユーザーと匿名の拒否、保存後のDB再オープン、既存DBの再移行。
- 文字数・必須項目・制御文字・URL検証、不正保存後も前の値を維持すること。
- HTMLエスケープ、ログイン/利用者画面/部分更新への反映、監査ログ。
- 実Chromiumで利用者マニュアル閲覧、管理画面の保存と全利用者への反映、htmxナビゲーション、1366/FHD/2K/4K/390pxでの表示。

AWSリソースを追加・変更する機能ではない。プロキシ/Gitミラーの実通信は従来の機能とインフラ準備に依存する。利用者EC2と社内ネットワークの実機疎通は別途確認する。
