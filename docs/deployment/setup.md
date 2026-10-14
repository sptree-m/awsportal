# 構築・初期設定

コマンドはリポジトリrootで実行する。指定VPC・Subnet・TGW・IP範囲は[組織ネットワーク](organization-network.md)、設計・権限境界は[アーキテクチャ](../architecture/overview.md)を参照。

## 準備と配布

Terraform 1.7以上、Go 1.27.2、ARM64 AMI、Private Subnet、社内／VPN CIDR、内部DNS、管理端末が信頼する社内PKI証明書を準備する。Goの基準版は`go.mod`の`go`行を正とし、開発・CI・リリースビルドで揃える。

開発する場合は`./scripts/setup-dev.sh`でコミット前hookを有効化し、[テスト方針](../development/testing.md)に従う。ARM64制約試験は`./scripts/test-t4g-micro.sh`、実t4g.microでは4 GiB swapも確認する。

`terraform/`の設定にVPC・Private Subnet・社内CIDR・AMI・管理対象Instance ARNを指定し、fmt／validate／planとreviewの後に適用する。組織環境は[設定例](../../terraform/examples/org-existing/README.md)と承認済みIAM／ネットワーク経路を使用する。

同じrelease tagの配布物・ソース・Terraformを使う。Portalは専用非特権userのsystemd serviceとして稼働し、固定Access Keyを保存せずInstance Profileを使う。ソースからのARM64ビルド：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o awsportal ./cmd/awsportal
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o awsportal-admin ./cmd/awsportal-admin
```

## ローカルHTTPSと初回端末準備

PortalのGoサーバーはHTTP待受で、証明書配布機能はTLSサーバーを自動構築しない。社内CAまたはローカル検証用CAで発行したサーバー証明書をHTTPSリバースプロキシへ設定する。社内の正式CAがある場合は組織の証明書発行手順を使う。

ローカル検証で独立CAを作る例（管理者端末の専用作業ディレクトリで実行）：

```sh
umask 077
openssl req -x509 -newkey rsa:3072 -nodes -sha256 -days 3650 \
  -keyout ca.key -out ca.crt -subj '/CN=awsportal local CA' \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign'
openssl req -new -newkey rsa:3072 -nodes -keyout portal.key \
  -out portal.csr -subj '/CN=portal.company.example'
cat > portal.ext <<'EXT'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:portal.company.example
EXT
openssl x509 -req -in portal.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out portal.crt -days 365 -sha256 -extfile portal.ext
openssl x509 -in ca.crt -noout -fingerprint -sha256
```

例示の名前は実ホスト名へ置き換える。ブラウザーで使う名前をSANへ含め、Proxy/DCVを同じCAで提供する場合も各ホスト名のSAN付き証明書を別途発行する。CA秘密鍵は発行用管理環境で保持し、Portalや利用者へ配布しない。portal.keyはHTTPS終端ホストだけへ配置する。ca.crtだけが端末へ配布する公開CA。

同一ホストのnginxで終端する例：

```nginx
server {
    listen 443 ssl;
    server_name portal.company.example;
    ssl_certificate /etc/nginx/tls/portal.crt;
    ssl_certificate_key /etc/nginx/tls/portal.key;
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Authorization $http_authorization;
    }
}
```

Portalは `AWSPORTAL_ADDR=127.0.0.1:8080` で内部待受とし、セキュアCookieの既定を維持する。v2.0.0では `AWSPORTAL_WEBAUTHN_ORIGIN=https://portal.company.example` をsystemdのEnvironmentへ必ず設定する。URLの末尾スラッシュ・パスは付けない。ブラウザーで443以外を使用する場合はOriginにそのポートも含める。RP IDはホスト名で固定され、表示設定のPortal URLとは独立する。未設定／不正Originでは起動しない。nginxの構文確認・reload、管理端末から443への疎通を確認する。別ホストで終端する場合は、組織の承認経路とバックエンド通信の保護を別途設計する。

[ログイン前の配布設定](../operations/site-customization.md#ログイン前の証明書配布とhosts設定)でca.crtとhosts対応表を用意する。hostsは利用端末から到達できるサーバーIPを指定する。サーバーが別のPC/EC2で動く場合、利用端末の127.0.0.1へ割り当てない。初回端末はまだ名前解決・CA信頼がないため、管理者が既存の信頼済み社内配布経路でZIPと別途フィンガープリントを渡す。準備済み端末ではログイン画面下から再取得できる。

## アカウントとEC2登録

Portal停止中、またはDB排他を考慮した保守時間に管理CLIを使う。一般UserにはAWS IAM User・Console・Access Keyを発行しない。

```sh
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd create-user -user admin01 -password '十分に長い初期パスワード' -role portal_admin
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd create-user -user user01 -role user
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd add-instance -instance i-0123456789abcdef0 -name DEV-01 -host dev01.internal.example
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd add-group -group ADAS
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd assign-group -instance i-0123456789abcdef0 -group ADAS
```

管理者作成時は表示されるTOTP URIをAuthenticatorへ登録し、password＋TOTPでログインする。初期passwordを必要に応じて変更し、ログイン後10分以内に「パスキー」でこのPCの鍵を登録する。User／Group Admin作成時は30分有効の登録コードが表示される。本人確認後にコードを配布し、利用者が `/passkeys/enroll` で端末固定型パスキーを登録してからログインする。一般ユーザーへのpassword配布は不要。[複数PC・復旧手順](../operations/passkeys.md)を参照。Userのgroup所属追加／除外、EC2の直接／group割り当てはPortal Adminの`/admin/instances`で管理する。[権限と無効化](../operations/instances.md)を参照。

## デスクトップ・受入

EC2をポータル外で新規作成する場合は[外部作成・種類選択・登録](../operations/instances.md#ポータル外でのec2作成と種類選択)、Golden AMIを準備する場合は[構築・検証・配布・更新](../operations/environments.md#golden-amiの作成配布更新)に従う。Portal Adminはログイン後の「利用者マニュアル」内にある[管理者マニュアル](../operations/site-customization.md#管理者マニュアル)からも確認できる。

管理対象OSへ対応版DCVを導入し、[DCV手順](../operations/dcv.md)でAgent・外部認証・permissionを設定する。SSH／RDPはOSとSGで遮断し、外部認証はstagingで検証してから本番へ適用する。Personal／SharedとEFSは[環境運用](../operations/environments.md)の手順を併用する。

一般Userは割り当て対象だけを表示・操作できること、起動／停止／schedule／DCVが動作すること、SSH／RDPと禁止転送が失敗することを確認する。一般User／Group Adminのpassword＋TOTPログイン拒否、UVなし／同期可能キーの拒否、複数PC登録、管理者リセットと旧セッション拒否を確認する。管理者はパスキーとpassword＋TOTPの両経路、全EC2表示、許可された持ち出しと監査記録を確認する。監査S3・Shared自動増減・Windows Importは初期OFFで、対象環境で必要な受入を確認してから有効化する。

## 64 IPの証明書・hosts生成支援

Portal Adminで `/manual#admin-preparation-guide` を開くと、CA準備、台帳、生成、ログイン画面への配布設定、更新・復旧を一緒に確認できる。表示設定画面からも同手順へ移動できる。管理者だけが `/admin/preparation/generate-preparation.py` の支援ツールを取得できる。

管理者専用Linux/macOS環境へPython 3とOpenSSLを用意し、実際に承認された64個のIPとホスト名を `hosts.json` に登録する。形式は配布設定と同じ `{"hosts":[{"name":"portal.company.example","ip":"192.0.2.10"}]}`。最大128件、64 KiB。各名前は一意とし、同じIPの別名は許可する。例示のIPを本番へ使ったり、CIDRから未承認のIPを自動登録したりしない。

社内PKIへ申請する場合：

```sh
python3 generate-preparation.py --hosts hosts.json --out generated-csr
```

生成されるホスト別 `server.csr` を社内CAへ申請し、DNS SAN・serverAuth付き証明書を取得する。ローカル発行CAを使える環境では署名まで一括実行できる：

```sh
python3 generate-preparation.py --hosts hosts.json --out generated \
  --ca-cert /secure/ca.crt --ca-key /secure/ca.key
```

独立した検証用CAは上のOpenSSL手順で準備する。CA証明書は365日以上の残存有効期間が必要。暗号化CA鍵はOpenSSLの対話入力を使う。出力は正規化した `hosts.json` と、ホスト名ごとのRSA 3072 bit `server.key`・DNS SAN付き `server.csr`・`server.ext`、署名時は365日有効な `server.crt`。鍵と出力ディレクトリは管理者専用権限で生成する。証明書ごとに別鍵とランダムなシリアル番号を使い、CA・接続先名を検証する。既存出力先は拒否し、失敗時の部分出力は管理者が確認して別出力先へ再実行する。

ツールはCAを自動作成せず、CA秘密鍵をコピーしない。証明書と鍵はTLSを使う対象サーバーだけへ配置し、nginx/DCVの設定・reload・実接続を確認する。TLS不要の台帳エントリーにもCSRは生成されるが配置は不要。AWS・TLS設定・端末信頼ストア・hostsはツールから変更しない。Portalへ配置するのは公開 `ca.crt` と生成済み `hosts.json` だけ。生成ディレクトリ全体や秘密鍵を利用者へ配布しない。

ログイン画面でのZIP配布・端末適用は[配布手順](../operations/site-customization.md#ログイン前の証明書配布とhosts設定)を使う。IPの変更は台帳と配布設定を更新して再配布する。同じ名前・CAで証明書を更新する場合、通常は端末CAの再登録は不要。台帳から削除した名前は現行スクリプトが自動削除しないため、管理者が確認して整理する。

v2.0.0のパスキー用に、Portalは組織の管理ドメイン配下の固定HTTPS名を使い、`AWSPORTAL_WEBAUTHN_ORIGIN`をそのOriginへ設定する。RP IDはPortalのホスト名。64個の接続先名すべてを認証元にする必要はない。証明書・hosts配布とは別に、本人確認後のパスキー登録コードを発行する。
