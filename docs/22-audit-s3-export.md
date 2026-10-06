# 監査ログの定期S3出力

SQLiteの`audit_log`に記録された全イベントを、差分JSON Lines（UTF-8、1行1イベント）にしてgzip圧縮し、S3へ定期出力する。ログイン成功・失敗、ログアウト、MFA変更、パスワード変更・リセット、ユーザー管理、EC2操作、DCV認証、プロキシ判定等、既存の監査記録が対象。CloudTrailやOSログの収集は別機能。

## 設定

Portalのsystemdサービスの環境変数に設定して再起動する。

```ini
[Service]
Environment=AWSPORTAL_AUDIT_BUCKET=REPLACE_APPROVED_BUCKET
Environment=AWSPORTAL_AUDIT_PREFIX=awsportal-production/audit/
Environment=AWSPORTAL_AUDIT_REGION=ap-northeast-1
Environment=AWSPORTAL_AUDIT_INTERVAL=5m
```

| 変数 | 意味・既定 |
|---|---|
| `AWSPORTAL_AUDIT_BUCKET` | 既存の保存先バケット。未設定では出力OFF |
| `AWSPORTAL_AUDIT_PREFIX` | 保存先prefix。既定`audit/`。環境ごとに別prefixを指定 |
| `AWSPORTAL_AUDIT_REGION` | S3リージョン。既定はAWS SDKの設定リージョン |
| `AWSPORTAL_AUDIT_INTERVAL` | 周期。既定`5m`。`1h`、`24h`等のGo duration形式、最低`1s` |

起動直後にも1回出力する。初回は既存の全履歴を古い順に送信し、その後は差分のみ。1ファイル最大1000イベント、1回最大10ファイル、1回の処理期限は1分。大量の履歴は次の周期へ継続する。出力処理はEC2や座席制御のworkerと独立する。

キー例：`awsportal-production/audit/date=2026-10-06/00000000000000000001-00000000000000001000-<SHA256>.jsonl.gz`。Content-Typeは`application/gzip`。日付はバッチ先頭イベントのUTC日付であり、ファイルには翌日以降のイベントも含まれ得る。個々の時刻は`at`を参照する。ダウンロード後は`gzip -dc ファイル名.jsonl.gz`で内容を読める。

```json
{"id":1,"at":"2026-10-06T11:00:00Z","actor":"alice","action":"login","target":"","result":"ok","detail":""}
```

送信前にファイルのキー・圧縮済み内容をSQLiteへ永続化し、S3成功応答の後だけカーソルを進める。通信失敗やPortal再起動は同じキー・同じ圧縮データで再試行する。S3への書き込み後に応答が失われた場合、Versioning有効バケットには同一内容の複数versionができることがあるが、別キーの重複ファイルは作らない。圧縮データのSHA256をS3のchecksumに指定する。

更新前から送信保留中の非圧縮`.jsonl`は、元のキー・内容で送信を完了し、次の新規バッチからgzipに切り替える。S3に保存済みのファイルは変換しない。件数による分割は継続するが、バイト数の上限は設定していない。

元のSQLite監査ログは削除しない。送信障害はサービスログに記録し、次の周期で再試行する。保存先バケット／prefix変更時は別カーソルで既存履歴を再出力する。DBバックアップには出力状態も含める。単一Portalプロセスで運用する。

## S3・IAM・ネットワーク

バケットを管理者が事前作成し、PortalのInstance Profileに指定prefixだけの書き込み権限を付与する。Access Keyは不要。

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "s3:PutObject",
    "Resource": "arn:aws:s3:::REPLACE_APPROVED_BUCKET/awsportal-production/audit/*"
  }]
}
```

SSE-KMSバケットの場合は承認KMS Keyの`kms:GenerateDataKey`と対応key policyも必要。暗号化はバケットの既定設定を使う。提供済みVPC／Subnet／TGW環境では、承認S3 EndpointまたはTGW経由のS3通信とEndpoint Policyも準備する。本機能はバケット・IAM・ルートを自動変更しない。

監査データの閲覧権限、Versioning、保持期間のLifecycle、必要に応じたObject Lockは管理者側で設定する。PortalからS3ログを削除する機能はない。出力対象はDBに記録済みの監査イベントで、IPアドレス／User-Agent追加や全HTTPアクセスの記録は含まない。

## 確認

起動後に指定prefixへ`.jsonl.gz`が生成され、展開したJSONLにログイン成功・失敗の`login`とログアウトの`logout`が確認できることを実機で確認する。S3権限を一時拒否した試験では、サービスログに送信失敗が出ること、権限復旧後に保留分が出力されることを確認する。S3停止中はSQLiteの容量を監視する。
