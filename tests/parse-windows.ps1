$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$asts=@{}
$clientScript='cmd/awsportal/preparation/setup-windows.ps1'
$tokens=$null;$errors=$null
$clientAst=[Management.Automation.Language.Parser]::ParseFile((Join-Path $PWD $clientScript),[ref]$tokens,[ref]$errors)
if($errors.Count){$errors|Format-List;throw 'Client preparation PowerShell parse failed'}
$hostsHelper=$clientAst.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-UpdatedHosts'},$true)
if(-not $hostsHelper){throw 'Client hosts helper missing'}
. ([ScriptBlock]::Create($hostsHelper.Extent.Text))
$mappings=@([pscustomobject]@{name='portal.company.example';ip='192.0.2.10'})
$revised=@(Get-UpdatedHosts @('127.0.0.1 localhost','192.0.2.20 PORTAL.company.example unrelated.example # keep note') $mappings)
if(($revised -join "`n") -notmatch 'unrelated.example # keep note' -or ($revised -join "`n") -notmatch '127.0.0.1 localhost'){throw 'Client hosts update removed unrelated entries'}
if(($revised -join "`n") -cne ((Get-UpdatedHosts $revised $mappings) -join "`n")){throw 'Client hosts update is not idempotent'}
foreach($file in Get-ChildItem -Path windows -Filter '*.ps1'){
    $tokens=$null;$errors=$null
    $ast=[Management.Automation.Language.Parser]::ParseFile($file.FullName,[ref]$tokens,[ref]$errors)
    if($errors.Count){$errors|Format-List;throw ('PowerShell parse failed: '+$file.Name)}
    $asts[$file.Name]=$ast
}
# Import only the helper function AST. Never install software, read credentials,
# sign in to Box or call AWS during a CI syntax/argument test.
$helper=$asts['Import-Box.ps1'].Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Invoke-AwsCli'},$true)
if(-not $helper){throw 'AWS CLI helper missing'}
. ([ScriptBlock]::Create($helper.Extent.Text))
function aws.exe {
    $script:received=@($args)
    $global:LASTEXITCODE=0
    return '{"ok":true}'
}
$null=Invoke-AwsCli @('s3','cp','C:\Users\Admin\Box\space and & unicode-日本語.bin','s3://bucket/datasets/a')
if($script:received.Count -ne 4 -or $script:received[2] -ne 'C:\Users\Admin\Box\space and & unicode-日本語.bin'){throw 'Native argument boundaries changed'}
function aws.exe {$global:LASTEXITCODE=2;return 'failure'}
$rejected=$false
try{$null=Invoke-AwsCli @('s3','cp','source','destination')}catch{$rejected=$true}
if(-not $rejected){throw 'CLI nonzero exit accepted'}
$global:LASTEXITCODE=0
Write-Output 'Windows PowerShell syntax and CLI argument handling: PASS'
