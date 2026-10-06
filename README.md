# awsportal

v1.9.0の変更・設定・更新時の注意は[リリースノート](docs/releases/v1.9.0.md)を参照してください。

AWS上のEC2/DCV環境を管理する、イントラネット向け超軽量ポータルです。

## 基本方針
- 一般ユーザーにはAWSアカウント、IAMユーザー、AWS Console、Access Keyを付与しません。
- AWS APIはポータルEC2のInstance Profileから最小権限で実行します。
- 管理対象デスクトップへの接続経路はAmazon DCV（TCP/8443）のみに限定します。
- SSH、RDP、SCP、一般ユーザー向けSSM対話セッションは禁止します。
- 一般ユーザーは本人または所属グループに割り当てられたEC2だけを表示・操作できます。
- ポータル管理者は全管理対象EC2を表示でき、TOTPによる2要素認証を必須とします。
- 一般ユーザーのDCVではファイル転送、クリップボード、印刷、USB等を制限します。
- 管理者によるデータ持ち出しは別権限とし、監査ログを残します。

## UI設計原則
- 超軽量・高速を最優先し、Go SSR + HTML + 自前CSSを基本とします。
- Bootstrap、Tailwind、React、Vue、jQuery、Google Fonts、CDNなどの外部UIリソースを使用しません。
- 1366x768のPC、小さいブラウザウィンドウ、狭幅画面でも主要操作が画面外へ消えないことを必須とします。
- 横長の一覧はページ全体ではなく一覧領域だけを横スクロール可能にします。
- 見栄えのためだけのJavaScriptや外部ダウンロード依存を追加しません。
- CIでHTML/CSSの外部URL、外部script、CSS importを検出して拒否します。

## 開発時の必須テスト
`./scripts/setup-dev.sh` を一度実行すると、Gitのpre-commit hookを有効化します。
以後は `./scripts/test-all.sh` が成功しない限りコミットできません。
`--no-verify` による回避は禁止です。GitHub側でもCIを必須チェックとして設定してください。

t4g.micro相当テストはARM64、2 vCPU、1 GiB RAMを前提とし、4 GiB swapを補助的に使用します。

## ドキュメント

[ドキュメント目次](docs/README.md)から導入・運用・開発の手順を探せます。現行機能は[機能一覧](docs/features.md)、変更履歴は[リリースノート](docs/releases/v1.9.0.md)にまとめています。

| 目的 | 手順 |
|---|---|
| 管理者指定のVPC・Subnet・TGW・IP範囲で導入 | [組織ネットワーク](docs/deployment/organization-network.md)、[設定例](terraform/examples/org-existing/README.md) |
| ユーザー・EC2の初期設定 | [初期設定](docs/deployment/initial-setup.md) |
| Personal／Shared・ストレージ・費用・Windows取り込み | [環境運用](docs/operations/environments.md) |
| ログイン履歴等をgzip圧縮してS3へ定期出力 | [監査ログの設定・IAM権限](docs/operations/audit-s3-export.md) |
| GitLab／Git LFS取得専用ミラー | [ミラー運用](docs/operations/git-mirror.md) |
| 利用者マニュアル・サイト文言の変更 | [表示設定](docs/operations/site-customization.md) |
| AWSネットワーク・権限の証拠を収集 | [CloudShell調査ツール](tools/aws-audit/README.md) |

監査S3出力、Shared自動増減、Windows取り込みは既定OFFです。AWS／DCV／Boxの実機受入を確認してから必要な機能を有効化します。
