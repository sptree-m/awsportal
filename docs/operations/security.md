# セキュリティ・運用手順

- ポータル管理者とAWS基盤管理者は別ロールとして扱います。
- `portal_admin` はTOTPを必須とし、復旧手順は複数人承認を前提にします。
- パスワードはbcryptでハッシュ化します。TOTP Secretは本番導入までにKMS等を利用した暗号化方式へ移行します。
- IMDSv2とEC2 Instance Profileを使用し、固定AWS Access Keyを保存しません。
- 管理対象EC2をタグ等で識別し、AWS APIが対応する範囲でIAM Resourceを限定します。
- SQLiteバックアップ、監査ログ、TOTP Secretを機密情報として扱います。
- Security Groupに22/3389が誤って開放されていないことを自動テストします。
- 一般ユーザーの迂回路となるSession Manager対話アクセスを許可しません。
- 外向き通信は社内承認済みFirewall/ProxyでAllowlist制御します。
- CloudTrail、VPC Flow Logs、中央ログ保管を本番要件とします。
- ポータルの操作・認証ログはSQLiteへ記録し、[gzip付きS3定期出力](audit-s3-export.md)を任意に有効化できます。CloudTrail・VPC Flow Logs・OSログの収集はこの出力とは別に準備します。
- 緊急管理アクセスは期限付き・記録必須とします。
- `--no-verify` によるローカルテスト回避を運用上禁止し、mainへのマージはGitHubの必須CIチェック成功後だけ許可します。
