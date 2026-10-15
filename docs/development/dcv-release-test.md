# v2.1.2 CloudShell試験・廃棄

v2.1.2の配布物と同じタグのラボスクリプトを使い、一時環境の作成・DCV試験・廃棄を行います。[ラボ構成](../../lab/README.md)、[v2.1.2の変更・受入](../releases/v2.1.2.md)も確認してください。以下は実行手順であり、この文書の更新だけで実AWS／DCVの受入完了を示すものではありません。

## 前提と旧ラボ

- 管理者用CloudShellでAWS CLI、Python 3、curl、OpenSSL、tar、sha256sumを使用します。通常経路ではGitも使用します。CloudFormation、EC2、IAM、SSMの作成・照会・削除権限が必要です。一般利用者にAWS権限を付与しません。
- 試験PCからPortalの8080/TCPとDCVの指定TCPポート（既定8443）へ、CloudShellからPortalの8080/TCPへ接続できることが必要です。`ALLOWED_CIDR`は両接続元を含む承認済み範囲を指定します。ラボの既定値は`0.0.0.0/0`なので、組織の接続元制限に合わせて明示してください。
- 試験PCに[公式DCVネイティブクライアント](https://www.amazondcv.com/)と、同期しない端末固定型パスキーを使用できる認証器を準備します。Portalは固定ホスト名・信頼済みHTTPSで使用します。
- 旧ラボはUserDataの更新だけでは更新されません。対象のリージョン・スタック名を確認し、そのラボの`lab/destroy.sh`で廃棄してから新規作成します。既存環境の更新は[リリースノート](../releases/v2.1.2.md#更新受入)に従い、DB・設定・TOTP暗号鍵をバックアップします。

以下のどちらかで資材を準備し、その後の共通手順へ進みます。

## GitHubへ接続できる場合

CloudShellと新規作成するPortal／Test EC2の両方がGitHubの配布物へ接続できる場合の手順です。取得にはリダイレクト先の`release-assets.githubusercontent.com`等へのHTTPS接続も必要です。

CloudShellで以下を実行します。既存ディレクトリへ上書きせず、新しい作業ディレクトリを使います。

```bash
set -euo pipefail
umask 077
export AWS_PAGER=""
export AWS_REGION=ap-northeast-1
export STACK=awsportal-lab-v212
export BINARY_URL=https://github.com/sptree-m/awsportal/releases/download/v2.1.2/awsportal-v2.1.2-linux-arm64.tar.gz
git clone --depth 1 --branch v2.1.2 https://github.com/sptree-m/awsportal.git "$HOME/awsportal-test-v212"
cd "$HOME/awsportal-test-v212"
test "$(tr -d '[:space:]' < VERSION)" = 2.1.2
```

`lab/create.sh`の既定配布URLもv2.1.2です。配布物とソースを同じタグにそろえ、上記の`BINARY_URL`を明示して共通手順へ進んでください。

## github.comへ接続できない場合

GitHubへ接続できる管理端末でv2.1.2のソース一式・ARM64配布物・チェックサムを取得し、承認済みの経路でCloudShellへ搬入します。以下は同じリージョンの非公開S3バケットを使う例です。組織内HTTPS配布サーバーを使う場合も、Portal／Test EC2から取得可能なURLを用意してください。

配布tar.gzにはPortal・管理CLI・DCV関連ファイルが入っていますが、`lab/`は含まれません。配布物だけでは作成・廃棄スクリプトを実行できないため、同じv2.1.2タグのソース一式も必ず搬入します。両EC2のGitHub依存をなくすため、`BINARY_URL`も搬入先へ変更します。

### 接続できる管理端末で資材を取得・配置

次はBash・AWS CLIが使える端末で実行します。`TRANSFER_S3`は管理者が用意した非公開バケット内の、この試験専用のプレフィックスへ置き換えてください。取得端末に書込権限、CloudShellに読取権限と署名用の`s3:GetObject`権限が必要です。SSE-KMSを使う場合は対応するKMS権限も必要です。

```bash
(
set -euo pipefail
umask 077
export AWS_REGION=ap-northeast-1
export TRANSFER_S3=s3://YOUR-PRIVATE-BUCKET/awsportal-lab/v2.1.2
git clone --depth 1 --branch v2.1.2 https://github.com/sptree-m/awsportal.git "$HOME/awsportal-source-v212"
mkdir -m 700 "$HOME/awsportal-transfer-v212"
cd "$HOME/awsportal-transfer-v212"
ASSET=awsportal-v2.1.2-linux-arm64.tar.gz
RELEASE=https://github.com/sptree-m/awsportal/releases/download/v2.1.2
curl -fL --retry 5 "$RELEASE/$ASSET" -o "$ASSET"
curl -fL --retry 5 "$RELEASE/$ASSET.sha256" -o "$ASSET.sha256"
# 公開チェックサムはdist/付きのファイル名を含むため、ハッシュを比較する。
EXPECTED="$(awk 'NR==1 {print $1}' "$ASSET.sha256")"
[[ "$EXPECTED" =~ ^[0-9a-fA-F]{64}$ ]]
test "$EXPECTED" = "$(sha256sum "$ASSET" | awk '{print $1}')"
test "$(tar -xOf "$ASSET" VERSION | tr -d '[:space:]')" = 2.1.2
tar --exclude='./.git' -C "$HOME/awsportal-source-v212" -czf awsportal-v2.1.2-source.tar.gz .
sha256sum awsportal-v2.1.2-source.tar.gz "$ASSET" "$ASSET.sha256" > SHA256SUMS
for FILE in awsportal-v2.1.2-source.tar.gz "$ASSET" "$ASSET.sha256" SHA256SUMS; do
  aws s3 cp "$FILE" "$TRANSFER_S3/$FILE" --region "$AWS_REGION" --only-show-errors
done
)
```

S3の公開設定は不要です。資材と`SHA256SUMS`は管理された搬入経路を使い、原本の取得元とハッシュの確認記録を保持してください。

### CloudShellで搬入・URL設定

CloudShellと両EC2から対象S3のリージョナルHTTPSエンドポイントへ接続できることを確認します。取得端末と同じ`TRANSFER_S3`を指定します。

```bash
set -euo pipefail
umask 077
export AWS_PAGER=""
export AWS_REGION=ap-northeast-1
export STACK=awsportal-lab-v212
export TRANSFER_S3=s3://YOUR-PRIVATE-BUCKET/awsportal-lab/v2.1.2
mkdir -m 700 "$HOME/awsportal-transfer-v212"
cd "$HOME/awsportal-transfer-v212"
for FILE in awsportal-v2.1.2-source.tar.gz awsportal-v2.1.2-linux-arm64.tar.gz awsportal-v2.1.2-linux-arm64.tar.gz.sha256 SHA256SUMS; do
  aws s3 cp "$TRANSFER_S3/$FILE" "$FILE" --region "$AWS_REGION" --only-show-errors
done
sha256sum -c SHA256SUMS
mkdir -m 700 "$HOME/awsportal-test-v212"
tar -xzf awsportal-v2.1.2-source.tar.gz -C "$HOME/awsportal-test-v212"
cd "$HOME/awsportal-test-v212"
test "$(tr -d '[:space:]' < VERSION)" = 2.1.2
test -s lab/cloudformation.yaml
test -s lab/destroy.py

# 作成直前に発行する。一時認証情報が先に失効するとURLも失効する。
export BINARY_URL="$(aws s3 presign "$TRANSFER_S3/awsportal-v2.1.2-linux-arm64.tar.gz" --region "$AWS_REGION" --expires-in 3600)"
export BINARY_CHECKSUM_URL="$(aws s3 presign "$TRANSFER_S3/awsportal-v2.1.2-linux-arm64.tar.gz.sha256" --region "$AWS_REGION" --expires-in 3600)"
```

**署名付きURLに`.sha256`を後付けしてはいけません。** テンプレートは通常`${BinaryURL}.sha256`からチェックサムを取得しますが、署名はオブジェクトごとに異なります。次で搬入したテンプレートを保存し、Portal／Testの両取得箇所をチェックサム専用URLへ変更します。配布物のチェックサム検証は維持します。

```bash
python3 - <<'PY'
import os
from pathlib import Path
from urllib.parse import urlsplit

path = Path('lab/cloudformation.yaml')
backup = path.with_suffix('.yaml.original')
if not backup.exists():
    backup.write_bytes(path.read_bytes())
source = backup.read_text()
marker = '${BinaryURL}.sha256'
assert source.count(marker) == 2, 'Portal/Testのチェックサム取得箇所を確認してください'
url = os.environ['BINARY_CHECKSUM_URL']
assert urlsplit(url).scheme == 'https' and urlsplit(url).netloc
assert not any(c in url for c in "\"'`$\\\r\n"), 'URLに想定外の文字があります'
path.write_text(source.replace(marker, url))
PY
```

変更は搬入先の作業コピーだけに適用します。署名付きURLは取得権限を持つため、変更済みテンプレート・CloudFormationパラメーター・UserDataを共有ログやGitへ掲載しないでください。URLと署名元の認証情報が有効な間に両EC2の取得を完了させます。期限切れで作成に失敗した場合は廃棄手順で削除し、両URLを再発行してテンプレートを再生成してから新規作成します。

この方法はGitHubへの接続制限に対応します。初期導入にはUbuntuのapt／snap、DCV配布先`d1uj6qtbmh3dt5.cloudfront.net`、SSM・EC2等のAWS API、DCVライセンス用S3への接続が引き続き必要です。外部接続全体を禁止する環境では、承認済みパッケージミラー・導入済みAMI・AWSサービスへの経路を別途準備し、[Golden AMI手順](../operations/environments.md#golden-amiの作成配布更新)を参照してください。`BINARY_URL`の変更だけでは完全閉域の構築にはなりません。

## 新ラボの作成と自動試験（共通）

資材準備と同じCloudShellセッションで、承認済みの接続元範囲を`export ALLOWED_CIDR=203.0.113.0/24`の形式で設定してください。このIP範囲は説明用なので実際の範囲へ置き換えます。CloudShellと試験PCの接続元が異なる場合は、両方を許可する承認済み構成が必要です。

新しいセッションでは`AWS_REGION`・`STACK`・`BINARY_URL`を再設定し、S3経由の場合は有効な両URLと変更済みテンプレートも用意してください。`env.sh`への依存はありません。作成失敗時も「廃棄」を実行してください。

```bash
(
set -euo pipefail
umask 077
export AWS_PAGER=""
: "${AWS_REGION:?資材準備のリージョンを設定してください}"
: "${STACK:?資材準備のスタック名を設定してください}"
: "${BINARY_URL:?v2.1.2の配布URLを設定してください}"
export INSTANCE_TYPE=t4g.micro
export ALLOWED_CIDR="${ALLOWED_CIDR:?CloudShellと試験PCの接続元を含む承認済みCIDRを設定してください}"
# ポートを変更する場合は作成前に AWSPORTAL_DCV_PORT=443 等をexportする。
cd "$HOME/awsportal-test-v212"
bash lab/create.sh
bash lab/test-dcv.sh
)
```

この一時ラボはPortal／Testそれぞれt4g.micro、PortalのEBS8GiB・TestのEBS16GiB（swap2GiBを含む）、公開IPv4を使用します。NAT Gateway・ALB・EIPは作成しません。初回DCVパッケージ導入に時間がかかります。快適性を優先する場合は`INSTANCE_TYPE=t4g.small`へ変更できます（両EC2に適用）。画面確認中もEC2・EBS・IPv4料金が発生します。

`create.sh`のPASSはARM64・EC2 running・Portal healthの確認です。`test-dcv.sh`のPASSはDCVサービス・本人のOSユーザーと実セッション・ライセンス・HTTPS認証・ワンタイムトークン再利用拒否等の確認です。画面描画・入力やv2.1.2の管理操作の本人確認は、次の手動試験を別途実施します。

## ポータル・ネイティブクライアント試験

1. 作成時に表示する公開CA（`$HOME/.awsportal-lab/ap-northeast-1/awsportal-lab-v212/portal-ca.pem`）を試験PCで信頼し、表示された公開IPと`portal.awsportal.test`をhostsに登録します。[端末準備](../operations/site-customization.md#ログイン前の証明書配布とhosts設定)も参照してください。表示された`https://portal.awsportal.test:8080`を開きます。IP直打ちや証明書警告の無視ではパスキー試験を行いません。
2. `labadmin`と、作成時に表示するpassword・TOTP秘密情報を用いてログインします。TOTP認証器へ登録し、直前に成功したコードを再利用せず次のコードを使います。`test-dcv.sh`が有効化する`labdebug`もpassword＋TOTP必須です。作成時の認証情報とPortal上の`/var/lib/awsportal/lab-login.txt`は機密として管理します。初回password変更が求められた場合は変更して再ログインします。
3. パスキー追加と管理操作で、登録済みパスキーまたは管理者の現在のpassword＋TOTPによる本人確認を求めることを確認します。確認後に戻った画面でもう一度操作し、2分以内・1回限りの確認結果を使います。管理者の最初のパスキー追加にはpassword＋TOTPを使います。ブラウザーを再読込して新しい同梱JavaScriptを使い、パスキーによる管理者ログインも確認します。
4. EC2一覧から「接続」を押し、ネイティブクライアントが本人の`awp-u<ID>`セッションへ接続し、Xfceデスクトップが表示されることを確認します。DCV側の一時試験用証明書は対象EC2のものと確認して許可します。
5. DCV内の端末で`whoami`と`id`を実行し、マニュアルに表示されたOSユーザーと一致することを確認します。セッション管理上のownerはrootですが、デスクトップの実行ユーザーは本人です。sudo権限は自動付与されません。
6. Usersで通常ユーザーを作成し、登録コードを本人へ渡します。本人のPCで端末固定型パスキーを初回登録してログインし、Instance Adminで個別またはグループ割り当てを行います。同期完了後に接続し、別のOSユーザー・ホーム・DCVセッションになることを確認します。通常User／Group Adminの通常ログインはパスキーのみです。
7. 通常ユーザーのすべての個別／グループ割り当てを解除すると、次の同期でデスクトップと実行中タスクが終了します。再割り当てするとホームが保持されることを確認します。Portal Adminは割り当てなしでも全有効インスタンスへの権限を持つため、この解除試験には通常ユーザーを使ってください。
8. インスタンスを無効化した場合は、管理者を含めて接続を拒否し、同期後にセッションを終了します。再有効化すると準備後に再接続できます。
9. Instance Adminで「DCV機能・持ち出し制御」を開き、初期設定のキャプチャ・クリップボード・ファイル転送が禁止されていることを確認します。本人確認後に設定を保存し、次の同期で旧セッションが閉じ、設定版とEC2適用版が一致するまで新規接続を停止することを確認します。一般ユーザー／Group Adminでは管理画面に入れず、DCV内から設定ファイルやセッション権限を変更できないことも確認します。
10. テストEC2を停止→起動し、起動と同期を待って再接続します。公開IP変更に追従します。Portal EC2を停止するとラボ画面自体が使えなくなるので、利用者EC2を操作してください。

v2.1.1以降はCookieだけでパスキー追加・削除や保護された管理操作が成功しないこと、本人確認結果の期限切れ・別操作への転用・再利用を拒否することも確認します。監査の受入では、監査intent保存失敗時の操作拒否と、結果保存失敗時の`SECURITY audit persistence failed`・未完了`audit_operations`を専用の試験環境で確認します。詳細は[リリースノート](../releases/v2.1.2.md#検証)と[必須テスト](testing.md)を参照してください。未完了intentが残った場合は実状態を照合し、外部操作を重複実行しません。LFS・プロキシ・SG適用の実機試験には別途環境設定が必要です。

## 廃棄

新しいCloudShellセッションでも、以下で今回のラボを削除します。リージョン・スタック名・作業ディレクトリを変更した場合は実際の値へ置き換えてください。試験用管理者の無効化に失敗してもスタック削除を実行します。

```bash
(
set -euo pipefail
export AWS_PAGER=""
export AWS_REGION=ap-northeast-1
export STACK=awsportal-lab-v212
cd "$HOME/awsportal-test-v212"
bash lab/debug-auth.sh disable || true
bash lab/destroy.sh
)
```

EC2の停止だけではEBS料金が残ります。`destroy.sh`のPASSでCloudFormation削除完了・EC2終了・保存済みEBS／ネットワーク／IAM資源の消滅を確認してください。ルートEBSはDeleteOnTermination=trueです。権限不足やAPI失敗を削除成功として扱いません。発生済み料金や別の既存環境は削除対象に含まれません。

S3へ搬入した4ファイルはスタック外のため`destroy.sh`では削除されません。試験終了後に、その試験専用プレフィックスの資材を管理者が削除してください。バージョニング有効時は旧バージョンも管理対象です。既存バケット全体を削除しないでください。署名付きURLを含む作業コピーと、不要になった試験用CA・hosts登録・認証情報も片付けます。削除確認用の`~/.awsportal-lab/region/stack/`の記録は確認完了まで保持します。

## 診断

```bash
export AWS_PAGER=""
export AWS_REGION=ap-northeast-1
export STACK=awsportal-lab-v212
aws cloudformation describe-stack-events --region "$AWS_REGION" --stack-name "$STACK" --output table
aws ssm describe-instance-information --region "$AWS_REGION" --output table
```

管理者のSSMで、Portal／Test EC2の`cloud-init status --long`と`/var/log/cloud-init-output.log`を確認します。Portalは`systemctl status awsportal nginx`・`journalctl -u awsportal`、Testは`systemctl status dcvserver awsportal-dcv-agent`・`journalctl -u awsportal-dcv-agent`・`dcv list-sessions`を確認します。S3経由の403では両URLの期限・署名元の一時認証情報・S3／KMS権限を確認します。チェックサムURLへの`.sha256`後付け、GitHub URLの残存、apt／snap／DCV配布先への接続制限も切り分けます。認証キー、password、TOTP秘密情報、署名付きURL、DCVトークンは共有ログへ貼り付けないでください。

## ネイティブ接続の制限

Web Viewerを削除し、DCVの`allowed-ws-origin-regex="^$"`で非空OriginのWebSocket接続を拒否します。ポータルは`dcv://`のみ発行します。ブラウザーからDCVのHTTPS URLを開いてもWeb Viewerを利用できないことを確認してください。外部Web SDKを含む実接続の拒否とネイティブ接続の成功は実機で確認が必要です。単なる画面非表示の試験はSDK拒否の証明にはなりません。

DCV認証にはクライアント種別を証明する情報がなく、Originを付けない独自クライアントやOriginを除去する中継まで識別できません。公式ネイティブアプリだけへの厳密な限定には管理端末のアプリ実行・通信制限が必要です。既存環境のAgent更新は[DCV運用](../operations/dcv.md)に従い、Portalと同じリリースのファイルを使ってください。
