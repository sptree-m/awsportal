# ドキュメント

現行版の機能は[機能一覧](features.md)、v1.9.0の変更は[リリースノート](releases/v1.9.0.md)を参照してください。実装・自動テストの成功と、AWS／DCV／Box実機での受入は別に扱います。

コマンドは、個別の`cd`指定がない限りリポジトリのルートで実行します。

## 目的から探す

| 目的 | 読む順序 |
|---|---|
| 初めて導入する | [構築手順](deployment/build-runbook.md) → [組織ネットワーク](deployment/organization-network.md) → [初期設定](deployment/initial-setup.md) |
| 管理者指定のVPC・Subnet・TGW・IP範囲を使う | [組織ネットワーク](deployment/organization-network.md) → [差し替え可能なTerraform例](../terraform/examples/org-existing/README.md) |
| ユーザーやEC2の権限を管理する | [運用概要](operations/administration.md) → [割り当て・無効化](operations/instances.md) |
| デスクトップへ接続する | [DCV設定・アカウント同期](operations/dcv.md) |
| Shared・EFS・Dataset・費用配賦・Windows取り込みを導入する | [環境運用](operations/environments.md) → [組織ネットワーク](deployment/organization-network.md) |
| 通信先を制限する | [Proxy](operations/proxy.md) → [EC2のOutbound](operations/egress.md) |
| 監査ログをS3に保存する | [gzip付き定期出力](operations/audit-s3-export.md) |
| 開発・変更を検証する | [テスト方針](development/testing.md) → [UI仕様](development/ui-visual-contract.md) |

## 導入・設計

- [構築・導入手順](deployment/build-runbook.md)
- [管理者指定ネットワークと新規／既存／混在構成](deployment/organization-network.md)
- [ユーザー・EC2の初期設定](deployment/initial-setup.md)
- [アーキテクチャと権限境界](architecture/overview.md)
- [CloudShellによるAWS環境・権限調査](../tools/aws-audit/README.md)

## 運用

- [運用者向け概要](operations/administration.md)
- [インスタンス管理とアクセス割り当て](operations/instances.md)
- [DCV接続とポータルアカウント同期](operations/dcv.md)
- [Personal／Shared・ストレージ・計測・費用配賦・Windows取り込み](operations/environments.md)
- [パスワード復旧](operations/password-recovery.md)
- [セキュリティと監査の要件](operations/security.md)
- [監査ログのS3定期出力](operations/audit-s3-export.md)
- [認証付きHTTP／HTTPSプロキシ](operations/proxy.md)
- [EC2のOutbound制御](operations/egress.md)
- [GitLab／Git LFS取得専用ミラー](operations/git-mirror.md)
- [利用者マニュアルとサイト表示設定](operations/site-customization.md)

## 開発・試験

- [自動テスト・コミット・main保護](development/testing.md)
- [htmx画面の応答仕様](development/htmx-console.md)
- [UIの表示・通信量仕様](development/ui-visual-contract.md)
- [v1.6.0使い捨てDCVラボの試験・廃棄](development/dcv-release-test.md)
- [画面キャプチャ](screenshots/)

## 履歴

- [v1.9.0](releases/v1.9.0.md)
- [v1.7.0-rc.1](releases/v1.7.0-rc.1.md)
- [v1.3.0](releases/v1.3.0.md)
- [旧版の調査・固定pilot記録](archive/README.md)

## 文書を更新するとき

現行手順は`deployment/`・`operations/`、開発ルールは`development/`、設計の共通原則は`architecture/`へ追記します。機能の有無は`features.md`にまとめ、同じ機能表を別文書へ複製しません。バージョン固有の差分は`releases/`、過去の調査記録は`archive/`へ置きます。新しい文書を追加・移動したら、この目次と参照リンクも更新してください。
