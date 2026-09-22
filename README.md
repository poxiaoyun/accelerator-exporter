# accelerator-exporter

`accelerator-exporter` 直接抓取 GPU、NPU、vGPU、vNPU exporter 的 Prometheus
`/metrics` 地址，在进程内完成指标名、标签和单位转换，并通过自身的 `/metrics`
统一暴露 `accelerator_*` 指标。

服务不依赖 Prometheus HTTP API，也不读取 RecordingRule。

Helm Chart 中的 DaemonSet 还会识别当前节点的 CPU 型号，并通过 Kubernetes API 发布为
Node 标签 `feature.node.cloud.xiaoshiai.cn/cpu-model.name`，供 Cloud 的节点资源发现直接使用。

- 设计规则（三层模型、标签契约、口径与派生、厂商映射、已知缺口）见 [DESIGN.md](DESIGN.md)。
- 规范本体见 `specification/Monitoring.md`；本文档只讲怎么配、怎么跑。

## 指标

产出的指标分两层：

- **设备层**：`accelerator_info` 与各观测量，只携带观测目标自身的身份与分类标签。
  观测目标清单的唯一来源是 `accelerator_info`。
- **关系层**：`accelerator_pod_info`，一条序列表示一个观测目标与一个 Pod 的分配关系。

归因层（`<domain>_accelerator_workload_*`）由消费侧 join 上面两层得到，本仓库不产出。
各 rule 用到的原生指标与契约指标的对应关系见 [DESIGN.md](DESIGN.md) 第 7 节。

## 配置

以下是一个配置文件的内容。用内置 Chart 部署时不必写这些：默认已经开启全部受支持的指标源
（见 [Helm 部署](#helm-部署)），只有非标准安装才需要覆盖。

每个数据源需要唯一的 `name` 和 `rule`，并且在 `podMonitor`、`serviceMonitor`
中配置且只配置一种发现方式。服务按节点运行，不支持静态地址。

`name` 标识**这一份配置**，取采集源的组件名 —— 与选择器里的 `app`/`component` 标签一致，
例如 `nvidia-dcgm-exporter`、`volcano-vgpu-device-plugin`、`npu-exporter`。它会成为每个抓取
目标名的前缀，也是 `accelerator_exporter_*{source=...}` 的取值，所以同一个 rule 配两份源时靠
它区分。

`rule` 指向内置的采集规则，它决定 `device_type`、`vendor` 以及每个观测目标的身份，
所以数据源不自己声明这些：

| rule | 采集源 | 能产出的观测目标 |
| :--- | :--- | :--- |
| `nvidia-dcgm` | NVIDIA DCGM exporter | `gpu` 的整卡（`physical`）与 MIG 切分（`partition`） |
| `volcano-vgpu` | Volcano vGPU monitor | `gpu` 的 vGPU 实例（`virtual`），以及这些实例所在的整卡（`physical`） |
| `ascend-npu` | 华为 npu-exporter | `npu` 的整卡（`physical`）与 vNPU（`virtual`） |

`ascend-npu` 一条规则覆盖 npu-exporter 的整卡与 vNPU 两族指标，所以同一个端点只配一次；
配成两条只会把同一端点抓两遍。

`sources` 是**数组，顺序就是优先级**。两个源都可能覆盖同一观测目标上的同一个指标时
（例如整卡利用率与整卡显存用量，DCGM 从驱动读、vGPU monitor 从自己的采样器读），
**配置在前的源优先**，后面那个按重复丢弃并计入 `accelerator_exporter_duplicate_samples_total`。
胜负不取决于源名，也不取决于哪次抓取先完成。Chart 的默认顺序已按此排好：DCGM 在前。

观测量的判重键是标签集；`accelerator_info` 例外 —— 只有知道驱动版本和 PCI 位置的源才填得出静态
信息，两个源的标签集因此不同，所以它按观测目标键 `(node, device_id)` 判重：每个观测目标只留一条，
同样保留配置在前的源。同一张卡不会在清单里出现两次。

只配 `volcano-vgpu` 时它自己提供整卡读数，但没有整卡显存总量 —— 该 monitor 不报。

```yaml
listenAddress: ":9400"
scrapeInterval: 15s
scrapeTimeout: 5s
maxConcurrency: 8

sources:
  # 按 Service 标签动态发现 DCGM，并只采集本节点 Endpoint。
  - name: nvidia-dcgm-exporter
    rule: nvidia-dcgm
    serviceMonitor:
      namespaceSelector:
        matchNames: [rune-system, gpu-operator]
      selector:
        matchLabels:
          app: nvidia-dcgm-exporter
      port: gpu-metrics

  # 按 Pod 标签动态发现。
  - name: volcano-vgpu-device-plugin
    rule: volcano-vgpu
    podMonitor:
      namespaceSelector:
        matchNames: [kube-system]
      selector:
        matchLabels:
          app.kubernetes.io/name: volcano-device-plugin
          app.kubernetes.io/component: volcano-vgpu-device-plugin
      port: metrics

  # 按 Service 标签动态发现，并通过 EndpointSlice 展开后端地址。
  - name: npu-exporter
    rule: ascend-npu
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

`labels` 只接受 `model`，用于补充厂商指标本身缺失的型号；`node` 永远是本进程所在节点。
身份与分类标签由规则从样本解析，配置里不允许指定，其他标签也不会进入公共指标。
完整示例见 [`config.example.yaml`](config.example.yaml)。

### 设备分配事实

`accelerator_pod_info` 的来源是 kubelet 的 pod-resources socket，默认
`/var/lib/kubelet/pod-resources/kubelet.sock`，可在配置里用 `podResourcesSocket` 改。
Pod 的 `pod_uid` 不在该接口里，由 Kubernetes API 补齐。

socket 不存在或不可读时，导出器照常产出**整个设备层**，只是没有关系层；这一点由
`accelerator_exporter_allocation_source_configured`（本节点有没有接上分配源）和
`accelerator_exporter_allocation_source_up`（最近一次读是否成功）区分，并在每轮重试，
所以 kubelet 后起来不需要重启容器。

例外是 `volcano-vgpu`：切片的编号只存在于分配事实里，读不到时该源没有 `device_id`，
设备层的这些序列会作为身份缺口上报（`missing_identity`），不会用猜出来的编号顶替。
原因见 `DESIGN.md` 第 9.1 节。

进程默认读取集群内 ServiceAccount；本地运行可在配置顶层设置 `kubeconfig`。
ServiceAccount 需要以下指标发现、Pod 元数据和 CPU 型号上报权限（`pods` 的 `get`
同时用于发现本节点 Pod 和补齐 `pod_uid`）：

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
go run ./cmd/accelerator-exporter --config ./config.example.yaml
```

命令只有一个 `--config`：节点名、pod-resources socket 路径等都在配置文件里，节点名留空时回落到
`NODE_NAME` 环境变量（DaemonSet 通过 Downward API 注入）。

HTTP 端点：

- `/metrics`：统一算力卡指标和 exporter 自监控指标。
- `/healthz`：进程存活检查。
- `/readyz`：目标发现正常且没有目标，或至少一个目标成功抓取后返回成功。

exporter 自身的自监控指标：

| 指标 | 含义 |
| :--- | :--- |
| `accelerator_exporter_ready` | 至少有一个数据源抓取成功 |
| `accelerator_exporter_last_refresh_success` | 最近一轮所有数据源都成功 |
| `accelerator_exporter_targets_discovered` | 本节点发现到的抓取目标数 |
| `accelerator_exporter_series_exposed` | 当前暴露的序列数 |
| `accelerator_exporter_target_up{rule,source}` | 某个数据源最近一次抓取是否成功 |
| `accelerator_exporter_target_scrape_duration_seconds{rule,source}` | 最近一次抓取耗时 |
| `accelerator_exporter_target_samples_read{rule,source}` | 最近一次读到的原始样本数 |
| `accelerator_exporter_scrape_errors_total{rule,source}` | 累计抓取错误 |
| `accelerator_exporter_discovery_errors_total` | 累计目标发现错误 |
| `accelerator_exporter_duplicate_samples_total` | 累计被丢弃的重复归一序列 |
| `accelerator_exporter_contract_violations_total{reason}` | 累计契约违约样本，按原因区分 |
| `accelerator_exporter_allocation_source_configured` | 本节点是否接上了设备分配源 |
| `accelerator_exporter_allocation_source_up` | 最近一次读分配事实是否成功 |
| `accelerator_exporter_allocation_errors_total` | 累计分配事实读取错误 |

## Helm 部署

项目内置 Chart：[`deploy/accelerator-exporter`](deploy/accelerator-exporter)。Chart
始终使用 DaemonSet，每个 Pod 通过 Downward API 获取所在节点名，并通过 Pod/Service
label selector 只发现本节点的采集端点。

**全部受支持的指标源默认已开启，不需要选择。** 组件没装的源发现不到端点，等于没配，不会
产生任何副作用。默认发现位置见 [`values.yaml`](deploy/accelerator-exporter/values.yaml)：

- `nvidia-dcgm-exporter`：`rune-system` 与 `gpu-operator` 中 `app: nvidia-dcgm-exporter`
  的 Service，兼容独立部署和由 NVIDIA Support 管理的 GPU Operator
- `volcano-vgpu-device-plugin`：`kube-system` 中 `app.kubernetes.io/name: volcano-device-plugin`
  且 `app.kubernetes.io/component: volcano-vgpu-device-plugin` 的 Pod
- `npu-exporter`：`npu-exporter` 命名空间中 `app: npu-exporter` 的 Service

组件装在别处时用 `config.sources` 整体覆盖（数组，顺序即优先级）。该字段默认隐藏：不需要配置，只在非标准安装时才用。

平台把镜像放在自有仓库时设置 `global.imageRegistry`：Chart 用它替换 `image.repository` 的仓库部分，
保留 `xiaoshiai/accelerator-exporter` 这段路径。下游不需要为此打补丁。

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
- 主进程内的 CPU 型号识别和当前 Node 元数据上报，无需 NFD
- 只读挂载 kubelet 的 pod-resources 目录，用于生成 `accelerator_pod_info`
  （`podResources.hostPath` 留空则不挂载，此时没有关系层，`volcano-vgpu` 也会失去切片编号）
- 指标动态发现、Pod 元数据和 Node `get/patch` 所需的 ServiceAccount、ClusterRole 和 ClusterRoleBinding
- 可选的 Prometheus Operator ServiceMonitor
- `values.schema.json`，用于 Helm 校验和配置页面 Schema 渲染

metrics Service 会选中所有节点上的 exporter Pod。Prometheus Operator 会分别抓取
这些 Pod；直接访问 Service `/metrics` 只会命中其中一个节点，不提供集群聚合视图。

ServiceMonitor 把**采集目标**（即 exporter 自己那个 Pod）的 `namespace`、`pod`、
`container` 重命名为 `exporter_namespace`、`exporter_pod`、`exporter_container`。这样
`accelerator_pod_info` 里表示**设备归属**的 `namespace`/`pod` 就不会和 exporter 自身的
标签混淆。自定义 relabelings 不应重新覆盖 `namespace`/`pod`/`container`。

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
启动或 readiness。主容器保持非 root 运行；除只读挂载 kubelet 的 pod-resources 目录
（可用 `podResources.hostPath: ""` 关掉）之外不挂载宿主机目录。
