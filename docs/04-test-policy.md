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
