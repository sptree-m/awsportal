# アーキテクチャとセキュリティ

## ID管理境界
一般ユーザーにはAWSアカウント、IAMユーザー、Access Key、Console/CLI認証情報を付与しません。ポータルユーザーとAWS Identityは完全に分離します。

ポータルのロールは `user`、`group_admin`、`portal_admin` とします。管理者はTOTP MFA必須です。一般ユーザーは本人に直接割り当てられたEC2と、所属グループに割り当てられたEC2だけを参照・操作できます。管理者は全管理対象EC2を参照できます。

## 接続境界
管理対象デスクトップは、承認済み社内/VPNネットワークからDCV TCP/8443だけを公開します。SSH/22、RDP/3389は開放しません。一般ユーザーにはSSMによる対話アクセスも許可しません。

## データ漏洩防止
1. 一般ユーザーのDCV権限でファイル送受信、クリップボード、印刷、USB等を禁止します。
2. 外向き通信は別レイヤーでAllowlist制御します。DCV制限だけではDLPとして不十分です。
3. ポータル操作とAWS操作を監査します。
4. 管理者のデータ持ち出しは独立した権限として監査します。

## ポータル
ARM64のt4g.microを基本ターゲットとします。Goのサーバーサイドレンダリング、SQLite、ローカル静的ファイルのみで構成し、CDNや外部JavaScript/CSSを実行時に読み込みません。4 GiB swapはOOM対策であり、1 GiB RAM不足を常時補う設計にはしません。

## 本番運用の要件

- Portal AdminとAWS基盤管理者は別ロール。管理者復旧は複数人承認、緊急操作は期限付き・記録必須とし、障害時もSSH／RDPを臨時開放しない。
- bcryptでpasswordを保存する。TOTP Secretは本番前にKMS等による暗号化へ移行する。SQLite backup・監査ログ・TOTP Secretは機密情報として扱う。
- IMDSv2とInstance Profileを使用し、AWS APIの権限を管理対象resourceへ限定する。22／3389の禁止を自動試験し、SSM対話アクセスによる迂回も許可しない。
- DCV制限と社内承認Firewall／ProxyのAllowlistを併用する。CloudTrail／VPC Flow Logs／中央ログ保管は管理者が別途準備する。
- Portalの操作・認証ログはSQLiteへ記録し、[gzip付きS3出力](../operations/audit-s3-export.md)を任意に有効化する。CloudTrail・OSログの収集を代替しない。
- コミット前テストとmainへのPR・必須CI成功は[開発方針](../development/testing.md)に従う。`--no-verify`による回避は禁止。
