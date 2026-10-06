# 管理者指定ネットワークの設定

この例は既存VPC・Subnet・Route Table・TGWを参照する。既存ネットワークの変更やTGW attachmentの作成は行わない。

```sh
cd terraform/examples/org-existing
cp terraform.tfvars.example terraform.tfvars
# terraform.tfvarsを編集して管理者から指定された値へ置き換える
terraform init
terraform validate
terraform plan
```

`aws_region`、`existing_vpc_id`、`transit_gateway_id`、`subnets`内のSubnet ID・AZ・実効Route Table IDを差し替える。Subnetはmapに追加できる。TGWを使わない構成では`transit_gateway_id = ""`とする。TGW attachment・TGW側ルート・戻り経路は管理者が準備する。

初期設定は既存ネットワークの参照のみ。動的SharedまたはWindows用の起動プロファイルが必要な場合、例の`compute_profile`をコメント解除し、SG・Instance Profile・Golden AMI・検証済みSHA256・SSM設定Parameterを指定する。この設定では新しいLaunch Templateを作成するがEC2は起動しない。Windows用は別stateで`resource_class = "windows-import"`とし、専用Windows AMI・SG・Role・IP範囲を指定する。

`compute_profile.subnet_key`は`subnets`のキー（例：`a`）。`allowed_ipv4_cidrs`は選択Subnet内の承認範囲を明示する。複数CIDRや単一IPの`/32`も指定できる。部分範囲は合計4096アドレス以下とし、SharedとWindowsで重複させない。空リストはこの例では拒否する。AWS予約アドレスは起動workerが除外し、枯渇時に範囲を広げない。

planで作成・変更内容を確認し、設定を適用した後の起動プロファイルは次のコマンドで保存・事前検査できる。

```sh
terraform output -json approved_profile > approved-profile.json
python3 ../../../scripts/network-preflight.py approved-profile.json --region ap-northeast-1
```

`--region`も指定リージョンへ変更する。事前検査はPythonのboto3とAWS読取権限が必要。`compute_profile`未設定では出力はnullなので実行しない。検査は設定の整合性を確認するもので、実際の通信成功は別途確認する。

生成された`approved_profile`は`two-stage.approved_pools`へ渡し、Portal側のAMI/LT登録と一致させる。これだけで自動増減は有効にならない。Portal本体の固定IPはルートTerraformの`portal_private_ip`、固定pilotは`fixed_private_ip`に別途指定する。

全体の導入手順は[組織ネットワーク構成](../../../docs/deployment/organization-network.md)を参照。
