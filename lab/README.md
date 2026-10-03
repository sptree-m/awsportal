# Disposable AWS lab

CloudShellで実験環境を作成し、テスト後にスタックと有料資源を廃棄します。

v1.6.0はDCV・軽量デスクトップ・ポータルアカウント同期に対応します。[CloudShell作成・試験・廃棄の手順](../docs/17-dcv-release-test.md) を参照してください。旧ラボは廃棄して作り直します。

```bash
export AWS_REGION=ap-northeast-1
export STACK=awsportal-lab-v160
bash lab/create.sh
bash lab/test-dcv.sh
# ブラウザ試験が終わったら
bash lab/destroy.sh
```

専用VPC / public subnet / IGW / route table / Portal SG / Test SG / Portal・Test IAM role + profile / t4g.micro x2 / encrypted gp3 8GiB + 16GiBを作成します。NAT Gateway・ALB・EIPは作成しません。SSMは管理者の導入・試験用で、利用者にAWS権限を付与しません。

一時ラボでは8080/TCP（Portal）と8443/TCP（DCV）をALLOWED_CIDR（既定0.0.0.0/0）に許可します。CloudShellのヘルス確認とブラウザ接続元の両方を許可する必要があります。Portal内部認証用8444/TCPはTest SGからだけ許可します。認証通信は専用証明書を検証するHTTPSです。ブラウザ画面用PortalはHTTP、DCVは自己署名HTTPSで、一時試験専用です。

create.shはARM64・EC2 running・Portal healthを検証します。test-dcv.shは実DCVサービス・OSユーザー／仮想セッション・ポータルトークンによる外部認証と再利用拒否を検証します。画面描画と入力はDCVネイティブクライアントで確認してください。

作成時にEC2とEBSのIDを ~/.awsportal-lab/region/stack/ に記録します。destroy.shはAPIの権限不足や取得失敗を成功扱いせず、CloudFormation削除完了、EC2終了、保存済みEBSとネットワーク・IAM資源の消滅を検査します。terminated EC2は履歴として扱います。ホーム上の検査記録は有料AWS資源ではありません。
