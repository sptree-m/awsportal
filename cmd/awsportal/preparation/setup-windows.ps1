#Requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$ExpectedFingerprint,
    [switch]$Apply
)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest

function Get-UpdatedHosts([string[]]$Lines, $Mappings) {
    $names=@($Mappings | ForEach-Object { $_.name.ToLowerInvariant() })
    $result=New-Object 'System.Collections.Generic.List[string]'
    foreach($line in $Lines) {
        $parts=$line -split '#',2
        $fields=@($parts[0].Trim() -split '\s+' | Where-Object { $_ })
        $changed=$false
        if($fields.Count -ge 2) {
            $aliases=@($fields[1..($fields.Count-1)] | Where-Object { $names -notcontains $_.ToLowerInvariant() })
            $changed=$aliases.Count -ne ($fields.Count-1)
        }
        if($changed) {
            $updated=''
            if($aliases.Count) { $updated=$fields[0]+"`t"+($aliases -join ' ') }
            if($parts.Count -eq 2 -and ($aliases.Count -gt 0 -or $parts[1].Trim() -ne 'awsportal')) { $updated+=' #'+$parts[1] }
            if($updated.Trim()) { $result.Add($updated) }
        } else { $result.Add($line) }
    }
    foreach($mapping in $Mappings) { $result.Add($mapping.ip+"`t"+$mapping.name+' # awsportal') }
    return $result.ToArray()
}

$config=Get-Content -LiteralPath (Join-Path $PSScriptRoot 'client-setup.json') -Raw | ConvertFrom-Json
$certPath=Join-Path $PSScriptRoot 'ca.crt'
$pem=[IO.File]::ReadAllText($certPath)
if(([regex]::Matches($pem,'-----BEGIN CERTIFICATE-----')).Count -ne 1 -or $pem.Contains('PRIVATE KEY')) { throw 'One public CA certificate required' }
$encoded=$pem.Replace('-----BEGIN CERTIFICATE-----','').Replace('-----END CERTIFICATE-----','') -replace '\s',''
$cert=New-Object Security.Cryptography.X509Certificates.X509Certificate2 -ArgumentList (,[Convert]::FromBase64String($encoded))
$sha=[Security.Cryptography.SHA256]::Create()
try { $fingerprint=([BitConverter]::ToString($sha.ComputeHash($cert.RawData))).Replace('-','') } finally { $sha.Dispose() }
$expected=$ExpectedFingerprint.Replace(':','').ToUpperInvariant()
if($expected -notmatch '^[0-9A-F]{64}$' -or $fingerprint -cne $expected -or $fingerprint -cne $config.ca_sha256) { throw 'CA fingerprint does not match the administrator-provided value' }
$basic=@($cert.Extensions | Where-Object { $_.Oid.Value -eq '2.5.29.19' })
if($basic.Count -ne 1 -or -not $basic[0].CertificateAuthority) { throw 'Certificate is not a CA' }
$now=[DateTime]::UtcNow
if($now -lt $cert.NotBefore.ToUniversalTime() -or $now -gt $cert.NotAfter.ToUniversalTime()) { throw 'CA is not currently valid' }
$mappings=@($config.hosts)
if($mappings.Count -lt 1 -or $mappings.Count -gt 32) { throw '1 to 32 host mappings required' }
$seen=@{}
foreach($mapping in $mappings) {
    $name=$mapping.name
    $ip=$null
    if($name.Length -gt 253 -or $name -cnotmatch '^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$' -or $seen.ContainsKey($name) -or -not [Net.IPAddress]::TryParse($mapping.ip,[ref]$ip)) { throw 'Invalid or duplicate host mapping' }
    $octets=$ip.GetAddressBytes()
    if($ip.Equals([Net.IPAddress]::Any) -or $ip.Equals([Net.IPAddress]::IPv6Any) -or $ip.IsIPv6Multicast -or ($octets.Length -eq 4 -and $octets[0] -ge 224)) { throw 'Invalid host IP' }
    $seen[$name]=$true
}
$hostsPath=Join-Path $env:SystemRoot 'System32\drivers\etc\hosts'
$original=[IO.File]::ReadAllLines($hostsPath)
$updated=Get-UpdatedHosts $original $mappings
Write-Output ('CA SHA256: '+$fingerprint)
foreach($mapping in $mappings) { Write-Output ('hosts: '+$mapping.ip+' '+$mapping.name) }
Write-Output 'System CA store will be updated. Existing unrelated hosts aliases are preserved.'
if(-not $Apply) { Write-Output 'Preview only. Review values, then run as Administrator with -Apply.'; exit 0 }
$identity=[Security.Principal.WindowsIdentity]::GetCurrent()
$principal=New-Object Security.Principal.WindowsPrincipal($identity)
if(-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run PowerShell as Administrator to apply' }
$backup=$hostsPath+'.awsportal-'+[DateTime]::Now.ToString('yyyyMMddHHmmssfffffff')+'.bak'
[IO.File]::Copy($hostsPath,$backup,$false)
$store=New-Object Security.Cryptography.X509Certificates.X509Store('Root','LocalMachine')
try { $store.Open('ReadWrite'); $store.Add($cert) } finally { $store.Close() }
[IO.File]::WriteAllLines($hostsPath,[string[]]$updated,(New-Object Text.UTF8Encoding($false)))
& ipconfig.exe /flushdns
if($LASTEXITCODE -ne 0) { throw ('DNS flush failed; hosts backup: '+$backup) }
Write-Output ('Applied. Hosts backup: '+$backup)
Write-Output 'Restart your browser and open the administrator-provided HTTPS hostname.'
