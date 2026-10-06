# ドキュメント

文書整理でも[最優先のコンセプト](../README.md#最優先のコンセプト)の削除・弱体化は禁止します。定義は現行READMEに残し、設計・UI文書から参照します。

現行版はv1.9.0。[機能一覧](features.md)で対応範囲を確認し、目的別の手順を参照してください。コマンドは個別の`cd`指定がない限りリポジトリrootで実行します。

| 区分 | 文書 |
|---|---|
| 導入 | [構築・アカウント・EC2初期設定](deployment/setup.md) |
| 導入 | [管理者指定VPC・Subnet・TGW・IP範囲](deployment/organization-network.md)、[差し替え設定例](../terraform/examples/org-existing/README.md) |
| 設計 | [権限境界・セキュリティ要件](architecture/overview.md) |
| 運用 | [ユーザー・グループ・EC2管理](operations/instances.md) |
| 運用 | [DCV接続・アカウント同期](operations/dcv.md) |
| 運用 | [Personal／Shared・EFS・計測・費用・Windows取り込み](operations/environments.md) |
| 運用 | [パスワード復旧](operations/password-recovery.md) |
| 運用 | [監査ログのgzip付きS3出力](operations/audit-s3-export.md) |
| 運用 | [Proxy](operations/proxy.md)、[EC2 Outbound](operations/egress.md) |
| 運用 | [GitLab／Git LFSミラー](operations/git-mirror.md) |
| 運用 | [利用者マニュアル・サイト表示](operations/site-customization.md) |
| 開発 | [必須テスト・コミット・main保護](development/testing.md) |
| 開発 | [UI・htmxの仕様と検証](development/ui.md)、[画面キャプチャ](screenshots/) |
| 試験 | [v1.6.0 DCVラボの作成・試験・廃棄](development/dcv-release-test.md) |
| 調査 | [CloudShellによるAWS環境・権限調査](../tools/aws-audit/README.md) |
| 更新 | [v1.9.0リリースノート](releases/v1.9.0.md)、[過去の記録](archive/README.md) |

新しい説明は既存の手順へ追記し、機能の有無は`features.md`に集約します。旧版の詳細は固定tagで参照し、現行文書へ複製しません。追加・移動時はこの目次と参照リンクを更新してください。実装・自動テストの成功とAWS／DCV／Box実機受入は別に扱います。
