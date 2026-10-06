# 初期設定手順

## 管理者作成
ポータル停止中、またはDB排他を考慮した保守時間に管理CLIを使用します。

`go run ./cmd/awsportal-admin -db /var/lib/awsportal/awsportal.db -cmd create-user -user admin01 -password '十分に長い初期パスワード' -role portal_admin`

表示されるTOTP URIをAuthenticatorへ登録します。初期パスワードはログイン確認後に運用ルールに従って変更してください。

## 一般ユーザー
`go run ./cmd/awsportal-admin -db ... -cmd create-user -user user01 -password '...' -role user`

一般ユーザーにはAWS IAM User、Console、Access Keyを発行しません。

## EC2登録
`go run ./cmd/awsportal-admin -db ... -cmd add-instance -instance i-0123456789abcdef0 -name DEV-01 -host dev01.internal.example`

## グループ
`go run ./cmd/awsportal-admin -db ... -cmd add-group -group ADAS`

インスタンスをグループへ割り当てる場合:
`go run ./cmd/awsportal-admin -db ... -cmd assign-group -instance i-0123456789abcdef0 -group ADAS`

ユーザーのグループ所属はPortal Adminの`/admin/instances`画面で追加・除外できます。EC2への直接／グループ割り当ても同じ画面から管理します。詳細は[インスタンス管理](../operations/instances.md)を参照してください。
