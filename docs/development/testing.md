# 自動テスト・コミットポリシー

## 原則
変更はコミット前に必須テストを実行し、1件でも失敗した状態ではコミットしません。

## ローカル強制
初回に `./scripts/setup-dev.sh` を実行します。Gitの `core.hooksPath` を `.githooks` に設定し、`git commit` 直前に `./scripts/test-all.sh` を自動実行します。

`git commit --no-verify` は運用上禁止します。Git hook自体は技術的に迂回可能なので、サーバー側でもmainブランチのRuleset/Branch ProtectionでCI成功を必須にします。

## CI
PRとpushの双方でGoテスト、gofmt、ARM64ビルド、Terraform fmt/validate、静的セキュリティ試験を実行します。ARM64 runnerではt4g.micro相当試験も実行します。

## Goのバージョンと更新

Goの基準版は`go.mod`の`go`行（現在1.27.2）に集約します。Goを使うすべてのGitHub Actionsは`go-version-file: go.mod`から同じ版を取得します。開発環境も基準版を使い、検証時は`go version`を確認します。

毎月およびリリース前に[Go公式リリース履歴](https://go.dev/doc/devel/release)を確認します。Goの公式サポート対象は最新の2系列です。awsportalではそのうち最も新しい系列の最新安定パッチを基準とし、利用構成に影響するセキュリティ修正は定期更新を待たず取り込みます。新しい系列への移行でも以下を確認します。

- `go.mod`と構築手順の基準版を更新し、`go mod tidy`の差分を確認する。
- `./scripts/test-all.sh`、ブラウザーの認証・画面試験、ARM64のt4g.micro相当試験を成功させる。
- 実t4g.microで認証、AWS操作、DCV連携、メモリ・CPUを確認する。

Go標準ライブラリは配布バイナリに組み込まれます。ビルド環境やOSの更新だけでは稼働中のPortalは修正されません。新しいrelease tagでPortalと管理CLIを再ビルド・配布し、DBのバックアップと切り戻しを準備して差し替え、サービス再起動後に受入確認します。

## t4g.micro相当試験
ARM64、2 vCPU、1 GiB RAMを対象とし、systemdの `MemoryMax=1G` と `CPUQuota=200%` をテストプロセスへ設定します。4 GiB swapは実t4g.microステージング環境で設定し、安全弁として確認します。

CIの制約試験は実EC2そのものではありません。リリース候補は実t4g.microで連続稼働、メモリ、swap、CPU Credit、DCV連携を確認してから本番へ進めます。

## main保護
mainへの直接pushを禁止しPRを必須化します。必須Status Checkとして `unit-security-terraform` と `t4g-micro-arm64` を指定し、可能なら管理者による迂回も禁止します。

## v2.0.0 認証試験

署名付きWebAuthn応答で全ロールのパスキーログインを確認し、User／Group Adminのpassword＋TOTPを拒否、Portal Adminの従来方式を継続する。UVなし、BE／BS、Origin／RP ID／challenge／署名／counter不正、challenge再利用、登録コード再利用／期限切れ、リセットとセッション／完了処理の競合、無効化／期限／ロック／待ち時間を検証する。Chromium仮想認証器で実template・CSP・同梱JavaScriptの登録からログインまでを検証する。
