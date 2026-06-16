# Gateway 模式手工验收

这组资产只验证 `trafficMode: Gateway`，使用独立命名空间 `gw-blue` 和 `gw-green`，不会修改现有 Ingress 手工验收样例。

## 前置条件

- 集群已安装 Gateway API CRD，能查询 `HTTPRoute`：

```bash
kubectl api-resources | grep -i httproute
```

- Operator 已部署并运行。
- 集群已有可用 Gateway。样例默认使用：

```bash
kubectl get gateway shared-gateway -n gateway-system
```

如果现场 Gateway 不同，先调整 `test/manual/gateway/gsb.yaml` 和 `test/manual/gateway/gsg.yaml` 中的：

```yaml
spec:
  route:
    host: game.example.com
  gateway:
    parentRef:
      name: shared-gateway
      namespace: gateway-system
      sectionName: http
```

## 部署蓝绿样例

```bash
kubectl apply -f test/manual/gateway/gsb.yaml
kubectl apply -f test/manual/gateway/gsg.yaml

kubectl wait --for=condition=Ready pod -n gw-blue --all --timeout=90s
kubectl wait --for=condition=Ready pod -n gw-green --all --timeout=90s
```

初始状态为 blue 活跃、green 备用：

```bash
kubectl get gameservice -n gw-blue gateway-blue -o wide
kubectl get gameservice -n gw-green gateway-green -o wide
```

## 验证主 HTTPRoute

blue 活跃后应创建主 `HTTPRoute`：

```bash
kubectl get httproute game-route-blue -n gw-blue
kubectl get httproute game-route-green -n gw-green
```

预期：

- `gw-blue/game-route-blue` 存在。
- `gw-green/game-route-green` 不存在，或 `kubectl get` 返回 `NotFound`。

检查主路由的 host、parentRef、path 和 backend：

```bash
kubectl get httproute game-route-blue -n gw-blue \
  -o jsonpath='{.spec.hostnames[0]}{"\n"}{.spec.parentRefs[0].name}{"\n"}{range .spec.rules[*]}{.matches[0].path.value}{" -> "}{.backendRefs[0].name}{":"}{.backendRefs[0].port}{"\n"}{end}'
```

预期包含：

```text
game.example.com
shared-gateway
/connector0 -> connector-0-svc:80
/connector1 -> connector-1-svc:80
/connector2 -> connector-2-svc:80
```

## 验证 extraIngress 映射出的 extra HTTPRoute

`extraIngress.name: gamelogin` 在 Gateway 模式下应生成 `gamelogin-gateway-blue`：

```bash
kubectl get httproute gamelogin-gateway-blue -n gw-blue
kubectl get httproute gamelogin-gateway-green -n gw-green
```

预期：

- `gw-blue/gamelogin-gateway-blue` 存在。
- `gw-green/gamelogin-gateway-green` 不存在，或 `kubectl get` 返回 `NotFound`。

检查 extra HTTPRoute 的路径映射：

```bash
kubectl get httproute gamelogin-gateway-blue -n gw-blue \
  -o jsonpath='{range .spec.rules[*]}{.matches[0].path.value}{" -> "}{.backendRefs[0].name}{":"}{.backendRefs[0].port}{"\n"}{end}'
```

预期包含：

```text
/serverlogin -> logingame-blue:9020
/servergovern -> servergovern-blue:9015
/backend -> backend-blue:9010
/statsgm -> statsgm-blue:9012
```

同时确认 Gateway 模式没有创建旧 Ingress：

```bash
kubectl get ingress -n gw-blue
kubectl get ingress -n gw-green
```

预期两个命名空间都没有由本样例创建的 Ingress。

## 验证真实流量

根据集群 Gateway 实现获取入口地址。若 Gateway 状态里有地址，可以先尝试：

```bash
export GATEWAY_ADDR=$(kubectl get gateway shared-gateway -n gateway-system -o jsonpath='{.status.addresses[0].value}')
echo "$GATEWAY_ADDR"
```

主 HTTPRoute：

```bash
curl -s -H "Host: game.example.com" "http://${GATEWAY_ADDR}/connector0"
# 预期: connector-blue-0
```

extra HTTPRoute：

```bash
curl -s -H "Host: game.example.com" "http://${GATEWAY_ADDR}/serverlogin"
# 预期: logingame-blue
```

如果你的 Gateway 需要固定 LB IP、NodePort、端口转发或域名解析，请把 `GATEWAY_ADDR` 替换为现场入口地址，并保持 `Host: game.example.com` 与样例 host 一致。

## 蓝绿切换验证

切换到 green：

```bash
kubectl patch gameservice gateway-green -n gw-green --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice gateway-blue -n gw-blue --type merge -p '{"spec":{"deployGroup":{"active":false}}}'
sleep 3
```

验证 green 的主 HTTPRoute 和 extra HTTPRoute 已创建：

```bash
kubectl get httproute game-route-green -n gw-green
kubectl get httproute gamelogin-gateway-green -n gw-green
```

验证 blue 的主 HTTPRoute 和 extra HTTPRoute 已清理：

```bash
kubectl get httproute game-route-blue -n gw-blue
kubectl get httproute gamelogin-gateway-blue -n gw-blue
```

预期两个 blue 路由都返回 `NotFound`。

验证切换后的真实流量：

```bash
curl -s -H "Host: game.example.com" "http://${GATEWAY_ADDR}/connector0"
# 预期: connector-green-0

curl -s -H "Host: game.example.com" "http://${GATEWAY_ADDR}/serverlogin"
# 预期: logingame-green
```

如需切回 blue：

```bash
kubectl patch gameservice gateway-blue -n gw-blue --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice gateway-green -n gw-green --type merge -p '{"spec":{"deployGroup":{"active":false}}}'
sleep 3
```

## 清理

```bash
kubectl delete gameservice gateway-blue -n gw-blue --ignore-not-found
kubectl delete gameservice gateway-green -n gw-green --ignore-not-found
kubectl delete namespace gw-blue gw-green --ignore-not-found
```
