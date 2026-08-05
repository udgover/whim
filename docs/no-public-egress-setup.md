# Setting up `--egress none` (no-public-egress)

`whim build --egress none` produces an image whose MicroVMs route outbound
traffic through a customer-managed VPC with no public path — direct public IPs
and public-hostname HTTPS fail. (DNS resolution is *not* blocked; see the
`NO_PUBLIC_EGRESS` note in the README's Security model.) This guide covers what
you need in AWS to use it, and how to grant those permissions following least
privilege.

## The one idea that makes least privilege make sense here

There are **two different AWS principals** involved, plus one AWS-owned role.
Keeping them straight is the whole game:

1. **Your caller identity** — the human, CI role, or `aws-vault` profile that
   *runs* `whim build`. Whim uses these credentials directly (it never
   resolves ambient credentials on its own — you inject an `aws.Config`). This
   identity needs permission to *create and read* the VPC/connector resources.
2. **The operator role** — an IAM role that **Lambda Core assumes** to manage
   the elastic network interfaces (ENIs) for the connector. You pass its ARN
   with `--egress-operator-role`. This is *not* your identity; it is a role
   Lambda uses on your behalf, and it needs a small, fixed permission set.
3. **`AWSServiceRoleForLambda`** — an AWS service-linked role that AWS creates
   and manages. It cleans up connector ENIs. You do not author its policy; you
   may only need permission to let AWS create it the first time (below).

Least privilege here means: give the *operator role* exactly the AWS-managed
minimal policy and nothing else, scope its trust so only your account's Lambda
can assume it, and give your *caller identity* a scoped policy rather than
admin. **Whim never creates IAM roles for you** — that is a deliberate
decision so Whim's own credentials never need `iam:CreateRole`.

## Baseline: an authenticated AWS session

Every `whim` command needs working AWS credentials resolvable by the default
chain (`aws sso login`, `aws-vault`, env vars, an assumed role, IRSA, etc.).
Verify with:

```console
aws sts get-caller-identity
```

`whim build` (any egress mode) additionally needs the one-time bootstrap that
`whim init` provisions — an artifact bucket, a build role, and the managed
base image. That is not specific to `--egress none`.

## The three `--egress none` modes and what each requires

`--egress none` needs exactly one connector-selection mode. They have very
different setup requirements — pick the least-powerful one that fits:

| Mode | You provide | Caller needs to create AWS resources? | Needs operator role? |
|---|---|---|---|
| `--egress-connector <arn\|name>` | an existing, ACTIVE connector | No | No (already created) |
| `--egress-subnet`/`--egress-security-group` + explicit `--egress-connector-name` | your own subnet(s) + SG | Only the connector | Yes |
| `--egress-auto-provision` | nothing (Whim builds the whole VPC) | Yes — VPC, subnet, route table, SG, connector | Yes |

If you already run an isolated VPC and connector, `--egress-connector` needs no
create permissions, but Whim still needs Lambda Core and EC2 read access to
prove the connector is safe before build and launch. The rest of this guide is
mostly about the two modes that create resources.

## Setting up the operator role (least privilege)

Required for `--egress-auto-provision` and the raw subnet/SG mode. Create it
**once** and reuse its ARN forever.

**Trust policy** — only Lambda may assume it. Use exactly this form,
which is live-verified end-to-end (a connector created against a role with
this trust policy reaches `ACTIVE`):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": { "Service": "lambda.amazonaws.com" },
      "Action": "sts:AssumeRole"
    }
  ]
}
```

**Permissions policy** — attach the AWS-managed
`AWSLambdaNetworkConnectorOperatorPolicy` and nothing else. It grants only
`ec2:CreateNetworkInterface` (+ a tightly conditioned `ec2:CreateTags`) — the
minimum Lambda Core needs to provision connector ENIs. ENI *deletion* is
handled by `AWSServiceRoleForLambda`, not this role, so it deliberately has no
delete permission.

```console
aws iam create-role --role-name whim-no-public-egress-operator-role \
  --assume-role-policy-document file://trust.json \
  --tags Key=ManagedBy,Value=you Key=Purpose,Value=no-public-egress

aws iam attach-role-policy --role-name whim-no-public-egress-operator-role \
  --policy-arn arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy
```

Then pass `--egress-operator-role arn:aws:iam::<account>:role/whim-no-public-egress-operator-role`.

Whim validates this role before using it (that it exists, trusts
`lambda.amazonaws.com`, and has at least one policy attached), but it cannot
fully verify the policy's *contents* without `iam:GetPolicyVersion` — so a
role that passes Whim's check can still fail at connector creation if you
attached the wrong policy. Prefer the AWS-managed policy above rather than a
hand-rolled one.

## Setting up caller permissions (least privilege)

Your *caller identity* needs permission for the resources Whim creates and
reads on your behalf. The exact set depends on the mode.

### EC2 reads (all modes) and creation (auto-provision only)

Every mode reads the connector's VPC topology; auto-provision also creates it.
These are all standard `ec2:` actions:

- Read (all modes that validate/reuse): `ec2:DescribeVpcs`,
  `ec2:DescribeSubnets`, `ec2:DescribeRouteTables`,
  `ec2:DescribeSecurityGroups`, `ec2:DescribeNetworkAcls`,
  `ec2:DescribeAvailabilityZones`.
- Create/mutate (auto-provision): `ec2:CreateVpc`, `ec2:CreateSubnet`,
  `ec2:CreateRouteTable`, `ec2:AssociateRouteTable`,
  `ec2:CreateSecurityGroup`, `ec2:RevokeSecurityGroupEgress`, and
  `ec2:CreateTags`. `ec2:CreateTags` is required because Whim tags every
  resource *at creation time* (via `TagSpecifications`) — scope it with the
  `ec2:CreateAction` condition so it only permits tagging during those
  creates, not arbitrary retagging:

  ```json
  {
    "Effect": "Allow",
    "Action": "ec2:CreateTags",
    "Resource": "*",
    "Condition": { "StringEquals": { "ec2:CreateAction": [
      "CreateVpc", "CreateSubnet", "CreateRouteTable", "CreateSecurityGroup"
    ] } }
  }
  ```

Whim does **not** create an internet gateway, NAT gateway, or `0.0.0.0/0`
route — so your caller policy should not grant those, and their absence is
part of what makes the VPC isolated. (Network ACL create/replace actions —
`ec2:CreateNetworkAcl`, `ec2:ReplaceNetworkAclAssociation` — are wired in the
code but the current auto-provision path does not create a NACL, so you do not
need them today.)

### `iam:PassRole` (auto-provision and raw subnet/SG modes)

Creating the connector passes the operator role to Lambda Core, which requires
`iam:PassRole` — **scoped to exactly that one role ARN**, never `*`:

```json
{
  "Effect": "Allow",
  "Action": "iam:PassRole",
  "Resource": "arn:aws:iam::<account>:role/whim-no-public-egress-operator-role",
  "Condition": { "StringEquals": { "iam:PassedToService": "lambda.amazonaws.com" } }
}
```

### IAM reads (auto-provision and raw subnet/SG modes)

Whim validates the operator role before use, calling `iam:GetRole`,
`iam:ListAttachedRolePolicies`, and `iam:ListRolePolicies` — scope these to the
operator role ARN too.

### Lambda Core (read in all modes; create in auto/raw modes)

Whim calls the Lambda Core control plane to read the connector in every mode
and create it in auto/raw modes. **Verify the exact IAM action names before writing this part of
the policy** — the reliable, least-privilege way to do it is deny-then-observe:

1. Grant the EC2/IAM permissions above but *not* the Lambda Core ones.
2. Run `whim build ... --egress none --egress-auto-provision ...`.
3. The `AccessDenied` error names the exact action and resource that was
   denied. Add precisely that action, scoped to the connector resource, and
   retry.

This is standard practice for building a minimal policy against any service
(action names are not reliably guessable and services add actions over time),
and it guarantees you grant exactly what this Whim version calls and nothing
speculative.

### Service-linked role (one-time, first connector only)

The first time a network connector is created in the account, AWS may need to
create the `AWSServiceRoleForLambda` service-linked role. If it does not
already exist, the creating identity needs `iam:CreateServiceLinkedRole` for
the `lambda.amazonaws.com` service principal, once. You do not author or
maintain this role.

## Cleanup permissions

`whim build` never deletes networking resources, and `whim image rm` only
prunes local config — so nothing you run routinely needs delete permissions.
Tearing down an auto-provisioned resource group is a separate, explicit,
still-manual step today (there is no `whim egress rm` yet). When you do it, the
deleting identity needs the `Delete*`/`Disassociate*` counterparts of the
create actions above (`ec2:DeleteVpc`, `ec2:DeleteSubnet`,
`ec2:DeleteRouteTable`, `ec2:DeleteSecurityGroup`, the Lambda Core
delete-connector action) plus `iam:DetachRolePolicy`/`iam:DeleteRole` if you
also remove the operator role. Grant these to a separate teardown identity, or
attach them only when tearing down — not to the everyday build identity.

## What Whim never does (so you never grant it)

- **Create IAM roles.** Whim needs no `iam:CreateRole`/`iam:AttachRolePolicy`;
  you create the operator role yourself, once.
- **Delete anything as a side effect.** No delete permission is needed for
  normal `build`/`run`/`image rm` use.
- **Widen the operator role.** The operator role never needs more than
  `AWSLambdaNetworkConnectorOperatorPolicy`; if a connector operation is
  denied, the fix is almost always a *caller* permission, not the operator
  role.

## See also

- README → "Building custom images (`whim build`)" for the full `--egress-*`
  flag semantics, and → "Troubleshooting `--egress-auto-provision`" for what
  each fail-closed error means.
- `whim build --help` for the flag reference.
