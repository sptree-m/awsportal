# 自動テスト・コミットポリシー

## 原則
変更はコミット前に必須テストを実行し、1件でも失敗した状態ではコミットしません。

## ローカル強制
初回に `./scripts/setup-dev.sh` を実行します。Gitの `core.hooksPath` を `.githooks` に設定し、`git commit` 直前に `./scripts/test-all.sh` を自動実行します。

`git commit --no-verify` は運用上禁止します。Git hook自体は技術的に迂回可能なので、サーバー側でもmainブランチのRuleset/Branch ProtectionでCI成功を必須にします。

## CI
PRとpushの双方でGoテスト、gofmt、ARM64ビルド、Terraform fmt/validate、静的セキュリティ試験を実行します。ARM64 runnerではt4g.micro相当試験も実行します。

## t4g.micro相当試験
ARM64、2 vCPU、1 GiB RAMを対象とし、systemdの `MemoryMax=1G` と `CPUQuota=200%` をテストプロセスへ設定します。4 GiB swapは実t4g.microステージング環境で設定し、安全弁として確認します。

CIの制約試験は実EC2そのものではありません。リリース候補は実t4g.microで連続稼働、メモリ、swap、CPU Credit、DCV連携を確認してから本番へ進めます。

## main保護
mainへの直接pushを禁止しPRを必須化します。必須Status Checkとして `unit-security-terraform` と `t4g-micro-arm64` を指定し、可能なら管理者による迂回も禁止します。

## v2.0.0 認証試験

署名付きWebAuthn応答で全ロールのパスキーログインを確認し、User／Group Adminのpassword＋TOTPを拒否、Portal Adminの従来方式を継続する。UVなし、BE／BS、Origin／RP ID／challenge／署名／counter不正、challenge再利用、登録コード再利用／期限切れ、リセットとセッション／完了処理の競合、無効化／期限／ロック／待ち時間を検証する。Chromium仮想認証器で実template・CSP・同梱JavaScriptの登録からログインまでを検証する。
