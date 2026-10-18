# v2.1.3 クイック導入ガイド

新規のPortal EC2へARM64配布物を配置し、管理者ログインと最初のDCV接続までを確認します。以下はUbuntu 24.04／ARM64、Go単体のHTTPS待受を使う例です。配布バイナリを使うためGoのインストールは不要です。既存環境の更新は[リリースノート](../releases/v2.1.3.md#更新受入)、構成の詳細は[構築・初期設定](setup.md)を参照してください。

| 目的 | 進め方 |
|---|---|
| まず一時環境で試す | [CloudShellラボの作成・試験・廃棄](../development/dcv-release-test.md)。試験後はスタックを削除する |
| 社内向けに新規導入する | 以下の手順。承認済みのEC2・ネットワーク・証明書を用意する |
| 制約確認から運用開始まで進める／固定EC2を公開IPv4で使う | [本番立ち上げマニュアル](production/README.md)。既存参照と変更権限を確認し、選んだネットワーク・TLS構成の手順を使う |
| github.comへ接続できない | 手順2で配布物とチェックサムを搬入する。ラボを使う場合は[ソース一式も搬入](../development/dcv-release-test.md#githubcomへ接続できない場合)する |

## 1. EC2・名前・証明書を用意する

管理者が以下を準備します。EC2等の作成は[組織ネットワークとTerraform設定](organization-network.md)に従い、planを確認してから適用してください。

以下は組織Private構成・Go単体HTTPSの例です。本番マニュアルの公開IPv4・nginx構成では、ネットワークとサービス設定をそちらに統一し、以下のPrivate SubnetやGo HTTPS用unitを混在させないでください。配布物の検証、専用ユーザー、DB・元のTOTP鍵の管理、認証仕様は共通です。

- Private Subnet内のPortal用新規ARM64 Linux EC2。t4g.microを基準とし、容量・swapは[構築手順](setup.md#準備と配布)を確認します。専用の非特権ユーザーで稼働させ、IAM Instance Profileを付与します。固定Access Keyは保存しません。
- 管理対象のUbuntu 24.04 EC2、DCVライセンス用S3の読取権限と到達経路。[DCV導入手順](../operations/dcv.md#導入手順)を参照してください。
- 固定名`portal.company.example`のDNSまたはhosts設定、その名前をSANに含むサーバー証明書・秘密鍵、端末が信頼する公開CA。例示の名前・リージョン・パスは実環境へ置き換えます。証明書の準備は[HTTPSと端末準備](setup.md#ローカルhttpsと初回端末準備)を参照してください。
- 管理者用の導入経路、Bash・sudo・curl・tar・sha256sum、試験PCのTOTP認証器・同期しない端末固定型パスキー・[DCVネイティブクライアント](https://www.amazondcv.com/)。一般利用者にはAWS IAM User・Access Key・SSM権限を付与しません。

通信許可を次の構成に合わせます。SSH／RDPを利用者向けに開放しません。

| 接続元 → 接続先 | 必要な通信 |
|---|---|
| 社内／VPNの利用端末 → Portal | HTTPS 443/TCP |
| 利用端末 → 管理対象DCV EC2 | DCV 8443/TCP（変更する場合はPortal・Agent・SGを統一） |
| DCV Agent → Portal | 検証済みHTTPSのAgent API経路。Agent用経路は管理対象EC2からだけ到達可能にする |
| Portal／DCV EC2 → 導入・運用先 | 承認済みの配布先、OSパッケージ、AWS API、DCV配布先・ライセンス用S3 |

IAMの管理対象ARNと通信先は[組織ネットワーク](organization-network.md)で限定します。Agent APIの経路ごとの接続元制限は、組織の承認済みL7制御等で設定します。nginx等でHTTPSを終端する構成は[別の設定例](setup.md#httpsリバースプロキシhttpバックエンド任意)を使ってください。

## 2. v2.1.3の配布物を取得する

Portal EC2の管理者用Bashで実行します。`$HOME/awsportal-v213-package`は新しい作業ディレクトリとして作成します。

```bash
set -euo pipefail
umask 077
mkdir -m 700 "$HOME/awsportal-v213-package"
cd "$HOME/awsportal-v213-package"
ASSET=awsportal-v2.1.3-linux-arm64.tar.gz
```

GitHubへ接続できる場合は、同じBashで取得します。

```bash
RELEASE=https://github.com/sptree-m/awsportal/releases/download/v2.1.3
curl -fL --retry 5 "$RELEASE/$ASSET" -o "$ASSET"
curl -fL --retry 5 "$RELEASE/$ASSET.sha256" -o "$ASSET.sha256"
```

**GitHubへ接続できない場合:** 上のcurlは実行せず、接続できる管理端末で同じ2ファイルを取得・検証し、非公開S3や社内配布サーバーなど承認済みの経路で上記ディレクトリへ搬入します。AWS CLIと対象オブジェクトへの読取権限がある場合のS3からの取得例：

```bash
ARTIFACTS_S3=s3://YOUR-PRIVATE-BUCKET/awsportal/v2.1.3
aws s3 cp "$ARTIFACTS_S3/$ASSET" "$ASSET" --region ap-northeast-1
aws s3 cp "$ARTIFACTS_S3/$ASSET.sha256" "$ASSET.sha256" --region ap-northeast-1
```

バケット・プレフィックス・リージョンは管理者が資材を配置した値へ置き換えます。Portal導入だけならソース・Git・Goは不要です。配布物からのDCV導入でもOSパッケージ、`d1uj6qtbmh3dt5.cloudfront.net`、ライセンス用S3等への通信は必要です。完全閉域では[導入済みAMI・パッケージミラー](../operations/environments.md#golden-amiの作成配布更新)を別途準備します。

搬入の場合も、同じBashで以下の検証から続けます。チェックサムの配布元も管理し、検証失敗時は配置しません。

```bash
cd "$HOME/awsportal-v213-package"
ASSET=awsportal-v2.1.3-linux-arm64.tar.gz
# 公開チェックサムのファイル名にはdist/が付くため、ハッシュを比較する。
EXPECTED="$(awk 'NR==1 {print $1}' "$ASSET.sha256")"
[[ "$EXPECTED" =~ ^[0-9a-fA-F]{64}$ ]]
test "$EXPECTED" = "$(sha256sum "$ASSET" | awk '{print $1}')"
mkdir -m 700 package
tar -xzf "$ASSET" -C package
test "$(tr -d '[:space:]' < package/VERSION)" = 2.1.3
test -s package/awsportal
test -s package/awsportal-admin
```

## 3. バイナリとHTTPS証明書を配置する

以下は新規ホスト用です。既存のサービスやDBへ上書きせず、更新時は[バックアップ・鍵管理・切り戻し](../releases/v2.1.3.md#更新受入)を先に行ってください。

```bash
test "$(uname -m)" = aarch64
cd "$HOME/awsportal-v213-package"
sudo useradd --system --user-group --home-dir /var/lib/awsportal --shell /usr/sbin/nologin awsportal
sudo install -d -o awsportal -g awsportal -m 0700 /var/lib/awsportal /var/lib/awsportal-secrets
sudo install -d -o root -g awsportal -m 0750 /etc/awsportal /etc/awsportal/tls
sudo install -o root -g root -m 0755 package/awsportal package/awsportal-admin /usr/local/bin/
# /secure/incoming/は管理者が証明書・鍵を搬入した実際のパスへ置き換える。
sudo install -o root -g awsportal -m 0640 /secure/incoming/portal.crt /etc/awsportal/tls/portal.crt
sudo install -o root -g awsportal -m 0640 /secure/incoming/portal.key /etc/awsportal/tls/portal.key
```

`portal.crt`は必要な中間CAを含むPEMチェーンです。CA秘密鍵はPortalや利用端末へ配置しません。公開CAの配布と端末のhosts設定は[ログイン前の端末準備](../operations/site-customization.md#ログイン前の証明書配布とhosts設定)に従います。

## 4. 初期管理者を作成し、サービスを起動する

Portalをまだ起動していない状態で、12文字以上の初期パスワードを対話入力します。表示されるTOTP URIを管理者の認証器へ登録し、共有ログへ保存しないでください。

```bash
read -r -s -p 'admin01の初期パスワード（12文字以上）: ' INITIAL_PASSWORD
printf '\n'
sudo -u awsportal env AWSPORTAL_TOTP_KEY_FILE=/var/lib/awsportal-secrets/totp.key \
  /usr/local/bin/awsportal-admin -db /var/lib/awsportal/awsportal.db \
  -cmd create-user -user admin01 -password "$INITIAL_PASSWORD" -role portal_admin
unset INITIAL_PASSWORD
```

次でsystemdサービスを作成します。`portal.company.example`と`ap-northeast-1`を実際の値へ置き換えてから実行してください。Portalと管理CLIは同じDB・TOTP暗号鍵を使用します。

```bash
sudo tee /etc/systemd/system/awsportal.service >/dev/null <<'UNIT'
[Unit]
Description=awsportal
Wants=network-online.target
After=network-online.target

[Service]
User=awsportal
Group=awsportal
UMask=0077
WorkingDirectory=/var/lib/awsportal
Environment=AWSPORTAL_DB=/var/lib/awsportal/awsportal.db
Environment=AWSPORTAL_TOTP_KEY_FILE=/var/lib/awsportal-secrets/totp.key
Environment=AWSPORTAL_HTTP_MODE=https
Environment=AWSPORTAL_ADDR=:443
Environment=AWSPORTAL_TLS_CERT=/etc/awsportal/tls/portal.crt
Environment=AWSPORTAL_TLS_KEY=/etc/awsportal/tls/portal.key
Environment=AWSPORTAL_WEBAUTHN_ORIGIN=https://portal.company.example
Environment=AWS_REGION=ap-northeast-1
Environment=AWS_DEFAULT_REGION=ap-northeast-1
ExecStart=/usr/local/bin/awsportal
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable --now awsportal
sudo systemctl status awsportal --no-pager
```

名前解決とCA信頼を準備した管理端末から`https://portal.company.example/healthz`を確認し、`ok`が返ることを確認します。公開CAを指定して確認する例：

```bash
curl --fail --show-error --cacert company-ca.pem https://portal.company.example/healthz
```

起動後はDB・設定・元のTOTP暗号鍵をバックアップし、DBと鍵の保管先を分けます。鍵の紛失時は再生成せず、対応する元の鍵とDBをセットで復旧します。

## 5. 初回ログインと最初のDCV接続を確認する

1. 信頼済みHTTPSの固定名でPortalを開き、`admin01`のpassword＋TOTPでログインします。初回password変更を求められた場合は変更して再ログインします。TOTPは直前に成功したコードを再利用せず次のコードを使います。
2. 「パスキー」で管理者端末の鍵を登録します。v2.1.1以降は追加・削除や保護された管理操作ごとに本人確認が必要です。最初は現在のpassword＋TOTPで確認し、戻った画面でもう一度操作します。確認結果は2分以内・1回限りです。登録後はパスキーでもログインできることを確認します。
3. Usersで通常ユーザーを作成し、本人確認後に30分有効の登録コードを渡します。本人のPCで`/passkeys/enroll`から端末固定型パスキーを登録します。通常User／Group Adminの通常ログインはパスキーのみです。
4. Instance Adminで管理対象EC2のID・名前・DCVホストを登録し、EC2専用認証キーを発行します。対象EC2へ同じv2.1.3の検証済み配布物を展開し、[DCV導入手順](../operations/dcv.md#導入手順)に従って`dcv/install.sh`、root所有0600の`/etc/awsportal-dcv/config.json`、公開CA、Agentサービスを設定します。認証キーはEC2ごとに分けます。
5. ユーザーまたはグループをEC2へ割り当て、Agentの同期と準備完了を待ちます。本人でログインして「接続」を押し、ネイティブクライアントでデスクトップが開くことを確認します。DCVの表示用証明書も対象ホストを検証できるよう準備してください。

導入完了の確認項目：

- 一般ユーザーには割り当てたEC2だけを表示し、他ユーザーのOSユーザー・ホーム・セッションに入れない。
- DCVの画面・入力と、許可した起動／停止が動作する。禁止したファイル転送・クリップボード等は失敗する。
- 割り当て解除後の同期で対象セッションを終了し、新しい接続を拒否する。
- 管理者の両ログイン方式、操作単位の本人確認、監査記録を確認する。監査S3出力・Shared自動増減・Windows取り込みは必要な受入後に有効化する。

詳細な受入は[DCV試験手順](../development/dcv-release-test.md#ポータルネイティブクライアント試験)と[v2.1.3の受入項目](../releases/v2.1.3.md#検証)を参照してください。

## 困ったとき

| 症状 | 最初に確認すること |
|---|---|
| Portalが起動しない | `sudo journalctl -u awsportal -n 80 --no-pager`。証明書・鍵の読取権限と一致、固定Origin、DB／TOTP暗号鍵の所有者・権限 |
| ブラウザーで証明書エラー／パスキーが使えない | DNS／hosts、SAN、公開CAの信頼、アクセスURLとOriginの一致。証明書検証を無効化せず修正する |
| DCVが準備中のまま | Agentサービス・ログ、HTTPS経路とCA、EC2専用キー、割り当て、DCVライセンス、設定版と適用版の一致 |
| GitHubから取得できない | 配布物とチェックサムを両方搬入し、配置前に検証。ラボの場合はソースも搬入する |

ログを共有する際はpassword・TOTP秘密情報・パスキー登録コード・EC2専用キー・署名付きURL・DCVトークンを除いてください。
