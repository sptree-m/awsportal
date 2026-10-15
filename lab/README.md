# Disposable AWS lab

CloudShellで実験環境を作成し、テスト後にスタックと有料資源を廃棄します。

create.shの既定の配布物はv2.0.0です。現行v2.1.1の試験では、同じタグのソースを使って`BINARY_URL`を明示してください。DCV・軽量デスクトップ・ポータルアカウント同期・接続ポート指定に対応します。[v2.1.1のCloudShell作成・試験・廃棄、GitHub接続制限時の資材搬入](../docs/development/dcv-release-test.md) を参照してください。旧ラボは廃棄して作り直します。

```bash
export AWS_REGION=ap-northeast-1
export STACK=awsportal-lab-v211
export BINARY_URL=https://github.com/sptree-m/awsportal/releases/download/v2.1.1/awsportal-v2.1.1-linux-arm64.tar.gz
bash lab/create.sh
bash lab/test-dcv.sh
# ブラウザ試験が終わったら
bash lab/destroy.sh
```

専用VPC / public subnet / IGW / route table / Portal SG / Test SG / Portal・Test IAM role + profile / t4g.micro x2 / encrypted gp3 8GiB + 16GiBを作成します。NAT Gateway・ALB・EIPは作成しません。SSMは管理者の導入・試験用で、利用者にAWS権限を付与しません。

一時ラボでは8080/TCP（Portal）と指定TCPポート（DCV、既定8443）をALLOWED_CIDR（既定0.0.0.0/0）に許可します。CloudShellのヘルス確認とブラウザ接続元の両方を許可する必要があります。Portal内部認証用8444/TCPはTest SGからだけ許可します。認証通信は専用証明書を検証するHTTPSです。Portalは固定ホスト名 `portal.awsportal.test` と自己署名HTTPS（8080）を使用します。create.shが表示する公開CAを試験PCで信頼し、表示されたIPとホスト名をhostsへ登録してください。`LAB_PORTAL_HOST`で名前を変更できます。ラボはDCV Agent用の経路制限も行うnginxを使い、Portalに `AWSPORTAL_HTTP_MODE=http` を明示して内部HTTP待受とします。通常の導入は[Go単体のHTTPS](../docs/deployment/setup.md#go単体のhttpsデフォルト)が既定です。DCVも一時試験用HTTPSです。認証の迂回は提供せず、管理者password＋TOTPまたは同期しないパスキーを使用します。

create.shはARM64・EC2 running・Portal healthを検証します。test-dcv.shは実DCVサービス・OSユーザー／仮想セッション・ポータルトークンによる外部認証と再利用拒否を検証します。画面描画と入力はDCVネイティブクライアントで確認してください。

作成時にEC2とEBSのIDを ~/.awsportal-lab/region/stack/ に記録します。destroy.shはAPIの権限不足や取得失敗を成功扱いせず、CloudFormation削除完了、EC2終了、保存済みEBSとネットワーク・IAM資源の消滅を検査します。terminated EC2は履歴として扱います。ホーム上の検査記録は有料AWS資源ではありません。

DCVポートを変更する場合は`AWSPORTAL_DCV_PORT=443 bash lab/create.sh`を使用します。CloudFormationの`DCVPort`をPortal・DCVインストーラー・SG・接続先出力へ共通で渡します。旧リリースの配布物はポート指定に未対応です。

## v2.1.1の認証試験

`debug-auth.sh enable|disable` は試験用管理者の有効／無効だけを変更し、TOTPを省略しません。`test-dcv.sh` は管理者password＋TOTPでDCV連携を自動試験します。配布PCのパスキーはCA・hosts準備後に「パスキー」で登録します。パスキー追加・削除や保護された管理操作では、登録済みパスキーまたは管理者password＋TOTPによる操作単位の本人確認が必要です。一般User／Group Adminを作成すると登録コードが表示され、各PCで登録してからログインします。[認証仕様](../docs/architecture/authentication.md)と[運用](../docs/operations/passkeys.md)を参照してください。
