# DCV

v1.4.0からUbuntu 24.04 ARM64/x86_64のDCV自動導入、ポータル割り当てに連動するOSユーザー・仮想セッション同期に対応します。

- `install.sh`: DCV / Xdcv / Web Viewer / Xfceとsystemdサービス
- `agent.py`: root専用の同期・外部認証仲介。EC2専用キーと検証済みHTTPSを使用
- `desktop.sh`: 本人の権限でXfceを起動
- `test_agent.py`: 作成・終了・再割り当て・既存ユーザー保護・通信障害の自動テスト

導入・設定・権限・構成図は [DCV接続手順](../docs/operations/dcv.md) を参照してください。既存環境へはdcv.confとSG設定を確認して適用します。
