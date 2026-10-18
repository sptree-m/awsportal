# awsportal 本番環境立ち上げマニュアル

版：1.1／対象アプリケーション：v2.1.3／対象：AWS基盤管理者・Portal管理者

この文書は、既存リソースや組織の指定値を優先し、awsportalと管理対象EC2・Amazon DCVを本番稼働させるための作業手順です。作業場所、設定の意味、確認方法、次へ進める条件を順に記載します。

## 1. 今回の前提と、このマニュアルの使い方

| 項目 | 今回の方針／調整条件 |
|---|---|
| リージョン | 東京 `ap-northeast-1` を例とする。組織指定があれば置き換える |
| 利用経路 | 本書の具体例はVPNなしの公開IPv4構成。Portalと固定DCV EC2のEIPへ接続する |
| 接続元 | 付属テンプレートの例は全IPv4 `0.0.0.0/0`。対象ポートはPortal TCP/443・DCV TCP/8443のみ。組織の指定値があればそちらに合わせる |
| 名前と証明書 | 公開ドメイン購入なし。固定内部名とMacのhosts、専用CAまたは組織CAを使用する |
| 初期規模 | Portal 1台・DCV 1台。人数・アプリ・CPUアーキテクチャの要件でサイジングする |
| データと可用性 | 単一AZは停止・障害時の中断を許容する構成。必要なRPO/RTOや可用性を満たせなければ設計を変更する |
| 秘密情報 | パスワード、TOTP、Agentキー、証明書秘密鍵をチャット・IaC・UserData・変更セット・共有ログへ記載しない |
| 作業環境 | AWS操作は対象アカウントのCloudShell等。OS操作は管理者のSSM。端末操作はMac |

「ドメインなし」は「ホスト名なし」ではありません。PortalのWebAuthnには固定ホスト名が必要です。例は `portal.awsportal.internal` と `desktop01.awsportal.internal` です。hostsだけで接続できるのは、設定済みの端末に限られます。専用CAの信頼設定も端末ごとに必要です。

### 1.1 既存の構築手順との関係

| 構成 | 使用する手順 | 適用範囲 |
|---|---|---|
| 組織管理のPrivate Subnet・社内/VPN/TGW経由 | [構築・初期設定](../setup.md)、[組織ネットワーク](../organization-network.md) | 既存の基本構成。Public IPなし・直接IGW/NATなし等のネットワーク契約を維持する |
| 公開IPv4による固定Portal・固定DCV EC2 | 本書の公開構成例 | 公開接続を採用できる独立した構成。付属CloudFormationは全新規基盤のみで、組織Private構成へそのまま適用しない |
| Shared自動増減・Windows Importの動的起動 | [組織ネットワーク](../organization-network.md)と[環境運用](../../operations/environments.md) | 本書の公開EC2は、非公開ネットワークの検査を伴う`managed-profile`／`approved_pools`に登録しない。検査を無効化しない |

所有権・既存参照・変更セット・復旧の確認手順は共通です。Private構成ではEIP、Internet Gateway、全IPv4公開等を採用せず、組織の承認経路へ読み替えます。公開構成でも[アーキテクチャの本番運用要件](../../architecture/overview.md#本番運用の要件)は維持し、外向き通信のAllowlist、監査・監視・バックアップを別途実装します。付属SGのTCP/80・443全IPv4送信許可だけでは、宛先Allowlist要件を満たしません。

### 1.2 既存ファイルの位置付け

| ファイル | 役割 | そのまま使える範囲 |
|---|---|---|
| [constraints.csv](constraints.csv) | 指定値、所有者、変更権限、確認結果の台帳 | まずこのシートを記入する。秘密情報は入れない |
| [foundation.json](foundation.json) | 全新規の公開IPv4基盤テンプレート | 制約を確認して全新規が適切と判断できた場合のみ |

**現在のfoundation.jsonは、既存VPC・Subnet・SG・IAM・EIPを切り替えて利用する機能を実装していません。** 本書の `ExistingVpcId` などは、制約確定後に実装するパラメーターの設計例です。現在のテンプレートへ名前を追加指定するだけでは動きません。

テンプレートにはアプリ、TLS、DCV、バックアップ、監視の完成設定は含まれません。スタック作成成功だけでは本番稼働可能と判定しません。リポジトリの `lab/create.sh`、`lab/test-dcv.sh`、`lab/destroy.sh` は本番に実行しません。

## 2. 制約・所有権を確定する

### 2.1 各リソースを4区分する

| 区分 | 作業方針 | CloudFormationでの扱い |
|---|---|---|
| 新規作成 | 指定条件内で作成し、今後の更新責任を持つ | `Resources`に定義する |
| 既存参照 | ID・名前・ARNで利用する。設定は変更しない | `Parameters`、既存スタックのExport等を参照する |
| 管理者側で準備 | 自分では変更せず、必要な設定と疎通条件を依頼する | 実装前提として記録し、完了を確認する |
| 利用しない | 今回の範囲外とする | 定義しない |

既存参照はCloudFormationへのimportではありません。既存リソースを自分のスタックで管理する必要がある場合だけ、所有者と管理移管・import方法を別途決めます。他スタック・Terraformが管理するものを二重管理しません。

「リソースを利用する権限」「ルールや設定を変更する権限」「削除する権限」は別です。例えば既存SGを指定してよい場合でも、そのSGへのルール追加が許されるとは限りません。既存EIPを関連付ける操作も、既存の接続先を変更するため、単なる参照ではありません。

### 2.2 設定対応表

| 対象 | 確定する制約 | 全新規テンプレート内の対応 | 既存利用・制約対応の実装方針 |
|---|---|---|---|
| アカウント・リージョン | 作業先、SCP、実行ロール、許可リージョン | CLI実行先 | 取得したアカウント・リージョンが台帳と一致することを確認 |
| VPC | VPC ID、CIDR、所有者、共有VPCの有無 | `Vpc`／`AWS::EC2::VPC` | 例：`ExistingVpcId`型`AWS::EC2::VPC::Id`。既存指定時はVPCを作らない |
| Subnet・AZ | Subnet ID、AZ、空きIP、公開接続可能か | `PublicSubnet`／`AWS::EC2::Subnet` | 例：`ExistingSubnetId`型`AWS::EC2::Subnet::Id`。実効ルートも確認 |
| IP | 固定内部IP、割当可能範囲、重複 | 両Instanceの`PrivateIpAddress` | 指定IPへ変更。空きIPを確認し、競合時に範囲を勝手に広げない |
| Gateway・経路 | IGW/NAT/TGW/Proxy、編集権限、戻り経路 | `InternetGateway`、`GatewayAttachment`、`RouteTable`、`SubnetAssociation`、`InternetRoute` | 既存経路ではこれらの新規作成・再関連付けを省く。既存tableへ無断でrouteを追加しない |
| 公開IP | 新規可否、指定EIP、現在の関連先、料金 | `PortalAddress`、`DesktopAddress`／`AWS::EC2::EIP` | 例：既存AllocationIdをStringで受け取る。付け替えの影響を確認 |
| SG | ID、必要ポート、ルール変更権限、IPv6 | `PortalSecurityGroup`、`DesktopSecurityGroup` | 例：既存ID一覧を`List<AWS::EC2::SecurityGroup::Id>`で受け取る。変更権限がなければruleも作らない |
| 内部Agent通信 | private宛先、8444、SG参照可否 | `AgentIngress`、`AgentEgress` | 管理者側設定も含めて両方向の許可を確認。別VPC等ではSG参照条件を確認 |
| IAM | 既存Role/Profile、PassRole、Boundary、SCP | `PortalRole`、`DesktopRole`、`PortalProfile`、`DesktopProfile`、各Policy | 既存Instance Profile名を使い、Role/Policy作成を省く。不足権限は所有者へ依頼 |
| AMI・OS | 承認AMI、所有者、CPU、管理製品 | `UbuntuAmi`と両Instanceの`ImageId` | 承認AMIを指定。自動取得する最新版AMIが許可されない場合は固定する |
| EC2 | 台数、機種、予約容量、Quota | `PortalInstanceType`、`DesktopInstanceType` | 許可機種に限定。AMI、配布バイナリ、アプリのARM64/x86_64互換性を合わせる |
| EBS・KMS | 容量・性能・暗号化キー・削除/保持方針 | 両Instanceの`BlockDeviceMappings` | `Encrypted`、`KmsKeyId`、容量、性能を指定。KMSのkey policyも確認 |
| 証明書・DNS | 既存CA、SAN、有効期間、更新責任、DNS/hosts管理 | 現テンプレート未実装 | 組織の発行手順で準備。CA秘密鍵をEC2へ置かない。ACMの非エクスポート証明書をPEMとして配置できるとは限らない |
| 配布・外向き通信 | GitHub、S3、OS配布、DCV配布、Proxy/検査CA | 両SGの`SecurityGroupEgress`は現在TCP/80・443を全IPv4へ許可 | 実際の承認経路へ変更。SGでは宛先ドメイン名の許可はできない。必要ならProxy/FWを使う |
| バックアップ・監視 | RPO/RTO、既存Vault、KMS、保存期間、通知先 | 現テンプレート未実装 | 共通基盤に登録。データと元のTOTP鍵の復旧手順も整備する |
| タグ・費用 | Owner、部署、費用コード、予算、運用期限 | 各Resourceの`Tags` | 必須タグと命名規約を反映する |

### 2.3 新規・既存を切り替える場合の設計ルール

1. 新規作成用の`Condition`を設け、既存IDがある場合は該当Resourceを作らない。
2. 参照先のIDは`Fn::If`等で統一し、関連するInstance、route、Outputsも同じ選択を使う。
3. SG本体の作成有無と、既存SGへのルール追加許可は別の条件にする。
4. 既存Role参照時にinline policyを自動追加しない。実効権限を事前確認する。
5. 既存EIPの利用は新規確保と別に扱い、現在の関連先を保護する。
6. 稼働後に「新規」から「既存」へフラグを変えると、以前のResourceが削除対象になり得る。管理移管とデータ保護を先に計画する。
7. `DeletionPolicy`・`UpdateReplacePolicy`は個別に設計する。`Retain`はバックアップではなく、保持した有料リソースの運用責任も残る。EC2のルートEBSは`DeleteOnTermination`も別途確認する。

**工程完了条件：** 各行に使用方式、指定値、所有者、変更可能範囲、確認結果がある。未確定の必須項目を例示値で補って適用しない。

## 3. 読取専用の事前調査

作業場所：対象アカウントのCloudShell。以下は作成・変更を行いません。ID等は台帳の実値に置き換えます。資格情報ファイルや環境変数の値を表示して調査しません。

```bash
export AWS_REGION=ap-northeast-1
export AWS_PAGER=""
aws sts get-caller-identity --query '{Account:Account,Arn:Arn}' --output table
```

既存ネットワークを利用する場合：

```bash
VPC_ID='vpc-REPLACE'
SUBNET_ID='subnet-REPLACE'
aws ec2 describe-vpcs --vpc-ids "$VPC_ID" --region "$AWS_REGION"
aws ec2 describe-subnets --subnet-ids "$SUBNET_ID" --region "$AWS_REGION"
aws ec2 describe-route-tables --region "$AWS_REGION" \
  --filters "Name=vpc-id,Values=$VPC_ID"
aws ec2 describe-network-acls --region "$AWS_REGION" \
  --filters "Name=association.subnet-id,Values=$SUBNET_ID"
```

Subnetに明示関連付けされたroute tableがなければ、VPCのmain tableが実効tableです。tableを1件取得できただけで採用しません。IGWへの有効なdefault route、NACLの往復通信、public IPv4の関連付け、SG、DNSを合わせて確認します。EIPだけ付けてもprivate subnetから公開接続できるとは限りません。

既存SGの調査例：

```bash
SG_ID='sg-REPLACE'
aws ec2 describe-security-groups --region "$AWS_REGION" --group-ids "$SG_ID"
```

他のSGもEC2に付く場合、受信・送信の許可は合算されます。既存SGに22/3389や広い内部通信があると、新しいSGだけを狭くしても遮断できません。既存のIPv6割当と`::/0`許可も別に確認します。今回の全公開指定はIPv4のみです。

その他、既存EIPのAllocationId・関連先、AMI所有者・CPU・利用許諾、Instance ProfileとRole、permissions boundary、SCP、Quota、KMS・S3・endpoint policy、配布先到達性、SSM管理経路を確認します。APIが成功しても、実際の操作や通信の許可まで証明したことにはなりません。

AccessDeniedのときは対象API、実行主体、時刻、Request IDを記録し、権限所有者へ依頼します。別アカウントや権限の広いロールへ無断で切り替えません。

**工程完了条件：** 台帳の指定値とAWS実値が一致し、利用者・管理者・配布・内部Agentの通信経路を説明できる。

## 4. 構成・通信・費用を確定する

### 4.1 通信表

| 接続元 | 接続先 | ポート | 設定する意味 |
|---|---|---|---|
| 利用端末（公開例：全IPv4） | PortalのEIP | TCP/443 | nginxのHTTPS。ログインと利用画面 |
| 利用端末（公開例：全IPv4） | DCVのEIP | TCP/8443 | DCVネイティブクライアント。Web Viewerは使用しない |
| DCVのprivate IP/SG | Portalのprivate IP | TCP/8444 | TLSで保護したAgent APIのみ |
| nginx | 同じPortal EC2の`127.0.0.1` | TCP/8080 | 公開しないPortalバックエンド |
| DCVサービス | 同じDCV EC2の`127.0.0.1` | TCP/8444 | ローカル外部認証ブローカー。Portal側8444とは別ホストの別サービス |
| 両EC2 | AWS API・SSM・配布先等 | 主にTCP/443、必要なOS配布のみ80 | 組織の承認経路で到達させる |
| AWS管理者 | AWS Systems Manager | AWS API経由 | 管理者専用。利用者へSSM権限は付与しない |

22/3389は開放しません。全IPv4からの接続許可は認証省略ではありません。Secure Cookie、TOTP・端末固定型パスキー、認証試行制限、監査を維持します。

### 4.2 初期サイジングと費用

全新規テンプレートはPortal `t4g.small`、DCV `t4g.medium`、暗号化gp3 16+40GiB、EIP 2個です。CPUデスクトップ1台を始める例であり、人数・用途の性能保証ではありません。x86専用アプリやGPU要件がある場合は、AMI・配布バイナリ・DCV対応を含めて設計し直します。

費用台帳にEC2、EBS、public IPv4、転送、CPU surplus credits、バックアップ、監視ログ、KMS等を記録します。採用時点の東京リージョンの料金と想定稼働時間で試算し、Budget通知と費用タグを設定します。停止してもEBSや確保したpublic IPv4等の費用は残ります。

**工程完了条件：** 所有者・構成・通信・台数・費用・RPO/RTOが記録されている。要件を満たさない単一AZ構成を高可用として扱わない。

## 5. CloudFormationを調整し、変更セットを確認する

既存制約がある場合は、工程2の内容を反映した別のテンプレートを準備します。以下では、その最終ファイルを`approved-foundation.json`と呼びます。これは本書に付属する完成済みファイルではありません。全新規のまま条件を満たす場合に限り、現在の`foundation.json`をその名前でコピーして使用できます。

例の`parameters.json`も最終テンプレートのParametersと一致させます。現在の全新規版で有効な入力例は次の通りです。秘密情報は入れません。

```json
[
  {"ParameterKey":"ClientCidr","ParameterValue":"0.0.0.0/0"},
  {"ParameterKey":"PortalInstanceType","ParameterValue":"t4g.small"},
  {"ParameterKey":"DesktopInstanceType","ParameterValue":"t4g.medium"}
]
```

### 5.1 新規スタックの差分作成

作業場所：CloudShellのテンプレート・パラメーター保存ディレクトリ。ローカルでcfn-lintを実行できる場合は構文・参照整合性も確認します。既存リソースのAPI検査・実通信は別工程です。

```bash
export AWS_REGION=ap-northeast-1
export AWS_PAGER=""
export STACK=awsportal-prod-foundation
CHANGE_SET="create-$(date -u +%Y%m%dT%H%M%SZ)"
aws cloudformation validate-template --region "$AWS_REGION" \
  --template-body file://approved-foundation.json
aws cloudformation create-change-set --region "$AWS_REGION" \
  --stack-name "$STACK" --change-set-name "$CHANGE_SET" \
  --change-set-type CREATE --template-body file://approved-foundation.json \
  --parameters file://parameters.json --capabilities CAPABILITY_IAM
aws cloudformation wait change-set-create-complete --region "$AWS_REGION" \
  --stack-name "$STACK" --change-set-name "$CHANGE_SET"
aws cloudformation describe-change-set --region "$AWS_REGION" \
  --stack-name "$STACK" --change-set-name "$CHANGE_SET" \
  --query 'Changes[].ResourceChange.{Action:Action,LogicalId:LogicalResourceId,Type:ResourceType,Replacement:Replacement}' \
  --output table
```

CREATE変更セットは実行前に`REVIEW_IN_PROGRESS`のスタックを作る場合があります。この段階ではEC2作成は実行していません。既存スタックの更新にCREATEを使わず、元テンプレート・パラメーター・保護設定を確認してUPDATE用の変更手順を用意します。

上の例はIAM名を自動生成する現在のテンプレートに合わせています。調整後に`RoleName`等でIAMの名前を明示する場合は、必要なcapabilityを`CAPABILITY_NAMED_IAM`へ変更し、同名リソースとの競合を確認します。既存IAMを使うことと、同じ名前のIAMを新規定義することは異なります。

| 確認項目 | 判定基準 |
|---|---|
| 既存ネットワーク | VPC・Subnet・route・associationの意図しない作成・変更・削除がない |
| 既存SG・IAM | 参照だけのはずのリソースへルール・Policyを書き込まない |
| IP | 利用中のEIPを付け替えず、内部IPも競合しない |
| EC2・EBS | 意図しないReplacement/削除がない。暗号化・容量・保持方針が合う |
| 公開範囲 | 443/8443とprivate Agentのみ。既存SGとの合算、NACL、IPv6も確認済み |
| データ・秘密 | DB・鍵の保護計画がある。テンプレート・UserDataに秘密値がない |
| 費用・責任 | 作成対象が台帳に一致し、維持・変更・廃止の責任者が決まっている |

組織の変更管理がある場合は、この具体的な変更セットと作業・復旧計画を提出します。差分が合意内容と異なる場合は実行せず、設定を直して新しい変更セットを作ります。

### 5.2 実行と基盤確認

差分と実行条件が確認できたら、同じCloudShellで実行します。別セッションでは元の変更セット名を再設定してください。

```bash
aws cloudformation execute-change-set --region "$AWS_REGION" \
  --stack-name "$STACK" --change-set-name "$CHANGE_SET"
aws cloudformation wait stack-create-complete --region "$AWS_REGION" \
  --stack-name "$STACK"
aws cloudformation describe-stacks --region "$AWS_REGION" --stack-name "$STACK" \
  --query 'Stacks[0].Outputs' --output table
```

失敗時は`describe-stack-events`で最初の失敗原因を確認します。EC2等が一部作成されて課金される場合があるため、スタック状態と残存リソースを調べます。更新失敗を理由に本番スタックを削除・再作成しません。

両EC2について、起動・ステータスチェック、EIP関連付け、private IP、SG、IAM Profile、暗号化EBS、cloud-init完了、SSM Onlineを確認します。`UserData`はスタック作成成功と同時に完了するとは限りません。現在のテンプレートにcfn-signalによるアプリ準備完了待ちはありません。

**工程完了条件：** 作成・参照した実リソースを台帳へ記録し、管理経路と必要な外向き通信が動作している。アプリ稼働完了とはまだ判定しない。

## 6. 配布物・証明書を準備する

### 6.1 バージョンと資材

Portal、管理CLI、DCV関連ファイル、参照するソース・手順を同じリリースにそろえます。本書の例はv2.1.3です。本番採用時にはその版のサポート・既知の問題・依存関係・リリース検証結果も確認します。

GitHubへ到達できなければ、承認済みの非公開S3または社内配布経路で資材とチェックサムを搬入します。EC2がソースのprivate repositoryを取得できることを前提にしません。配布アーカイブのCPUと`uname -m`を照合します。以下はARM64配布物の検証例で、作業場所は**EC2上の新しい専用ディレクトリ**です。

```bash
ASSET=awsportal-v2.1.3-linux-arm64.tar.gz
EXPECTED="$(awk 'NR==1 {print $1}' "$ASSET.sha256")"
[[ "$EXPECTED" =~ ^[0-9a-fA-F]{64}$ ]]
test "$EXPECTED" = "$(sha256sum "$ASSET" | awk '{print $1}')"
```

チェックサムの取得元も管理します。検証失敗時に期待値を書き換えたり、TLS・署名検証を無効化して進めません。S3署名付きURLは本体とチェックサムで個別発行し、URLを共有ログへ載せません。

### 6.2 証明書の台帳

| 用途 | 接続名・例 | 証明書のSAN | 保管先 |
|---|---|---|---|
| Portal公開側 | `portal.awsportal.internal` | `DNS:portal.awsportal.internal` | nginx用証明書・鍵 |
| DCV公開側 | `desktop01.awsportal.internal` | `DNS:desktop01.awsportal.internal` | DCV用証明書・鍵 |
| Portal private Agent | `https://10.42.1.10:8444` の例 | `IP:10.42.1.10`。実IPへ置換 | private nginxリスナー用証明書・鍵 |

内部用も固定DNS名を使う場合は、Agentから解決できるprivate名と対応するDNS SANに統一します。CA署名鍵は安全な発行環境で管理し、EC2へ配置しません。サーバー秘密鍵は対応するサービスだけが読める権限で配置します。

専用CAを新規作成する場合は、発行主体、署名鍵の保護・バックアップ、証明書期限、更新・失効手順を決めます。7日間のラボ証明書や、証明書警告の無視を本番手順へ流用しません。TLS終端がnginx/Go/DCVなら、そこで使用可能なPEM証明書と鍵が必要です。ACM ARNだけではそのまま設定できません。

**工程完了条件：** 資材と版・CPU・ハッシュが一致し、全経路の証明書名・CA・期限・更新責任が確定している。

## 7. Portalを構築する

作業場所：Portal EC2の管理者セッション。既存ホストでは設定・サービス・利用ポート・ファイル所有権を調査し、既存のファイルを無条件に上書きしません。

### 7.1 ファイルと権限

| 対象 | 例 | 方針 |
|---|---|---|
| サービスユーザー | `awsportal` | 非特権・対話ログイン不可 |
| 実行ファイル | `/usr/local/bin/awsportal`、`awsportal-admin` | 検証済み配布物。root所有、実行可能 |
| DB | `/var/lib/awsportal/awsportal.db` | サービスユーザー専用。親directory 0700 |
| TOTP暗号鍵 | `/var/lib/awsportal-secrets/totp.key` | DBとは別の保護対象。0600、サービスユーザー所有 |
| サーバー証明書 | `/etc/nginx/tls/` | 鍵の読取を必要なサービスに限定 |
| Agent認証キー | Portal発行・DCV EC2で保持 | EC2ごとに個別発行。IaCへ記録しない |

### 7.2 systemd設定例

以下は**同一EC2のnginxでTLS終端する構成**です。[クイック導入](../quickstart.md)と[構築手順](../setup.md)の既定はGo単体HTTPSですが、本書では公開画面とprivate Agent APIの経路を同一ホストで分離するためnginxを選択します。Go単体HTTPSのunitやTLS環境変数と混在させません。新規unitは`/etc/systemd/system/awsportal.service`へ配置し、既存unitへの統合方法は管理標準に合わせます。nginx導入・証明書配置は承認済みの配布経路を使います。

```ini
[Unit]
Description=awsportal
Wants=network-online.target
After=network-online.target

[Service]
User=awsportal
Group=awsportal
UMask=0077
WorkingDirectory=/var/lib/awsportal
Environment=AWSPORTAL_DB=/var/lib/awsportal/awsportal.db
Environment=AWSPORTAL_TOTP_KEY_FILE=/var/lib/awsportal-secrets/totp.key
Environment=AWSPORTAL_HTTP_MODE=http
Environment=AWSPORTAL_ADDR=127.0.0.1:8080
Environment=AWSPORTAL_WEBAUTHN_ORIGIN=https://portal.awsportal.internal
Environment=AWSPORTAL_COOKIE_SECURE=1
Environment=AWSPORTAL_TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
Environment=AWS_REGION=ap-northeast-1
Environment=AWS_DEFAULT_REGION=ap-northeast-1
ExecStart=/usr/local/bin/awsportal
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

このHTTPバックエンド設定では`AWSPORTAL_TLS_CERT`・`AWSPORTAL_TLS_KEY`を設定しません。AWS認証はInstance Profileを使用し、固定Access Keyをunitやファイルへ保存しません。Optionalな監査S3出力、自動増減、Windows Import等は必要な権限・受入がそろうまで有効化しません。

### 7.3 nginxの経路分離例

次は例示値です。内部IP、ホスト名、証明書パスを台帳の確定値に置き換えてから適用します。既存nginxがある場合はdefault server、競合するlisten、他のsiteを調査して統合します。

```nginx
server {
    listen 443 ssl;
    server_name portal.awsportal.internal;
    ssl_certificate /etc/nginx/tls/portal.crt;
    ssl_certificate_key /etc/nginx/tls/portal.key;
    ssl_protocols TLSv1.2 TLSv1.3;

    location = /api/dcv/agent { return 404; }
    location ^~ /api/dcv/agent/ { return 404; }
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Authorization $http_authorization;
    }
}

server {
    listen 10.42.1.10:8444 ssl;
    server_name _;
    ssl_certificate /etc/nginx/tls/portal-agent.crt;
    ssl_certificate_key /etc/nginx/tls/portal-agent.key;
    ssl_protocols TLSv1.2 TLSv1.3;

    location ^~ /api/dcv/agent/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Authorization $http_authorization;
    }
    location / { return 404; }
}
```

SGによるprivate TCP/8444の接続元制限と、公開443上のAgent経路拒否を両方確認します。他のnginx server blockが同経路を公開していないことも確認します。

```bash
sudo nginx -t
```

ここでは構文確認までとし、Portalの起動は次節の初期管理者登録後に行います。既存サービスを変更する場合は保守時間と停止影響を確認してください。

### 7.4 初期管理者

最初の管理者作成はPortalを停止した保守状態で管理CLIを使い、12文字以上のパスワードとTOTPを設定します。unit配置後に次を実行し、管理CLIのコマンド形は[クイック導入ガイドの初期管理者作成](../quickstart.md#4-初期管理者を作成しサービスを起動する)に従います。同ガイドのGo単体HTTPS用unitはコピーせず、本書7.2のnginx用unitを使います。

```bash
sudo systemctl daemon-reload
sudo systemctl stop awsportal
```

管理CLIとサービスには**同じDB・同じ`AWSPORTAL_TOTP_KEY_FILE`**を指定します。パスワードは対話入力し、履歴へリテラルで残しません。CLIはパスワードを引数で受けるため、プロセス引数が見える環境の制約も確認します。表示するTOTP URIは秘密です。SSM Session ManagerやRun Commandのログ、端末録画、共有画面へ保存されない初期登録・受渡し方法を決めてから実行します。組織の記録要件を勝手に無効化せず、保護された出力保存・秘密管理経路を用意します。

全管理者がログイン不能になった場合の復旧は[パスワード復旧手順](../../operations/password-recovery.md)へつなぎます。管理CLI操作の実行者・理由は秘密値を除いて記録します。

初期管理者登録後に起動・自動起動設定とnginxの設定反映を行います。

```bash
sudo nginx -t
sudo systemctl enable --now awsportal
sudo systemctl enable --now nginx
sudo systemctl reload nginx
sudo systemctl status awsportal nginx --no-pager
```

通常の保護操作では、直近のログインまたは本人確認から10分以内という認証鮮度に加え、操作ごとに既存パスキー、または管理者のpassword＋TOTPで本人確認します。確認結果は同じセッション・同じ操作に限り2分間・1回だけ使えます。10分を超えて再ログインした場合も、通常の保護操作ではこの確認が必要です。強制初回password変更の限定例外は[認証仕様](../../architecture/authentication.md#v211の操作単位の本人確認)に従います。

**工程完了条件：** 公開HTTPSのhealth/login画面、private Agentの到達制限、非公開バックエンド、管理者認証が動作し、秘密情報が共有ログに残らない。

## 8. DCV EC2を構築する

作業場所：DCV EC2の管理者セッション。自動インストーラーはUbuntu 24.04が対象です。既存のDCV設定を置き換えるため、既存共有ホストでは導入前にバックアップと設定統合を行います。

1. 検証済みの同版配布物を展開し、必要なOS/DCV配布先・ライセンスS3への到達性を確認する。
2. 新規専用ホストへ`sudo env AWSPORTAL_DCV_PORT=8443 bash dcv/install.sh`を実行する。インストーラーが生成する設定と既存のセキュリティ製品・Firewallの整合を確認する。
3. DCVの表示用TLS証明書・秘密鍵を、台帳のDCV名に一致する正式なものへ設定する。DCV版のAWS公式TLS設定手順に従い、所有者・権限・再起動後の実際の提示証明書を確認する。自己署名警告を無視して完了にしない。
4. PortalのInstance AdminへEC2 ID、表示名、DCVホスト`desktop01.awsportal.internal`を登録し、EC2専用認証キーを発行する。
5. `/etc/awsportal-dcv/config.json`をroot所有0600で配置する。実際のAgentキーは秘密管理経路で設定する。

```json
{
  "portal_url": "https://10.42.1.10:8444",
  "instance_id": "i-REPLACE",
  "token": "REPLACE_WITH_PER_INSTANCE_SECRET",
  "ca_file": "/etc/awsportal-dcv/portal-agent-ca.pem"
}
```

このJSONは書式例で、実行可能な完成設定ではありません。private IP、EC2 ID、キーを置き換え、`portal_url`の宛先が証明書SANと一致することを確認します。AgentからのリダイレクトやHTTPへの秘密情報送信は利用しません。

```bash
sudo chown root:root /etc/awsportal-dcv/config.json
sudo chmod 600 /etc/awsportal-dcv/config.json
sudo systemctl enable --now awsportal-dcv-agent
sudo systemctl status dcvserver awsportal-dcv-agent --no-pager
sudo dcv list-sessions
```

6. Portalの同期結果、ユーザー準備状態、DCVのライセンス有効状態を確認する。
7. テスト用の通常ユーザーを作成し、承認済み端末固定型パスキーを登録し、対象EC2へ割り当てる。
8. MacのDCVネイティブクライアントで実際の画面・入力を確認する。管理者だけの試験で利用者隔離が確認できたとしない。

インストーラーの`authentication="none"`だけを抜き出して認証設定を変更しません。同梱の`auth-token-verifier`、ローカル認証ブローカー、Portal側の一回限りのトークン検証を含む構成です。ブローカーなしでDCVを利用者へ提供しません。

**工程完了条件：** 正しいユーザーの非特権OSアカウント・本人専用セッションで接続でき、他ユーザーのセッションと隔離されている。

## 9. Macを準備して実通信を確認する

1. 別の信頼済み経路でCA公開証明書とSHA-256フィンガープリントを受領し、一致を確認する。CA秘密鍵は受け取らない。
2. System keychain等の指定ストアへCAを登録する。ブラウザー/DCVクライアントが別の信頼ストアを使う場合はそちらも確認する。
3. `/etc/hosts`の既存内容をバックアップし、対象名の行だけ追加・調整する。次のIPは文書用の例で、実際のEIPへ置き換える。

```text
192.0.2.10 portal.awsportal.internal
192.0.2.11 desktop01.awsportal.internal
```

リポジトリの端末配布機能を使う場合は[端末準備手順](../../operations/site-customization.md)に従います。プレビューを確認してから適用し、他のhosts行を維持します。未設定端末からTLS検証を無効にしてCAを最初にダウンロードする方法は使いません。

作業場所：Macのターミナル。`company-ca.pem`は確認済みのCA公開証明書へ置き換えます。

```bash
curl --fail --show-error --cacert company-ca.pem \
  https://portal.awsportal.internal/healthz
curl --fail --show-error --cacert company-ca.pem \
  -o /dev/null -w '%{http_code}\n' https://portal.awsportal.internal/login
```

期待値はhealthが`ok`、loginがHTTP 200です。公開側の`/api/dcv/agent/state`は404等で拒否され、Agentのprivate経路は専用キーでのみ動作することも確認します。`curl -k`は使用しません。

管理者はpassword＋TOTPでログインします。TOTPは直前に消費したコードを再利用しません。一般User／Group Adminはパスキー必須です。iCloudキーチェーン等の同期可能なパスキーは本アプリで拒否されるため、採用Mac・OS・ブラウザーで**同期しない端末固定型かつ本人確認必須の認証器**を実際に用意できることを先に検証します。利用できなければ一般ユーザー運用の準備未完了とし、仕様を緩めて通しません。

**工程完了条件：** 固定名・信頼済みHTTPSで接続でき、利用端末の認証器とDCVアプリが正式運用に適合する。

## 10. バックアップ・監視・更新・復旧を設定する

| 項目 | 必須の決定・設定 | 確認方法 |
|---|---|---|
| Portal DB | RPOに合う頻度・保存先・保持期間・暗号化 | 停止中の整合バックアップ、または検証済みSQLite backup方式。実行中DBの本体だけをコピーしない |
| TOTP暗号鍵 | DBとは別のアクセス制御・保管・復旧権限 | 元の鍵とDBの対応を記録。鍵を失って再生成しても復旧にならない |
| 設定・バイナリ | unit/nginx/CA公開証明書/証明書鍵/Agent設定/リリースハッシュ | 機密区分に応じて保護し、再構築に必要な情報を確保 |
| 利用者データ | DCVのホーム・必要な永続領域の保存方針 | 復旧先で所有UID・権限・データを確認。利用中アプリの整合性も考慮 |
| 実行監視 | EC2 status、CPU/Memory、disk、サービス、health、同期・証明書期限 | Memory/diskはOS側監視設定が必要。通知先への実通知も試す |
| 認証・監査 | 認証失敗、管理操作、監査永続化失敗・未完了intent | 秘密を含めないログ保管、保管期間と参照権限、対応担当を定義 |
| 更新 | OS、DCV、アプリ、CA/証明書の更新周期と保守時間 | 同版のアプリ・Agent、承認済み配布物、更新後受入 |
| 費用 | 予算、Owner、稼働時間、通知 | Budget等の通知と請求項目を確認 |
| 復旧 | RPO/RTO、予備担当、対象鍵・アクセス経路 | 本番と隔離した復旧先でDBと元鍵・利用者データを復元し、実時間を測る |

EC2/EBS snapshotだけでSQLiteの整合性と元TOTP鍵の別保護を満たしたとは判断しません。稼働中SQLiteの`.db`本体だけのコピーはWALを落とす可能性があります。復旧試験の合格まで運用開始条件を満たしません。

CloudFormationのstack termination protection、重要Resourceの保持方針、サービスの停止権限、バックアップ削除権限も運用設計に合わせます。現在の全新規テンプレートではルートEBSが`DeleteOnTermination: true`で、バックアップは自動作成されません。

## 11. 本番受入チェックリスト

結果は合格・不合格・未実施を分け、担当者、時刻、対象版、証跡の保管先を記録します。認証情報や接続トークンを証跡に含めません。

| ID | 確認項目 | 合格条件 |
|---|---|---|
| A01 | 制約・所有権 | 台帳と実設定が一致し、既存リソースに未合意の変更がない |
| A02 | 公開範囲 | 外部から443/8443が利用可能。22/3389・8080・Portalの8444は利用不可。既存SG/NACL/IPv6も照合 |
| A03 | TLS・名前 | Portal、DCV、private Agentすべてで名前・CA・期限が一致し、検証無効化なし |
| A04 | HTTP経路分離 | 公開443のAgent APIを拒否。private8444はAgent API以外を拒否。Agentキー不正も拒否 |
| A05 | 管理者認証 | 正しいpassword＋TOTPで成功。誤り、TOTPなし、消費済みTOTPで失敗 |
| A06 | パスキー | 一般ユーザーの端末固定型・本人確認付きで成功。同期型・不正応答を拒否 |
| A07 | 操作単位本人確認 | 通常の保護操作では10分以内の認証鮮度に加え、操作別の本人確認が必要。2分の期限切れ・別操作流用・再利用を拒否。強制初回password変更の限定例外も照合 |
| A08 | 権限分離 | 通常Userは割当EC2だけ表示・操作。一般Userのpassword＋TOTPや管理画面アクセスを拒否 |
| A09 | DCV実接続 | ネイティブアプリで画面・マウス・キーボード。本人のOSユーザー・セッションである |
| A10 | 利用者間隔離 | 他ユーザーのホーム・セッションへ入れず、sudo/SSM等の管理権限を持たない |
| A11 | 失効 | 割当解除・無効化・キー更新で規定どおり新規接続拒否とセッション終了。管理者の全EC2権限を解除試験に流用しない |
| A12 | DCV機能制御 | 禁止した転送・clipboard・印刷等を実際に拒否。設定版とAgent適用版が一致 |
| A13 | 起動・停止 | 許可されたEC2のみ制御可能。停止/再起動後もEIP・名前・同期が正しい |
| A14 | 外部認証 | 有効な一回限りトークンが動作し、再利用・期限切れ・別EC2を拒否する。任意の試験コードは本番DBに残さない |
| A15 | 障害・監査 | 通信失敗時の新規接続拒否、監査異常の通知、外部操作の二重実行を避けた照合ができる |
| A16 | 再起動 | 両EC2の再起動後にサービス・認証・DCVが復帰する |
| A17 | バックアップ復旧 | 隔離先でDB＋元鍵＋利用者データを復旧し、RPO/RTOに適合する |
| A18 | 運用引継ぎ | Owner、監視通知、更新期限、費用、障害対応、秘密管理、廃止手順が引継ぎ済み |

本番のロック・無効化・停止試験は、隔離した受入用ユーザー/EC2と保守計画を使い、稼働利用者へ影響を出さないよう実施します。高度な障害注入や実装レベルのトークン再利用試験は同等構成の受入環境で実施し、対象版と設定の同一性を記録します。未実施を合格にしません。

## 12. 障害時と切り戻し

| 症状 | まず確認するもの | 対応方針 |
|---|---|---|
| CloudFormation失敗 | stack events、Quota、IAM/SCP、AMI/KMS、IP競合 | 原因を直して差分を再確認。既存資源を消して作り直さない |
| SSM接続不可 | Instance Profile、Agent、443経路、DNS、endpoint policy | 管理経路を復旧。原因確認前にSSHを全公開しない |
| health不可 | EIP/route/SG/NACL、証明書、nginx、Portal、DB/key権限 | 層ごとに分離。`cloud-init`失敗とアプリ失敗を区別 |
| ログイン不可 | 固定Origin、時刻同期、CA、Cookie、TOTP、アカウント状態 | CA無効化やMFA省略で通さない。秘密を除いて診断 |
| DCV準備不可 | private8444、Agentキー・SAN・CA、ライセンス、OSアカウント、適用版 | Agentログと実状態を照合。キーを共有・使い回ししない |
| パスキー不可 | 同期型か、OS本人確認、Origin、ブラウザー | 対応端末/認証器を用意。管理者復旧は所定の経路を使用 |
| 更新後不具合 | 更新版・旧版・DB移行・元鍵・backup時点 | 互換性を確認した復旧点へDB/元鍵/設定/バイナリを組で復元する |

更新前にサービス停止、整合バックアップ、旧資材、DB移行の可逆性、停止時間、復旧手順を確認します。旧バイナリへ戻すだけで新しいDB形式を読めるとは限りません。切り戻し時にその後の更新データが失われる範囲も事前に説明します。

本番撤去は立ち上げと別の変更作業です。所有資源・既存参照・保持データを一覧化し、必要な保存・移行を確認してから削除します。共有VPC・SG・IAM・S3等を巻き込まず、残ったEIP/EBS/backup等の課金と責任も確認します。ラボのdestroyスクリプトは使いません。

## 13. 記録と完了判定

保存する記録：制約確認シート、最終IaCとハッシュ、パラメーター（非秘密）、変更セット、資源台帳、採用版・チェックサム、証明書期限、受入結果、バックアップ/復旧結果、監視通知先、運用担当。

| 段階 | 完了の意味 |
|---|---|
| 設計完了 | 制約と所有権・構成・費用が確定 |
| 基盤完了 | AWSの作成/参照、OSと管理経路が正常 |
| 機能構築完了 | Portal・認証・Agent・DCVが動作 |
| 本番受入完了 | 対象版・対象設定・対象端末で必要な受入を完了 |
| 運用開始 | バックアップ復旧・監視・更新・担当の条件も満たし、利用者へ提供 |

この文書の作成時点ではAWS実機への適用・ログイン・DCV接続・復旧試験を実施していません。作成済みの基盤テンプレートも、既存利用への切替・アプリ・運用設定が完成したものではありません。

文書確認では相対リンク、Bash例の構文、JSON例、systemd例の基本書式、制約確認シートの列数を確認しました。nginxの`nginx -t`、systemdの実サービス起動、AWS変更セットと実機受入は、値と環境を確定してから行う確認です。

## 14. 参照資料

- [awsportal クイック導入](../quickstart.md)
- [構築・TLS設定](../setup.md)
- [組織ネットワーク・所有権](../organization-network.md)
- [既存ネットワークのTerraform例](https://github.com/sptree-m/awsportal/blob/v2.1.3/terraform/examples/org-existing/README.md) — 設計の参考。CloudFormationへ自動変換されるものではありません。
- [DCV導入・権限制御](../../operations/dcv.md)
- [端末準備・CA・hosts](../../operations/site-customization.md)
- [パスキー運用](../../operations/passkeys.md)
- [認証仕様](../../architecture/authentication.md)
- [v2.1.3 更新・受入](../../releases/v2.1.3.md)
- [AWS CloudFormation変更セット](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/using-cfn-updating-stacks-changesets.html)
- [Amazon DCVのTLS証明書設定](https://docs.aws.amazon.com/dcv/latest/adminguide/manage-security-cert.html)
