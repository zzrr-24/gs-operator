# 手动测试指南

Operator 已在后台运行，按以下步骤手动验证。

## 当前状态

| CR | active | Ingress 命名空间 | 版本 |
|----|--------|-----------------|------|
| blue | false | 无 | adventure-blue (v1.0) |
| green | true | adventure-green | adventure-green (v2.0) |

## 测试用例

### 查看状态

```bash
kubectl get gameservice -o wide
kubectl get ingress -A -o wide
kubectl get svc -n adventure-green -l app.kubernetes.io/managed-by=gs-operator
```

### 测试当前访问（green 活跃）

```bash
NODE_IP=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[0].address}')
curl -s -H "Host: game.example.com" "http://$NODE_IP:31546/connector0"
# → connector-0 from adventure-green (v2.0)
```

### 切换回 blue

```bash
kubectl patch gameservice blue --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice green --type merge -p '{"spec":{"deployGroup":{"active":false}}}'
sleep 3
curl -s -H "Host: game.example.com" "http://$NODE_IP:31546/connector0"
# → connector-0 from adventure-blue (v1.0)
```

### 切换回 green

```bash
kubectl patch gameservice green --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice blue --type merge -p '{"spec":{"deployGroup":{"active":false}}}'
```

### 扩容（green）

```bash
kubectl run connector-3 -n adventure-green --image=nginx:alpine \
  --labels="adventure=connector,statefulset.kubernetes.io/pod-name=connector-3" \
  --port=80 -- /bin/sh -c "
echo 'connector-3 from adventure-green (v2.0)' > /usr/share/nginx/html/index.html
cat > /etc/nginx/conf.d/default.conf << 'EOF2'
server { listen 80 default_server; location / { root /usr/share/nginx/html; try_files \$uri /index.html =404; } }
EOF2
nginx -g 'daemon off;'"
kubectl wait --for=condition=Ready pod connector-3 -n adventure-green --timeout=60s
kubectl get ingress game-ingress-green -n adventure-green \
  -o jsonpath='{range .spec.rules[0].http.paths[*]}{.path}{"\n"}{end}'
# 预期新增 /connector3
```

### 缩容

```bash
kubectl delete pod connector-3 -n adventure-green --now
```

### 查看日志

```bash
tail -f /tmp/operator3.log
```
