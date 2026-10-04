# Shared第1段階：固定pilot基盤の実装と導入手順

対象：二段階実装仕様書 v1.0（2026-10-04）、基準main 933e6d4 / v1.6.0。

## この変更の範囲

これは第1段階の接続・予約・HOME・計測基盤を追加する変更であり、二段階全体の完成版ではない。第1段階の公開受入も未完了。本基盤PR単体ではVERSIONを変更しない。後続のJob計測変更と合わせて1.7.0-rc.1候補版を配布する（第1段階受入完了ではない）。本番へ自動反映しない。既存instancesは自動でOwnerを推定せずlegacyとして残す。

| 領域 | この変更で追加したもの | 未完了・別変更が必要なもの |
|---|---|---|
| Environment | Personal/Shared型、Owner制約、Group、承認profile名、ACL、登録EC2、revisionと監査 | 既存PersonalのEFS HOME移行・上位UI統合 |
| 固定Shared | 1環境1台、定員2人、要求の冪等性、原子的予約、単一HOME lease、controller再開 | 120秒予約TTLの自動整理、Workflow期限、複数controller fencing |
| 割当 | CPU/Memory平均70%未満、5分窓、90秒鮮度、boot/sequence/generation、60秒warmup例外の記録 | 本番負荷による閾値・sample粒度の承認 |
| DCV | Assignment限定manifest、本人EFS・session・policy証明、native token、旧API電源/接続/スケジュール迂回拒否 | AWS上のDCV実接続・EFS他人HOME遮断試験 |
| Job/終了 | Job登録、cgroup v2 CPU/Memory/I/O raw計測、systemd user scopeラッパー、/procの保守的作業検出、切断保持、2段階の明示解放、idle15分の候補記録のみ | Memory時間積分・終了瞬間counter完全性、プロセス分類の実機精査 |
| Metrics | Agentの30秒sample、実接続とsessionを別計測、永続S3出力queue・checksum・retry、重複再送排除 | Parquet出力、Storage容量/counter計測、30日Dashboard、履歴保持期間/圧縮 |
| IaC | オプトインの固定EC2/User EFS/AP/Backup/S3/限定SG/限定Role module | Group EFS、scratch XFS quota、Dataset cache/manifest、CUR出力・保存設定 |
| 第2段階 | 後方互換のrequest/assignment/generation契約とfeature境界 | 動的増設/drain/終了、請求配賦/照合、Windows Box Importは未実装 |

第1段階の必須項目がすべて揃う前に「pilot受入済み」と扱わない。UIに表示する第1段階は機能境界を示し、受入完了を意味しない。

## 追加UI/API

| 操作 | 経路 | 認可/副作用 |
|---|---|---|
| 一覧・進捗 | GET /environments | 本人ACLのある環境。読み取りのみ |
| 要求 | POST /environments/{id}/connect | idempotency_key必須、本人ACL再確認、最大2席 |
| 状態 | GET /environment-requests/{id} | 本人のみ。作成/終了/トークン発行なし |
| 利用終了 | POST /environment-requests/{id}/end | 本人のみ。Job/不明作業/古いAgentは保持 |
| Native接続 | POST /environment-requests/{id}/dcv | READY/CONNECTEDと本人EFS/session/policyを再確認 |
| 管理 | GET/POST /admin/environments | Portal Admin限定。create/register/acl/storageのみ |
| Agent | 既存 /api/dcv/agent/state, heartbeat, auth | EC2固有credential。Sharedはv2報告のみ |

Sharedの一般ユーザーにはEC2を選択させない。接続要求はPOSTで作成し、画面の10秒pollは読み取りだけを行う。席待ち、負荷、HOME未設定、Agent/policy/metrics異常の理由を表示する。controllerは専用 `internal/shared` に置き、Web要求から分離した。

一般ユーザー/Group Adminがenvironment.manage ACLを持っていても、profile/権限/Storage/EC2登録などの管理操作はPortal Admin限定とする。Stage 1の固定profileは2席・CPU/Memory 70%・5分窓・idle15分を変更できない。auto-scale-out、auto-terminate、ImportをPOSTしても拒否する。AWS create/terminate capability自体をworkerに渡していない。

## 状態と永続性

- Request：WAITING → PREPARING_USER → READY → CONNECTED。明示終了でRELEASING → RELEASED。
- Assignment：PREPARING → READY/CONNECTED → DISCONNECTED_GRACE/JOB_HELD。切断だけでは解放しない。
- READY以降のcancelは明示終了と分ける。WAITINGの取消はCANCELLED。
- RELEASINGも占有席とHOME leaseを保持する。Agentの本人sessionなし、接続なし、Jobなし、不明作業なしとclosed_assignments確認後にだけ解放する。
- ACL剥奪/無効化は新規認証を即拒否する。既存セッション/Jobのセキュリティ失効は従来Agent方針を継承する。席は自動解放しない。
- SharedのPortal通信障害は既存desktop/Jobをkillしない。認証はPortal再確認が必要なので新規接続は失敗する。Shared判定はroot保護のlocal状態に保存し、Agent再起動後にも継承する。
- sampleの同一boot/sequenceの同一内容再送は受理するが、鮮度を更新しない。変更再送、順序逆転、古いbootへの復帰、異なるgenerationは拒否する。
- 未使用デスクトップも保持するため、予約TTLによる自動破棄はこの変更では提供しない。放置した席は本人の利用終了とAgent確認で解放する。Portalが不通のときに管理者がDBだけを操作して解放しない。

SQLiteの1接続・単一workerを使用する。新しいテーブルとindexは再実行可能なmigrationで追加し、既存User ID/legacyテーブルを変更しない。leaseとrequest/assignmentは永続DBにあるため、worker再起動で二重予約を作らない。複数Portalプロセスでの運用は未対応。

## AWS構築の前提

`terraform/modules/shared-pilot` は既存Terraformから呼び出すオプトインmodule。既存Portal Terraformに自動組み込んでいない。

必要な入力：管理済VPC、AZごとのprivate subnet、社内DCV接続CIDR、Portal/Interface Endpoint SG、S3 Gateway Endpointのprefix list、承認CPU Golden AMI、Portal Role、既存Dataset bucket ARN、metrics bucket名、Environment/Group ID、pilot User IDのmap。

Golden AMIには次が必要。

1. Ubuntu 24.04 / DCV / Xfce / 更新版Agent・desktop・job wrapper。
2. 承認版amazon-efs-utils。TLS/IAM mount helper、DNS/EFS Endpointへの到達性。
3. rootで起動するAgent、sudo/CAP_SYS_ADMINのない一般User、既存IMDS遮断、native-only policy。
4. AMIから全User HOME、Agent machine credential、local accounts/sequence/environment状態を除去する。credentialは起動後に登録/配布する。
5. Group EFS、scratch quota、Dataset cache、CUR設定の後続変更を適用してからpilot受入へ進む。

このmoduleはEFS暗号化、Regional/elastic throughput、AWS Backup有効化、1 User=1 FS/AP、0700と固定UID/GID、EFS保持、EC2 root gp3暗号化/終了時削除、IMDSv2を設定する。root EBSは100GiBの基盤用であり、Dataset用2.5TBやUser scratch quotaは未提供。

**Sharedのroot-mounted EFSでは、Access PointのPosixUser強制置換を使わない。** クライアントのUID/GIDをそのまま使ってEFS側の0700を評価する。別ユーザーの要求までOwnerのUIDへ置き換える構成を避ける。APはHOME rootの範囲とCreationInfoを提供し、TLS/IAM/AP条件とClientRootAccess拒否を組み合わせる。既存APを登録する場合もこの方式を実機確認する。

参照：[AWSのPOSIX UID/GID](https://docs.aws.amazon.com/efs/latest/ug/user-and-group-permissions.html)、[APのidentity置換](https://docs.aws.amazon.com/efs/latest/ug/enforce-identity-access-points.html)。この設計の採用可否、他User UIDによる読み取り拒否はS1-02/S1-10の実機受入で確認する。

SGに一般Internet向けegressは追加しない。443はPortal/承認Endpoint/S3 prefix list、2049はEFS SGに限定する。DNS制限、Endpoint policy、VPC経路/Firewall、既存別SGの広いegressがないことはIT管理者が確認する。SGは複数付与時に許可が合算されるため、別SGで一般Internetを許可しない。

moduleはS3 versioning/public block/AES256/TLS必須とPortal Roleのusage/v2 prefixへのPutObjectを追加する。CUR bucketとは別である。S3 bucketを削除したりEFSを自動destroyしたりする操作は提供しない。

## 管理者の導入順序

1. Portalを停止しSQLite backupを取得する。backupはDBとWALの整合を確保して行う。labの複製DBでmigrationと既存ログイン/MFA/DCV/mirrorを確認する。
2. 管理者が承認AMIとnetwork profileを準備する。pilotは一般User2名に限定する。既存Personal/legacyでの作業を停止・保存し、同時HOME利用を避ける。Personal EFS切替はまだ行わない。
3. Environment AdminでShared環境を作成する。Groupを持つだけでは利用権限にならない。
4. IaCで固定EC2/User EFSを準備する。Group/UserのAWS quota、AZ/IP余裕、Backup保持を確認する。module outputのFS/AP/UID/GIDを正本として登録する。
5. Instance AdminでEC2/接続先/native DCVを登録し、EC2固有credentialを安全に配布する。更新版Agentを導入する。旧AgentはSharedとしてReadyにできない。
6. Environment AdminでEC2をSharedへ登録する。この際に旧scheduleを無効化し旧tokenを破棄する。1環境1台に限定する。
7. pilot User EFSとconnect ACLを登録する。HOME 0700、Owner UID/GIDを確認する。AgentはローカルHOMEが空でなければ自動上書きせず失敗する。移行コピー・検証は本人作業停止中に管理者が実施する。
8. Portalへ `AWSPORTAL_USAGE_BUCKET=<module output>` を設定して再起動する。未設定でもDBにsampleは保存するが、S3保存の受入は未完了となる。bucketを推測/自動作成しない。
9. 残りのGroup EFS/cache/quota/CUR/計測変更を揃え、S1受入と2ユーザー5営業日以上のpilotを実施する。未解決の分離/データ/Job不具合があれば第2段階へ進まない。

Stage 1では停止中EC2を要求から起動しない。管理者がIaC/AWS管理面で復旧してからAgent同期とsampleを確認する。終了候補が表示されてもTerminateは実行しない。

## 利用者の操作

1. EnvironmentsでSharedカードの「接続を要求」を押す。
2. 待機理由を確認する。準備中に画面を更新しても要求は重複しない。
3. READYで「Native DCVで接続」を押す。60秒・1回tokenをその場で発行する。
4. 長時間Jobは `awsportal-job python train.py` 等で起動する。systemd user scopeがない環境ではwrapperは失敗する。手動起動の作業も不明作業として保護する。
5. 通信切断後は同じ要求から再接続する。席もHOMEも維持される。
6. Jobを終え、作業を保存し、アプリを終了して「利用終了」を押す。RELEASINGはAgentの確認待ち。手動でDBからseatを削除しない。

現在のプロセス分類は保守的で、通常アプリを残すと解放が保留される。認識済desktopプロセス名に依存するため、これを自動Terminateの最終安全証明として利用してはいけない。Job/cgroupの追加・品質・既知制限は docs/19-release-1.7.0-rc.1.md を参照。第2段階には欠測/終了瞬間counterと未分類作業の実機検証が必要。

## 検証と受入の対応

| 受入ID | 自動テストで確認した部分 | 残る確認 |
|---|---|---|
| S1-01 | 全既存Go回帰、認証/DCV/mirror、Agent回帰、ARM64 build | AWS既存環境での回帰 |
| S1-02 | API本人境界、ACL剥奪、管理者境界、旧電源/スケジュール拒否 | 他Group・HOME・OS・credentialの実機遮断 |
| S1-03 | 3要求を並行実行し2席だけ予約 | 実DCV2接続 |
| S1-04 | 70%境界、古い計測、warmup、boot/sequence/generation | 実CPU/Memory負荷 |
| S1-05 | token準備条件、mount失敗/ローカルHOME禁止、policy変更保留 | 実EFS/AP異常・session異常 |
| S1-06 | HOME固定identityとIaC保持設定 | EC2再作成とデータ照合 |
| S1-07 | Job時解放拒否、切断保持、通信障害でSharedをkillしない | sleep/I/O/Jupyter/未保存desktop実機試験 |
| S1-08 | 冪等性、取消、RELEASING lease、Agent確認後解放、migration/worker再実行 | 実Portal再起動と障害復旧 |
| S1-09 | 同一再送の鮮度維持、変更再送/retired boot拒否、export失敗→再開 | Storage counter、Parquet、実S3障害 |
| S1-10 | SG/IAM/IMDS設定の静的チェック | 本番相当の通信試験 |
| S1-11 | registryのEC2帰属・storage変更履歴 | CUR構築/請求元保存・帰属完全性 |
| S1-12 | stale/seat/job等の保留とidle timer reset | 15分の実機連続性/終了候補監査 |

ローカル検証結果（2026-10-04）：`go test -race ./...` 合格、Linux ARM64 Portal/Admin build合格、Agent 40テスト合格、lab Python 3テスト・CloudFormation graph/substitutions/UserData・cfn-lint合格、security/font/htmx合格、Terraform recursive fmtチェック合格。

`scripts/test-all.sh` はTerraform validateで停止した。実行環境でproviderのUnix socket作成が拒否されるため、Terraform schema validationは未完了。GitHub CIでの確認が必要。AWS実機の証跡はこの変更では取得していない。S1全項目が合格するまでは第1段階完了と記録しない。S2は全項目未受入。

## 復旧

- DB backupと変更版binaryをセットで管理する。追加テーブルがある状態の無条件downgradeは禁止する。
- Shared不具合時は新規ACLを外して新規要求を止め、既存Job/EFS/席の状況を確認する。ACL解除は既存セキュリティ失効にも影響するため事前に作業状態を確認する。
- S3障害時はqueueを削除しない。EXPORTEDになっていない行を次のworkerが再送する。DB容量を監視する。履歴保持/圧縮は後続変更なので無制限の長期運用を開始しない。
- HOME mountが壊れたときにローカルHOMEへfallbackしない。Agent同期を復旧してから接続する。
- 手動でEC2を入れ替える場合は既存Job/sessionを保存・終了し、HOMEを保持して行う。動的再生成・credential失効Workflowは第2段階の別変更。

## 次の変更単位

1. 第1段階の残り：Personal EFS切替、Group EFS、cache/manifest/quota、Job/cgroupの実機検証と計測完全性、Storage/Parquet/履歴、CUR、移行/復旧試験。
2. 第1段階のAWS受入と5営業日pilot。
3. 第2段階：Provisioning/再照合を別PRで追加。
4. 第2段階：drain/安全な終了を別PRで追加。まず判定のみ。
5. 第2段階：CUR配賦/請求照合を別PRで追加。
6. 第2段階：Windows Box Importを別PRで追加。
7. 10人/5台、障害、実費、約500GB Boxの統合受入後に第2段階を公開する。
