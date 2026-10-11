# AWS環境・権限調査ツール（CloudShell）

Policy Simulator権限なしで、組織から提供されたVPC/Subnet/TGW/ルートと、自分の要求に対する権限の証拠を収集します。AWS CLI、Python 3標準ライブラリのみを使用します。リソース作成・変更・削除を実行しません。

## 実行

既存のawsportalチェックアウトを最新のmainに更新して、通常のCloudShellで実行します。

```bash
cd ~/awsportal
bash tools/aws-audit/run.sh --regions ap-northeast-1
```

未取得の場合：

```bash
git clone --depth 1 https://github.com/sptree-m/awsportal.git
cd awsportal
bash tools/aws-audit/run.sh --regions ap-northeast-1
```

デフォルトはCloudShellの現在リージョン（未設定ならus-east-1）のみです。複数リージョンは `--regions ap-northeast-1 us-east-1`、全有効リージョンは `--all-regions` で指定します。全リージョンの調査は時間がかかります。APIごとの総タイムアウトは60秒で、`--timeout 120` などに変更できます。ページングはAWS CLIに任せ、途中でタイムアウトした場合はUNKNOWNにします。

最後に表示される `Download path: /home/cloudshell-user/aws-environment-audit-....zip` を、CloudShellの **Actions → Download file** に入力します。VPC CloudShellではこのダウンロード機能を使用できません。

## 作成・削除などのDryRun調査

対象が不明な状態で適当なIDを補完すると誤判定するため、変更系操作は初期状態ではUNKNOWNです。`dry-run.example.json` をコピーし、**自分の組織の実際のリージョン・AZ・対象ID・必須タグ・暗号鍵・要求条件**に置き換えてください。

```bash
cp tools/aws-audit/dry-run.example.json ~/aws-audit-requests.json
# CloudShellエディター等で内容を置き換える
bash tools/aws-audit/run.sh --regions ap-northeast-1 --dry-run-config ~/aws-audit-requests.json
```

JSONは配列で、各項目に `operation`, `region`, `scenario`, `resource`, `parameters` が必要です。`operation` はAWS CLIのEC2操作名、`parameters` はその操作の `--cli-input-json` 形式です。`DryRun` は指定できません。ツールが常に `DryRun: true` を注入します。サポートしていない操作の指定、選択外リージョン、DryRunの上書きは拒否します。

対応操作は `audit.py` の `DRY` に明示しています。EC2インスタンスの作成/削除/起動/停止、EBS作成/削除/接続/切断、VPC/Subnet/SG/RouteTable/Snapshot/AMI/タグなどです。インスタンス作成はAMI、サブネット、SG、インスタンスタイプ、必須タグ、Instance Profile等を含む実際の起動要求で指定してください。`RunInstances` の成功だけで個別の `iam:PassRole` を許可と判定しません。

同じ比較シナリオの作成・削除に同じ `scenario` を付けると、非対称性レポートで比較します。`resource` は人が対象を識別する説明で、権限評価に使うIDは `parameters` に設定します。タグ付きで作る権限と、既存の別リソースを消す権限は異なります。**比較結果は新規作成したリソースの削除可否を保証しません。** 例のプレースホルダーを残した要求が失敗しても、権限拒否とは限りません。

## 結果ファイル

| ファイル | 内容 |
|---|---|
| `PERMISSION_MATRIX.csv` | 読取API、指定DryRun、対象操作カタログの未確認項目。対象・リージョン・要求SHA256・証拠パス・時刻付き |
| `DRYRUN_RESULTS.csv` | 指定された要求のDryRun結果 |
| `DENIED_ACTIONS.csv` | 当該要求がAccessDenied/UnauthorizedOperation等で拒否された証拠 |
| `UNKNOWN_ACTIONS.csv` | 安全に確認できない操作、対象未指定、通信・構文・サービスエラー |
| `PERMISSION_ASYMMETRY.csv` | 作成/削除要求の比較。未確認項目も含む |
| `CREATE_BUT_CANNOT_DELETE.csv` | 作成ALLOW、削除DENYの**潜在的な**非対称性。両側の対象・証拠を併記 |
| `POLICY_VISIBILITY.csv` | IAM/Organizationsの読取可否 |
| `AWSPORTAL_REQUIREMENTS.csv` | Portalで使うサービスの操作別証拠。機能の実行成功を保証するものではない |
| `CLOUDTRAIL_PERMISSION_HISTORY.csv` | 明示指定した場合のみ、現在のSTS principal/sessionと一致する過去の履歴 |
| `ENVIRONMENT_SUMMARY.json`, `SUMMARY.txt` | 現在のidentity、結果件数、判定限界 |
| `raw/` | 有効なJSON形式の標準出力（失敗時は空）、別ファイルの標準エラー、実行要求・終了コード |

CSVはUTF-8 BOM付きで、Excel数式になる先頭文字を無害化しています。Read APIの成功はその要求の成功を表すだけです。STS GetCallerIdentityの成功は、他の権限が付与されている証拠にはなりません。

DryRunは `DryRunOperation` の場合だけALLOW、明確な認可エラーの場合だけDENYです。存在しないID、無効なパラメータ、タイムアウト、予想外の成功などはUNKNOWNです。DryRunでALLOWでも、サービスクォータ、依存サービス/KMS、リソース状態などにより本実行は失敗することがあります。

自分のRole/User、ユーザーのグループ、Managed/Inline Policy、Permissions Boundaryを読める範囲で収集します。Organizationsはアカウント→OU→Rootを辿り、各階層のSCP/RCPと文書を読める範囲で収集します。見えない階層は未確認です。文書から実効権限を推測してALLOWへ変換しません。Session Policy、Resource Policy、Endpoint Policy等を含む完全な権限評価や全AWS Action列挙には対応しません。カタログ外の操作も未確認です。

CloudTrail補助履歴は `--cloudtrail-days 7` で取得します（最大90日）。別ユーザー/別セッションの実績は自分の権限へ転用しません。過去の成功/失敗も現在のALLOW/DENYに変換しません。イベント履歴は管理イベント中心で、S3オブジェクト等のデータイベントを網羅しません。

秘密値を取得するAPI、VPN設定（PSKを含む）、EC2 user-data、Lambda環境変数、CloudFormationパラメータは取得しません。ただし、要求ファイルやタグ、ポリシー、明示指定したCloudTrail履歴に機密情報が含まれる場合があります。要求に秘密を入れず、ZIPは構成・権限情報として扱い、リポジトリへコミットしないでください。出力は専用の新規ディレクトリに保存し、既存ファイルを上書きしません。

## テスト

```bash
python3 -m unittest discover -s tests -p 'test_aws_audit.py'
bash -n tools/aws-audit/run.sh
```

モックAWS CLIで、DryRun強制、未対応操作拒否、UNKNOWN分類、Policy/Boundary/グループと組織階層の収集、履歴のidentity絞込み、非対称性CSVとZIPを検証します。実環境の権限はCloudShellでの実行結果により確認してください。

参考: [EC2エラーコード](https://docs.aws.amazon.com/ec2/latest/devguide/errors-overview.html)、[CloudShellでのファイルダウンロード](https://docs.aws.amazon.com/cloudshell/latest/userguide/getting-started.html)、[IAMポリシー評価](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic.html)。
