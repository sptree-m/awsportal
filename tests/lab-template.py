"""Validate CloudFormation dependencies, Fn::Sub variables and rendered shell syntax."""
import pathlib
import re
import subprocess
import tempfile
import yaml

document = yaml.safe_load(pathlib.Path("lab/cloudformation.yaml").read_text())
resources = document["Resources"]
parameters = document["Parameters"]
dependencies = {}
def refs(value):
    out = set()
    if isinstance(value, dict):
        if "Ref" in value and value["Ref"] in resources:
            out.add(value["Ref"])
        if "Fn::GetAtt" in value:
            out.add(value["Fn::GetAtt"][0])
        if "Fn::Sub" in value:
            sub = value["Fn::Sub"]
            text, variables = (sub, {}) if isinstance(sub, str) else sub
            for variable in re.findall(r"\$\{([^}]+)\}", text):
                if variable.startswith("!") or variable in variables:
                    continue
                name = variable.split(".")[0]
                assert name in resources or name in parameters or name.startswith("AWS::"), variable
                if name in resources:
                    out.add(name)
        for item in value.values():
            out.update(refs(item))
    elif isinstance(value, list):
        for item in value:
            out.update(refs(item))
    return out
for name, resource in resources.items():
    depends = resource.get("DependsOn", [])
    if isinstance(depends, str):
        depends = [depends]
    dependencies[name] = refs(resource) | set(depends)
def visit(name, path):
    assert name not in path, "CloudFormation cycle: " + " -> ".join(path + [name])
    for dependency in dependencies[name]:
        visit(dependency, path + [name])
for name in resources:
    visit(name, [])
for name in ("TestInstance", "PortalInstance"):
    sub = resources[name]["Properties"]["UserData"]["Fn::Base64"]["Fn::Sub"]
    text = sub if isinstance(sub, str) else sub[0]
    with tempfile.NamedTemporaryFile(mode="w", suffix=".sh") as f:
        f.write(text)
        f.flush()
        subprocess.run(["bash", "-n", f.name], check=True)
assert resources["TestInstance"]["Properties"]["IamInstanceProfile"] == {"Ref": "TestProfile"}
assert resources["PortalAgentIngress"]["Properties"]["SourceSecurityGroupId"] == {"Ref": "TestSG"}
print("PASS: CloudFormation dependency graph, substitutions and userdata shell syntax")

license_policy = resources["TestRole"]["Properties"]["Policies"]
assert license_policy == [{"PolicyName": "DCVLicenseRead", "PolicyDocument": {
    "Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "s3:GetObject",
    "Resource": {"Fn::Sub": "arn:${AWS::Partition}:s3:::dcv-license.${AWS::Region}/*"}}]}}]
print("PASS: EC2 DCV license role grants only regional license object reads")
