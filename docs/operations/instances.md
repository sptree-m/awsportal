# インスタンス管理とアクセス割り当て

## 管理画面の機能

`/admin/instances` はPortal Admin専用。一般ユーザーとGroup Adminは
GET/POSTとも403。管理操作は監査ログに記録する。

- 登録済みインスタンスを有効/無効に切り替える。無効なものも管理画面には残る。
- ユーザーとグループを各インスタンスへ割り当て、解除する。
- 割り当ての権限は「接続・閲覧」または「接続・電源操作」。後者はスケジュール操作にも適用。
- グループ作成、ユーザーのグループ追加/除外を行う。
- 複数の直接/グループ割り当てがある場合、許可を合成する。電源操作の許可が1つでも
  あれば操作可。一覧は1インスタンス1行。Portal Adminは有効インスタンス全体を操作できる。
- 既存のinstance_users/instance_groups/group_membersを維持し、Environment/Assignment/Workflow等は追加migrationで拡張する。
- 毎リクエストでユーザーの有効期限・有効状態・現在のロールをDBから再確認する。
- ログアウトを小さい画面幅でも表示する。
- DCV認証成功応答をAWS仕様のXML（auth result=yes / username）に修正。
  従来のユーザー名だけのplain text応答はDCV仕様と不一致だった。

「無効化」はPortal利用の禁止。AWS上の停止/終了ではない。EC2とEBSの課金は継続する。
無効化後はPortal上の閲覧、新しいDCV接続、電源操作、スケジュール実行を禁止し、
発行済み未使用DCVトークンを破棄する。管理対象Linuxでは次回Agent同期で既存sessionとOS accountへ失効を適用する。Sharedの席/HOME leaseは失効操作だけでは解放せず、安全なcleanup確認を待つ。
再有効化は既存割り当てを維持し、既存の有効スケジュールも再び対象になる。

スケジュールは起動／停止・曜日・時刻・タイムゾーンを指定し、30秒周期で評価する。同じ分の予定は重複実行しない。Sharedの電源スケジュールは許可しない。

スケジューラーは実行候補を取得するたびにインスタンスの有効状態、所有ユーザーの
有効状態/期限と現在の電源操作権限を検査する。ユーザー/グループの割り当て解除後は、
新しいDCVトークンの認証時と次回スケジュール選択時に最新権限で拒否する。
権限変更前にAWSへ送信済みの操作は取り消せない。

## PortalユーザーとOSログイン

管理対象Linux Agentが固定UID/GID=200000+User IDのOS accountと本人DCV Virtual Sessionを準備し、policy revision、native-only制限、準備結果をPortalへ報告する。Sharedでは有効AssignmentのUserだけを準備し、EFS HOME mount失敗ではReadyにしない。一般UserはEC2電源/旧接続APIでSharedを迂回できない。

Personal EFSの停止移行、Group共有領域、HOME lease、Shared作成/安全な終了、Windows専用Box accountは [二段階導入](environments.md) と [組織構成](../deployment/organization-network.md) を参照する。HOME leaseはAgentのsession/jobなし、sync/unmount済み報告後に解放する。一般Windows UserのAD/SSO連携は別仕様であり、Linux UID方式を流用しない。

## ポータル外でのEC2作成と種類選択

Portal Adminはポータルの「利用者マニュアル」内の管理者マニュアル（`/manual#admin-provision-guide`）からも導入方法を確認できる。現行機能の有無は[機能一覧](../features.md)を参照。ポータル外で管理者が作成したEC2を登録する運用は可能だが、利用者が起動ごとに種類を選択して任意Personal EC2を作成するUI/Workflowは未実装。

| 方式 | 起動時の種類指定とデータ保持 |
|---|---|
| 毎回新しいEC2を作成 | 承認済みGolden AMIと起動テンプレートから作成。作成時に種類を指定し、保存データをEFS/S3等へ分離 |
| 同じEC2を停止・再起動 | 停止確認後に対応する種類へ変更して起動。既存のroot EBSは保持し、AMI更新は別工程 |

### 管理者による外部作成・登録

1. [Golden AMIの手順](environments.md#golden-amiの作成配布更新)で検証済みAMIを用意する。起動テンプレートへAMI、承認済みPrivate Subnet/SG、IAM Profile、公開IPなし、IMDSv2とディスク構成を固定する。固定Personalの永続root EBSを、動的Sharedの削除前提設定と混同しない。
2. 組織の管理者用CLI/IaCから新規作成し、許可された種類だけを選ぶ。一般利用者にAWS Console、Access Key、RunInstances権限を配布しない。起動テンプレートの値はAWS APIでは上書き可能なので、運用ツールとIAMでAMI/Role/SG/種類/上限を制限する。AMIと種類のCPUアーキテクチャを一致させる。ARM64 AMIをm7i等のx86_64へ使用しない。
3. EC2の実IP/SG/公開IPなし・DCV通信を確認し、Portal停止中またはDB排他を確保した保守時間に既存の管理CLIで登録する。次は作成済みEC2の例で、ID・表示名は実環境へ置き換える。

   ```sh
   ./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd add-instance \
     -instance i-0123456789abcdef0 -name DEV-01 -host ec2-private
   ```

4. Portalを起動し、`/admin/instances` でDCVホスト、EC2専用認証キー、利用者・グループ割り当てを設定する。EC2側は[DCV導入手順](dcv.md#導入手順)に従ってroot所有・0600のconfig.jsonを配置しAgentを開始する。外部作成だけではPortal登録・資格情報発行は行われない。動的Shared用BootstrapはworkerがSSMへ発行する認証情報を前提とするため、固定Personalにそのまま流用しない。
5. Personal/EFSを使う場合は `/admin/environments` でOwner、User/Group EFS、HOME移行とleaseを設定する。HOMEはPersonal/Sharedで同時保持できない。Agentの準備、設定版一致、本人のネイティブ接続・HOME・停止/起動を実機確認する。

毎回新規作成する場合、旧EC2終了前にデータの永続化、DCV/job停止、sync/unmount、HOME lease解放を確認する。任意Personal削除・自動cleanupは未実装であり、管理者が所有権とEBS保持設定を確認して処理する。停止中もEBS/EFS等の費用は続く。

### 起動ごとの種類選択を追加する場合

管理者用起動ツール、またはPortal→承認Workflow→workerを拡張し、許可された種類の一覧、利用者単位の作成/種類変更権限、同時台数と費用上限、アーキテクチャ・AMI互換性、重複作成防止、失敗資源の再照合を実装する。既存Shared workerは起動テンプレートの種類を使い、依頼ごとのInstanceType上書きを実装していない。managed-profileのinstance_type変更は管理者のIaC更新であり、起動ごとの利用者選択ではない。Portal RoleへRunInstancesを追加せず、AWS作成権限はworkerへ分離する。

AWS公式：[起動テンプレートからの新規作成とパラメーター上書き](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/launch-instances-from-launch-template.html)、[既存EC2の種類変更](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-resize.html)。

## 将来拡張（未実装）

| 機能 | 設計要件 |
|---|---|
| 任意Personal EC2作成 | SharedとWindowsの承認Workflowは実装済み。任意Personal作成の権限/UIは別仕様 |
| 任意Personal EC2削除 | Shared/Windowsの安全な終了・cleanupは実装済み。任意Personalの破棄は別仕様 |
| SG割り当て変更 | IT管理部門が指定する候補からのみ選択。VPC設定変更権限と分離 |
| イメージバックアップ | AMIとEBSスナップショットを追跡。整合性、保持期間、削除、復元、費用を表示 |
| 容量変更 | ユーザー別 `instance.resize_storage` 許可。EBS拡張とOSファイルシステム拡張を別工程に。縮小は移行方式 |
| インスタンス種類変更 | ユーザー別 `instance.resize_type` 許可。対応候補、停止要否、再起動、復旧、費用を管理 |
| Windows共有ホーム | LinuxのUser/Group EFSは実装済み。Windows SMB/FSx/ADは別仕様 |

拡張権限は `instance.create` / `instance.delete` / `instance.change_security_groups` /
`instance.backup_image` / `instance.resize_storage` / `instance.resize_type` として
ユーザー単位で有効/無効にする計画。既定は無効。管理者が設定し、画面表示だけでなく
APIとバックグラウンドジョブで検査する。現状のcan_controlを拡張権限へ流用しない。
ユーザーが権限を自分で変更できるAPIは設けない。

EFSホームは許可された各Linuxインスタンスで同じユーザーID/パスを使い、他ユーザーの
ホームを読めない権限を設ける。複数ログイン時の設定ファイル競合も受入試験に含める。
Windowsホームの共有は別途SMB/FSx/ディレクトリ連携の設計対象とし、Linux EFS設計を
そのまま適用しない。このインスタンス管理画面はEFS/AMI/Snapshot等を新規作成しない。Shared用IaCでのEFS等の作成は[環境運用](environments.md)の別工程として扱う。

AWS公式:
- https://docs.aws.amazon.com/efs/latest/ug/mounting-fs.html
- https://docs.aws.amazon.com/efs/latest/ug/enforce-identity-access-points.html

## 検証

GoのStore/APIテストで管理者専用操作、無効化後の操作/トークン/スケジュール拒否、
割り当て解除後のトークン拒否、複数グループの権限合成、閲覧のみ、存在しない割当先、
ログイン済み管理者のロール変更を検証する。

ブラウザテストでユーザー/グループ割り当て、メンバー登録、一般ユーザーとしての一覧と
操作権限、無効化/再有効化、1366/FHD/2K/4K/390pxの表示、スマホのログアウトを検証する。
AWS APIはテストコントローラーで代替。OSユーザー同期と実DCV/EFSログインは未検証。
