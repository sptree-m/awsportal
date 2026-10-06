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

管理対象OSへ対応版DCVを導入し、[DCV手順](../operations/dcv.md)でAgent・外部認証・permissionを設定する。SSH／RDPはOSとSGで遮断し、外部認証はstagingで検証してから本番へ適用する。Personal／SharedとEFSは[環境運用](../operations/environments.md)の手順を併用する。

一般Userは割り当て対象だけを表示・操作できること、起動／停止／schedule／DCVが動作すること、SSH／RDPと禁止転送が失敗することを確認する。管理者はTOTP必須、全EC2表示、許可された持ち出しと監査記録を確認する。監査S3・Shared自動増減・Windows Importは初期OFFで、対象環境で必要な受入を確認してから有効化する。
