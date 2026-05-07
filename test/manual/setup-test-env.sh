# 创建两个测试命名空间，模拟蓝绿环境

## 创建 blue 命名空间测试 Pod
kubectl create ns adventure-blue 2>/dev/null || true
for i in $(seq 0 2); do
  kubectl run "connector-$i" -n adventure-blue \
    --image=nginx:alpine \
    --labels="adventure=connector,statefulset.kubernetes.io/pod-name=connector-$i" \
    --port=3010 \
    -- /bin/sh -c "echo 'connector-$i from adventure-blue (v1.0)' > /usr/share/nginx/html/index.html; nginx -g 'daemon off;'"
done

## 创建 green 命名空间测试 Pod
kubectl create ns adventure-green 2>/dev/null || true
for i in $(seq 0 2); do
  kubectl run "connector-$i" -n adventure-green \
    --image=nginx:alpine \
    --labels="adventure=connector,statefulset.kubernetes.io/pod-name=connector-$i" \
    --port=3010 \
    -- /bin/sh -c "echo 'connector-$i from adventure-green (v2.0)' > /usr/share/nginx/html/index.html; nginx -g 'daemon off;'"
done

echo "=== Waiting for pods to be ready ==="
kubectl wait --for=condition=Ready pod -n adventure-blue -l adventure=connector --timeout=60s
kubectl wait --for=condition=Ready pod -n adventure-green -l adventure=connector --timeout=60s

echo ""
echo "=== Blue pods ==="
kubectl get pod -n adventure-blue -l adventure=connector -o wide
echo ""
echo "=== Green pods ==="
kubectl get pod -n adventure-green -l adventure=connector -o wide
