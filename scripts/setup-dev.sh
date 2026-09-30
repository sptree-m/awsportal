#!/usr/bin/env bash
set -euo pipefail
git config core.hooksPath .githooks
chmod +x .githooks/pre-commit scripts/*.sh tests/*.sh
echo '開発環境を設定しました。以後、コミット前に必須テストを自動実行します。'
