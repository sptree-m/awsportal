# 認証・パスキー・管理者パスワード復旧手順

## ログイン失敗・待ち時間・ロック（v1.9.2以降）

パスキー認証・登録コード、または管理者のパスワード／TOTPの誤りをユーザーごとに合算する。Portal Adminにも適用する。成功したログインで連続失敗回数をリセットする。ユーザー不明、アカウント無効／期限切れ、管理者の一時パスワード期限切れ、認証DBエラーは記録するが誤入力回数を増やさない。

| 連続失敗回数 | 次のログインまでの待ち時間 |
|---|---|
| 1〜3回 | なし |
| 4回 | 1分 |
| 5回 | 5分 |
| 6回 | 15分 |
| 7回 | 1時間 |
| 8回 | 3時間 |
| 9回 | 8時間 |
| 10回 | 最終ロック。管理者解除が必要 |

回数と待ち時間は[AppleのiOS／iPadOSロック画面仕様](https://support.apple.com/ja-jp/guide/security/sec20230a10d/web)に合わせる。Portalの最終ロックはデータ消去ではなく管理者解除とする。管理者の同じ間違ったパスワードも毎回数え、入力したpassword・TOTP・登録コード・パスキー応答は保存しない。一般User／Group Adminのpasswordログインは認証方式の拒否として記録し、失敗回数を増やさない。

待ち時間中は認証情報を検証せず、HTTP 429と`Retry-After`（残り秒数）で再試行を案内する。その間の再試行では回数や待ち時間を増やさない。DBへ期限を保存するため、再起動によって解除されない。最終ロックには時間による自動解除がない。最終ロック時に既存のPortalセッションも破棄し、解除後は新たなログインを必要とする。

## 管理者によるロック解除

Portal Adminは`/admin/users`で失敗回数、ロック時刻／再試行期限を確認する。本人確認と原因調査の後、ロック中の行へ解除理由を入力し「ロック解除」を実行する。解除者、対象、日時、理由、解除前の失敗回数とロック時刻を記録する。一般User／Group Adminは解除できない。

解除で失敗回数とロック／待ち時間をリセットする。無効化されたアカウントは解除後も無効のままなので、必要なら別途再有効化する。パスキー登録コード発行・リセット・再登録、管理者パスワード変更、再有効化ではロックを解除しない。待ち時間のみの状態には解除操作を表示せず、期限まで待つ。

## 詳細監査記録

管理者の`/admin/audit`で直近200件を確認する。`login`の`deny`、待ち時間開始の`account.login_delay`、最終ロックの`account.lock`、管理者解除の`account.unlock`を記録する。既存のS3監査出力を有効化していれば同じ記録が出力される。

`detail`はJSONで、`reason`、`remote_ip`、`user_agent`、`forwarded_for`、`failed_attempts`、`retry_at`、`locked_at`、`failure_limit`を保持する。`at`はUTC、期限とロック時刻はUnix秒（0はなし）。失敗理由は`invalid_password`、`invalid_mfa`、`unknown_user`、`account_disabled`、`account_expired`、`temporary_password_expired`、`login_throttled`、`account_locked`、`authentication_error`、`account_state_changed`、`invalid_request`。v2.0.0では`invalid_passkey`、`invalid_enrollment_code`、`passkey_required`、`passkey_unavailable`、`enrollment_required`も記録する。待ち時間中の試行は`login_throttled`、ロック後は`account_locked`で記録する。

`remote_ip`は直接TCP接続した相手のIP。リバースプロキシ経由の場合はそのプロキシのIPになる。`forwarded_for`はクライアントが指定できる参考情報であり、本人確認の根拠にはしない。実際の送信元は管理下のプロキシのアクセスログと照合する。User-Agent／転送元ヘッダーは最大512文字、未登録のユーザー名は監査上最大256文字。パスワード、MFAコード、Cookie、セッションtoken、リクエストURLは記録しない。

ロック・待ち時間の更新と失敗ログは同時に確定し、記録できない場合は認証処理を失敗させる。Portalの`/login`が対象で、既存のDCVワンタイム認証、Proxy認証、GitミラーAPI認証の試行はこの回数に合算しない。既存DCVセッションのOS側終了は行わない。

## 一般ユーザー・Group Admin

通常ログインは端末固定型パスキーのみ。紛失・交換時はPortal Adminが本人確認と原因調査を行い、`/admin/users` で理由付きのパスキーリセットを実行する。全鍵・旧Portalセッション・未完了challenge・未使用DCV tokenを失効させ、30分・使い捨ての登録専用コードを表示する。本人へ承認済みの社内手段で渡し、`/passkeys/enroll` で必要なPCへ再登録する。登録成功ではログインセッションを作らず、パスキーでの新規ログインを必要とする。

追加PCだけなら「登録コード発行」を選び、既存キーを残す。コード再発行で旧コードを失効する。無効／期限切れは別途再有効化、最終ロックは独立した解除、待ち時間は期限まで待機する。[利用者・管理者の詳細手順](passkeys.md)を参照。一般ユーザーの一時password復旧は廃止する。

## Portal Admin
Portal Adminの復旧を別のPortal Adminだけに依存させません。AWS基盤管理者がサーバー上の awsportal-admin を使用してBreak-glass復旧します。

Break-glassでは管理者パスワードとTOTP Secretを同時に再発行し、旧TOTP端末・パスキー・登録コード・challenge・DCV tokenを削除し、失敗回数・待ち時間・最終ロックを解除します。次回ログイン後にパスワード変更を必須とします。実行記録はDBとOS/運用監査にも残してください。既存セッションを残さないよう、Portalを停止した保守時間に実行してください。

```sh
./awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd break-glass-admin -user admin01 -password '十分に長い新しいパスワード'
```

表示される新しいTOTP URIを管理者の端末へ登録してからPortalを再開します。password＋TOTPでログインしてpasswordを変更し、必要なパスキーを再登録します。全管理者がパスキーを失った場合もこの経路で復旧できます。

## 禁止事項
秘密の質問、共通初期パスワード、メールの代替として外部個人サービスへ認証情報を送る運用は使用しません。
