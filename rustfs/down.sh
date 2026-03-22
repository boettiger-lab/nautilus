#!/bin/bash
# Tears down the RustFS deployment. Data on /mnt/nvme/rustfs is NOT deleted.
set -e
NS=biodiversity

kubectl -n $NS delete -f k8s/ingress-s3.yaml      --ignore-not-found
kubectl -n $NS delete -f k8s/ingress-console.yaml  --ignore-not-found
kubectl -n $NS delete -f k8s/service-s3.yaml       --ignore-not-found
kubectl -n $NS delete -f k8s/service-console.yaml  --ignore-not-found
kubectl -n $NS delete -f k8s/deployment.yaml       --ignore-not-found

echo "RustFS deployment removed. Data on stratus1:/mnt/nvme/rustfs is intact."
