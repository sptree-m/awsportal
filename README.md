# awsportal

AWS上のEC2／Amazon DCV環境を管理する、イントラネット向け軽量ポータルです。v1.9.0の変更は[リリースノート](docs/releases/v1.9.0.md)を参照してください。

## 基本方針

- 一般UserにAWSアカウント・IAM User・Console・Access Keyを付与せず、PortalのInstance Profileで最小権限のAWS APIを実行します。
- 本人または所属groupへ割り当てられたEC2だけを表示・操作します。Portal Adminは全体を参照でき、TOTP認証が必須です。
- 接続はDCV（TCP/8443）に限定し、SSH／RDP／SCP・一般UserのSSM対話アクセスを許可しません。
- DCVの転送・clipboard・印刷・USB等を制限し、許可された管理者の持ち出しは監査します。

UIはGo SSR・同梱htmx・自前CSS・ローカルfontで構成し、外部CDNを使いません。1366×768から狭幅まで主要操作を表示し、一覧だけを横scrollさせます。

## 導入・運用

[文書目次](docs/README.md)を入口とし、[現行機能](docs/features.md)、[構築・初期設定](docs/deployment/setup.md)、[管理者指定ネットワーク](docs/deployment/organization-network.md)を参照してください。監査S3出力、Shared自動増減、Windows取り込みは既定OFFです。

## 開発

`./scripts/setup-dev.sh`でpre-commit hookを有効化します。コミット前の`./scripts/test-all.sh`成功が必須で、`--no-verify`は禁止です。GitHub側でもPR・必須CI成功を要求します。[テスト方針](docs/development/testing.md)を参照してください。

t4g.micro相当試験はARM64・2 vCPU・1 GiB RAMを前提とし、実機では4 GiB swapを補助的に使用します。
