# Disposable AWS lab

CloudShellで実験専用環境を作成・完全削除するための構成です。

## 作成
```bash
git clone --depth 1 https://github.com/sptree-m/awsportal.git && cd awsportal && bash lab/create.sh
```

## 完全削除
```bash
cd ~/awsportal && bash lab/destroy.sh
```

作成物: 専用VPC / public subnet / IGW / route table / Portal SG / Test SG / Portal IAM role + instance profile / SSM bootstrap parameter / t4g.micro Portal EC2 / t4g.nano Ubuntu test EC2 / encrypted 8 GiB gp3 x2。NAT Gateway、ALB、Elastic IPは作成しません。

Portalの8080/TCPはcreate.shが実行元のグローバルIPv4を自動取得し、その /32 だけに許可します。`ALLOWED_CIDR` 指定時も `0.0.0.0/0` は拒否します。create.shはCloudFormation作成後、2台のEC2がrunningかつARM64であることとPortalの `/healthz` 応答まで確認し、全検査合格時のみ `RESULT: PASS` を出力します。テストEC2はinbound 0です。両EC2のEBSはDeleteOnTermination=trueです。

destroy.shは削除前にCloudFormationの物理Resource IDを記録し、削除完了後にStack消滅、Project=awsportal-labタグ、EC2/EBS/ENI/VPC/SG、IAM role/profile、および記録済みVPC/Subnet/SG/IGW/RouteTable等を再検査します。残留または削除エラーが1件でもあれば `RESULT: FAIL` と終了コード2、全検査合格時のみ `RESULT: PASS` を出力します。Tagging APIの参照件数と、AWSが履歴として返すterminated EC2の件数・Instance IDも表示しますが、terminated EC2は残留リソースとは判定しません。
