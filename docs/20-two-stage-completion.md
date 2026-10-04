# 二段階実装：v1.8.0-rc.1

この候補版は第1段階のストレージ・計測基盤と、第2段階のShared自動増減、CUR配賦、専用Windows Box取り込みを追加する。第1段階の実機受入、2名・5営業日の観測、500 GB級Windows転送は、AWS/Boxの実環境で別途実施する。コード・CIの成功と実機受入を区別する。自動増減と取り込みは初期OFF。

## 導入順序とスイッチ

1. SQLiteの停止バックアップと既存HOMEのバックアップを取得する。Portalを更新すると既存の認証・権限・DCV設定を保ったまま追加テーブルを作成する。
2. `terraform/modules/shared-pilot` で固定CPU EC2、User別EFS、Group EFS、暗号化gp3 Scratch、計測S3を準備する。EFS/Access Point/S3は `prevent_destroy`。固定EC2は自動削除しない。
3. `/admin/environments` でPersonal/Shared、Owner、Group、ACL、User EFS、Group EFSを登録する。PersonalはOwnerのみにDCV権限を与える。Group共有領域は `/srv/projects/awp-g<ID>/{projects,common,small-datasets}`、GID=2000000+Group ID、2770。HOMEはUID/GID=200000+User ID、0700。EFSはTLS/IAM/AP指定、通常Compute RoleのRoot書き込みは禁止。
4. Golden AMIに `dcv/install.sh` でAgent、job broker、Storage/Bootstrapを配置する。awscli、amazon-efs-utils、XFSツール、systemd user manager、DCV/XFCEが必要。固定pilotでは承認済みAgent設定をroot専用で配置し、Scratch初期化を実行する。動的SharedではLaunch TemplateのUserDataがBootstrapを実行する。
5. `managed-profile` で固定AMI/数値Launch Templateバージョン、プライベートSubnet/SG、IMDSv2、CPUオンデマンド、暗号化DeleteOnTermination EBSを定義する。AMIには検証成果物のSHA256を `awsportal:golden-sha256` タグとして付ける。これはAMIバイナリをAWSが返すチェックサムではなく、管理者が検証したビルドのattestationである。
6. `two-stage` をShared用とWindows用に**別々に**適用する。Windows RoleにShared EFS権限を与えない。承認済みCompute Role ARN、KMS Key、SSM設定Parameter ARN、credential prefixを指定する。Portalには承認済みSTANDARD WorkflowのStart/Describeだけを許可し、RunInstances/TerminateInstancesを与えない。Lambdaの公開バージョンをWorkflowに固定し、既存executionの承認設定を変えない。
7. `/admin/two-stage` で検証記録付きNEXTを登録しSTABLEへ昇格する。PortalのAMI/LT/数値version/SHAとIaCのapproved_poolsを一致させる。不一致では起動しない。既存EC2は昇格だけでは入れ替わらず、利用終了→連続idle→drain→終了後の次回依頼で新STABLEを使う。
8. 固定pilotの実機受入と2名・5営業日を記録する。その参照と理由をpoolに保存してから、全体とpoolの両スイッチを有効にする。初回は増設のみ有効にし、drainの実機検証後に終了を有効にする。

| Portal環境変数 | 値・既定 |
|---|---|
| `AWSPORTAL_USAGE_BUCKET` | Parquet保管S3。未設定ではローカルqueueを保持 |
| `AWSPORTAL_PROVISION_WORKFLOW` / `AWSPORTAL_TERMINATE_WORKFLOW` | Shared STANDARD Workflow ARN |
| `AWSPORTAL_SCALE_OUT` / `AWSPORTAL_TERMINATE` | `true`のみ許可、既定false |
| `AWSPORTAL_CUR_BUCKET` / `AWSPORTAL_CUR_PREFIX` | 承認CUR S3、prefix既定`cur/` |
| `AWSPORTAL_IMPORT_PROVISION_WORKFLOW` / `AWSPORTAL_IMPORT_CLEANUP_WORKFLOW` | 専用Windows Workflow ARN |
| `AWSPORTAL_IMPORT_ENABLED` | `true`のみ許可、既定false。DB側Import許可も必要 |
| `AWSPORTAL_IMPORT_BUCKET` | 独立検証を許可する取り込みS3 |

実際の環境変数名は `cmd/awsportal/main.go` の設定箇所と照合する。デプロイ先のAWS認証・ネットワーク・SSM/KMS・Boxログインはこのリリースに含めない。

## HOME移行とScratch/Dataset

Personalの既存HOMEは移行ロックを取得し、元EC2を停止、元EBSをスナップショットから専用移行ホストへ読取専用でマウントする。原本を削除しない。専用ホストは対象Userだけの一時 `migration_roles` とNFS SGを使う。Compute Roleを移行Roleに指定してはいけない。

```
python3 dcv/storage.py /mnt/original/home /mnt/new-home \
  --user-id 123 --offline-proof 'stopped EC2 + retained snapshot evidence'
```

rsyncのmetadata/ACL/xattrと全件checksumによるdry-run検証後、JSONを移行完了フォームに記録する。新UID/GIDへ所有権を写す。コピー失敗時はロックと原本を維持する。検証後に一時migration_rolesとNFS許可を削除し、User EFSを登録して新HOMEを有効化する。移行ホスト上の停止証跡とコピー結果は管理者が実機で確認する。

Personal/Sharedは同一User HOMEを同時に保持できない。Personal leaseはAgent欠測では解除せず、AWS SDKがstopped/terminatedを確認して解除する。Shared releaseはDCV/job終了とsync/unmountのAgent ACKが必要。

Scratchは専用暗号化XFSのproject quota（初期50 GiB/User、管理画面変更可能）とprivate TMPDIR。ローカルscratchとDataset cacheは消えてよいデータ。HOME/Group EFS/S3は終了時も保持する。Group EFSへDatasetを全量複製しない。

Dataset cache manifestは次の形。pathは相対パス、重複・Traversal・ファイル/ディレクトリ衝突を拒否する。全オブジェクトのVersionId、size、SHA256を固定する。

```json
{"version":1,"files":[{"path":"train/data.bin","size":3,"version_id":"S3-version","sha256":"64-hex-digest"}]}
```

Portalがcanonical JSONのSHA256をDataset IDとする。専用threadでScratchへダウンロードし、容量確認・サイズ・checksum・全path検証、sync、atomic rename後にAVAILABLE。途中のpending領域は利用者に公開せず、同じmanifestで再試行できる。cache中はidleを抑止する。

## Shared制御・復旧

全Shared合計もpoolごとも最大5台、1台2席。固定・起動中・未確認・QUARANTINEDも上限に含め、事前暖機せず依頼に応じて増設する。DB transactionで容量を予約してから永続Workflowへ渡し、同じClientTokenで再照合する。EC2 runningだけでは割り当てない。世代/boot/連番、policy/browser、90秒以内のAgent、5分CPU/Memory平均ともに厳密70%未満が必要。候補は占有数、CPU、Memory、ID順で全候補を試す。初回warmup60秒も満たす。

未接続、座席/HOMEなし、依頼なし、job/desktop/不明workなし、storage処理なし、CPU<10%、計測正常の15分連続idleだけをdrain対象とする。root Agentがsync、User/Group EFS unmount、処理IDをACKしてから終了Workflowへ進む。新規依頼や新workはAWS送信前ならdrainを取消す。送信後の不明状態は枠を維持して照合する。OFFは未送信の操作だけを取消し、既存executionの追跡を続ける。

Workflowが失敗した操作はQUARANTINEDとして保持する。管理画面の再照合で理由を記録し、同じcapacity/ClientToken/元プロフィールを維持した別executionを作る。TERMINATEは再びfresh root proofを必要とする。管理者がDBから予約行を消して枠を空けてはいけない。終了時はタグ所有権/世代、全EBS DeleteOnTerminationを再検証し、EC2 terminatedとタグ付きEBS消失、SecureString削除を確認して完了する。PortalもAgent/DCV bearerを削除する。IAM Role自体は共有プロファイルなので削除しない。

## 計測・費用・運用画面

Agentは1分ごとのNode/desktop/jobのCPU、memory、IO raw countersを収集する。desktopと `awsportal-job` は別cgroup。cgroup epoch/bootが変わる、欠測・カウンター減少がある場合のdeltaはNULLとする。root brokerはpeer credentialで利用者を確定し、子プロセス終了、reboot、欠測、Portal ACK後のterminal pruneを追跡する。

EFSのAWS metered sizeとUser権限で実行したhourly `du`を区別する。EBSはSDKで容量/type/IOPS/throughput/encryptionを収集し、1分inventoryで起動/停止・構成変更を検出、通常はhourly保存する。未取得を0として扱わない。CUR取込、AWS制御、S3 export、storage inventoryは座席処理から独立したworker。

raw JSONをdurable queueから最大100行/8 MiBのParquetに固め、同じkey/bytesで再送する。S3 ACK済みrawだけをローカル7日経過後に削除。S3 raw Parquetは396日（約13か月）、接続区間・所有権・membership履歴を保持する。queue keyの`.json`は論理sample IDで、通常の保存物は`.parquet`。未送信分は7日で消さない。長期S3障害時はdurable queueとDBディスク容量を監視する。

`cur` moduleはus-east-1 providerでCUR 2.0/INCLUDE_RESOURCES/HOURLYを設定する。配賦入力は原本のS3 versionを固定した小さいmanifest（Portalが自動生成するAWS exportのmanifestとは別）で、gzip CSVをstreamする。

```json
{"version":1,"files":[{"key":"cur/export/data.csv.gz","version_id":"immutable-version"}]}
```

Scopeはaccount/currency/期間/basis。UnblendedまたはNetUnblended（Net空欄はUnblended）を選ぶ。小数はbig.Ratで保持し、run合計の6桁通貨小数へsigned largest remainder、同率User ID順で一度だけ丸める。CUR行、original decimal、resource履歴、weight、式version、source versionを保持する。

Personalは歴史上のOwner100%。Shared EC2/EBSは実DCV接続区間。Group EFSやGroup共通費用は当月Group接続時間→当月有効membership均等→明示fallback owner。Resource未対応、Tax/Credit/Refund/RI/SP未使用/Support等はline typeごとの明示policyが必要。暗黙に除外/0にしない。

```json
{"common_group_id":1,"fallback_user_id":2,"non_resource":{"Tax":"group","Credit":"group","Refund":"group","RIFee":"group","SavingsPlanRecurringFee":"group","Fee":"fallback_owner"}}
```

未対応type/Resourceはresidualへ。暫定User合計+residual=対象原価。residual=0だけFINALにできる。修正CURは新source version/new runで、旧FINALを上書きしない。`/settlements` とCSVはUser自身のみ、Portal Adminは全User。Cost Explorer速報画面は別に残す。

運用画面は直近30日の終了保留理由（1分観測件数）、欠測を除いた観測/idle秒、動的EC2の予約→Agent準備平均/p95を表示する。未完了・固定EC2は起動時間の母集団から除外する。p95はnearest-rank。

## Windows取り込み

Windows専用Role/SG/AMI/LT、m7i.2xlarge、暗号化1 TiB gp3を用意する。Windows Golden AMIに3つのPowerShell scriptと初期AWS CLIを配置する。Portal/SSM/S3/Box/DCV/SSO/セキュリティ製品への承認通信が必要。Shared Role/networkを流用しない。

SSM設定は `portal_url`, `interactive_user` (`AwsImportAdmin`), `credential_prefix`, `installer_bucket`, `installer_key`, `installer_version_id`, `installer_sha256`。installer manifestはBoxDrive/7zip/Falcon/AWSCLI/DCVの各S3 bucket/key/version_id/sha256/signer_thumbprint/extension/argumentsを固定する。Authenticodeを確認し、root protected checkpointとstartup taskで3010再起動後に再開する。Box普通cache上限は必要時 `box_cache_maximum_gb` (DWORD GB)を設定。offline対象は普通cache上限とは別に容量を見積もる。

Workflowが作る専用WindowsパスワードはSSM SecureStringだけに保存する。Portal password、AMI、UserData、ログへ入れない。管理者がSSMから資格情報を取得して専用DCVセッションでログインし、Box SSO/MFA、フォルダーoffline化を手動で行う。Box APIは使わない。

同じinteractive userから次を実行する。SYSTEM実行、Box内部cache、別userのrootを拒否する。手動確認前のフラグを付けない。

```
.\Import-Box.ps1 -ConfigPath C:\ProgramData\awsportal-import\runtime.json -Action LoginReady
# offline化を完了してから
.\Import-Box.ps1 -ConfigPath C:\ProgramData\awsportal-import\runtime.json -Action Upload
```

対象全ファイルの読取・path/size/mtime/SHA256を凍結後、AWS CLI multipart転送。`--delete`は使わず、500 GBの別staging copyも作らない。checkpointで再開し、変更/読取失敗/CLI失敗は停止する。全path/件数/bytes/原本hash/固定S3 VersionIdと検証・ログをS3へ永続化する。

Portalの独立workerがmanifestとログのimmutable version/hash、正確なS3 path set、size、全オブジェクトのSHA256をstream検証する（multipart ETagをchecksum扱いしない）。大規模転送ではこの再読取の時間/通信費も計上する。検証前にAVAILABLEにはしない。検証済みDatasetを公開後、Windows EC2/EBS削除とmachine/admin資格情報失効を確認してSUCCEEDED。

失敗/取消/48時間timeoutはresourcesを保持する。管理画面で理由を入力して保留リソースを削除できるが、FAILED/CANCELLED/TIMED_OUTを成功へ書き換えない。Cleanup failureはCLEANUP_FAILEDで保持し、理由付き再試行は別execution。応答不明のPROVISIONINGを勝手に取消/削除しない。AWSのoperation tagから実体を調査して整合を回復する。

## 検証とリリース

Go race/全unit/ARM64、DCV/Storage Python、AWS worker fake、CloudFormation/security、Terraform各module validate、WindowsネイティブPowerShell AST/CLI引数、実サーバーbrowser consoleをCIで実行する。実AWS権限、EFS mount、DCV、Box/Falcon導入、実機故障復旧と大規模転送はこのCIでは証明できない。受入証跡なしにproductionスイッチをONにしない。

候補アーカイブは `scripts/release-files.txt` の14ファイルとSHA256。Terraform/文書は同じGit tagのソースを使う。安定版v1.6.0はそのまま、候補版v1.7.0-rc.1の既知未実装項目は本版の対応表としてこの文書を参照する。
