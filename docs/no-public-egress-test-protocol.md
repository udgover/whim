# Manual Testing Protocol — `--egress*` switches

A repeatable, end-to-end manual test of `whim build`'s no-public-egress
switches: auto-provisioning, existing-connector and raw subnet/SG modes, the
flag-validation matrix, launch-time wiring, and teardown. Designed to run
top-to-bottom in one shell session.

Companion to [no-public-egress-setup.md](no-public-egress-setup.md) (the AWS
requirements and least-privilege setup) and the README's "Building custom
images" / "Troubleshooting `--egress-auto-provision`" sections.

Phases are marked **[mutates AWS]** (creates billable resources — VPC,
connector, MicroVM) or **[no AWS]** (validation fails before any AWS call).
Every command form here has been exercised against a real account. Run
Phase 8 to remove everything the earlier phases create.

## Phase 0 — One-time setup

```bash
# 0.1  Session + binary
aws sts get-caller-identity          # must succeed
go build -o whim ./cmd/whim
export REGION=us-east-1
export ACCOUNT="$(aws sts get-caller-identity --query Account --output text)"
# whim init must have run once (artifact bucket + build role):
./whim preflight-check --region "$REGION"
```

```bash
# 0.2  [mutates AWS] Create the operator role — Whim never creates this itself.
cat > /tmp/trust.json <<'EOF'
{ "Version": "2012-10-17",
  "Statement": [ { "Effect": "Allow",
    "Principal": { "Service": "lambda.amazonaws.com" },
    "Action": "sts:AssumeRole" } ] }
EOF
aws iam create-role --role-name whim-test-operator-role \
  --assume-role-policy-document file:///tmp/trust.json \
  --tags Key=ManagedBy,Value=whim Key=Purpose,Value=egress-test
aws iam attach-role-policy --role-name whim-test-operator-role \
  --policy-arn arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy
sleep 10   # IAM propagation
export OPERATOR_ROLE_ARN="arn:aws:iam::${ACCOUNT}:role/whim-test-operator-role"
```

```bash
# 0.3  A minimal probe image. Pulling its public container base needs public
#      build-time egress; Amazon Linux 2023 already ships curl + getent.
mkdir -p /tmp/eg-norun && cat > /tmp/eg-norun/Dockerfile <<'EOF'
FROM public.ecr.aws/amazonlinux/amazonlinux:2023
CMD ["sleep", "infinity"]
EOF
```

## Phase 1 — Flag-validation matrix `[no AWS]`

Each of these must **fail before any AWS call**. They error on flags, not
build (they run without valid AWS creds).

```bash
run() { echo "== $* =="; ./whim build /tmp/eg-norun --name vtest --region "$REGION" "$@"; echo "exit=$?"; }

# 1.1  public rejects every egress-* provisioning flag
run --egress public --egress-auto-provision           # ERR: --egress-* require --egress none
run --egress public --egress-connector arn:x
run --egress public --egress-vpc-cidr 10.9.0.0/24
run --egress public --egress-strict-dns

# 1.2  bare --egress none fails closed
run --egress none                                      # ERR: requires connector/auto-provision/...

# 1.3  strict DNS is not implemented
run --egress none --egress-auto-provision --egress-strict-dns   # ERR: not implemented yet

# 1.4  more than one mode is ambiguous
run --egress none --egress-connector arn:x --egress-auto-provision   # ERR: only one of

# 1.5  foreign flag for the selected mode is rejected
run --egress none --egress-auto-provision --egress-subnet subnet-a   # ERR: not used by selected mode
run --egress none --egress-connector arn:x --egress-vpc-cidr 10.9.0.0/24

# 1.6  raw subnet/SG needs an EXPLICIT connector name (default not used silently)
run --egress none --egress-subnet subnet-a --egress-security-group sg-a   # ERR: requires ...
```

**Expected:** every line exits non-zero with the described error, no image built.

## Phase 2 — Auto-provision happy path `[mutates AWS]`

```bash
# 2.1  Image creation uses public egress. The command also creates the isolated
#      runtime resources and records egress=none + connector for later launches.
./whim build /tmp/eg-norun --name whim-eg-auto \
  --egress none --egress-auto-provision \
  --egress-operator-role "$OPERATOR_ROLE_ARN" \
  --region "$REGION" --json | tee /tmp/eg-auto.json

# 2.2  Derive resource IDs from the JSON (no hand-typing).
J=/tmp/eg-auto.json
export CONNECTOR_ARN="$(python3 -c "import json;print(json.load(open('$J'))['egress_connector'])")"
export VPC_ID="$(python3 -c "import json;print(json.load(open('$J'))['egress_resource_group']['vpc_id'])")"
export SUBNET_ID="$(python3 -c "import json;print(json.load(open('$J'))['egress_resource_group']['subnet_ids'][0])")"
export RT_ID="$(python3 -c "import json;print(json.load(open('$J'))['egress_resource_group']['route_table_id'])")"
export SG_ID="$(python3 -c "import json;print(json.load(open('$J'))['egress_resource_group']['security_group_id'])")"
echo "conn=$CONNECTOR_ARN vpc=$VPC_ID subnet=$SUBNET_ID rt=$RT_ID sg=$SG_ID"
```

Verify the isolation shape:

```bash
# 2.3  Route table has ONLY the local route (no 0.0.0.0/0, no igw/nat/etc.)
aws ec2 describe-route-tables --route-table-ids "$RT_ID" --region "$REGION" \
  --query 'RouteTables[0].Routes'          # expect one route, GatewayId "local"

# 2.4  Security group has ZERO egress rules
aws ec2 describe-security-groups --group-ids "$SG_ID" --region "$REGION" \
  --query 'SecurityGroups[0].IpPermissionsEgress'   # expect []

# 2.5  Resources are Whim-tagged; connector is ACTIVE, VPC egress
aws ec2 describe-vpcs --vpc-ids "$VPC_ID" --region "$REGION" --query 'Vpcs[0].Tags'
aws lambda-core get-network-connector --identifier "$CONNECTOR_ARN" --region "$REGION" \
  --query '{State:State,Type:Type}'        # expect ACTIVE / VPC_EGRESS

# 2.6  Config persisted the resource group
python3 -c "import json;print(json.load(open('$HOME/Library/Application Support/whim/config.json'))['image_egress_resource_groups']['whim-eg-auto'])"
```

Launch and prove the guarantee (the AL2023 image has curl + getent, and
`whim run` explicitly overrides the public build connector with its recorded
isolated runtime connector):

```bash
# 2.7  Launch a persistent box from the recorded no-egress image
VM_ID="$(./whim run -d --image whim-eg-auto --region "$REGION")"; echo "$VM_ID"

# 2.8  Metadata must NOT show INTERNET_EGRESS
aws lambda-microvms get-microvm --microvm-identifier "$VM_ID" --region "$REGION" \
  --query '{state:state,egress:egressNetworkConnectors}'
#   expect egress == [ "<CONNECTOR_ARN>" ], never *:INTERNET_EGRESS

# 2.9  Guest probes: DNS resolves, public connections blocked
./whim exec "$VM_ID" --region "$REGION" -- sh -c '
  getent hosts example.com && echo "INFO: DNS resolved" || echo "INFO: DNS did not resolve"
  timeout 8 curl -s --connect-timeout 3 https://example.com/ -o /dev/null && echo "FAIL: HTTPS" || echo "PASS: HTTPS blocked"
  timeout 8 curl -s --connect-timeout 3 http://1.1.1.1/ -o /dev/null && echo "FAIL: IP" || echo "PASS: IP blocked"'
#   expect: DNS resolved (INFO), PASS: HTTPS blocked, PASS: IP blocked
```

Reuse path:

```bash
# 2.10  Re-run build → must REUSE, not create a second VPC.
BEFORE=$(aws ec2 describe-vpcs --region "$REGION" --filters Name=tag:ManagedBy,Values=whim --query 'length(Vpcs)')
./whim build /tmp/eg-norun --name whim-eg-auto2 \
  --egress none --egress-auto-provision --egress-operator-role "$OPERATOR_ROLE_ARN" \
  --region "$REGION" --json | python3 -c "import json,sys;print('connector:',json.load(sys.stdin)['egress_connector'])"
AFTER=$(aws ec2 describe-vpcs --region "$REGION" --filters Name=tag:ManagedBy,Values=whim --query 'length(Vpcs)')
echo "vpc count before=$BEFORE after=$AFTER"   # expect equal; same connector ARN as 2.2
```

## Phase 3 — Auto-provision naming/CIDR overrides `[mutates AWS]`

```bash
# 3.1  Custom prefix + custom CIDRs → a SEPARATE resource group (needs its own cleanup).
./whim build /tmp/eg-norun --name whim-eg-acme \
  --egress none --egress-auto-provision --egress-operator-role "$OPERATOR_ROLE_ARN" \
  --egress-resource-prefix acme \
  --egress-vpc-cidr 10.231.0.0/24 --egress-subnet-cidr 10.231.0.0/25 \
  --region "$REGION" --json | tee /tmp/eg-acme.json
#   expect connector name "acme-no-public-egress"; vpc CIDR 10.231.0.0/24
python3 -c "import json;d=json.load(open('/tmp/eg-acme.json'));print(d['egress_connector'])"
aws ec2 describe-vpcs --region "$REGION" \
  --vpc-ids "$(python3 -c "import json;print(json.load(open('/tmp/eg-acme.json'))['egress_resource_group']['vpc_id'])")" \
  --query 'Vpcs[0].CidrBlock'     # expect 10.231.0.0/24
```

## Phase 4 — Existing-connector mode `[mutates AWS]`

```bash
# 4.1  Point at the connector auto-provision already made (by ARN).
./whim build /tmp/eg-norun --name whim-eg-existing \
  --egress none --egress-connector "$CONNECTOR_ARN" \
  --region "$REGION" --json
#   expect egress_connector == $CONNECTOR_ARN and validated egress_resource_group topology
```

## Phase 5 — Raw subnet/SG mode `[mutates AWS]`

```bash
# 5.1  Create a connector directly against the auto-provisioned subnet/SG under a NEW name.
#      Whim creates only the connector here, then validates the supplied topology.
./whim build /tmp/eg-norun --name whim-eg-raw \
  --egress none --egress-connector-name whim-raw-test \
  --egress-subnet "$SUBNET_ID" --egress-security-group "$SG_ID" \
  --egress-operator-role "$OPERATOR_ROLE_ARN" \
  --region "$REGION" --json
aws lambda-core get-network-connector --identifier whim-raw-test --region "$REGION" \
  --query '{State:State}'          # expect ACTIVE
```

## Phase 6 — Launch wiring + image-rm pruning `[mutates AWS]`

```bash
# 6.1  One-shot run also uses the recorded connector
./whim run --image whim-eg-auto --region "$REGION" -- \
  sh -c 'timeout 6 curl -s --connect-timeout 3 http://1.1.1.1/ -o /dev/null && echo FAIL || echo PASS-blocked'
#   expect PASS-blocked

# 6.2  image rm prunes local config but does NOT delete the AWS VPC/connector
./whim image rm whim-eg-auto --region "$REGION"
python3 -c "import json;print('whim-eg-auto' in json.load(open('$HOME/Library/Application Support/whim/config.json')).get('image_egress_resource_groups',{}))"
#   expect False (pruned)
aws lambda-core get-network-connector --identifier "$CONNECTOR_ARN" --region "$REGION" --query State
#   expect still ACTIVE — image rm never touches AWS networking
```

## Phase 7 — Build-time public egress `[mutates AWS]`

```bash
# 7.1  A Dockerfile that needs network at build time still builds successfully:
#      --egress none is the runtime policy, while image creation remains public.
mkdir -p /tmp/eg-netbuild && cat > /tmp/eg-netbuild/Dockerfile <<'EOF'
FROM public.ecr.aws/amazonlinux/amazonlinux:2023
RUN dnf install -y jq && dnf clean all
CMD ["sleep", "infinity"]
EOF
./whim build /tmp/eg-netbuild --name whim-eg-netbuild \
  --egress none --egress-connector "$CONNECTOR_ARN" --region "$REGION"
#   expect: image ready; future launches use $CONNECTOR_ARN at runtime
```

## Phase 8 — Cleanup `[mutates AWS]`

```bash
# 8.1  Terminate any VMs by id (NOT `whim gc` — that's a bulk reaper)
./whim rm "$VM_ID" --region "$REGION"
for i in $(seq 1 15); do
  S=$(aws lambda-microvms get-microvm --microvm-identifier "$VM_ID" --region "$REGION" --query state --output text 2>&1)
  case "$S" in TERMINATED|*NotFound*) break;; esac; sleep 5; done

# 8.2  Delete images
./whim image rm whim-eg-auto2 whim-eg-acme whim-eg-existing whim-eg-raw whim-eg-netbuild --region "$REGION" 2>/dev/null

# 8.3  Delete connectors (poll each until gone).
#      $CONNECTOR_ARN (auto), whim-raw-test (raw), and the acme connector.
for C in "$CONNECTOR_ARN" whim-raw-test \
         "$(python3 -c "import json;print(json.load(open('/tmp/eg-acme.json'))['egress_connector'])")"; do
  aws lambda-core delete-network-connector --identifier "$C" --region "$REGION"
  for i in $(seq 1 20); do
    aws lambda-core get-network-connector --identifier "$C" --region "$REGION" 2>&1 | grep -q ResourceNotFound && break; sleep 10; done
done

# 8.4  For EACH resource group, delete SG → subnet → route table → VPC (order matters).
#      The whim-eg-auto group (vars from 2.2):
aws ec2 delete-security-group --group-id "$SG_ID" --region "$REGION"
aws ec2 delete-subnet --subnet-id "$SUBNET_ID" --region "$REGION"
aws ec2 delete-route-table --route-table-id "$RT_ID" --region "$REGION"
aws ec2 delete-vpc --vpc-id "$VPC_ID" --region "$REGION"
#      The acme group (IDs from /tmp/eg-acme.json):
A=/tmp/eg-acme.json
aws ec2 delete-security-group --group-id "$(python3 -c "import json;print(json.load(open('$A'))['egress_resource_group']['security_group_id'])")" --region "$REGION"
aws ec2 delete-subnet        --subnet-id  "$(python3 -c "import json;print(json.load(open('$A'))['egress_resource_group']['subnet_ids'][0])")"      --region "$REGION"
aws ec2 delete-route-table   --route-table-id "$(python3 -c "import json;print(json.load(open('$A'))['egress_resource_group']['route_table_id'])")" --region "$REGION"
aws ec2 delete-vpc           --vpc-id     "$(python3 -c "import json;print(json.load(open('$A'))['egress_resource_group']['vpc_id'])")"             --region "$REGION"

# 8.5  Operator role
aws iam detach-role-policy --role-name whim-test-operator-role \
  --policy-arn arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy
aws iam delete-role --role-name whim-test-operator-role

# 8.6  Verify nothing whim-tagged remains
aws lambda-core list-network-connectors --region "$REGION"
aws ec2 describe-vpcs --region "$REGION" --filters Name=tag:ManagedBy,Values=whim --query 'Vpcs[].VpcId'
#   expect [] and no VPC ids
```

## Notes

- **One minimal probe image is enough** (Phase 0.3): its public base is pulled
  during the public build phase, and AL2023 already contains `curl` and
  `getent`. `whim run` then applies the isolated connector at runtime. Phase 7
  adds one network-dependent `RUN` only to prove the build/runtime split.
- **Phases 3–5 leave extra resource groups/connectors.** Phase 8 removes the
  `whim-eg-auto` and `acme` groups and the `whim-raw-test` connector; the
  `whim-eg-existing` build reuses `$CONNECTOR_ARN` (no new resources).
- **Cleanup is manual by design** — there is no `whim egress rm` yet (see the
  spec's Managed Resource Cleanup design), so image deletion never removes AWS
  networking; only Phase 8's explicit AWS CLI calls do.
