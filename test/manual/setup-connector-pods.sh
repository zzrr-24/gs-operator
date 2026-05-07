#!/bin/bash
set -e

NAMESPACE=${1:-adventure}
COUNT=${2:-3}
IMAGE=${3:-nginx:alpine}

echo "=== Creating $COUNT connector pods in namespace '$NAMESPACE' ==="

kubectl create ns "$NAMESPACE" 2>/dev/null || true

for i in $(seq 0 $((COUNT-1))); do
  kubectl run "connector-$i" \
    -n "$NAMESPACE" \
    --image="$IMAGE" \
    --labels="adventure=connector,statefulset.kubernetes.io/pod-name=connector-$i" \
    --port=3010
done

echo ""
echo "=== Waiting for pods to be ready ==="
kubectl wait --for=condition=Ready pod -n "$NAMESPACE" -l adventure=connector --timeout=60s

echo ""
echo "=== Pods: ==="
kubectl get pod -n "$NAMESPACE" -l adventure=connector -o wide
