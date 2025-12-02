#!/bin/bash

kubectl apply -f service.yaml -n espm-157 
kubectl apply -f ingress.yaml -n espm-157
kubectl apply -f deployment.yaml -n espm-157

kubectl get pods -n espm-157

