# K8s 部署

把 mockupstream 部署进 Kubernetes 的清单。服务监听容器内 `:9050`（不可配置），默认**不做认证**，因此只提供 `ClusterIP` Service，**禁止**通过 Ingress / LoadBalancer / NodePort 暴露到集群外。

## 前置：构建并推送镜像

集群节点拉不到本地镜像，先推到集群可达的 registry：

```bash
docker build -t <registry>/mockupstream:latest .
docker push <registry>/mockupstream:latest
```

本地开发集群可以直接导入，跳过 registry：

```bash
kind load docker-image mockupstream:latest        # kind
minikube image load mockupstream:latest           # minikube
```

## 部署

```bash
# 用 kustomize 一键应用（推荐，可顺便覆盖镜像名）
kubectl apply -k k8s/

# 或逐个应用
kubectl apply -f k8s/configmap.yaml -f k8s/deployment.yaml -f k8s/service.yaml
```

覆盖镜像引用（不改 deployment.yaml）：

```bash
cd k8s && kustomize edit set image mockupstream=<registry>/mockupstream:v1
```

验证：

```bash
kubectl rollout status deploy/mockupstream
kubectl port-forward svc/mockupstream 9050:9050 &
curl localhost:9050/__mock/healthz   # → ok
```

## 网关接入

同集群内把渠道 `BaseURL` 指向：

```
http://mockupstream.<namespace>.svc:9050
```

网关代码零改动，路径按后缀匹配（详见根目录 README）。

## 配置

三层叠加不变：内置默认值 < ConfigMap 里的 `config.json` < Deployment 的 `env`。

- 改场景配置：编辑 `configmap.yaml` 后 `kubectl apply -k k8s/`，再 `kubectl rollout restart deploy/mockupstream`（ConfigMap 更新不会自动重启 Pod，而进程只在启动时读一次配置）。
- 临时覆盖单项：往 `deployment.yaml` 的 `env` 加 `MOCK_*` 变量，与 docker-compose 里的写法一致。

## 注意事项

- **replicas 必须为 1**：请求捕获（`/__mock/requests`）和 DashScope / GLM / MiniMax 的异步任务状态都在进程内存里，多副本时提交与轮询可能落到不同 Pod。要更高吞吐请加大 resources（并同步调 `GOMAXPROCS`），不要横向扩。
- **GOMAXPROCS 跟随 CPU limit**：Go 运行时默认按宿主机核数调度，deployment.yaml 里已把它钉到 limit（6）。改 CPU limit 时记得同步改。
- **多租户集群**：建议应用 `networkpolicy.yaml`（默认未包含在 kustomization 里），只放行带 `mockupstream-client: "true"` 标签的 Pod；或在 ConfigMap 里设 `require_key: true` + `api_key` 开启 Bearer 校验。
- **压测场景**：K8s 节点的容器运行时默认 nofile 限制通常已足够（≥1M）；若压测报 `too many open files`，检查 kubelet / containerd 的 `LimitNOFILE`。
