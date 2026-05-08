# 构建、推送、部署流程

## 1. 设置镜像标签

```bash
export IMG=registry.cn-hangzhou.aliyuncs.com/zzrr_images/gs-operator:v2
```

## 2. 登录阿里云镜像仓库

```bash
docker login --username=<阿里云账号> registry.cn-hangzhou.aliyuncs.com
```

## 3. 构建镜像

```bash
docker build --network host -t ${IMG} .
```

> 如果 `docker` 实际为 podman，需加 `--network host` 避免 CNI 插件缺失导致 `go mod download` 失败。

## 4. 推送镜像

```bash
docker push ${IMG}
```

## 5. 部署到集群

```bash
make deploy IMG=${IMG}
```

该命令会：
- `make manifests` — 生成 CRD/RBAC 清单
- `kustomize edit set image` — 注入镜像地址
- `kustomize build config/default | kubectl apply -f -` — 部署

## 6. 验证

```bash
# 确认新 Pod 运行
kubectl get pods -n gs-operator-system -w

# 确认镜像版本
kubectl get deployment -n gs-operator-system \
  -o jsonpath='{.items[0].spec.template.spec.containers[0].image}'

# 查看日志
kubectl logs -n gs-operator-system \
  deployment/gs-operator-controller-manager -c manager -f
```

## 一键脚本

```bash
#!/bin/bash
set -e
IMG=registry.cn-hangzhou.aliyuncs.com/zzrr_images/gs-operator:v2
docker build --network host -t ${IMG} .
docker push ${IMG}
make deploy IMG=${IMG}
echo "Deployed: ${IMG}"
```
