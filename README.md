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

## UI設計原則
- 超軽量・高速を最優先し、Go SSR + HTML + 自前CSSを基本とします。
- Bootstrap、Tailwind、React、Vue、jQuery、Google Fonts、CDNなどの外部UIリソースを使用しません。
- 1366x768のPC、小さいブラウザウィンドウ、狭幅画面でも主要操作が画面外へ消えないことを必須とします。
- 横長の一覧はページ全体ではなく一覧領域だけを横スクロール可能にします。
- 見栄えのためだけのJavaScriptや外部ダウンロード依存を追加しません。
- CIでHTML/CSSの外部URL、外部script、CSS importを検出して拒否します。

## 開発時の必須テスト
`./scripts/setup-dev.sh` を一度実行すると、Gitのpre-commit hookを有効化します。
以後は `./scripts/test-all.sh` が成功しない限りコミットできません。
`--no-verify` による回避は禁止です。GitHub側でもCIを必須チェックとして設定してください。

t4g.micro相当テストはARM64、2 vCPU、1 GiB RAMを前提とし、4 GiB swapを補助的に使用します。

詳細は `docs/` を参照してください。

## GitLab取得専用ミラー

Portalで同期対象・間隔・読み取り/同期権限を設定し、利用者EC2から `awsportal-mirror sync ID --wait` で同期完了を待てます。GitLab資格情報はPortalだけに保持し、ミラーはclone/fetchとGit LFSの読み取りだけを提供します。利用者EC2はGit LFSをインストールし、LFS配布用に管理画面のポータルHTTPS URLを設定してください。初期設定と自動化例は [Gitミラー運用](docs/14-git-mirror.md) を参照してください。

## 利用者マニュアル・サイト表示設定

ログイン後の「利用者マニュアル」でプロキシ設定とGitミラーの利用・自動化手順を確認できます。Portal Adminの「表示設定」から、左上のサイト名・説明、ホームの見出し・お知らせ、ログイン案内、問い合わせ案内、マニュアルの接続先を変更できます。設定はDBに保存します。[表示設定の運用](docs/15-site-customization.md) を参照してください。

DCV接続・アカウント同期・v1.6.0ラボ試験は [DCV試験手順](docs/17-dcv-release-test.md) を参照してください。

1.3.0への更新とLFS準備は [リリース手順](docs/16-release-1.3.0.md) を参照してください。
