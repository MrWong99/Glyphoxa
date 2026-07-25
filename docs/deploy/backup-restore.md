# Postgres backup and restore

The chart's in-cluster Postgres is a **single StatefulSet on one node's disk**
(ADR-0034 — the CNPG operator is out of scope). Everything Glyphoxa persists
lives there: Tenants, Campaigns, Agents, Transcript Lines and Chunks with their
embeddings, the usage ledger, encrypted BYOK credentials. There is no second
copy unless you make one.

This page covers the chart's backup CronJob (#520) and — the part that actually
matters — how to restore from what it writes.

## What the CronJob does

Enable it in your values:

```yaml
backup:
  enabled: true
  schedule: "15 4 * * *"   # nightly, cluster timezone
  retentionDays: 14
  persistence:
    size: 10Gi             # retentionDays × dump size, with headroom
```

Each run:

1. `pg_dump -Fc` (PostgreSQL **custom format** — compressed, and the input
   `pg_restore` takes) of the database named by the app Secret's
   `database-url`, into `/backup/glyphoxa-<UTC timestamp>.dump` on a PVC of its
   own — never the database's volume, because a dump on the disk it protects is
   not a backup;
2. records the filename in `/backup/.latest`;
3. deletes dumps older than `retentionDays` — **after** a successful dump, so a
   failing night never eats the last good one.

The PVC carries `helm.sh/resource-policy: keep`: `helm uninstall` leaves your
dumps alone.

### Off-site copies

A dump on the same VM survives a bad migration or a `DROP TABLE`, but not a dead
disk. Point the optional push at any S3-compatible bucket (Backblaze B2, Wasabi,
MinIO, S3):

```sh
kubectl -n glyphoxa create secret generic glyphoxa-backup-offsite \
  --from-literal=access-key-id=... \
  --from-literal=secret-access-key=...
```

```yaml
backup:
  enabled: true
  offsite:
    enabled: true
    bucket: glyphoxa-backups
    prefix: nightly/            # optional, include the trailing slash
    endpoint: https://s3.eu-central-003.backblazeb2.com   # omit for AWS S3
    region: us-east-1
    existingSecret:
      name: glyphoxa-backup-offsite
```

The dump then runs as an **initContainer** and the upload as the pod's
container, so a push can never start against a half-written file. The chart
deliberately does not template object-store credentials into its own Secret —
you create and rotate that one.

## Verify a backup exists (do this once, not after the fire)

```sh
# The CronJob has run at least once:
kubectl -n glyphoxa get jobs -l app.kubernetes.io/component=backup

# What is on the volume (any pod that mounts it will do):
kubectl -n glyphoxa run backup-ls --rm -it --restart=Never \
  --image=pgvector/pgvector:pg17 \
  --overrides='{"spec":{"containers":[{"name":"backup-ls","image":"pgvector/pgvector:pg17","command":["ls","-lh","/backup"],"stdin":true,"tty":true,"volumeMounts":[{"name":"backup","mountPath":"/backup"}]}],"volumes":[{"name":"backup","persistentVolumeClaim":{"claimName":"glyphoxa-backup"}}]}}'
```

Trigger an ad-hoc run without waiting for the schedule:

```sh
kubectl -n glyphoxa create job --from=cronjob/glyphoxa-backup backup-manual-1
kubectl -n glyphoxa logs job/backup-manual-1
```

## Restore

`pg_restore` is not a merge: restoring into a database that already has the
schema needs `--clean --if-exists`, and **anything written after the dump is
gone**. Stop the app first so nothing writes into a half-restored schema.

```sh
# 1. Stop the writers (both, if you run split mode).
kubectl -n glyphoxa scale deployment/glyphoxa-web  --replicas=0
kubectl -n glyphoxa scale deployment/glyphoxa-voice --replicas=0

# 2. Restore, from a pod that mounts the backup volume and can reach Postgres.
kubectl -n glyphoxa apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: glyphoxa-restore
spec:
  restartPolicy: Never
  containers:
    - name: restore
      image: pgvector/pgvector:pg17
      command: ["sleep", "3600"]
      env:
        - name: GLYPHOXA_DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: glyphoxa-db
              key: database-url
      volumeMounts:
        - name: backup
          mountPath: /backup
  volumes:
    - name: backup
      persistentVolumeClaim:
        claimName: glyphoxa-backup
EOF

kubectl -n glyphoxa exec -it glyphoxa-restore -- sh -c '
  ls -1 /backup/glyphoxa-*.dump | tail -5'          # pick one

kubectl -n glyphoxa exec -it glyphoxa-restore -- sh -c '
  pg_restore -d "$GLYPHOXA_DATABASE_URL" --clean --if-exists --no-owner \
    /backup/glyphoxa-20260722T041500Z.dump'

# 3. Confirm the schema matches the image you are about to run, then bring the
#    app back. A serving pod refuses to start on a stale schema (EnsureCurrent,
#    ADR-0031) rather than serving against it — run `migrate up` if the restored
#    dump predates a migration.
kubectl -n glyphoxa delete pod glyphoxa-restore
kubectl -n glyphoxa scale deployment/glyphoxa-web  --replicas=1
kubectl -n glyphoxa scale deployment/glyphoxa-voice --replicas=1
```

Restoring an **off-site** dump is the same, one `aws s3 cp` earlier:

```sh
aws s3 cp s3://glyphoxa-backups/nightly/glyphoxa-20260722T041500Z.dump . \
  --endpoint-url https://s3.eu-central-003.backblazeb2.com
```

### Restoring into a scratch database first

Recommended, and the way to rehearse this before you need it: restore into a
throwaway database on the same server, poke at it, and only then decide.

```sh
kubectl -n glyphoxa exec -it glyphoxa-restore -- sh -c '
  psql "$GLYPHOXA_DATABASE_URL" -c "CREATE DATABASE glyphoxa_scratch;" &&
  scratch="$(echo "$GLYPHOXA_DATABASE_URL" | sed "s#/glyphoxa?#/glyphoxa_scratch?#")" &&
  pg_restore -d "$scratch" --no-owner /backup/glyphoxa-20260722T041500Z.dump &&
  psql "$scratch" -c "SELECT count(*) FROM transcript_line;" \
                  -c "SELECT count(*) FROM campaign;"'
```

A restore that reports plausible row counts here is a restore you can trust; one
that errors on `pgvector` types means the scratch database is missing the
extension the dump expects (the `pgvector/pgvector` image ships it — use that
image, not stock `postgres`).

Drop the scratch database when you are done:

```sh
kubectl -n glyphoxa exec -it glyphoxa-restore -- \
  psql "$GLYPHOXA_DATABASE_URL" -c "DROP DATABASE glyphoxa_scratch;"
```

## What this does not cover

- **Blob storage.** Highlight clips and other blobs live in Postgres today
  (ADR-0048, blob seam v1), so they are inside the dump. That stops being true
  the day the seam moves to object storage — revisit this page then.
- **Point-in-time recovery.** Logical dumps give you nightly granularity, not
  "five minutes ago". WAL archiving is a bigger change (and a reason to move to
  a managed Postgres or CNPG) than this chart takes on.
- **The node itself.** For the home-VM deployment, keep taking VM-level
  snapshots too — see [k3s-proxmox.md](k3s-proxmox.md) §8. Crash-consistent
  snapshots catch everything the logical dump does not (certificates, tunnel
  credentials, the cluster).

## See also

- [k3s-proxmox.md](k3s-proxmox.md) — the home-lab deployment this chart backup
  replaces the hand-rolled CronJob in.
- [ADR-0031](../adr/0031-postgres-migration-tooling.md) — migrations and the
  serving process's refusal to run against a stale schema.
- [ADR-0034](../adr/0034-deployment-artifacts.md) — deployment artifacts and the
  single-StatefulSet Postgres posture.
