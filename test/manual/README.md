# 手动测试指南

Operator 已运行，按以下步骤手动验证。

### 查看状态

```bash
# pod
kubectl get statefulset -n gsg
kubectl get statefulset -n gsb

kubectl get gameservice -A -o wide
kubectl get ingress -A -o wide
kubectl get svc -n gsg -l app.kubernetes.io/managed-by=gs-operator
```

### 测试当前访问（green 活跃）

```bash
curl -s  "http://game.zzrr.io/connector0"
# → connector-0 from gsg
```

### 切换回 blue

```bash
kubectl patch gameservice blue -n gsb --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice green -n gsg --type merge -p '{"spec":{"deployGroup":{"active":false}}}'
sleep 2
curl -s  "http://game.zzrr.io/connector0"
# → connector-0 from gsb
```

### 切换回 green

```bash
kubectl patch gameservice green -n gsg --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice blue -n gsb --type merge -p '{"spec":{"deployGroup":{"active":false}}}'
```

### 扩容（green）

```bash
kubectl scale statefulset -n gsg connector --replicas 12
kubectl get ingress game-ingress-green -n gsg \
  -o jsonpath='{range .spec.rules[0].http.paths[*]}{.path}{"\n"}{end}'
```

### 缩容

```bash
kubectl scale statefulset -n gsg connector --replicas 5
```

### 查看日志

```bash
kubectl logs -n gs-operator-system gs-operator-controller-manager-564bf9d8d7-q724t
```

