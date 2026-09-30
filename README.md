# awsportal

AWS上のEC2/DCV環境を管理する、イントラネット向け超軽量ポータルです。

## 基本方針
- 一般ユーザーにはAWSアカウント、IAMユーザー、AWS Console、Access Keyを付与しません。
- AWS APIはポータルEC2のInstance Profileから最小権限で実行します。
- 管理対象デスクトップへの接続経路はAmazon DCV（TCP/8443）のみに限定します。
- SSH、RDP、SCP、一般ユーザー向けSSM対話セッションは禁止します。
- 一般ユーザーは本人または所属グループに割り当てられたEC2だけを表示・操作できます。
- ポータル管理者は全管理対象EC2を表示でき、TOTPによる2要素認証を必須とします。
- 一般ユーザーのDCVではファイル転送、クリップボード、印刷、USB等を制限します。
- 管理者によるデータ持ち出しは別権限とし、監査ログを残します。

## 開発時の必須テスト
`./scripts/setup-dev.sh` を一度実行すると、Gitのpre-commit hookを有効化します。
以後は `./scripts/test-all.sh` が成功しない限りコミットできません。
`--no-verify` による回避は禁止です。GitHub側でもCIを必須チェックとして設定してください。

t4g.micro相当テストはARM64、2 vCPU、1 GiB RAMを前提とし、4 GiB swapを補助的に使用します。

詳細は `docs/` を参照してください。
