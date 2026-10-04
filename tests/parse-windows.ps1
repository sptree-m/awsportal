$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$asts=@{}
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
