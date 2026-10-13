awsportal client preparation (Windows / macOS / Linux)

1. Extract all files into one directory. Obtain the CA SHA256 certificate
   fingerprint and approved hostname/IP mappings separately from your administrator.
2. Preview changes:
   Windows PowerShell 5.1+: .\setup-windows.ps1 -ExpectedFingerprint FINGERPRINT
   macOS / Linux: python3 setup-unix.py --expected-fingerprint FINGERPRINT
3. Review the mappings. Apply from an Administrator PowerShell or using sudo:
   .\setup-windows.ps1 -ExpectedFingerprint FINGERPRINT -Apply
   sudo python3 setup-unix.py --expected-fingerprint FINGERPRINT --apply
4. Restart your browser and use the approved HTTPS hostname (not its raw IP).

Windows: follow your organization's script-signing/execution policy; do not
disable it. macOS/Linux require Python 3 and openssl. Supported Linux system CA
stores: Debian/Ubuntu and RHEL/Fedora. Applications using independent CA stores
(Firefox settings, Java, containers, etc.) may require separate administrator setup.

The scripts do not download anything, request passwords, generate certificates,
configure TLS servers, or change firewall rules. Only the supplied public CA and
listed hosts mappings are applied. Unrelated aliases remain; a timestamped hosts
backup is created before applying. Run again to refresh the listed mappings.

First use: if hostname resolution or CA trust is not ready, your administrator
must provide this ZIP and the fingerprint via an existing trusted internal channel
(managed software distribution/shared folder). Do not bypass HTTPS verification
to retrieve your first trust certificate.

Recovery: inspect the printed hosts backup and restore only the desired mappings.
Remove the exact CA by its fingerprint from Windows LocalMachine Root / macOS
System keychain, or remove awsportal-FINGERPRINT_PREFIX.crt from the Linux system
CA directory and refresh the CA store. A later CA rotation installs a different
certificate; the old CA remains until the administrator removes it.
