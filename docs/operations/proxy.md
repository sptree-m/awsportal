# 明示型HTTP/HTTPSプロキシ

Portal Adminの「Proxy」画面から管理する。既定はリスナー未起動・すべて拒否。既存DBへテーブルを追加する自動移行（既存データは保持）。先にDBのバックアップを取る。

## 許可設定

- ドメインまたはIP/CIDRを選んでルールを作成・編集・無効化・削除。名前、全員/ユーザー/グループ、ドメイン、ポート一覧、メソッド一覧、許可/拒否を指定。
- 未登録は拒否。同じ通信に許可と拒否が一致した場合は拒否を優先。管理者ユーザーにも例外なし。
- ドメインはASCII完全一致か `*.example.com`。後者はサブドメインだけで頂点example.comを含まない。IDNはpunycodeで登録。
- HTTPはGET/HEAD/POST/PUT/PATCH/DELETE/OPTIONSを制御。HTTPSはCONNECTのホストとポートのみ制御。HTTPS内部のメソッド・パス・SNI・通信内容は検査しない。CONNECT許可は指定先へのTCPトンネル許可であり、TLSやHTTPに限定する検証はしない。
- HTTPのURLパス指定・HTTPS復号・透過プロキシ・キャッシュは未対応。HTTP Upgradeは拒否。
- ユーザーの無効化・期限・初回パスワード変更未完了を毎リクエストで検査。グループ所属も現在のDBを参照。

## 認証

管理画面でユーザーと用途名を選び、有効期間1〜90日の専用トークンを発行。表示は発行時の1回のみ。SQLiteにはSHA-256ハッシュだけを保存。ユーザー名とトークンをProxy Basic認証で使用。Portalのパスワード/TOTPをツールに保存する必要はない。任意のトークンを画面で失効できる。

トークンは機械向けのBearer相当資格情報。漏洩時は直ちに失効する。認証成功をPortalログインと見なさないため、Portalの30日未ログイン失効は引き続き適用される。

## 起動

同じawsportalプロセスの別ポートで動作。最初はEC2にSSH/SSM接続したシェルからローカルテストする。

```sh
# awsportalのsystemd環境設定に追加してサービスを再起動
AWSPORTAL_PROXY_ADDR=127.0.0.1:3128
```

管理画面で `example.com / 443 / CONNECT / 許可 / 有効` を追加し、通信を「ルールに従って許可」に変更。その後プロキシの動くEC2で実行：

```sh
read -r -p 'Proxy user: ' PROXY_USER
read -r -s -p 'Proxy token: ' PROXY_TOKEN; echo
# パスワードを引数/履歴へ直接出さず、標準入力経由の設定で渡す
printf 'proxy-user = "%s:%s"\n' "$PROXY_USER" "$PROXY_TOKEN" |
  curl --config - --proxy http://127.0.0.1:3128 --max-time 20 https://example.com/
# 未許可はCONNECT 403になる
printf 'proxy-user = "%s:%s"\n' "$PROXY_USER" "$PROXY_TOKEN" |
  curl --config - --proxy http://127.0.0.1:3128 --max-time 20 https://example.net/
unset PROXY_USER PROXY_TOKEN
```

別EC2から使う場合は、リスナーの非ループバックアドレス、`AWSPORTAL_PROXY_TLS_CERT`、`AWSPORTAL_PROXY_TLS_KEY` を設定する。非ループバックで証明書なしは起動を拒否。TLS1.2以上、HTTP/1.1。証明書はクライアントの信頼ストアに登録し、検証を無効化しない。HTTPSプロキシ対応クライアントを使用し、SGのインバウンドは利用EC2のSGだけに限定。3128をインターネットへ公開しない。証明書はHTTPSプロキシへの接続用であり、接続先HTTPSを復号するCAではない。

## 接続先の保護と制限

IP直指定はIP/CIDRルールで制御。DNS応答の全IPを検査し、内部・ループバック・リンクローカル・CGNAT・予約範囲・IPv6変換/トンネル範囲を拒否。検査したIPに直接接続し、DNS再解決で内部IPへ変える攻撃を防ぐ。AWSメタデータ、VPC内サービスへの転送も拒否する。内部向けVPC Endpointを使うドメインはこの実装では接続できない。

同時64通信、HTTP送信ボディ32MiB、HTTP通信最長2分、CONNECT最長5分。長いダウンロード、Git操作等はタイムアウトする可能性がある。ルール変更・全拒否・トークン失効は新規リクエストから適用し、既存CONNECTは最長5分継続。HTTP通信は最長2分継続。即時停止が必要な場合はプロセス再起動またはネットワークで遮断する。

プロキシには送信元URL・トークン・ペイロードを記録せず、ユーザー、ホスト:ポート、メソッド、許可/拒否を監査ログへ記録。認証前の失敗はDBログを増やさない。

**プロキシを設定するだけでは、直接通信の迂回を防げない。** 利用EC2のHTTP_PROXY/HTTPS_PROXY設定とともに、SG・ルート・ファイアウォール等で利用EC2の直接外向き通信を制限する。新しいOutbound画面による専用SGへの適用と初回インフラ準備はdocs/operations/egress.mdを参照。従来SGの443許可は自動変更しない。リソース追加は不要で同一EC2に同居可能。ただしプロキシ利用分の転送量などはAWS課金対象となり得る。

テスト終了時：管理画面で「すべて拒否」、テスト用トークン失効、AWSPORTAL_PROXY_ADDR設定を解除してサービス再起動。Disposable Lab全体を作った場合は `bash lab/destroy.sh` とCloudFormation削除完了待機を行う。プロキシ停止だけではEC2/EBSの課金は止まらない。
