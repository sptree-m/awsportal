# awsportal

AWS上のEC2／Amazon DCV環境を管理する、イントラネット向け軽量ポータルです。v2.1.3の変更と移行手順は[リリースノート](docs/releases/v2.1.3.md)を参照してください。

## 最優先のコンセプト

**超軽量・高速・高いメンテナンス性・プロ仕様UI**を、awsportalの最上位の設計原則とします。

| コンセプト | 守る基準 |
|---|---|
| 超軽量 | Go SSR・SQLite・同梱assetsを基本とし、依存・メモリ・通信量を抑える。t4g.micro相当で検証し、外部CDNや巨大なUI frameworkを導入しない。 |
| 高速 | 必要なデータと画面部分だけを取得・更新する。待機中の不要な通信を避け、応答時間と転送量の悪化を検証する。 |
| 高いメンテナンス性 | 責務を明確にし、依存と重複を減らす。共通の実装・設定・文書を使い、必須テストで変更を検証する。 |
| プロ仕様UI | 承認済みOperations Consoleの外観、情報密度、桁の揃う文字、明確な状態表示を維持する。各画面幅で主要操作とアクセシビリティを確保する。 |

機能追加・リファクタリング・文書削減でも、この四つのコンセプトの削除・弱体化は禁止します。常に現行READMEに明記し、過去の文書への退避だけで済ませません。設計・UIの変更はこの原則に照らしてレビューします。

## 基本方針

- 一般UserにAWSアカウント・IAM User・Console・Access Keyを付与せず、PortalのInstance Profileで最小権限のAWS APIを実行します。
- 本人または所属groupへ割り当てられたEC2だけを表示・操作します。一般User／Group Adminの通常ログインは本人確認必須・クラウド同期不可のパスキーに限定します。Portal Adminは全体を参照でき、同じパスキーまたはパスワード＋TOTPで認証します。
- 接続はDCV（指定TCPポート、既定8443）に限定し、SSH／RDP／SCP・一般UserのSSM対話アクセスを許可しません。
- DCVの転送・clipboard・印刷・USB等を制限し、許可された管理者の持ち出しは監査します。

UIはGo SSR・同梱htmx・自前CSS・ローカルfontで構成し、外部CDNを使いません。1366×768から狭幅まで主要操作を表示し、一覧だけを横scrollさせます。

認証の最終仕様は[認証仕様](docs/architecture/authentication.md)、PCごとの登録・管理者リセットは[パスキー運用](docs/operations/passkeys.md)を参照してください。v2.0.0への更新では固定HTTPS Originの設定と既存ユーザーへの登録コード配布が必要です。

## 導入・運用

PortalはGo単体のHTTPS待受が既定です。サーバー証明書と秘密鍵を設定します。HTTPSリバースプロキシを使う場合は `AWSPORTAL_HTTP_MODE=http` で内部HTTP待受へ切り替えます。既存のHTTP構成から更新する場合も明示設定が必要です。[TLS設定](docs/deployment/setup.md#ローカルhttpsと初回端末準備)を参照してください。

[クイック導入ガイド](docs/deployment/quickstart.md)でv2.1.3の新規導入を進められます。[文書目次](docs/README.md)から[現行機能](docs/features.md)、[構築・初期設定](docs/deployment/setup.md)、[管理者指定ネットワーク](docs/deployment/organization-network.md)も参照してください。監査S3出力、Shared自動増減、Windows取り込みは既定OFFです。

本番構築では[制約確認・既存資源利用・CloudFormation・受入と復旧の手順](docs/deployment/production/README.md)を参照してください。付属のCloudFormationは全新規基盤の例で、既存資源切替・アプリ設定・バックアップは含みません。

## 開発

`./scripts/setup-dev.sh`でpre-commit hookを有効化します。コミット前の`./scripts/test-all.sh`成功が必須で、`--no-verify`は禁止です。GitHub側でもPR・必須CI成功を要求します。[テスト方針](docs/development/testing.md)を参照してください。

t4g.micro相当試験はARM64・2 vCPU・1 GiB RAMを前提とし、実機では4 GiB swapを補助的に使用します。
