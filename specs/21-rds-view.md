# 21 — RDS View

List RDS DB instances, copy the SSM port-forward command for a chosen instance, and pull up details / connection info. Read-only in v1 — no start / stop / modify / snapshot / delete from the TUI; those are easy to misclick and the console covers them well. The killer feature is **"give me a working port-forward command that targets this RDS endpoint via my bastion"** which is the single most common reason to open the RDS console.

## Layout — list

```
┌─ RDS Instances (5) ───────────────────────────────────────────────┐
│ Filter: ___________                                                │
│                                                                    │
│   Identifier            Engine       Status     Endpoint           │
│ > app-prod-db           postgres 15  available  app-prod-db.xyz... │
│   app-prod-db-replica   postgres 15  available  app-prod-db-rep... │
│   app-staging-db        postgres 14  available  app-staging-db... │
│   analytics-warehouse   mysql 8.0    stopped    analytics-ware... │
│   sandbox-mysql         mysql 8.0    available  sandbox-mysql.... │
│                                                                    │
│ enter: details · f: port-forward command · y: yank menu · /: filter│
└────────────────────────────────────────────────────────────────────┘
```

Status is colour-coded (`available` green, `stopped` muted, `backing-up`/`modifying`/`storage-optimization`/`rebooting`/etc yellow, `failed`/`incompatible-*` red, empty N/A) — same pattern as the other status badges.

## Layout — details

```
┌─ app-prod-db ─────────────────────────────────────────────────────┐
│ Engine:      postgres 15.4                                         │
│ Class:       db.r6g.xlarge                                         │
│ Status:      available                                             │
│ Multi-AZ:    yes                                                   │
│ Endpoint:    app-prod-db.abc123.ap-southeast-2.rds.amazonaws.com   │
│ Port:        5432                                                  │
│ DB name:     appdb                                                 │
│ Master user: app_admin                                             │
│ VPC:         vpc-0abc                                              │
│ Subnet grp:  default-vpc-0abc                                      │
│ Security:    sg-1111, sg-2222                                      │
│ Storage:     200 GB gp3 (iops 3000)                                │
│ Created:     2023-04-12 14:30:01                                   │
│                                                                    │
│ f: build & yank port-forward command · y h: yank endpoint host    │
│ · y p: yank port · y u: yank master user · esc back               │
└────────────────────────────────────────────────────────────────────┘
```

## Port-forward command builder

Most RDS users hit their DB through a bastion EC2 over Session Manager. Building that command by hand is tedious:

```
aws ssm start-session \
  --profile <profile> \
  --region <region> \
  --target <bastion-instance-id> \
  --document-name AWS-StartPortForwardingSessionToRemoteHost \
  --parameters '{"host":["<endpoint>"],"portNumber":["<remote-port>"],"localPortNumber":["<local-port>"]}'
```

Pressing `f` opens a small modal:

```
┌─ Port-forward to app-prod-db ─────────────────────────────────────┐
│ Bastion instance:  [i-_______________________________]             │
│ Remote port:       [5432]                                          │
│ Local port:        [15432]                                         │
│                                                                    │
│ enter: yank command (does not run)                                 │
│ esc: cancel                                                        │
└────────────────────────────────────────────────────────────────────┘
```

`enter` puts the fully-built command on the clipboard. It does **not** execute. The user pastes it into their own shell. This is deliberate — the SSM session opens a long-running process and the TUI shouldn't fight for stdin while it does. (We can offer a separate "run it now" key later via `tea.ExecProcess` if that turns out to be a common ask.)

Bastion instance defaults to `i-` so the user can paste / type the rest. Future polish: pre-populate with a remembered last-used bastion per profile (would live in `state.json`).

## API

`rds.DescribeDBInstances` is paginated. Cache the same way other views do, 60s TTL. No DB-level credentials are fetched.

```go
import rds "github.com/aws/aws-sdk-go-v2/service/rds"

paginator := rds.NewDescribeDBInstancesPaginator(client, &rds.DescribeDBInstancesInput{})
```

Fields used:
- DBInstanceIdentifier
- Engine + EngineVersion
- DBInstanceStatus
- Endpoint.Address / Endpoint.Port
- DBInstanceClass
- MultiAZ
- DBName
- MasterUsername
- VpcSecurityGroups -> id list
- AllocatedStorage + StorageType + Iops
- InstanceCreateTime

## AWS context wiring

Add `RDS()` to `internal/aws/client.go` mirroring SSM / SecurityHub. Required IAM: `rds:DescribeDBInstances`.

## Dashboard wiring

Add `TabRDS` to dashboard constants and tab names. Adjust the array size and test fixtures.

The new order keeps Beanstalk first per recent preference; placing RDS right after EC2 makes the "SSM to bastion -> RDS endpoint" workflow adjacent in the menu:

```
Beanstalk · EC2 · RDS · Logs · CloudFront · S3 · Parameter Store · SecurityHub
```

## Acceptance criteria

- `DescribeDBInstances` runs paginated on Init(), 60s cache.
- List shows Identifier / Engine / Status / Endpoint with the status colour-coded.
- `/` filters by identifier / engine / endpoint / DB name (case-insensitive).
- `enter` opens details with full metadata.
- `f` opens port-forward modal; `enter` yanks the built command without running.
- `y` opens yank menu: `h` endpoint host, `p` port, `u` master user.
- `ctrl+r` refreshes (invalidates cache).
- View implements CapturingInput / InSubnav / HelpItems / StatusFooter just like every other tab.
EOF
