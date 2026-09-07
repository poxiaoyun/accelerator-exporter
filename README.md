# accelerator-exporter

`accelerator-exporter` 直接抓取 GPU、NPU、vGPU、vNPU exporter 的 Prometheus
`/metrics` 地址，在进程内完成指标名、标签和单位转换，并通过自身的 `/metrics`
统一暴露 `accelerator_*` 指标。

服务不依赖 Prometheus HTTP API，也不读取 RecordingRule。

Helm Chart 中的 DaemonSet 还会识别当前节点的 CPU 型号，并通过 Kubernetes API 发布为
Node 标签 `feature.node.cloud.xiaoshiai.cn/cpu-model.name`，供 Cloud 的节点资源发现直接使用。

## 配置

每个数据源需要唯一的 `name`、`acceleratorType`，并且在 `podMonitor`、
`serviceMonitor` 中配置且只配置一种发现方式。服务按节点运行，不支持静态地址。
支持的类型为 `gpu`、`npu`、`vgpu` 和 `vnpu`。

```yaml
listenAddress: ":9400"
scrapeInterval: 15s
scrapeTimeout: 5s
maxConcurrency: 8

sources:
  # 按 Service 标签动态发现 DCGM，并只采集本节点 Endpoint。
  - name: dcgm-services
    acceleratorType: gpu
    serviceMonitor:
      namespaceSelector:
        matchNames: [rune-system]
      selector:
        matchLabels:
          app: nvidia-dcgm-exporter
      port: gpu-metrics

  # 按 Pod 标签动态发现。
  - name: vgpu-pods
    acceleratorType: vgpu
    podMonitor:
      namespaceSelector:
        matchNames: [kube-system]
      selector:
        matchLabels:
          app.kubernetes.io/name: volcano-device-plugin
          app.kubernetes.io/component: volcano-vgpu-device-plugin
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

Pod 动态发现使用 `spec.nodeName` 字段选择器，只通过 Pod IP 采集当前节点上的匹配
Pod。Service 动态发现通过 EndpointSlice 展开后端，只保留 `nodeName` 等于当前节点
的 Ready Endpoint，不访问会随机命中其他节点的 ClusterIP。没有 `nodeName` 的
Endpoint 会被忽略。目标会在每轮采集前重新发现，因此 Pod 扩缩容和 IP 变化不需要
修改配置。

`labels` 用于补充底层指标缺失的固定标签，底层指标自身的标签优先。动态发现会使用
当前 DaemonSet Pod 所在节点补充 `node`。完整示例见
[`config.example.yaml`](config.example.yaml)。

进程默认读取集群内 ServiceAccount；本地运行可在配置顶层设置 `kubeconfig`。
ServiceAccount 需要以下指标发现和 CPU 型号上报权限：

```yaml
- apiGroups: [""]
  resources: ["pods", "services"]
  verbs: ["get", "list"]
- apiGroups: ["discovery.k8s.io"]
  resources: ["endpointslices"]
  verbs: ["get", "list"]
- apiGroups: [""]
  resources: ["nodes"]
  verbs: ["get", "patch"]
```

## 运行

```bash
go run ./cmd/accelerator-exporter \
  --config ./config.example.yaml \
  --node-name k8s-node1
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

统一指标契约包含：

- 设备与分配：`accelerator_info`、`accelerator_total_count`、
  `accelerator_used_count`、`accelerator_share_percentage`、
  `accelerator_partition_total_count`
- 利用率：`accelerator_core_utilization_percentage`、
  `accelerator_memory_bandwidth_utilization_percentage`、
  `accelerator_io_utilization_percentage`、
  `accelerator_overall_utilization_percentage`
- 显存：`accelerator_memory_total_bytes`、`accelerator_memory_used_bytes`、
  `accelerator_memory_free_bytes`、`accelerator_memory_utilization_percentage`、
  `accelerator_memory_oom_total_count`
- 健康与功耗：`accelerator_health_status`、
  `accelerator_temperature_celsius`、`accelerator_power_usage_watts`、
  `accelerator_power_limit_watts`、`accelerator_throttle_reason_info`、
  `accelerator_error_total_count`
- AI workload：`accelerator_operations_total_count`、
  `accelerator_operations_per_second`、`accelerator_batch_size_count`、
  `accelerator_latency_seconds`、`accelerator_queue_depth_count`

所有统一名称都支持 canonical 透传：自定义采集源已经暴露上述 `accelerator_*`
指标时，exporter 会校验并补全统一身份标签后继续暴露。

内置厂商映射和严格派生目前可以直接生产：

- `accelerator_info` 和 `accelerator_total_count`：从任意有效设备样本派生，每个设备或
  分区的值为 1
- `accelerator_used_count`：工作负载标签存在时值为 1；物理 NPU 使用
  `npu_chip_info_process_info_num > 0` 判断
- `accelerator_partition_total_count`：每个带 `partition` 标签的设备样本值为 1
- `accelerator_share_percentage`：vGPU memory limit 除以物理 GPU 总显存，带
  `share_basis="memory"` 标签
- core、显存、温度和功耗指标：使用下方厂商映射；NPU 的显存带宽和整体利用率分别
  使用 `npu_chip_info_hbm_bandwidth_utilization` 和
  `npu_chip_info_overall_utilization`
- `accelerator_health_status`：1 表示健康，0 表示不健康；GPU 优先使用
  `DCGM_EXP_GPU_HEALTH_STATUS` 的逐项健康检查，未启用时仍可根据
  `DCGM_FI_DEV_XID_ERRORS == 0` 和 `DCGM_FI_DEV_ROW_REMAP_FAILURE == 0`
  判断，NPU 使用 `npu_chip_info_health_status`
- `accelerator_throttle_reason_info`：使用
  `DCGM_EXP_CLOCK_EVENTS_COUNT`，按 `reason` 输出最近采集窗口内出现过的降频原因
- `accelerator_error_total_count`：使用 NVIDIA XID/ECC/退役页/重映射行/PCIe/NVLink
  和 Ascend HBM ECC/RoCE/MAC 的累计错误指标，按 `error_type` 区分

`io_utilization`、`memory_oom`、operations、batch、latency 和 queue depth 没有跨厂商
可靠的设备级派生方式。只有底层采集源提供同名 canonical 指标时才会输出，不会根据
其他利用率伪造数据。`overall_utilization` 当前可由 Ascend NPU 原生指标产生，其他类型
仍只支持 canonical 透传。

NVIDIA 的 `DCGM_EXP_GPU_HEALTH_STATUS`、`DCGM_EXP_XID_ERRORS_TOTAL`、
`DCGM_EXP_CLOCK_EVENTS_COUNT` 以及部分 ECC/NVLink 指标可能未在 dcgm-exporter
默认 collectors 中启用，需要在 DCGM collectors 配置中显式打开后才会产生对应
统一指标。

显存容量利用率统一由 `used / total * 100` 得出。DCGM 的
`DCGM_FI_DEV_MEM_COPY_UTIL` 映射为显存带宽利用率，不与显存容量利用率混用。

多个目标并发采集。单个目标失败不会影响其他目标；重复目标产生相同指标和标签时，
按目标名称排序后靠前的目标优先。

## Helm 部署

项目内置 Chart：[`deploy/accelerator-exporter`](deploy/accelerator-exporter)。Chart
始终使用 DaemonSet，每个 Pod 通过 Downward API 获取所在节点名，并通过 Pod/Service
label selector 只发现本节点的 DCGM、Volcano vGPU 和 Ascend NPU/vNPU 采集端点。

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

- DaemonSet、ConfigMap 和 metrics Service
- 主进程内的 CPU 型号识别和当前 Node 元数据上报，无需 NFD 或 hostPath
- 指标动态发现及 Node `get/patch` 所需的 ServiceAccount、ClusterRole 和 ClusterRoleBinding
- 可选的 Prometheus Operator ServiceMonitor
- `values.schema.json`，用于 Helm 校验和配置页面 Schema 渲染

metrics Service 会选中所有节点上的 exporter Pod。Prometheus Operator 会分别抓取
这些 Pod；直接访问 Service `/metrics` 只会命中其中一个节点，不提供集群聚合视图。

ServiceMonitor 保留指标自身的工作负载标签，并把采集目标的 `namespace`、`pod`、
`container` 重命名为 `exporter_namespace`、`exporter_pod`、`exporter_container`。
这些规则在自定义目标重标记之后执行，避免无工作负载标签的整卡指标被误归到 exporter
自身。自定义 metricRelabelings 不应重新覆盖工作负载身份标签。

独占 NVIDIA GPU 的实例指标来自 DCGM。DCGM 必须启用 Kubernetes 模式并能访问
kubelet 的 `/var/lib/kubelet/pod-resources`，且设备分配信息必须能匹配物理 GPU UUID，
才能给指标附加实际的 `namespace`、`pod`、`container`。没有这些标签时只能展示
节点/整卡指标。vGPU 的实例用量来自 Volcano vGPU monitor，不能将 DCGM 的整卡
用量作为某个共享实例的用量。

## CPU 型号上报

每个 DaemonSet Pod 的主进程通过 CPUID 读取所在节点的 CPU BrandName，将例如：

```text
Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz
```

转换成合法的 Kubernetes label value：

```text
Intel-Xeon-Gold-6330-CPU
```

进程仅查询 Downward API 注入的 `NODE_NAME` 对应 Node，并使用 merge patch 更新：

```text
label:      feature.node.cloud.xiaoshiai.cn/cpu-model.name=Intel-Xeon-Gold-6330-CPU
annotation: feature.node.cloud.xiaoshiai.cn/cpu-model.raw-name=Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz
```

型号来源的优先顺序与 PAI 的 `pkg/node` 实现一致：CPUID BrandName、节点预设标签、
CPUID VendorString，最后回退到 `Generic`。可以在特殊硬件上预先设置以下标签覆盖空的
BrandName：

```text
feature.node.cloud.xiaoshiai.cn/cpu-model.predefined-name=<model>
```

进程启动后立即上报，此后每 30 分钟校验一次；失败时每分钟重试。上报不影响指标服务
启动或 readiness。主容器保持非 root 运行，也不挂载宿主机目录。
