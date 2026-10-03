# v1.4.1 CloudShell試験・廃棄

## 旧ラボ

v1.3.0のEC2にはDCVがありません。UserDataの更新だけでは導入されないため、旧ラボを先に廃棄します。以前の手順で作ったスタック名が `awsportal-lab-v130` の場合:

```bash
export AWS_PAGER=""
aws cloudformation delete-stack --region ap-northeast-1 --stack-name awsportal-lab-v130
aws cloudformation wait stack-delete-complete --region ap-northeast-1 --stack-name awsportal-lab-v130
```

別名で作成した場合は実際のラボスタック名を指定してください。

## 新ラボの作成と自動試験

CloudShellで以下を実行します。env.shへの依存はありません。作成失敗時も廃棄コマンドを実行してください。

```bash
(
set -euo pipefail
umask 077
export AWS_PAGER=""
export AWS_REGION=ap-northeast-1
export STACK=awsportal-lab-v141
export INSTANCE_TYPE=t4g.micro
export ALLOWED_CIDR=0.0.0.0/0
export BINARY_URL=https://github.com/sptree-m/awsportal/releases/download/v1.4.1/awsportal-v1.4.1-linux-arm64.tar.gz
git clone --depth 1 --branch v1.4.1 https://github.com/sptree-m/awsportal.git "$HOME/awsportal-test-v141"
cd "$HOME/awsportal-test-v141"
bash lab/create.sh
bash lab/test-dcv.sh
)
```

この一時ラボはPortal/Testそれぞれt4g.micro、PortalのEBS8GiB・TestのEBS16GiB（swap2GiBを含む）、公開IPv4を使用します。NAT Gateway・ALB・EIPは作成しません。初回DCVパッケージ導入に時間がかかります。快適性を優先する場合は `INSTANCE_TYPE=t4g.small` を指定できます（両EC2に適用）。画面確認中もEC2・EBS・IPv4料金が発生します。

## ブラウザ試験

1. 作成コマンドが表示するPortal URLを開きます。
2. `labdebug` と `debug_password=` のパスワードでログインします。OTP不要です。`labadmin` はOTPが必要です。
3. `lab/test-dcv.sh` が表示するDCV URLを開き、ラボEC2の自己署名証明書を確認して許可します。
4. ポータルのEC2一覧から「接続」を押します。ブラウザDCVが本人の `awp-u<ID>` セッションへ接続し、Xfceデスクトップが表示されることを確認します。
5. DCV内の端末で `whoami` と `id` を実行し、マニュアルに表示されたOSユーザーと一致することを確認します。sudo権限は自動付与されません。
6. 通常ユーザーも試す場合、Usersで作成→本人ログインで初回パスワード変更→Instance Adminで個別またはグループ割り当て→同期完了後に接続します。別のOSユーザー・ホーム・DCVセッションになることを確認します。
7. 通常ユーザーのすべての個別／グループ割り当てを解除すると、次の同期でデスクトップと実行中タスクが終了します。再割り当てするとホームが保持されることを確認します。Portal Adminは割り当てなしでも全有効インスタンスへの権限を持つため、この解除試験には通常ユーザーを使ってください。
8. インスタンスを無効化した場合は、管理者を含めて接続を拒否し、同期後にセッションを終了します。再有効化すると準備後に再接続できます。
9. テストEC2を停止→起動し、起動と同期を待って再接続します。公開IP変更に追従します。Portal EC2を停止するとラボ画面自体が使えなくなるので、利用者EC2を操作してください。

自動試験はDCVサービス・実セッション・認証通信を確認します。画面描画と操作はブラウザで最終確認してください。LFS・プロキシ・SG適用の実機試験には別途環境設定が必要です。

## 廃棄

新しいCloudShellセッションでも、以下で今回のラボを削除します。デバッグ無効化の成否によらずスタックを削除します。

```bash
(
set -euo pipefail
export AWS_PAGER=""
export AWS_REGION=ap-northeast-1
export STACK=awsportal-lab-v141
cd "$HOME/awsportal-test-v141"
bash lab/debug-auth.sh on || true
bash lab/destroy.sh
)
```

EC2の停止だけではEBS料金が残ります。スタックを削除し、destroy.shのPASSを確認してください。作成したEC2のルートEBSはDeleteOnTermination=trueです。Test用SSM IAMロール、Portal IAM、ネットワークもスタックとともに削除します。発生済み料金や別の既存環境は削除対象に含まれません。

## 診断

```bash
export AWS_REGION=ap-northeast-1
export STACK=awsportal-lab-v141
aws cloudformation describe-stack-events --stack-name "$STACK" --output table
aws ssm describe-instance-information --output table
```

SSMでTest EC2上の `cloud-init status --long`、`systemctl status dcvserver awsportal-dcv-agent`、`journalctl -u awsportal-dcv-agent`、`dcv list-sessions` を確認します。認証キー、ユーザーパスワード、DCVトークンは共有ログへ貼り付けないでください。
