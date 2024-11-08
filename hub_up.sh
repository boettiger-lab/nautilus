#!/bin/bash

helm repo add jupyterhub https://hub.jupyter.org/helm-chart/
helm repo update

## use your name for install name and namespace name
helm upgrade --cleanup-on-fail \
  --install juypterhelm jupyterhub/jupyterhub \
  --namespace espm-157 \
  --create-namespace \
  --version=3.3.7 \
  --timeout 90m0s \
  --values values.yaml \
  --values secrets.yaml

