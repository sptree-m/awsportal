# Run in the dedicated administrator's interactive Box session, never SYSTEM.
# Box sign-in/SSO/MFA and Make Available Offline are manual. No Box API is used.
param([Parameter(Mandatory=$true)][string]$ConfigPath,
      [ValidateSet('LoginReady','Upload')][string]$Action)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
if ([Security.Principal.WindowsIdentity]::GetCurrent().IsSystem) { throw 'Interactive Box user session required' }
$config=Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
if ([Security.Principal.WindowsIdentity]::GetCurrent().Name.Split('\')[-1] -ne $config.InteractiveUser) { throw 'Dedicated approved interactive user required' }
if ($config.PortalURL -notmatch '^https://[^/]+$') { throw 'HTTPS Portal URL required' }
function Write-AtomicText([string]$Path,[string]$Text) {
    $temporary=$Path+'.tmp'
    $bytes=[Text.UTF8Encoding]::new($false).GetBytes($Text)
    $stream=[IO.File]::Open($temporary,[IO.FileMode]::Create,[IO.FileAccess]::Write,[IO.FileShare]::None)
    try{$stream.Write($bytes,0,$bytes.Length);$stream.Flush($true)}finally{$stream.Dispose()}
    if([IO.File]::Exists($Path)){[IO.File]::Replace($temporary,$Path,$null)}else{[IO.File]::Move($temporary,$Path)}
}
$statePath=Join-Path $env:LOCALAPPDATA 'awsportal-import-state.json'
$state=@{sequence=0; job_id=$config.JobID}
if(Test-Path -LiteralPath $statePath){$prior=Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json;if($prior.job_id -ne $config.JobID){throw 'Job identity changed'};$state.sequence=[long]$prior.sequence}
function Invoke-AwsCli([string[]]$Arguments) {
    $result=& aws.exe @Arguments 2>&1
    if($LASTEXITCODE -ne 0){throw 'AWS operation failed; resources retained'}
    return ($result -join "`n")
}
# Machine credential is retrieved from SecureString at runtime. It is absent
# from AMI, UserData, task arguments, transcripts and state files.
$token=(Invoke-AwsCli @('ssm','get-parameter','--name',$config.CredentialParameter,'--with-decryption','--output','json') | ConvertFrom-Json).Parameter.Value
$headers=@{Authorization="Bearer $token"}
function Event([string]$Next,$Evidence) {
    $sequence=[long]$state.sequence+1
    $body=@{state=$Next;sequence=$sequence;evidence=$Evidence}|ConvertTo-Json -Depth 12 -Compress
    # Portal acknowledges identical retries. Preserve request before submission.
    $requestPath=Join-Path $env:LOCALAPPDATA 'awsportal-import-pending.json'
    Write-AtomicText $requestPath $body
    $null=Invoke-RestMethod -Method Post -Uri ($config.PortalURL+'/api/import/agent/event') -Headers $headers -ContentType 'application/json' -Body $body
    $state.sequence=$sequence
    Write-AtomicText $statePath ($state|ConvertTo-Json -Compress)
    Remove-Item -LiteralPath $requestPath
}
$pendingPath=Join-Path $env:LOCALAPPDATA 'awsportal-import-pending.json'
if(Test-Path -LiteralPath $pendingPath){$pending=Get-Content -LiteralPath $pendingPath -Raw;$null=Invoke-RestMethod -Method Post -Uri ($config.PortalURL+'/api/import/agent/event') -Headers $headers -ContentType 'application/json' -Body $pending;$state.sequence=($pending|ConvertFrom-Json).sequence;Write-AtomicText $statePath ($state|ConvertTo-Json -Compress);Remove-Item -LiteralPath $pendingPath}
$job=Invoke-RestMethod -Uri ($config.PortalURL+'/api/import/agent/state') -Headers $headers
if($job.ID -ne $config.JobID){throw 'Unexpected job'}
if($Action -eq 'LoginReady'){if($job.State -eq 'WAITING_BOX_LOGIN'){Event 'WAITING_OFFLINE_READY' @{}};return}
if($job.State -in @('VALIDATING','VERIFIED','CLEANING_UP','SUCCEEDED')){return}
if($job.State -notin @('WAITING_OFFLINE_READY','UPLOADING')){throw 'Upload not authorized in current state'}
$root=[IO.Path]::GetFullPath($job.SourceRoot).TrimEnd('\')
$approved=[IO.Path]::GetFullPath((Join-Path $env:USERPROFILE 'Box')).TrimEnd('\')
if(-not $root.StartsWith($approved+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'Only approved public Box subfolder may be read'}
if($root -match '\\(AppData|Box\s*Cache)\\'){throw 'Box internal cache access forbidden'}
$work=Join-Path $env:LOCALAPPDATA ('awsportal-evidence-'+$job.ID)
$null=New-Item -ItemType Directory -Path $work -Force
function EvidenceFile([string]$Name,$Object){
    $file=Join-Path $work $Name
    Write-AtomicText $file ($Object|ConvertTo-Json -Depth 12 -Compress)
    $key='.awsportal-evidence/'+$job.ID+'/'+$Name
    $null=Invoke-AwsCli @('s3','cp',$file,('s3://'+$job.Bucket+'/'+$key),'--no-progress','--only-show-errors','--checksum-algorithm','SHA256')
    $head=Invoke-AwsCli @('s3api','head-object','--bucket',$job.Bucket,'--key',$key,'--output','json')|ConvertFrom-Json
    if(-not $head.VersionId -or $head.VersionId -eq 'null'){throw 'S3 bucket versioning required'}
    return (@{bucket=$job.Bucket;key=$key;version_id=$head.VersionId;sha256=(Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant()}|ConvertTo-Json -Compress)
}
try {
$manifestPath=Join-Path $work 'expected.json'
if(Test-Path -LiteralPath $manifestPath){$manifest=Get-Content -LiteralPath $manifestPath -Raw|ConvertFrom-Json}
else{
    $files=@()
    # Reading/hashing every file is the offline-readiness probe. Cloud placeholder
    # attributes alone cannot prove that 500 GB is available offline.
    foreach($file in Get-ChildItem -LiteralPath $root -Recurse -File -Force){
        $relative=$file.FullName.Substring($root.Length+1).Replace('\','/')
        if($relative -match '(^|/)\.\.(/|$)'){throw 'Unsafe source path'}
        $hash=(Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        $files+=@{path=$relative;size=[long]$file.Length;sha256=$hash;mtime=$file.LastWriteTimeUtc.ToString('o')}
    }
    if($files.Count -eq 0){throw 'Nonempty expected dataset required'}
    $manifest=@{version=1;files=$files}
    Write-AtomicText $manifestPath ($manifest|ConvertTo-Json -Depth 12 -Compress)
}
if($job.State -eq 'UPLOADING'){
    $expectedRef=$job.ExpectedManifest
    $frozen=$expectedRef|ConvertFrom-Json
    if((Get-FileHash -LiteralPath $manifestPath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $frozen.sha256){throw 'Frozen manifest changed during resume'}
}else{$expectedRef=EvidenceFile 'expected.json' $manifest}
$expectedBytes=[long]0;foreach($f in $manifest.files){$expectedBytes+=[long]$f.size}
if($job.State -eq 'WAITING_OFFLINE_READY'){Event 'UPLOADING' @{expected_manifest=$expectedRef;expected_files=$manifest.files.Count;expected_bytes=$expectedBytes}}
$uploaded=@()
$checkpointPath=Join-Path $work 'uploaded-checkpoint.json'
$checkpoint=@{}
if(Test-Path -LiteralPath $checkpointPath){$saved=Get-Content -LiteralPath $checkpointPath -Raw|ConvertFrom-Json;foreach($f in $saved){$checkpoint[$f.path]=$f}}
    foreach($f in $manifest.files){
        $source=Join-Path $root $f.path.Replace('/','\')
        $before=Get-Item -LiteralPath $source
        if($before.Length -ne $f.size -or $before.LastWriteTimeUtc.ToString('o') -ne $f.mtime){throw 'Frozen source changed'}
        $current=Invoke-RestMethod -Uri ($config.PortalURL+'/api/import/agent/state') -Headers $headers
        if($current.State -ne 'UPLOADING'){throw 'Import cancelled or timed out'}
        if($checkpoint.ContainsKey($f.path)){
            $prior=$checkpoint[$f.path]
            if($prior.size -ne $f.size -or $prior.sha256 -ne $f.sha256){throw 'Resume checkpoint changed'}
            $existing=Invoke-AwsCli @('s3api','head-object','--bucket',$job.Bucket,'--key',($job.Prefix+$f.path),'--version-id',$prior.version_id,'--output','json')|ConvertFrom-Json
            if($existing.ContentLength -ne $f.size){throw 'Resume object missing or changed'}
            $uploaded+=$prior
            continue
        }
        # CLI multipart supports files over 5 GiB. No 500 GB staging copy and no --delete.
        $key=$job.Prefix+$f.path
        $null=Invoke-AwsCli @('s3','cp',$source,('s3://'+$job.Bucket+'/'+$key),'--no-progress','--only-show-errors','--checksum-algorithm','SHA256')
        $after=Get-Item -LiteralPath $source
        if($after.Length -ne $f.size -or $after.LastWriteTimeUtc.ToString('o') -ne $f.mtime -or (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash.ToLowerInvariant() -ne $f.sha256){throw 'Source changed during transfer'}
        $head=Invoke-AwsCli @('s3api','head-object','--bucket',$job.Bucket,'--key',$key,'--output','json')|ConvertFrom-Json
        if(-not $head.VersionId -or $head.VersionId -eq 'null' -or $head.ContentLength -ne $f.size){throw 'Uploaded version/size mismatch'}
        $entry=@{path=$f.path;size=$f.size;sha256=$f.sha256;version_id=$head.VersionId}
        $uploaded+=$entry
        $checkpoint[$f.path]=$entry
        Write-AtomicText $checkpointPath (@($checkpoint.Values)|ConvertTo-Json -Depth 12 -Compress)
    }
    $paths=@(Get-ChildItem -LiteralPath $root -Recurse -File -Force|ForEach-Object {$_.FullName.Substring($root.Length+1).Replace('\','/')})
    if(@(Compare-Object -ReferenceObject @($manifest.files.path) -DifferenceObject $paths).Count -ne 0){throw 'Frozen source file set changed'}
    $validationRef=EvidenceFile 'validation.json' @{version=1;exit_code=0;read_errors=0;mismatches=0;files=$uploaded}
    $logsRef=EvidenceFile 'logs.json' @{version=1;job_id=$job.ID;completed_at=[DateTime]::UtcNow.ToString('o');files=$uploaded.Count;errors=0;method='interactive-Box-readable-files-cli-multipart'}
    Event 'VALIDATING' @{expected_manifest=$expectedRef;validation=$validationRef;logs=$logsRef}
}catch{
    if(Test-Path -LiteralPath $pendingPath){throw 'Event acknowledgement pending; rerun to retry the saved event'}
    Event 'FAILED' @{}
    throw 'Import failed; Windows instance and volumes retained for administrator investigation'
}finally{$token=$null;$headers=$null}
