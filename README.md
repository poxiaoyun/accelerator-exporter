# accelerator-exporter

`accelerator-exporter` 直接抓取 GPU、NPU、vGPU、vNPU exporter 的 Prometheus
`/metrics` 地址，在进程内完成指标名、标签和单位转换，并通过自身的 `/metrics`
统一暴露 `accelerator_*` 指标。

服务不依赖 Prometheus HTTP API，也不读取 RecordingRule。

## 配置

每个数据源需要唯一的 `name`、`acceleratorType`，并且在 `address`、
`podMonitor`、`serviceMonitor` 中配置且只配置一种发现方式。支持的类型为 `gpu`、
`npu`、`vgpu` 和 `vnpu`。

```yaml
listenAddress: ":9400"
scrapeInterval: 15s
scrapeTimeout: 5s
maxConcurrency: 8

sources:
  # 静态地址，可配置任意多个。
  - name: gpu-node-a
    acceleratorType: gpu
    address: http://10.0.0.11:9400/metrics

  # 按 Pod 标签动态发现。
  - name: vgpu-pods
    acceleratorType: vgpu
    podMonitor:
      namespaceSelector:
        matchNames: [volcano-system]
      selector:
        matchLabels:
          app.kubernetes.io/name: volcano-device-plugin
      port: metrics

  # 按 Service 标签动态发现，并通过 EndpointSlice 展开后端地址。
  - name: npu-services
    acceleratorType: npu
    serviceMonitor:
      namespaceSelector:
        matchNames: [npu-exporter]
      selector:
        matchLabels:
          app.kubernetes.io/component: npu-exporter
      port: http
```

`podMonitor` 和 `serviceMonitor` 都支持：

- `namespaceSelector.matchNames` 指定一个或多个命名空间，或显式设置
  `namespaceSelector.any: true` 发现所有命名空间
- `selector.matchLabels`
- `selector.matchExpressions`，操作符与 Kubernetes LabelSelector 一致
- 命名端口或数字端口 `port`
- `path`，默认 `/metrics`
- `scheme`，默认 `http`

Pod 动态发现通过 Pod IP 直接采集。Service 动态发现通过 EndpointSlice 展开每个
Ready 后端，不访问会随机命中单个实例的 ClusterIP。目标会在每轮采集前重新发现，
因此 Pod 扩缩容和 IP 变化不需要修改配置。

`labels` 用于补充底层指标缺失的固定标签，底层指标自身的标签优先。动态发现会从
Pod 或 EndpointSlice 自动补充 `node`。静态 NPU/vGPU 地址如果没有 `node` 标签，
应在 `labels` 中配置对应的 `node`。完整示例见
[`config.example.yaml`](config.example.yaml)。

使用动态发现时，进程默认读取集群内 ServiceAccount；本地运行可在配置顶层设置
`kubeconfig`。ServiceAccount 至少需要以下只读权限：

```yaml
- apiGroups: [""]
  resources: ["pods", "services"]
  verbs: ["get", "list"]
- apiGroups: ["discovery.k8s.io"]
  resources: ["endpointslices"]
  verbs: ["get", "list"]
```

## 运行

```bash
go run ./cmd/accelerator-exporter --config ./config.example.yaml
```

HTTP 端点：

- `/metrics`：统一算力卡指标和 exporter 自监控指标。
- `/healthz`：进程存活检查。
- `/readyz`：目标发现正常且没有目标，或至少一个目标成功抓取后返回成功。

## 统一标签

每个对外算力卡指标均包含：

- `node`
- `device`
- `model`
- `accelerator_type="gpu|npu|vgpu|vnpu"`
- `vendor`

如果原始指标没有型号，`model` 为 `unknown`。无法确定 `node` 或 `device` 的样本
会被丢弃，并计入 `accelerator_exporter_samples_dropped_total`。

工作负载级指标会继续保留并标准化 `namespace`、`pod`、`container` 和
`partition` 标签。

## 指标口径

目前统一暴露：

- `accelerator_info`
- `accelerator_core_utilization_percentage`
- `accelerator_memory_bandwidth_utilization_percentage`
- `accelerator_memory_total_bytes`
- `accelerator_memory_used_bytes`
- `accelerator_memory_free_bytes`
- `accelerator_memory_utilization_percentage`
- `accelerator_temperature_celsius`
- `accelerator_power_usage_watts`
- `accelerator_power_limit_watts`

显存容量利用率统一由 `used / total * 100` 得出。DCGM 的
`DCGM_FI_DEV_MEM_COPY_UTIL` 映射为显存带宽利用率，不与显存容量利用率混用。

多个目标并发采集。单个目标失败不会影响其他目标；重复目标产生相同指标和标签时，
按目标名称排序后靠前的目标优先。

## Helm 部署

项目内置 Chart：[`deploy/accelerator-exporter`](deploy/accelerator-exporter)。默认配置会
通过 Pod/Service label selector 发现 DCGM、Volcano vGPU 和 Ascend NPU/vNPU
采集端点。

```bash
helm upgrade --install accelerator-exporter \
  ./deploy/accelerator-exporter \
  --namespace accelerator-system \
  --create-namespace
```

需要自动创建供 Prometheus Operator 抓取的 ServiceMonitor 时：

```bash
helm upgrade --install accelerator-exporter \
  ./deploy/accelerator-exporter \
  --namespace accelerator-system \
  --create-namespace \
  --set metrics.serviceMonitor.enabled=true
```

Chart 包含：

- Deployment、ConfigMap 和 metrics Service
- 动态发现所需的最小只读 ServiceAccount、ClusterRole 和 ClusterRoleBinding
- 可选的 Prometheus Operator ServiceMonitor
- `values.schema.json`，用于 Helm 校验和配置页面 Schema 渲染

纯静态 `address` 配置时，Chart 不生成发现 RBAC，并自动设置
`automountServiceAccountToken: false`。
