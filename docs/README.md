# ドキュメント

文書整理でも[最優先のコンセプト](../README.md#最優先のコンセプト)の削除・弱体化は禁止します。定義は現行READMEに残し、設計・UI文書から参照します。

現行版はv2.1.1。[機能一覧](features.md)で対応範囲を確認し、目的別の手順を参照してください。コマンドは個別の`cd`指定がない限りリポジトリrootで実行します。

| 区分 | 文書 |
|---|---|
| 導入 | [構築・アカウント・EC2初期設定](deployment/setup.md) |
| 導入 | [ローカルHTTPSの設定](deployment/setup.md#ローカルhttpsと初回端末準備)、[ログイン前のCA証明書・hosts更新スクリプト配布](operations/site-customization.md#ログイン前の証明書配布とhosts設定) |
| 導入 | [管理者指定VPC・Subnet・TGW・IP範囲](deployment/organization-network.md)、[差し替え設定例](../terraform/examples/org-existing/README.md) |
| 設計 | [権限境界・セキュリティ要件](architecture/overview.md) |
| 運用 | [ユーザー・グループ・EC2管理](operations/instances.md) |
| 運用 | [DCV接続・アカウント同期](operations/dcv.md) |
| 運用 | [Personal／Shared・EFS・計測・費用・Windows取り込み](operations/environments.md) |
| 設計 | [認証仕様・ロール別方式・同期禁止](architecture/authentication.md) |
| 運用 | [パスキー初回登録・複数PC・管理者リセット](operations/passkeys.md) |
| 運用 | [認証・パスキー・管理者パスワード復旧](operations/password-recovery.md) |
| 運用 | [監査ログのgzip付きS3出力](operations/audit-s3-export.md) |
| 運用 | [Proxy](operations/proxy.md)、[EC2 Outbound](operations/egress.md) |
| 運用 | [GitLab／Git LFSミラー](operations/git-mirror.md) |
| 運用 | [利用者マニュアル・サイト表示](operations/site-customization.md) |
| 管理者マニュアル | [EC2外部作成・種類選択・Portal登録](operations/instances.md#ポータル外でのec2作成と種類選択)、[Golden AMI構築・検証・配布・更新](operations/environments.md#golden-amiの作成配布更新)。ポータル内掲載先は[表示設定](operations/site-customization.md#管理者マニュアル) |
| 開発 | [必須テスト・コミット・main保護](development/testing.md) |
| 開発 | [UI・htmxの仕様と検証](development/ui.md)、[画面キャプチャ](screenshots/) |
| 試験 | [v2.1.1 DCVラボの作成・試験・廃棄、GitHub接続制限時の資材搬入](development/dcv-release-test.md) |
| 調査 | [CloudShellによるAWS環境・権限調査](../tools/aws-audit/README.md) |
| 更新 | [v2.1.1リリースノート・受入](releases/v2.1.1.md)、[v2.1.0の移行](releases/v2.1.0.md)、[v2.0.0](releases/v2.0.0.md)、[v1.9.2リリースノート](releases/v1.9.2.md)、[v1.9.1](releases/v1.9.1.md)、[v1.9.0](releases/v1.9.0.md)、[過去の記録](archive/README.md) |

新しい説明は既存の手順へ追記し、機能の有無は`features.md`に集約します。旧版の詳細は固定tagで参照し、現行文書へ複製しません。追加・移動時はこの目次と参照リンクを更新してください。実装・自動テストの成功とAWS／DCV／Box実機受入は別に扱います。
