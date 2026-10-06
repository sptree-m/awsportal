# 構築・導入手順書

全体の入口は[ドキュメント目次](../README.md)。管理者指定のネットワークでは[組織ネットワーク手順](organization-network.md)を併用します。初期ユーザー・EC2登録は[初期設定](initial-setup.md)、開発時の強制テストは[テスト方針](../development/testing.md)を参照してください。

## 1. 前提
Terraform 1.7以上、Go 1.23以上、ARM64対応AMI、Private Subnet、社内/VPN CIDR、内部DNS、管理端末が信頼する社内PKI証明書を準備します。

## 2. 開発環境
リポジトリ取得後に `./scripts/setup-dev.sh` を実行します。これにより `core.hooksPath=.githooks` が設定されます。コミット時にはpre-commitから全ローカルテストが自動実行され、1件でも失敗するとコミットを中止します。

## 3. t4g.micro相当試験
`./scripts/test-t4g-micro.sh` を使用します。ARM64 Linux上で2 vCPU、1 GiB RAMを上限としてポータルをビルド・起動し、ヘルスチェックとメモリ制約下の基本試験を行います。実EC2での最終試験ではt4g.micro + 4 GiB swapを使用します。

## 4. Terraform
`terraform.tfvars` にVPC、Private Subnet、社内CIDR、AMI、管理対象Instance ARNを設定します。fmt、validate、plan、レビュー後にapplyします。

## 5. ポータル
`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o awsportal ./cmd/awsportal` でビルドします。専用の非特権ユーザーでsystemdサービスとして実行します。AWS Access Keyは保存せずInstance Profileを使用します。

## 6. 管理対象デスクトップ
対象OSでAWSがサポートするAmazon DCVを導入し、権限ファイルを適用します。SSH/RDPは停止しSecurity Groupでも遮断します。DCV外部認証はステージング環境で検証後に本番へ適用します。

## 7. 受入試験
一般ユーザーについて、割当済みEC2だけが見えること、許可EC2だけを起動・停止・スケジュール設定できること、DCV接続できること、SSH/RDPが失敗すること、禁止したデータ転送経路が利用できないことを確認します。管理者についてTOTP必須、全EC2表示、許可されたダウンロードと監査記録を確認します。
