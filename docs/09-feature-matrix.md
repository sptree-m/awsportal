# 機能実装・検証マトリクス

更新基準: main相当コード、必須CI、Disposable Lab、本番向けTerraform/DCV設定を分けて評価する。

## ステータス
- ① 実装済み・テスト済み: 実装があり、自動テストで主要な正常系/拒否系を検証している。
- ② 実装済み・実機未確認: 実装は完了しているが、AWS/DCV等の実サービスを含むEnd-to-End確認が未完了。
- ③ 部分実装: 中核部品はあるが、要求された運用フローを完結するUI/連携/保護の一部が不足。
- ④ 未実装: 設計・文書上の要件/候補はあるが実装がない。

「①」は本番運用承認を意味しない。AWS/DCV固有の挙動は②の実機試験を別途必要とする。

## 認証・アカウント

| 機能 | 状態 | 根拠 / 残課題 |
|---|---|---|
| ID/パスワード認証、bcrypt | ① | auth unit test。 |
| Portal Admin TOTP必須 | ① | RFC6238 test + HTTP loginでTOTPなし拒否/TOTPあり成功を検証。 |
| MFA端末登録・検証・削除、最大3台 | ① | store unit test。QR生成/画面操作のブラウザE2Eは別。 |
| パスワード変更 | ① | DB状態遷移をunit test。 |
| 一時パスワード30分・初回変更必須 | ① | store unit test。 |
| Portal Admin通常リセット禁止 | ① | unit test。 |
| Break-glass Admin復旧 | ② | CLI/Store実装あり。実運用の複数人承認、OS監査連携は実機運用確認が必要。 |
| 30日未アクセス自動無効化・再有効化 | ① | store unit test。 |
| TOTP Secret暗号化/KMS | ④ | 現状SQLiteに平文。docs/03でも本番前移行要件。 |

## RBAC・ユーザー/グループ

| 機能 | 状態 | 根拠 / 残課題 |
|---|---|---|
| user / group_admin / portal_admin ロール | ③ | ロール値と表示制御は実装。group_admin専用の管理権限フローは未実装。 |
| ユーザー直接割当によるEC2参照/操作制御 | ① | store RBAC test + HTTP Start/Stop拒否系をmock AWSで検証。 |
| グループ割当によるEC2参照/操作制御 | ① | store RBAC test。 |
| グループ作成・Instance→Group割当 | ① | Portal Admin画面/APIとCLI。Store/API/ブラウザで割り当て・権限合成を検証。 |
| User→Group所属管理 | ① | Portal Admin画面/API。所属追加/除外、割当解除後の認証拒否を検証。 |
| Group Adminによる所属/Instance管理 | ④ | 権限モデル/管理画面/操作APIが未実装。 |
| Portal Admin ユーザー作成/無効化/再有効化 | ① | handler実装 + storeの無効化/再有効化テスト。UIブラウザE2Eは別。 |

| インスタンス無効化/再有効化 | ① | Portal Admin専用画面/API。無効化後の電源/DCV/スケジュール拒否を検証。AWS停止/削除は行わない。 |

## EC2・スケジュール

| 機能 | 状態 | 根拠 / 残課題 |
|---|---|---|
| EC2一覧・詳細・現在状態表示 | ② | AWS SDK実装。mock/DBテストはあるが実EC2 Describeのv1.1.3 E2E待ち。 |
| EC2 Start / Stop | ② | HTTP RBAC + mock AWS test。実AWS Start/Stopのv1.1.3 E2E待ち。 |
| 遷移中ポーリング | ① | dashboard.js実装、静的配信をHTTP test。ブラウザDOM E2Eは未実施。 |
| 曜日/時刻 Start/Stop scheduler | ③ | DB・handler・30秒tick実装。登録UI、一覧/削除/有効無効、scheduler自動テストが不足。 |
| ユーザーごとのCost Allocation Tag集計 | ② | Cost Explorer実装。実アカウントのタグ/反映データでE2E未確認。 |
| Cost CSV | ② | handler実装。実Cost Explorer結果でE2E未確認。 |

## DCV

| 機能 | 状態 | 根拠 / 残課題 |
|---|---|---|
| DCVワンクリック用60秒/1回Token | ① | store unit + HTTP発行/消費/再利用拒否test。 |
| TokenをSHA-256で保存 | ① | unit test対象。 |
| Instance割当のないユーザーへのToken拒否 | ① | VisibleInstances/RBACに依存し、HTTP発行経路で割当確認を実施。 |
| DCV External Authenticator連携 | ② | AWS仕様のXML応答とescapeをテスト。従来のplain textを修正。実DCV Serverからの照会は未確認。 |
| OSユーザー/Session自動プロビジョニング | ④ | docs/07で別途必要と明記、実装なし。 |
| DCV一般ユーザーDLP permission | ② | deny設定ファイルと静的テストあり。利用するDCV Server versionで未検証。 |

## UI・配布・AWS基盤

| 機能 | 状態 | 根拠 / 残課題 |
|---|---|---|
| Go SSR + ローカルCSS/JS | ① | 外部UI依存検査あり。 |
| Rounded M+ Regular/Bold | ① | sfnt/weight検査 + CSS参照 + HTTP 200/MIME test。 |
| /static CSS/JS/font配信 | ① | StripPrefix不備を修正しHTTP integration testを追加。 |
| ARM64 t4g.micro相当 | ① | ARM64 runnerで2vCPU/1GiB制約test。実EC2継続稼働は②。 |
| Release ARM64 awsportal + awsportal-admin | ① | CIで両方buildし、Release workflowがremote assetを再DLしてchecksumとtar内容を検証。 |
| Disposable Lab create | ② | ARM64/health自動判定あり。v1.1.3のAWS実機再作成確認待ち。 |
| Disposable Lab destroy / 残留検査 | ② | スクリプト実装・既往実機確認あり。継続的AWS CIではない。 |
| Release checksum検証後のLab install | ① | UserDataでsha256照合、両binary存在確認後にinstall。 |
| 本番Terraform SG/IAM/IMDSv2/EBS暗号化 | ① | terraform validate + security static test。AWS apply E2Eは②。 |
| Outbound Allowlist / Firewall | ③ | Terraformは443/0.0.0.0/0のbaseline。承認済みFirewall/Proxyへの制限は未実装。 |
| CloudTrail / VPC Flow Logs / 中央ログ | ④ | docs/03の本番要件だがTerraform実装なし。 |
| Proxy管理機能 | ② | UI/認証付きHTTP転送・HTTPS CONNECT・許可/拒否ルールを実装。実ネットワーク/利用ツールは未検証。docs/12-proxy.md。 |
| HTTPS内部メソッド/URL検査・直接通信の迂回防止 | ④ | TLS検査は対象外。利用EC2の直接外向き通信制限は別途必要。 |

## 今回の監査で修正した不具合・不足

1. Embedded FSのrootは `web/...` だが、HTTP pathは `/static/web/...` だったため、FileServerへ渡すprefix処理が不足していた。 `/static/` をStripPrefixし、CSS/JS/Regular/Bold fontのHTTP 200/MIME/Cache-Controlを自動検証する。
2. v1.1.2でRelease archiveとLab UserDataの要求binaryが不一致だった。v1.1.3では `awsportal` と `awsportal-admin` を同梱。さらにCIで両ARM64 binaryをbuildし、Release tar内容を検証する。
3. Release assetは名前の存在だけでなく、GitHub Releaseから再downloadしてsha256とtar内容を検証する。既存tagでも壊れていれば再build/uploadする。
4. Lab EC2はRelease tarとsha256を両方downloadし、checksum一致と両binary存在を確認してからinstallする。
5. Lab READMEが旧CIDR挙動と旧Instance Typeを記載していたため、現行Disposable Lab仕様へ同期した。

## AWS/DCV実機で残る受入試験

- v1.1.3でDisposable Labを新規作成し `/healthz` PASS。
- Portal UIへログインしCSS/2 fontが200で取得され実際に適用されること。
- 実EC2のStart→running、Stop→stoppedをPortalから確認。
- Cost Explorerに対象月/タグデータがある環境で表示/CSVを確認。
- 実DCV ServerでExternal Authenticationとone-time tokenを確認。
- 使用するDCV versionでDLP permission名/挙動を確認。

今回のインスタンス管理と将来拡張の詳細は docs/11-instance-administration.md を参照。
