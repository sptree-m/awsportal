#!/usr/bin/env bash
# Test actual EC2 services and the portal -> DCV token authentication path.
set -euo pipefail
export AWS_PAGER=""
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-ap-northeast-1}}"
STACK="${STACK:-awsportal-lab}"
HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
umask 077
aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" --output json > "$WORK/stack.json"
output() { python3 -c 'import json,sys; print(next(x["OutputValue"] for x in json.load(open(sys.argv[1]))["Stacks"][0]["Outputs"] if x["OutputKey"]==sys.argv[2]))' "$WORK/stack.json" "$1"; }
PORTAL_ID="$(output PortalInstanceId)"
TEST_ID="$(output TestInstanceId)"
for ID in "$PORTAL_ID" "$TEST_ID"; do
  READY=0
  for attempt in $(seq 1 120); do
    STATUS="$(aws ssm describe-instance-information --region "$REGION" --filters "Key=InstanceIds,Values=$ID" --query 'InstanceInformationList[0].PingStatus' --output text)"
    if [[ "$STATUS" == Online ]]; then READY=1; break; fi
    sleep 5
  done
  [[ "$READY" == 1 ]] || { echo "FAIL: SSM is not online for $ID"; exit 1; }
done
# Enabling labdebug is explicit; do not bypass authentication for any other account.
bash "$HERE/debug-auth.sh" off

remote() {
  local instance="$1" script="$2" command_id status
  command_id="$(aws ssm send-command --region "$REGION" --instance-ids "$instance" --document-name AWS-RunShellScript --parameters "$(python3 -c 'import json,sys; print(json.dumps({"commands":[sys.argv[1]],"executionTimeout":["900"]}))' "$script")" --query 'Command.CommandId' --output text)"
  # The standard command-executed waiter expires too soon for first desktop installation.
  for attempt in $(seq 1 100); do
    if aws ssm get-command-invocation --region "$REGION" --instance-id "$instance" --command-id "$command_id" --output json > "$WORK/invocation.json" 2> "$WORK/invocation.err"; then
      status="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["Status"])' "$WORK/invocation.json")"
      case "$status" in
        Success) python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["StandardOutputContent"])' "$WORK/invocation.json"; return 0;;
        Failed|Cancelled|TimedOut) cat "$WORK/invocation.json"; return 1;;
      esac
    fi
    sleep 10
  done
  echo 'FAIL: SSM test timeout'; return 1
}

# Wait for installation, per-user account provisioning and desktop readiness.
remote "$TEST_ID" 'set -e
sudo cloud-init status --wait
sudo systemctl is-active dcvserver
sudo systemctl is-active awsportal-dcv-agent
sudo python3 - <<'"'"'PY'"'"'
import json, pathlib, subprocess, time, pwd
config=json.loads(pathlib.Path("/etc/awsportal-dcv/config.json").read_text())
import importlib.machinery, types
m=types.ModuleType("agent")
loader=importlib.machinery.SourceFileLoader("agent","/usr/local/libexec/awsportal-dcv-agent")
loader.exec_module(m)
a=m.Agent(config,"/var/lib/awsportal-dcv")
for attempt in range(60):
    manifest=json.loads(a.request("/api/dcv/agent/state"))
    accounts=manifest["accounts"]
    debug=next((x for x in accounts if x["username"]=="labdebug"),None)
    if debug:
        result=subprocess.run(["dcv","describe-session",debug["session_id"],"--json"],capture_output=True,text=True)
        if result.returncode==0:
            session=json.loads(result.stdout)
            licensed=any(item.get("product")=="dcv" and str(item.get("status","")).lower()=="licensed" for item in session.get("licenses",[]))
            desktop=subprocess.run(["pgrep","-u",str(pwd.getpwnam(debug["os_user"]).pw_uid),"-x","xfce4-session"],capture_output=True)
            if session.get("owner")=="root" and session.get("type")=="virtual" and session.get("x11-display") and licensed and desktop.returncode==0:
                print("PASS: labdebug OS account, licensed DCV and virtual desktop",debug["os_user"])
                a.validate_enforcement()
                print("PASS: native-only server configuration and Web Viewer removal (actual client connection requires manual test)")
                baseline=pathlib.Path("/etc/dcv/awsportal-policy/enforced.perm").read_text()
                assert "screenshot" in baseline and "clipboard-copy" in baseline, "FAIL: default capture protection missing"
                for filename in ("/etc/dcv/dcv.conf", "/etc/dcv", "/etc/awsportal-dcv", "/etc/dcv/awsportal-policy", "/etc/dcv/awsportal-policy/enforced.perm", "/usr/local/libexec/awsportal-dcv-agent", "/etc/systemd/system/awsportal-dcv-agent.service"):
                    writable=subprocess.run(["runuser","-u",debug["os_user"],"--","test","-w",filename],capture_output=True)
                    assert writable.returncode!=0, "FAIL: user can write protected DCV path"
                for option in (["--reset-builtin"], ["--file","/etc/dcv/default.perm"]):
                    change=subprocess.run(["runuser","-u",debug["os_user"],"--","dcv","set-permissions","--session",debug["session_id"],*option],capture_output=True)
                    assert change.returncode!=0, "FAIL: user changed administrator DCV permissions"
                print("PASS: root-managed DCV policy and user permission-change rejection")
                subprocess.run(["iptables","-C","OUTPUT","-d","169.254.169.254/32","-j","AWSPORTAL_IMDS"],check=True)
                for address in ("http://169.254.169.254/latest/meta-data/", "http://[fd00:ec2::254]/latest/meta-data/"):
                    blocked=subprocess.run(["runuser","-u",debug["os_user"],"--","curl","--noproxy","*","-sS","--max-time","3",address],capture_output=True)
                    assert blocked.returncode!=0, "FAIL: desktop user can access IMDS"
                print("PASS: desktop user metadata access blocked")
                break
    time.sleep(10)
else: raise SystemExit("FAIL: labdebug desktop is not ready")
PY'

# Get a real portal token using the MFA-free account, using a short-lived lab-only credential.
AUTH_FORM="$(remote "$PORTAL_ID" 'sudo python3 - <<'"'"'PY'"'"'
import http.cookiejar, pathlib, urllib.parse, urllib.request, urllib.error
credentials=dict(line.split("=",1) for line in pathlib.Path("/var/lib/awsportal/lab-login.txt").read_text().splitlines() if "=" in line)
cookies=http.cookiejar.CookieJar()
client=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args,**kwargs): return None
form=urllib.parse.urlencode({"username":"labdebug","password":credentials["debug_password"]}).encode()
client.open("http://127.0.0.1:8080/login",form,timeout=20).read()
import sqlite3
db=sqlite3.connect("/var/lib/awsportal/awsportal.db")
instance_id=db.execute("SELECT instance_id FROM instances LIMIT 1").fetchone()[0]
client=urllib.request.build_opener(NoRedirect(),urllib.request.HTTPCookieProcessor(cookies))
try: client.open("http://127.0.0.1:8080/dcv/"+instance_id,timeout=20)
except urllib.error.HTTPError as e:
    if e.code!=302: raise
    destination=urllib.parse.urlsplit(e.headers["Location"])
    print(urllib.parse.urlencode({"sessionId":destination.fragment,"authenticationToken":urllib.parse.parse_qs(destination.query)["authToken"][0]}))
else: raise SystemExit("FAIL: no DCV redirect")
PY')"
AUTH_FORM="$(printf '%s' "$AUTH_FORM" | tail -n 1)"
[[ "$AUTH_FORM" == sessionId=awp-u* ]] || { echo 'FAIL: token generation'; exit 1; }
FORM_B64="$(printf '%s' "$AUTH_FORM" | base64 -w0)"
remote "$TEST_ID" "sudo python3 - <<'PY'
import base64,urllib.request,urllib.error,xml.etree.ElementTree as ET
body=base64.b64decode('$FORM_B64')
req=urllib.request.Request('http://127.0.0.1:8444',body,{'Content-Type':'application/x-www-form-urlencoded'})
with urllib.request.urlopen(req,timeout=20) as r:
    reply=ET.fromstring(r.read())
    assert reply.get('result')=='yes' and reply.findtext('username').startswith('awp-u')
print('PASS: DCV broker -> HTTPS portal -> per-user external authentication')
try: urllib.request.urlopen(req,timeout=20)
except urllib.error.HTTPError as e: assert e.code==401
else: raise SystemExit('FAIL: token replay accepted')
print('PASS: one-time token replay rejected')
PY"
echo 'RESULT: PASS - EC2 DCV service, account/session synchronization and external authentication'
DCV_IP="$(aws ec2 describe-instances --region "$REGION" --instance-ids "$TEST_ID" --query 'Reservations[0].Instances[0].PublicIpAddress' --output text)"
echo "DCV native endpoint: $DCV_IP:8443 (connect from Portal using the native client)"
echo 'Final desktop rendering and input: install the native client, then click Connect in the Portal.'
