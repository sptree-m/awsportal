# 組織管理ネットワークと混在構成（v1.8.0-rc.2）

二段階仕様のPersonal/Shared、HOME/Group EFS、Dataset cache、Job保護、動的増減、計測、CUR配賦、Windows Box Importを維持し、指定済みVPC・IP範囲・TGW/ルートテーブルを使う構成を追加した。将来拡張と明記されたGPU、FSx、予測起動、Software Catalog等は二段階の範囲に含めない。

## 所有権と選択

リソースごとに新規作成と既存利用を選ぶ。既存利用はIDを参照し、組織のTerraform stateへimportしない。新規と既存は同じ構成内で混在できる。提供済みリソースへpolicyやrouteを上書きしない。

| リソース | 新規 | 既存・提供済み |
|---|---|---|
| VPC | 承認CIDRまたはIPAM pool/netmask | `existing_vpc_id` |
| Subnet | AZ・承認CIDRを明示 | Subnet IDとAZ |
| Route Table | 新規Subnet用だけ作成し承認TGW宛routeを追加 | IDと実効associationを読取確認。再関連付け・route編集なし |
| TGW | 新規VPC attachmentを明示指定した場合のみ作成 | TGW本体・TGW側table・return route・acceptanceはIT管理 |
| AWS endpoint | 承認serviceで新規Interface endpoint、所有tableにS3 gateway endpoint | 作成指定を空にし、既存endpointまたはTGW経由の承認経路を利用 |
| Portal/Compute/Storage SG | 専用SGを作成 | 既存IDを選択しSG本体を編集しない。peer SG rule作成も個別OFF |
| IAM | permissions boundary付き専用Role/Profile | 承認Role/Profile名・ARN。既存Roleへinline policyを書かない設定 |
| User EFS/AP | User単位で新規 | `existing_user_storage` に指定。暗号化、UID、0700、PosixUserなしを確認 |
| Group EFS/AP | 新規・保持 | `existing_group_storage`。policy/mount target/backup変更なし |
| Metrics S3 | 新規・13か月保持 | `existing_metrics_bucket_name`。設定/policy変更なし |
| CUR | Bucket/Exportを独立に新規 | `use_existing_bucket`, `create_export=false`, `existing_export_arn` |

`terraform/modules/org-network` の出力をPortal/Shared/managed-profileへ渡す。構成例は `terraform/examples/org-existing`、`org-mixed`、`new-private`。例のID/CIDR/bucketは置換してplanをレビューする。初期設定でIGW/NAT/IPv6出口を新設しない。新規VPCで社内DCVを使う場合はIT承認のTGW attachmentと戻り経路を用意する。TGWのdefault association/propagationは自動で有効にしない。

既存ルートテーブルへの追加権限がない場合、新規Subnetを既存tableへ関連付けることは明示指定した新規Subnetだけに限定する。TGW route追加とS3 gateway endpoint関連付けは新規所有tableだけに限定する。提供Subnetのtableがmainの場合も、起動workerが実効tableを解決して一致を確認する。

## IP範囲と承認profile

Portalの固定IPは `portal_private_ip`、固定pilotは `fixed_private_ip`。動的Shared/Windowsは `managed-profile.allowed_ipv4_cidrs` を指定する。空ならSubnet全体を承認範囲とする。小さい範囲は同じSubnet内のCIDRを複数指定でき、全アドレス合計4096以下。AWS予約のSubnet先頭4個と末尾は使用しない。

全Subnet範囲ではAWSの自動割当。部分範囲ではworkerが使用中ENIを調べ、選択IPを操作IDに紐付くSSM SecureStringへ先に保存してからRunInstancesする。再試行は同じIP/ClientTokenで行い、競合や枯渇時は範囲を広げず失敗として保持する。全workerはreserved concurrency=1。別の組織システムが同じIPを取得する競合はAWSが拒否し、Portalの再照合で調査する。独立したShared/Windowsには重ならないIP範囲を割り当てる。

`managed-profile.approved_profile` を `two-stage.approved_pools` に渡す。承認情報には実所有者のSubnet/SG ARN（共有VPC/RAMでも現在accountを推測しない）、VPC/Subnet/SG、allowed IPv4 CIDR、実効Route Table/TGW、AMIと数値LT versionを含める。Portal UIから任意VPC・Roleへ変更しない。組織提供AMIは `approved_ami_owners` に承認accountを指定できる。ITが承認profileを更新し、そのAMI/LTをNEXT→STABLEへ登録する。

起動前に承認VPC・Subnet・SG、LTの非公開primary interface、実効table/TGW route、blackhole、直接IGW/NAT/egress-only IGWを確認する。既存リソースの採用・起動後も実IP、SG、Public IPなし、IPv6なしを確認する。不一致なら追加起動せず調査用に保持する。旧rc.1のnetwork契約なしprofileは新規起動を拒否するため、先にIaCのapproved_poolsを更新する。終了処理と既存sessionは継続する。

```sh
terraform output -json approved_profile > approved-profile.json
python3 -m pip install boto3
python3 scripts/network-preflight.py approved-profile.json --region ap-northeast-1
```

事前検査はDescribe APIのみで、通信失敗を直す目的でroute/SGを変更しない。これはAWS設定の検査で、接続成功の証明ではない。社内DCV、Portal TLS、EFS、SSM/S3/KMS、DNS resolver、NACL、TGWの往復routeとfirewall、Windows Box/SSO/Falconへの承認通信を実機で確認する。TGW default routeだけをInternet遮断の証明にしない。中央のFW/ProxyとOSの既存制限も必要。

## IAM・既存ストレージ・更新

Portal root module: `existing_portal_security_group_ids`, `existing_portal_instance_profile_name`, `create_dcv_security_group=false` を選択できる。新規SGの443 egressは既定で空。`approved_https_egress_cidrs`/`approved_https_prefix_list_ids` を承認先に限定する。

Shared pilot: `existing_compute_security_group_ids`, `existing_storage_security_group_id`, `existing_compute_role_arn`, `existing_compute_instance_profile_name/arn`, `manage_peer_security_group_rules=false`, `manage_portal_policies=false` を使用する。中央endpointへTGW経由で行く場合、ローカルendpoint/Portal SG参照はnull、承認CIDRを443 egressに指定できる。提供SGやEFSのNFS/TLS/IAM/AP限定・RootAccess禁止・backup・mount targetはIT側で事前に設定する。既存HOME移行のコピー・停止証跡と同時HOME leaseはrc.1の手順を維持する。

Workflow: `worker_role_arn`, `workflow_role_arn`, `manage_portal_policy=false`, `manage_node_policies=false`。外部worker用には `worker_policy_json` をITへ渡せる。既存Roleの権限・信頼policy・KMS key policy/SSM・endpoint policyはITが設定する。部分IP指定に必要なRunInstancesのnetwork override権限は承認LT・Subnet・SGの範囲に限定し、公開IPを拒否する。PortalのRoleにはRunInstances/ModifyVolume/TerminateInstancesを付与しない。

既存stateの単一リソース→count[0]変更にはmoved blockを添付。既にawsportalが作成・所有したリソースの設定を「既存利用」に変更する場合は、別stateへの移管を先に実施する。フラグだけで既存stateから外すと削除planになり得る。EFS/AP/S3のprevent_destroyを解除しない。新規・既存の選択は初回構築時に確定し、所有権変更時はplanで置換/削除がないことを確認する。

## 一時EBS性能変更と30日観測

root/scratchサイズ・IOPS・throughputはmanaged-profileで指定する。初期gp3範囲は3000～16000 IOPS、125～1000 MiB/s、Windows rootは1 TiB以上。EC2帯域は採用instance typeで実測する。

`AWSPORTAL_PERFORMANCE_WORKFLOW` に `two-stage.workflow_arns.performance` を指定すると、管理画面からREADYの動的Sharedに記録された一時EBSを非同期変更できる。管理者・理由必須。AWS上のタグ/世代/接続先/暗号化/gp3/DeleteOnTerminationを再確認する。重複依頼を拒否し、modifying/optimizingは待機、completed後に完了。固定6時間待ちを仮定せずAWSの変更回数制限に従う。FAILED/応答不明は自動終了を保留し、同じ目標性能を理由付きで再照合する。EFS・永続volumeは対象外。

30日画面にはCPU/Memoryの時間加重平均と1分サンプルp95、起動平均/p95、idle秒/終了件数、増設予約理由件数を表示。欠測とboot変更を時間に加算しない。複数待機理由が混在した増設予約は辞書順の代表理由を固定する。過去network契約や増設理由がない履歴はlegacyとして表示し、後から推測して書き換えない。

## 検証と本番受入

Go/Agent/AWS fake/所有権plan/Windows構文/実サーバーUIを必須CIに含める。組織IAMのSCP、permission boundary、共有VPC/RAM、TGW accept/return、DNS/Falcon/Boxの実到達性、EFSの実隔離、10人・500 GB・確定CURの受入は対象AWS/Box環境で実施する。資格情報や実機証跡なしに完了したとは扱わず、自動増減・Importは既定OFFを維持する。

既存の公開Internet型 `lab/create.sh` は単独検証専用。組織ネットワークでは使用せず、上記Terraform構成例を独立stateで利用する。破棄時には新規所有分だけをplanし、組織所有のVPC/TGW/Route Table/SG/Role/EFS/S3を削除しない。

CURバケットがPortalと別リージョンの場合、Portalに `AWSPORTAL_CUR_REGION` を設定する。既存バケットへExportを新設する場合は `existing_bucket_region` も指定する。既存Bucket/Exportを利用する場合はVersioning、有効なmanifest、承認prefix、読取権限と対象リージョンへの承認通信をIT側で用意する。

## 未接続予約とHOME解放

更新Agentが期限処理対応を報告した場合、初回READYから120秒経過した未接続予約を安全な解放へ進める。過去に実接続したUser、Job、不明作業、期限内DCV tokenと認証直後の猶予は自動解放しない。期限処理中に実接続/Jobが判明した場合は取り消して保持する。古いAgentに期限処理を指示しない。

Root Agentも実接続数とJobを再確認し、session終了、user manager停止、HOME sync/unmountを確認してACKする。PortalはHOMEがまだmountedという報告を受けたら席/HOME leaseを解放しない。期限切れはcleanup確認後TIMED_OUT。準備失敗やAgent欠測を理由にHOMEを解放しない。PortalとGolden AMIのAgentを同時に更新し、旧Agentの解放が保留された場合は更新してから再照合する。
