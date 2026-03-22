#!/bin/bash
set -e
NS=biodiversity

# 1. Create credentials secret (skip if already exists)
if ! kubectl -n $NS get secret rustfs-credentials &>/dev/null; then
  echo "Creating rustfs-credentials secret..."
  kubectl -n $NS create secret generic rustfs-credentials \
    --from-literal=RUSTFS_ACCESS_KEY=$(openssl rand -hex 12) \
    --from-literal=RUSTFS_SECRET_KEY=$(openssl rand -hex 24)
  echo ""
  echo "⚠  Save these credentials now — the secret key is not retrievable later:"
  kubectl -n $NS get secret rustfs-credentials -o jsonpath='{.data.RUSTFS_ACCESS_KEY}' | base64 -d; echo
  kubectl -n $NS get secret rustfs-credentials -o jsonpath='{.data.RUSTFS_SECRET_KEY}' | base64 -d; echo
else
  echo "rustfs-credentials secret already exists, skipping."
fi

echo ""
echo "Applying manifests..."
kubectl -n $NS apply -f k8s/secret.yaml 2>/dev/null || true   # template only, skip errors
kubectl -n $NS apply -f k8s/deployment.yaml
kubectl -n $NS apply -f k8s/service-s3.yaml
kubectl -n $NS apply -f k8s/service-console.yaml
kubectl -n $NS apply -f k8s/ingress-s3.yaml
kubectl -n $NS apply -f k8s/ingress-console.yaml

echo ""
echo "Waiting for rollout..."
kubectl -n $NS rollout status deployment/rustfs

echo ""
echo "RustFS is up."
echo "  S3 endpoint : https://s3-berkeley.nrp-nautilus.io"
echo "  Console     : https://s3-console.nrp-nautilus.io"
