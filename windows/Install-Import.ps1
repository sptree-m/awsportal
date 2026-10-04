# Approved installer manifest must be placed by the administrator, root protected.
# Each installer is pinned to an S3 version, SHA256 and signer thumbprint.
param([Parameter(Mandatory=$true)][string]$ManifestPath,[string]$AdminParameter)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$manifest=Get-Content -LiteralPath $ManifestPath -Raw|ConvertFrom-Json
$root='C:\ProgramData\awsportal-import'
$null=New-Item -ItemType Directory -Path $root -Force
& icacls $root /inheritance:r /grant:r 'SYSTEM:(OI)(CI)F' 'BUILTIN\Administrators:(OI)(CI)F' | Out-Null
if($LASTEXITCODE -ne 0){throw 'Root ACL failed'}
$statePath=Join-Path $root 'install-state.json'
$done=@{};if(Test-Path -LiteralPath $statePath){$saved=Get-Content -LiteralPath $statePath -Raw|ConvertFrom-Json;foreach($p in $saved.PSObject.Properties){$done[$p.Name]=$p.Value}}
$required=@('BoxDrive','7zip','Falcon','AWSCLI','DCV')
$names=@($manifest.installers|ForEach-Object {$_.name})
if($names.Count -ne $required.Count -or @($names|Select-Object -Unique).Count -ne $required.Count -or @($required|Where-Object {$_ -notin $names}).Count){throw 'All five pinned installers are required'}
foreach($installer in $manifest.installers){
    if(-not $installer.version_id -or $installer.version_id -eq 'null' -or $installer.sha256 -notmatch '^[a-f0-9]{64}
foreach($installer in $manifest.installers){
    if($installer.name -notin @('BoxDrive','7zip','Falcon','AWSCLI','DCV')){throw 'Unapproved installer'}
    if($done.ContainsKey($installer.name) -and $done[$installer.name] -eq $installer.sha256){continue}
    $file=Join-Path $root ($installer.name+'.'+$installer.extension)
    & aws s3api get-object --bucket $installer.bucket --key $installer.key --version-id $installer.version_id $file | Out-Null
    if($LASTEXITCODE -ne 0){throw 'Installer download failed'}
    if((Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant() -ne $installer.sha256){throw 'Installer digest mismatch'}
    $signature=Get-AuthenticodeSignature -LiteralPath $file
    if($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Thumbprint -ne $installer.signer_thumbprint){throw 'Installer signature mismatch'}
    if($installer.extension -eq 'msi'){$process=Start-Process msiexec.exe -ArgumentList @('/i',('"'+$file+'"'),'/qn','/norestart') -Wait -PassThru}
    else{$process=Start-Process $file -ArgumentList $installer.arguments -Wait -PassThru}
    if($process.ExitCode -notin @(0,3010)){throw 'Installer failed'}
    $done[$installer.name]=$installer.sha256
    [IO.File]::WriteAllText($statePath,($done|ConvertTo-Json -Compress))
    if($process.ExitCode -eq 3010){$reboot=$true}
}
# Dedicated Windows password is supplied only by Secrets Manager, never Portal
# password/UserData/AMI. Permit the administrator role to retrieve it separately.
if($AdminParameter){$secret=& aws ssm get-parameter --name $AdminParameter --with-decryption --query Parameter.Value --output text}
else{$secret=& aws secretsmanager get-secret-value --secret-id $manifest.admin_secret_arn --query SecretString --output text}
if($LASTEXITCODE -ne 0){throw 'Dedicated Windows credential unavailable'}
$credential=$secret|ConvertFrom-Json
$password=ConvertTo-SecureString $credential.password -AsPlainText -Force
if(Get-LocalUser -Name $credential.username -ErrorAction SilentlyContinue){Set-LocalUser -Name $credential.username -Password $password}
else{New-LocalUser -Name $credential.username -Password $password | Out-Null}
Add-LocalGroupMember -Group 'Administrators' -Member $credential.username -ErrorAction SilentlyContinue
$secret=$null;$credential=$null;$password=$null
# Optional Box ordinary-cache maximum: offline files can exceed this setting.
if($manifest.PSObject.Properties.Name -contains 'box_cache_maximum_gb'){
    New-Item -Path 'HKLM:\SOFTWARE\Box\Box' -Force | Out-Null
    New-ItemProperty -Path 'HKLM:\SOFTWARE\Box\Box' -Name 'MaximumCacheSize' -PropertyType DWord -Value ([int]$manifest.box_cache_maximum_gb) -Force | Out-Null
}
if($reboot){Restart-Computer -Force; throw 'REBOOT_PENDING'}
 -or $installer.extension -notin @('msi','exe')){throw 'Immutable signed installer required'}
}
$reboot=$false
foreach($installer in $manifest.installers){
    if($installer.name -notin @('BoxDrive','7zip','Falcon','AWSCLI','DCV')){throw 'Unapproved installer'}
    if($done.ContainsKey($installer.name) -and $done[$installer.name] -eq $installer.sha256){continue}
    $file=Join-Path $root ($installer.name+'.'+$installer.extension)
    & aws s3api get-object --bucket $installer.bucket --key $installer.key --version-id $installer.version_id $file | Out-Null
    if($LASTEXITCODE -ne 0){throw 'Installer download failed'}
    if((Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant() -ne $installer.sha256){throw 'Installer digest mismatch'}
    $signature=Get-AuthenticodeSignature -LiteralPath $file
    if($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Thumbprint -ne $installer.signer_thumbprint){throw 'Installer signature mismatch'}
    if($installer.extension -eq 'msi'){$process=Start-Process msiexec.exe -ArgumentList @('/i',('"'+$file+'"'),'/qn','/norestart') -Wait -PassThru}
    else{$process=Start-Process $file -ArgumentList $installer.arguments -Wait -PassThru}
    if($process.ExitCode -notin @(0,3010)){throw 'Installer failed'}
    $done[$installer.name]=$installer.sha256
    [IO.File]::WriteAllText($statePath,($done|ConvertTo-Json -Compress))
    if($process.ExitCode -eq 3010){$reboot=$true}
}
# Dedicated Windows password is supplied only by Secrets Manager, never Portal
# password/UserData/AMI. Permit the administrator role to retrieve it separately.
if($AdminParameter){$secret=& aws ssm get-parameter --name $AdminParameter --with-decryption --query Parameter.Value --output text}
else{$secret=& aws secretsmanager get-secret-value --secret-id $manifest.admin_secret_arn --query SecretString --output text}
if($LASTEXITCODE -ne 0){throw 'Dedicated Windows credential unavailable'}
$credential=$secret|ConvertFrom-Json
$password=ConvertTo-SecureString $credential.password -AsPlainText -Force
if(Get-LocalUser -Name $credential.username -ErrorAction SilentlyContinue){Set-LocalUser -Name $credential.username -Password $password}
else{New-LocalUser -Name $credential.username -Password $password | Out-Null}
Add-LocalGroupMember -Group 'Administrators' -Member $credential.username -ErrorAction SilentlyContinue
$secret=$null;$credential=$null;$password=$null
# Optional Box ordinary-cache maximum: offline files can exceed this setting.
if($manifest.PSObject.Properties.Name -contains 'box_cache_maximum_gb'){
    New-Item -Path 'HKLM:\SOFTWARE\Box\Box' -Force | Out-Null
    New-ItemProperty -Path 'HKLM:\SOFTWARE\Box\Box' -Name 'MaximumCacheSize' -PropertyType DWord -Value ([int]$manifest.box_cache_maximum_gb) -Force | Out-Null
}
if($reboot){Restart-Computer -Force; throw 'REBOOT_PENDING'}
