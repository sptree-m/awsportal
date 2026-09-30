# DCVワンクリック接続

## 動作
ユーザーはawsportalへ一度ログインします。EC2一覧の「DCV接続」を押すと、ポータルが対象EC2への権限を再確認し、暗号学的乱数から60秒・1回限りの認証トークンを発行します。

ブラウザーは `dcv://<host>:8443/?authToken=<token>#<session-id>` を開き、Amazon DCVネイティブクライアントを起動します。DCV ServerはExternal Authenticatorとしてawsportalの認証エンドポイントへトークンを照会します。成功時はポータルユーザー名を返し、トークンはその時点で使用済みにします。

ユーザーがEC2/DCV用パスワードを入力する方式ではなく、ポータルにもDCVパスワードを保存しません。

## DCV Server設定
各DCV Serverから、TLSで保護されたawsportalの `/dcv-auth` へ到達できるようにします。DCV ServerのExternal Authentication URLにはこのURLを設定します。設定キー・設定ファイルの場所はDCV ServerのOS/バージョンにより異なるため、導入時はAWS公式のExternal Authentication設定手順に従います。

認証エンドポイントは一般クライアント向けに公開せず、Security Group/FirewallでDCV Server群からだけ到達可能にする構成を推奨します。

## セキュリティ
- Token TTL: 60秒
- Tokenは1回限り
- DBには生Tokenを保存せずSHA-256値のみ保存
- Tokenはユーザー、対象Instance、DCV Session IDに紐付け
- 発行と消費を監査ログへ記録
- Portalのログインセッションとは別Token
- DCV接続権限がないInstanceにはTokenを発行しない

## OSユーザー
External AuthenticationはDCVへの認証をポータルへ委譲する仕組みです。実際のデスクトップセッションで使用するOSユーザー/セッションの準備は別途必要です。ユーザーに個別EC2パスワードを管理させない方針のため、OSユーザーはポータルのユーザー割当と同期して自動プロビジョニングする設計とします。

## クライアント
利用PCにはAmazon DCVネイティブクライアントを導入し、`dcv://` URL SchemeをDCVクライアントへ関連付けます。初回だけブラウザーが外部アプリ起動確認を表示する場合があります。
