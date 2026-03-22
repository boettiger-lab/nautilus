# RustFS on NRP Nautilus

Self-hosted S3-compatible object storage deployed on the NRP Nautilus Kubernetes cluster, running on our owned node (`stratus1.nrp-espm.berkeley.edu`) with local NVMe storage.

## Why RustFS

NRP's native Ceph S3 lacks fine-grained access key management — there is no built-in way to issue per-bucket scoped credentials or time-limited collaborator keys. RustFS provides a full S3-compatible API with MinIO-compatible IAM: create/expire access keys, scope them to specific buckets, and manage everything through a web console.

## Architecture

```
clients
  │  (S3 API — standard AWS SDK / boto3 / rclone)
  ▼
https://s3-berkeley.nrp-nautilus.io   (HAProxy ingress, TLS terminated by cluster)
  │
  ▼
rustfs Deployment  (namespace: biodiversity, pinned to stratus1)
  │
  ▼
/mnt/nvme/rustfs   (7.68 TB PCIe4 NVMe on stratus1.nrp-espm.berkeley.edu)
```

The web console is at `https://s3-console.nrp-nautilus.io`.

## Prerequisites

### 1. NRP approvals (already obtained)

- **Whitelisted Deployment** — long-running Deployments are normally auto-deleted after 2 weeks; this one is whitelisted.
- **hostPath volumes** — normally restricted on shared clusters; permitted for our owned node.

### 2. Verify the hostPath exists on stratus1

The pod mounts `/mnt/nvme/rustfs` directly from the node filesystem. The parent directory `/mnt/nvme` must exist before deploying — Kubernetes will create `rustfs/` inside it (`DirectoryOrCreate`), but will not create missing parent directories.

**Validate before deploying:**

```bash
# SSH into the node and check
ssh stratus1.nrp-espm.berkeley.edu
ls -lh /mnt/nvme/
df -h /mnt/nvme   # confirm ~7.68 TB available
```

If `/mnt/nvme` does not exist, either the drive is mounted elsewhere or needs mounting. Check with:

```bash
lsblk
findmnt | grep nvme
```

Mount it if needed (requires root on the node):

```bash
# Example — actual device name may differ
sudo mkfs.xfs /dev/nvme1n1          # only if unformatted
sudo mkdir -p /mnt/nvme
sudo mount /dev/nvme1n1 /mnt/nvme
# Add to /etc/fstab for persistence across reboots
echo '/dev/nvme1n1 /mnt/nvme xfs defaults 0 0' | sudo tee -a /etc/fstab
```

Then create the RustFS data directory:

```bash
sudo mkdir -p /mnt/nvme/rustfs
sudo chmod 777 /mnt/nvme/rustfs   # RustFS runs as non-root (UID 10001)
```

## Deployment

### Step 1 — Create credentials secret

```bash
kubectl -n biodiversity create secret generic rustfs-credentials \
  --from-literal=RUSTFS_ACCESS_KEY=$(openssl rand -hex 12) \
  --from-literal=RUSTFS_SECRET_KEY=$(openssl rand -hex 24)
```

**Save the generated credentials immediately** — the secret key cannot be retrieved from the cluster later (it is stored as opaque bytes). To view them right after creation:

```bash
kubectl -n biodiversity get secret rustfs-credentials \
  -o jsonpath='{.data.RUSTFS_ACCESS_KEY}' | base64 -d; echo
kubectl -n biodiversity get secret rustfs-credentials \
  -o jsonpath='{.data.RUSTFS_SECRET_KEY}' | base64 -d; echo
```

### Step 2 — Deploy

```bash
cd rustfs
./up.sh
```

This applies all manifests in `k8s/` and waits for the rollout to complete.

### Step 3 — Verify

```bash
kubectl -n biodiversity get pods -l k8s-app=rustfs
kubectl -n biodiversity logs deployment/rustfs

# Check the S3 endpoint responds
curl -I https://s3-berkeley.nrp-nautilus.io/health
```

### Step 4 — Open the console

Navigate to `https://s3-console.nrp-nautilus.io` and log in with the credentials from Step 1.

From the console you can:
- Create buckets
- Create IAM users and access keys scoped to specific buckets
- Set bucket policies (public read, private, etc.)
- Monitor storage usage

## Data Migration (Ceph S3 → RustFS)

After RustFS is running and buckets have been created in the console:

```bash
# Review the job first — it syncs all buckets found in the source Ceph account
cat k8s/migration-job.yaml

kubectl -n biodiversity apply -f k8s/migration-job.yaml
kubectl -n biodiversity logs -f job/rustfs-migrate
```

The job:
- Runs on stratus1 (data stays on-node, fast transfer)
- Uses `rclone sync --checksum` for integrity verification
- Syncs all buckets found in the source Ceph account automatically
- Uses 16 parallel transfers per bucket

After migration, do a final incremental sync before switching clients:

```bash
# Re-run the job to catch any writes during migration
kubectl -n biodiversity delete job rustfs-migrate --ignore-not-found
kubectl -n biodiversity apply -f k8s/migration-job.yaml
```

Then update client `endpoint_url` from `https://s3-west.nrp-nautilus.io` to `https://s3-berkeley.nrp-nautilus.io`.

## Key Management

Create scoped collaborator keys via the console UI or RustFS-compatible API:

- **Admin key** — full access to all buckets (from Step 1 credentials)
- **Collaborator key** — create a new IAM user, attach a policy restricting access to specific bucket(s), generate an access key for that user
- **Time-limited key** — RustFS supports key expiration via the console

Example IAM policy for read-only access to one bucket:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["s3:GetObject", "s3:ListBucket"],
    "Resource": [
      "arn:aws:s3:::my-bucket",
      "arn:aws:s3:::my-bucket/*"
    ]
  }]
}
```

## Teardown

```bash
./down.sh
```

Data on `/mnt/nvme/rustfs` is **not** deleted. To redeploy, run `./up.sh` again — existing data is preserved.

## Files

```
rustfs/
  up.sh                    Deploy all manifests
  down.sh                  Remove deployment (data untouched)
  k8s/
    secret.yaml            Credential secret template (see Step 1 for creation command)
    deployment.yaml        RustFS Deployment, pinned to stratus1 via nodeSelector
    service-s3.yaml        ClusterIP service for S3 API (port 9000)
    service-console.yaml   ClusterIP service for web console (port 9001)
    ingress-s3.yaml        HAProxy ingress: s3-berkeley.nrp-nautilus.io
    ingress-console.yaml   HAProxy ingress: s3-console.nrp-nautilus.io
    migration-job.yaml     rclone sync job: NRP Ceph S3 → RustFS
```

## Blockers — resolve before deploying

### Node network issue (UNRESOLVED)

stratus1 is currently tainted by NRP due to a network issue. No pods can be scheduled
there until NRP resolves it. Check back with NRP support for status.

---

## Troubleshooting

**Pod stuck in `Pending`**
- The pod can only schedule on stratus1 (nodeSelector). Check the node is Ready: `kubectl get node stratus1.nrp-espm.berkeley.edu`
- If no events appear on the pod, the node taint is likely missing from tolerations — see Blockers above.
- Check for resource contention: `kubectl -n biodiversity describe pod -l k8s-app=rustfs`

**Pod `CrashLoopBackOff`**
- Most likely the hostPath directory doesn't exist or has wrong permissions.
- Check logs: `kubectl -n biodiversity logs deployment/rustfs`
- SSH to stratus1 and verify `/mnt/nvme/rustfs` exists and is writable by UID 10001.

**`403 Forbidden` on S3 requests**
- Verify you're using the correct access key and secret key.
- Check that the IAM policy attached to the key permits the operation on that bucket.

**Console unreachable**
- The console ingress (`s3-console.nrp-nautilus.io`) is separate from the S3 ingress. Check: `kubectl -n biodiversity get ingress rustfs-console`
