param([Parameter(Mandatory=$true)][string]$ConfigParameter)
$ErrorActionPreference='Stop'
$root='C:\ProgramData\awsportal-import'
$null=New-Item -ItemType Directory -Path $root -Force
& icacls $root /inheritance:r /grant:r 'SYSTEM:(OI)(CI)F' 'BUILTIN\Administrators:(OI)(CI)F' | Out-Null
if($LASTEXITCODE -ne 0){throw 'ACL failed'}
$imds=Invoke-RestMethod -Method Put -Uri 'http://169.254.169.254/latest/api/token' -Headers @{'X-aws-ec2-metadata-token-ttl-seconds'='60'}
$iid=Invoke-RestMethod -Uri 'http://169.254.169.254/latest/meta-data/instance-id' -Headers @{'X-aws-ec2-metadata-token'=$imds}
$identity=Invoke-RestMethod -Uri 'http://169.254.169.254/latest/dynamic/instance-identity/document' -Headers @{'X-aws-ec2-metadata-token'=$imds}
$env:AWS_DEFAULT_REGION=$identity.region
$value=& aws ssm get-parameter --name $ConfigParameter --with-decryption --query Parameter.Value --output text
if($LASTEXITCODE -ne 0){throw 'Approved bootstrap configuration unavailable'}
$config=$value|ConvertFrom-Json
if($config.interactive_user -ne 'AwsImportAdmin'){throw 'Dedicated workflow administrator required'}
# The workflow tags contain the durable job identity; no login secrets are tags.
$instance=& aws ec2 describe-instances --instance-ids $iid --output json
if($LASTEXITCODE -ne 0){throw 'Instance identity unavailable'}
$tags=($instance|ConvertFrom-Json).Reservations[0].Instances[0].Tags
$job=($tags|Where-Object Key -eq 'awsportal:operation').Value
$runtime=@{PortalURL=$config.portal_url;InteractiveUser=$config.interactive_user;JobID=$job;CredentialParameter=($config.credential_prefix+'/'+$iid+'/credential')}
[IO.File]::WriteAllText((Join-Path $root 'runtime.json'),($runtime|ConvertTo-Json -Compress))
$installer=Join-Path $root 'installers.json'
& aws s3api get-object --bucket $config.installer_bucket --key $config.installer_key --version-id $config.installer_version_id $installer | Out-Null
if($LASTEXITCODE -ne 0 -or (Get-FileHash -LiteralPath $installer -Algorithm SHA256).Hash.ToLowerInvariant() -ne $config.installer_sha256){throw 'Approved installer manifest verification failed'}
# Persist/retry the same bootstrap after reboot. Installer checkpoints prevent
# repeated installations. No credential appears in task arguments.
$action=New-ScheduledTaskAction -Execute 'powershell.exe' -Argument ('-NoProfile -File "'+$root+'\Bootstrap-Import.ps1" -ConfigParameter "'+$ConfigParameter+'"')
$trigger=New-ScheduledTaskTrigger -AtStartup
$principal=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
Register-ScheduledTask -TaskName 'awsportal-import-bootstrap' -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
& (Join-Path $root 'Install-Import.ps1') -ManifestPath $installer -AdminParameter ($config.credential_prefix+'/'+$iid+'/windows-admin')
# Installation/account provisioning alone is not offline/login readiness.
Unregister-ScheduledTask -TaskName 'awsportal-import-bootstrap' -Confirm:$false
