# 構築・初期設定

コマンドはリポジトリrootで実行する。指定VPC・Subnet・TGW・IP範囲は[組織ネットワーク](organization-network.md)、設計・権限境界は[アーキテクチャ](../architecture/overview.md)を参照。

## 準備と配布

Terraform 1.7以上、Go 1.23以上、ARM64 AMI、Private Subnet、社内／VPN CIDR、内部DNS、管理端末が信頼する社内PKI証明書を準備する。

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

Portalは `AWSPORTAL_ADDR=127.0.0.1:8080` で内部待受とし、セキュアCookieの既定を維持する。nginxの構文確認・reload、管理端末から443への疎通を確認する。別ホストで終端する場合は、組織の承認経路とバックエンド通信の保護を別途設計する。

[ログイン前の配布設定](../operations/site-customization.md#ログイン前の証明書配布とhosts設定)でca.crtとhosts対応表を用意する。hostsは利用端末から到達できるサーバーIPを指定する。サーバーが別のPC/EC2で動く場合、利用端末の127.0.0.1へ割り当てない。初回端末はまだ名前解決・CA信頼がないため、管理者が既存の信頼済み社内配布経路でZIPと別途フィンガープリントを渡す。準備済み端末ではログイン画面下から再取得できる。

## アカウントとEC2登録

Portal停止中、またはDB排他を考慮した保守時間に管理CLIを使う。一般UserにはAWS IAM User・Console・Access Keyを発行しない。

```sh
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd create-user -user admin01 -password '十分に長い初期パスワード' -role portal_admin
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd create-user -user user01 -password '十分に長い初期パスワード' -role user
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd add-instance -instance i-0123456789abcdef0 -name DEV-01 -host dev01.internal.example
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd add-group -group ADAS
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd assign-group -instance i-0123456789abcdef0 -group ADAS
```

表示される管理者TOTP URIをAuthenticatorへ登録し、ログイン確認後に初期passwordを変更する。Userのgroup所属追加／除外、EC2の直接／group割り当てはPortal Adminの`/admin/instances`で管理する。[権限と無効化](../operations/instances.md)を参照。

## デスクトップ・受入

EC2をポータル外で新規作成する場合は[外部作成・種類選択・登録](../operations/instances.md#ポータル外でのec2作成と種類選択)、Golden AMIを準備する場合は[構築・検証・配布・更新](../operations/environments.md#golden-amiの作成配布更新)に従う。Portal Adminはログイン後の「利用者マニュアル」内にある[管理者マニュアル](../operations/site-customization.md#管理者マニュアル)からも確認できる。

管理対象OSへ対応版DCVを導入し、[DCV手順](../operations/dcv.md)でAgent・外部認証・permissionを設定する。SSH／RDPはOSとSGで遮断し、外部認証はstagingで検証してから本番へ適用する。Personal／SharedとEFSは[環境運用](../operations/environments.md)の手順を併用する。

一般Userは割り当て対象だけを表示・操作できること、起動／停止／schedule／DCVが動作すること、SSH／RDPと禁止転送が失敗することを確認する。管理者はTOTP必須、全EC2表示、許可された持ち出しと監査記録を確認する。監査S3・Shared自動増減・Windows Importは初期OFFで、対象環境で必要な受入を確認してから有効化する。
