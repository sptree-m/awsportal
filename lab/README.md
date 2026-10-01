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

Portalの8080/TCPはcreate.sh実行元のグローバルIPv4 /32だけに許可します。テストEC2はinbound 0です。両EC2のEBSはDeleteOnTermination=trueです。

destroy.shはCloudFormation削除完了まで待機し、Project=awsportal-labタグとIAM role/profileの残存検査を行います。
