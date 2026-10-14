# 現行の機能一覧

対象はv2.0.0。詳細設定は各運用手順を参照してください。「実装あり」はコードが存在することを示し、AWS／DCV／Box実環境での本番受入完了を示すものではありません。過去の①〜④による調査表は[履歴](archive/README.md)に保存しています。

| 領域 | 実装されている機能 | 設定・制約 |
|---|---|---|
| 認証・アカウント | 全ロールの端末固定型パスキー（最大10台、UV必須・同期不可）、一般User／Group Adminのパスキー必須、Portal Adminのパスワード＋TOTP併用（TOTP最大3台）、30分・使い捨て登録コード、管理者リセット、無操作無効化・再有効化、段階的待ち時間・10回失敗ロック、管理者解除・復旧CLI | [仕様](architecture/authentication.md)、[初期設定](deployment/setup.md)、[パスキー運用](operations/passkeys.md)、[復旧](operations/password-recovery.md) |
| ユーザー・権限 | Portal Adminによるユーザー・グループ所属・EC2割り当て管理、本人／グループの参照・操作制限 | [インスタンス管理](operations/instances.md)。Group Adminへの管理委譲は未実装 |
| EC2・スケジュール | 状態表示、起動／停止、遷移中の更新、曜日・時刻による操作、ポータル上の無効化 | [運用概要](operations/instances.md)。無効化はAWS停止・削除ではない |
| DCV | ワンタイム認証、ネイティブ接続、OSアカウント・session同期、接続権限・DLP設定、接続ポート指定 | [DCV](operations/dcv.md)。利用DCV版と実OSで検証が必要 |
| Personal／Shared | 専用・共用環境、席の予約、切断後保持、明示解放、未接続予約の期限処理、HOME lease | [環境運用](operations/environments.md) |
| ストレージ・Dataset | User／Group EFS、停止中HOME移行、Scratch quota、S3 manifestによる検証付きcache | [環境運用](operations/environments.md)。HOME隔離・mountは実機受入が必要 |
| Job・使用量 | job broker、CPU／memory／IO計測、子プロセス・再起動・欠測追跡、履歴、ParquetのS3出力 | [環境運用](operations/environments.md) |
| Shared自動増減 | 依頼に応じた増設、安全なidle／drain／終了、失敗操作の隔離・再照合 | 既定OFF。全体・poolごと最大5台、1台2席。[導入と受入](deployment/organization-network.md) |
| AMI・EBS性能 | NEXT→STABLE、承認AMI／LT／SHA256、一時gp3のIOPS・throughput変更 | [組織ネットワーク](deployment/organization-network.md)。永続volumeは性能変更対象外 |
| 費用 | Cost Explorer速報・CSV、CUR取り込み、接続時間等による配賦、残額・確定管理、本人明細 | [環境運用](operations/environments.md)。実CURでの照合が必要 |
| Windows Box取り込み | 専用Windows作成、手動SSO／MFA・offline化、再開可能S3転送、独立検証、Dataset公開、cleanup | 既定OFF。Box API不使用。[手順](operations/environments.md#windows取り込み) |
| Proxy・Outbound | 認証付きHTTP／CONNECT、許可／拒否、ユーザー／グループ別ルール、専用SGへのIP／CIDR適用 | [Proxy](operations/proxy.md)、[Outbound](operations/egress.md)。HTTPS内容の復号検査は対象外 |
| Gitミラー | GitLab定期・手動同期、読取／同期権限、clone／fetch専用、Git LFS、完了待機CLI | [Gitミラー](operations/git-mirror.md)。push、artifact、submodule再帰取得は対象外 |
| 監査 | 操作・ログイン成功／失敗詳細・待ち時間・ロック／解除・ログアウトのDB記録、gzip JSONLの定期S3差分出力、永続再送 | S3出力は既定OFF。[設定](operations/audit-s3-export.md)。CloudTrail／OSログ収集は別途 |
| 表示・UI | ローカル資産のGo SSR／htmx、サイト文言・利用者マニュアル表示設定、ログイン前のCA／hosts配布、管理者向け証明書生成手順・支援ツール（最大128件、64 IP対応）、Portal Admin向けEC2作成／Golden AMI導入・配布マニュアル、ログイン前の公開CA・hosts準備スクリプト配布 | [表示設定](operations/site-customization.md)、[UI仕様](development/ui.md)。マニュアルは未実装の作成／種類選択機能を追加しない。サーバーTLSは別途設定 |
| 導入・AWS調査 | 既存／新規／混在リソース、管理者指定VPC／Subnet／TGW／IP範囲、preflight、CloudShell調査 | [組織構成](deployment/organization-network.md)、[調査ツール](../tools/aws-audit/README.md) |

## 未実装・別途準備するもの

- Group Adminへの管理委譲、任意Personal EC2の作成・削除・種類変更等は[インスタンス管理の将来拡張](operations/instances.md)を参照。
- GPU、FSx／Windows共有HOME、予測起動、Software Catalog等は現行の二段階機能の対象外。
- TOTP SecretのKMS等による暗号化、CloudTrail／VPC Flow Logsの構築、中央Firewall／DNS／TLS検査は[セキュリティ要件](architecture/overview.md)として別途準備する。
- 自動テストと実機受入の範囲は[テスト方針](development/testing.md)を参照。自動増減とImportは受入証跡なしに有効化しない。
