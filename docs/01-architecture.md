# Architecture

## Identity boundary
End users have no AWS account, IAM user, access key, console or CLI credentials. Portal users and AWS identities are separate concepts.

Roles: user, group_admin, portal_admin. Portal administrators require TOTP MFA. Users see instances assigned directly to them plus instances assigned to their groups. portal_admin sees all registered managed instances.

## Access boundary
Managed desktops expose DCV TCP/8443 only to approved corporate/VPN CIDRs. Do not create inbound SSH/22 or RDP/3389 rules. Do not grant interactive SSM access to portal users. DCV is the intended desktop access path.

## DLP layers
1. DCV normal-user permissions deny download/upload, clipboard, printing, USB and screenshot features.
2. Network egress must be allowlisted separately. DCV permissions alone are not DLP.
3. Portal and AWS activity are audited.
4. Administrator export is a distinct audited privilege.

## Portal
ARM64 t4g.micro target, Go server-side rendering, SQLite, local static assets only, no CDN/npm runtime dependency. Use 4 GiB swap only as OOM protection, not capacity replacement.
