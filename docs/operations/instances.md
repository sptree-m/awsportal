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

スケジューラーは実行候補を取得するたびにインスタンスの有効状態、所有ユーザーの
有効状態/期限と現在の電源操作権限を検査する。ユーザー/グループの割り当て解除後は、
新しいDCVトークンの認証時と次回スケジュール選択時に最新権限で拒否する。
権限変更前にAWSへ送信済みの操作は取り消せない。

## PortalユーザーとOSログイン

管理対象Linux Agentが固定UID/GID=200000+User IDのOS accountと本人DCV Virtual Sessionを準備し、policy revision、native-only制限、準備結果をPortalへ報告する。Sharedでは有効AssignmentのUserだけを準備し、EFS HOME mount失敗ではReadyにしない。一般UserはEC2電源/旧接続APIでSharedを迂回できない。

Personal EFSの停止移行、Group共有領域、HOME lease、Shared作成/安全な終了、Windows専用Box accountは [二段階導入](environments.md) と [組織構成](../deployment/organization-network.md) を参照する。HOME leaseはAgentのsession/jobなし、sync/unmount済み報告後に解放する。一般Windows UserのAD/SSO連携は別仕様であり、Linux UID方式を流用しない。

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
