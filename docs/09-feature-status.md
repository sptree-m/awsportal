# 機能ステータス

> この表は作成時の旧版の調査記録です。現行の二段階機能と組織ネットワーク構成は [二段階実装](20-two-stage-completion.md) と [新規・既存・混在構成](21-organization-deployment.md) を参照してください。


凡例:
- ① 実装済み・自動テスト済み
- ② 実装済み・AWS/DCV実機未確認
- ③ 部分実装
- ④ 未実装

## ① 実装済み・自動テスト済み

| 領域 | 機能 | 主な検証 |
|---|---|---|
| 認証 | bcryptパスワード、Portal Admin TOTP、Lab限定MFA bypass | auth/main tests |
| アカウント | 一時パスワード、初回変更、30日無操作無効化、再有効化 | store/main tests |
| MFA | 複数端末、最大3台、登録/削除 | store/main tests |
| RBAC | user/group割当のEC2表示・操作制御、portal_admin全件参照 | store/main tests |
| EC2 | 状態取得、Start/Stop、未割当EC2操作拒否 | main tests |
| DCV認証 | 60秒・1回限りToken、SHA-256保存、再利用拒否 | DCV token/main tests |
| UI | Go SSR、ローカルCSS/Font、外部CDN禁止 | security/static tests |
| htmx UI | ローカルhtmx、boosted navigation、EC2行partial、遷移中のみpoll、User/MFA partial | main/security tests |
| Build | Linux ARM64 portal/admin build | CI |
| 制約試験 | ARM64 / 2 vCPU / 1 GiB相当 | CI t4g-micro-arm64 |
| IaC/Security | Terraform validate、22/3389禁止、IMDSv2、EBS暗号化 | CI |

## ② 実装済み・実機未確認

| 領域 | 機能 | 必要な実機確認 |
|---|---|---|
| Cost Explorer | ユーザーTag別コスト表示/CSV | 実AWS請求データ |
| Scheduler | 曜日・時刻Start/Stop | 実EC2で時刻跨ぎ確認 |
| DCV | External Authenticator用Tokenフロー | 実DCV Server/Client |
| htmx | Browserでのboost/partial操作 | disposable AWS Lab |

## ③ 部分実装

| 領域 | 現状 | 残課題 |
|---|---|---|
| group_admin | role/schemaは存在 | グループ管理画面・メンバー管理UI |
| Group | Portal Admin向け所属変更・割り当てUI/APIを実装・テスト済み | Group Adminへの委譲は未実装 |
| DCV OS user | 設計文書あり | OSユーザー/Session自動provision |
| Cost分類 | usage type文字列で分類 | AWSサービス別の厳密な分類ルール |
| 監査 | Portal SQLite監査あり | CloudTrail/中央保管との統合 |

## ④ 未実装

- Proxy管理画面/接続制御
- TOTP SecretのKMS等による暗号化
- CloudTrail / VPC Flow Logs / 中央ログ保管の自動構築
- 本番Outbound Allowlist/Firewall連携
- DCV Server設定とOSユーザー自動構築

## 方針

③/④のうちインフラ構成や運用要件が必要な項目は、推測で実装しない。
コード単体で安全に閉じる課題はPR内で自動テストを追加してからmainへ入れる。

## インスタンス管理追記

Portal Admin限定の無効化/再有効化、ユーザー/グループ割り当て、所属変更を実装。
Store/API/ブラウザテストで検証。OSユーザー/Session/EFS自動準備は未実装。
詳細: docs/11-instance-administration.md。
