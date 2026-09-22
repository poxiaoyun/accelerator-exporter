# 加速卡指标采集设计

本文档是 accelerator-exporter 的唯一权威设计规范，[README.md](README.md) 是配置与部署指南。

契约本体（指标名、标签、单位、值域、适用形态）定义在 `specification/Monitoring.md`，本文档
不重复它，只描述**本仓库为每个厂商怎么实现它**：从哪个原生指标取、换什么单位、值域约定是什么、
哪些取不到以及为什么。第 7–9 节是本文档的主体。

## 1. 定位与边界

本模块把节点上各厂商 exporter 的原生指标解释成厂商中立的加速卡契约，并组装出设备与 Pod 的
分配关系。

拥有：读取本节点 Pod/Service/EndpointSlice 解析抓取端点；按厂商 rule 归一指标名、标签和单位；
从 kubelet 的 pod-resources 分配事实组装关系层；把本节点 CPU 型号发布到 Node 对象；采集自监控。

不拥有：不持久化库存；不产出归因层；不依赖 Prometheus HTTP API 与 RecordingRule；不做告警判定。

## 2. 契约与分层

| 层 | 承载 | 谁产出 |
| :--- | :--- | :--- |
| 设备层 | 观测目标自身的观测事实 | 本仓库 |
| 关系层 | 观测目标与 Pod 的分配关系 | 本仓库（来自 kubelet 分配事实） |
| 归因层 | 应用与 Instance 视角的聚合 | 消费侧 join 上面两层 |

分层的原因是**基数**：归属若压进设备层，每次 Pod 变化都会在几十个设备级指标名上产生一组新序列。
三层只通过 `(node, device_id)` 关联，只能从设备层向关系层 join，不能反向写回。

## 3. 设备层标签与不变式

| 组 | 标签 | 出现条件 |
| :--- | :--- | :--- |
| 身份 | `node`、`device_id` | 必选 |
| 身份 | `parent_device_id` | `device_mode != physical` |
| 分类 | `device_type`、`device_mode`、`vendor`、`model` | 必选 |
| 位置 | `device_index` | 仅 `device_mode = partition` |
| 静态信息 | `profile`、`driver_version`、`firmware_version`、`serial_number`、`pci_bus_id`、`architecture` | 仅 `accelerator_info` |

- `(node, device_id)` 是观测目标的唯一键；`device_id` 不透明，消费方只比较和保存。
- `device_id` 不得是纯十进制数字，不得用 `unknown` 之类哨兵值。取值优先用厂商的实例标识；
  只能给出序号时用 `<parent_device_id>.<实例序号>`（标识位置而非身份，实例重建后不保证延续）；
  什么都给不出时不产出该目标。
- `accelerator_info` 是观测目标清单的唯一来源，每目标一条、值恒 1。
- 标签值一律小写；`device_type`、`device_mode`、`state`、`severity` 是闭集。
- 同一指标名与同一观测目标只能由一条映射产生。
- **身份不完整不丢样本**：缺的标签不出现，同时计入 `accelerator_exporter_contract_violations_total{reason}`。
  只有三类情况丢弃样本：指标名不在契约里、适用形态与该目标形态不符、`health_state` 的 `state`
  不在闭集内。所有丢弃与违约都按 reason 计数，不静默。

**适用形态**：`accelerator_temperature_celsius`、`accelerator_power_usage_watts`、
`accelerator_power_limit_watts`、`accelerator_throttle_reason_info`、`accelerator_errors_total`
描述物理设备，只出现在 `physical` 目标上。切分与虚拟实例没有独立来源，所以不产出而不是复制父设备
读数 —— 这不是违约，因此不计入违约计数器。该判断在 `Apply` 里按契约声明执行，不在各 rule 里重复。

## 4. 关系层

`accelerator_pod_info` 值恒 1，唯一键是它的全部标签：`node`、`device_id`、`namespace`、`pod`、
`pod_uid`、`container`。

归属的权威来源是**设备分配事实**（kubelet pod-resources），不是厂商指标上的 workload 标签：
厂商标签的可用性取决于采集器配置，换一个采集器就换一套归属。`pod_uid` 不在该接口里，由
Kubernetes API 补齐；`container` 与 `pod_uid` 必选，否则同一 Pod 的多容器或同名 Pod 的不同生命周期
会产生重复关系。

解析不到真实观测目标的分配关系**不产出序列**，也不退化成节点级聚合。分配标识到观测目标的对应
由 rule 声明（`NativeResource`），因为只有厂商知道自己的设备插件按什么标识分配、哪个资源名对应哪种形态。

## 5. 派生与值域约定

- `accelerator_info`、`accelerator_health_diagnostic_info`、`accelerator_throttle_reason_info`
  值恒为 1：rule 决定哪些序列存在，值由契约固定。
- 显存：`total` 是**设备容量**，`free` 是**可分配余量**。驱动保留或当前不可分配的部分两者都不计，
  所以 `used + free` 可以小于 `total`，差额就是那部分。`used` 必须存在；`total` 与 `free` 至少一个，
  缺的那个由另外两个推出，两者都在时不互相覆盖。
- `total` 无法由 `used` 与 `free` 精确算出：厂商给容量时直接用（Ascend 的
  `npu_chip_info_hbm_total_memory`），给不出时由规则选一个合理的派生方式（DCGM 用三件套之和，
  见 §7.2），但同一观测目标上的取值必须稳定，不得随 `used` 波动。
- `accelerator_memory_utilization_percentage` 由 `used / total * 100` 得出 —— 不用厂商的「显存
  利用率」，那个数字各厂商口径不同。虚拟实例的 `total` 是限额而非容量，不得跨实例求和。
- 健康：`accelerator_health_diagnostic_info` 每条诊断一条；`state` 由该目标的诊断扇入，
  `critical` → `fault`、`warning` → `degraded`、其余不升级，最坏获胜，每目标恰好一个为 1。
  **该目标没有任何健康来源时不产出 `state`**，这样「健康」与「没有上报」可区分。
- 派生按观测目标分组，不按标签集分组，否则厂商行里多一个无关枚举就会复制一份派生序列。

## 6. 厂商 Rule 的约定

一个 Rule 是一个采集源的无状态解释，拥有该源的观测目标身份构造、指标映射与**原生单位、值域约定**、
原生扩展资源名到观测目标的映射。厂商逻辑按 rule 分文件，理由与 `cloud/pkg/noderesource` 相同：
一个厂商的原生指标名、单位约定和身份语义只有放在一起才看得出对不对。

- 入口是 `Rule.Apply`（方法而非自由函数）：一次抓取进去，设备层样本与观测目标清单出来。
- 映射有两种形态：一对一的 `Source`；以及把一个观测目标的多个原生读数**相加**的 `Sum`，用于厂商
  只分开公布一个读数的情况（DCGM 不公布显存总量）。`Sum` 列出的原生指标必须全部存在，否则整个
  映射不产出、退回派生 —— 部分和是另一个数，静默暴露它会低估。
- 身份解析、单位倍率、值域转换必须在 rule 里**显式**声明。健康值尤其：DCGM 是 0 表示健康，
  Ascend 是 1 表示健康，写反了不会报错。
- 一个 Rule 可以有多个 `Target`（观测目标类别）。npu-exporter 一个端点暴露两族指标，所以
  `ascend-npu` 承载两个 Target，端点只抓一次；配成两条源会把同一端点抓两遍，重复样本被去重丢弃。

## 7. NVIDIA DCGM（`nvidia-dcgm`）

采集源：`dcgm-exporter` 的 `/metrics`。一个指标族同时覆盖整卡与 MIG 切分：带 `GPU_I_ID`
的样本是切分，不带的是整卡。

### 7.1 身份

| 契约标签 | 来源 |
| :--- | :--- |
| `node` | 样本 `node`/`Hostname`，回落 source 的 `node` |
| `model` | 样本 `model`/`modelName`，回落 source 的 `model`，兜底 `unknown` |
| `device_id` | `UUID`；缺失时退到 `device`（会被报成 `decimal_device_id`） |
| `device_mode` | 有 `GPU_I_ID` → `partition`，否则 `physical` |
| `parent_device_id` | 切分时 = 整卡 `UUID` |
| `device_index` | 切分时 = `GPU_I_ID` |
| `profile` | `GPU_I_PROFILE` |
| `driver_version` | `driver_version` / `DCGM_FI_DRIVER_VERSION` |
| `pci_bus_id` | `pci_bus_id` |
| `device_type` / `vendor` | 规则固定 `gpu` / `nvidia` |

整卡与切分只有一个 Target：`GPU_I_ID` 的有无决定形态，而不是靠配两个源。

### 7.2 指标映射

| 原生指标 | 原生单位 / 值域 | 契约指标 | 倍率 |
| :--- | :--- | :--- | :--- |
| `DCGM_FI_DEV_GPU_UTIL` | 0–100 | `accelerator_core_utilization_percentage` | ×1 |
| `DCGM_FI_DEV_MEM_COPY_UTIL` | 0–100 | `accelerator_memory_bandwidth_utilization_percentage` | ×1 |
| `DCGM_FI_DEV_FB_USED` | MiB | `accelerator_memory_used_bytes` | ×1 MiB |
| `DCGM_FI_DEV_FB_FREE` | MiB | `accelerator_memory_free_bytes` | ×1 MiB |
| `FB_USED` + `FB_FREE` + `FB_RESERVED`（三者求和） | MiB | `accelerator_memory_total_bytes` | ×1 MiB |
| `DCGM_FI_DEV_GPU_TEMP` | ℃ | `accelerator_temperature_celsius` | ×1 |
| `DCGM_FI_DEV_POWER_USAGE` | W | `accelerator_power_usage_watts` | ×1 |
| `DCGM_FI_DEV_ENFORCED_POWER_LIMIT` | W | `accelerator_power_limit_watts` | ×1 |
| `DCGM_EXP_GPU_HEALTH_STATUS` | 0 = 健康 | `accelerator_health_diagnostic_info`（仅非 0 产出） | — |
| `DCGM_FI_DEV_XID_ERRORS` | 0 = 健康 | 同上，`category=xid`、`code=xid_error`、`severity=warning` | — |
| `DCGM_FI_DEV_ROW_REMAP_FAILURE` | 0 = 健康 | 同上，`category=row_remap`、`code=row_remap_failure`、`severity=warning` | — |
| `DCGM_EXP_CLOCK_EVENTS_COUNT` | 采集窗口内次数 | `accelerator_throttle_reason_info`，`reason`←`clock_event`，`Encode` 为「出现过即 1」 | — |

`DCGM_FI_DEV_FB_RESERVED` 是厂商预留的显存，既不在 `FB_USED` 里也不在 `FB_FREE` 里。DCGM 不公布
总量，只用 `USED + FREE` 会少算这一块（H100 上 1638 MiB，约 2%），而契约的 `total` 是设备容量，
所以它必须计入；该族缺失时整个求和映射不产出，退回派生（§5）。

`accelerator_errors_total`（仅 physical，原生必须是单调递增计数器）：

| 原生指标 | `error_type` | 额外标签 |
| :--- | :--- | :--- |
| `DCGM_EXP_XID_ERRORS_TOTAL` | `xid` | `error_code` ← `xid` |
| `DCGM_FI_DEV_ECC_SBE_VOL_TOTAL` | `ecc_volatile_single_bit` | |
| `DCGM_FI_DEV_ECC_DBE_VOL_TOTAL` | `ecc_volatile_double_bit` | |
| `DCGM_FI_DEV_ECC_SBE_AGG_TOTAL` | `ecc_persistent_single_bit` | |
| `DCGM_FI_DEV_ECC_DBE_AGG_TOTAL` | `ecc_persistent_double_bit` | |
| `DCGM_FI_DEV_RETIRED_SBE` | `retired_page_single_bit` | |
| `DCGM_FI_DEV_RETIRED_DBE` | `retired_page_double_bit` | |
| `DCGM_FI_DEV_RETIRED_PENDING` | `retired_page_pending` | |
| `DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS` | `remapped_row_correctable` | |
| `DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS` | `remapped_row_uncorrectable` | |
| `DCGM_FI_DEV_PCIE_REPLAY_COUNTER` | `pcie_replay` | |
| `DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL` | `nvlink_crc_flit` | |
| `DCGM_FI_DEV_NVLINK_CRC_DATA_ERROR_COUNT_TOTAL` | `nvlink_crc_data` | |
| `DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL` | `nvlink_replay` | |
| `DCGM_FI_DEV_NVLINK_RECOVERY_ERROR_COUNT_TOTAL` | `nvlink_recovery` | |

健康诊断的 `category`/`code` 直接取 `health_error_category`/`health_error_code`（小写规范化）；
`severity` 经映射表转换（下表写的是 DCGM 的原生大小写，比较前统一小写，见第 3 节的标签值约定）：

| DCGM 取值 | 契约取值 |
| :--- | :--- |
| `ISOLATE` | `critical` |
| `WARNING` | `warning` |
| `INFO`、`NOT_APPLICABLE`、`NONE`、`UNKNOWN` | `info` |
| 表外取值 | 不产生 `severity` 标签，该诊断被契约校验丢弃 |

### 7.3 不映射的与原因

- `DCGM_FI_DEV_MEM_COPY_UTIL` 是**带宽**利用率，映射到
  `accelerator_memory_bandwidth_utilization_percentage`；容量利用率由 `used / total` 派生，
  两者不混用。
- DCGM 没有 `accelerator_io_utilization_percentage`、`accelerator_memory_oom_events_total`、
  `accelerator_overall_utilization_percentage` 的对应来源（规范里前两个标「仅透传」）。
- 整卡利用率不另设指标：消费方按 `device_mode="physical"` 过滤即有整卡口径。

### 7.4 上游前提与注意点

- `DCGM_EXP_*` 与部分 ECC/NVLink 指标需在 dcgm-exporter 的 collectors 里显式启用，否则该指标
  不存在而不是取到 0。
- `BuildInventory` 从 DCGM 样本抽出整卡的 `node`/`model`/总显存（`FB_USED + FB_FREE` 的窗口最大值），
  供 Volcano vGPU 的切片补齐 `node`/`model`——切片自己的指标族没有这两个标签。
- 关系层：`nvidia.com/gpu` → `UUID`，仅 physical。MIG 的分配标识未知，见第 10 节。

## 8. 华为 Ascend（`ascend-npu`）

采集源：`npu-exporter` 的 `/metrics`。一个端点同时暴露整卡族（`npu_chip_*`）与 vNPU 族
（`vnpu_pod_*`），所以一条 rule 承载两个 Target，端点只抓一次。

### 8.1 整卡（`npu_chip_*`，`physical`）

身份：`device_id` ← `vdie_id`（缺失时退到 `id`/`device`，会被报成 `decimal_device_id`）；
`device_type` = `npu`，`vendor` = `huawei`。该族**不提供任何静态信息**，所以整卡 NPU 的
`accelerator_info` 上没有 `profile`/`driver_version`/`firmware_version`/`pci_bus_id`/`architecture`。

| 原生指标 | 原生单位 / 值域 | 契约指标 | 倍率 |
| :--- | :--- | :--- | :--- |
| `npu_chip_info_utilization` | 0–100 | `accelerator_core_utilization_percentage` | ×1 |
| `npu_chip_info_hbm_bandwidth_utilization` | 0–100 | `accelerator_memory_bandwidth_utilization_percentage` | ×1 |
| `npu_chip_info_overall_utilization` | 0–100 | `accelerator_overall_utilization_percentage` | ×1 |
| `npu_chip_info_hbm_used_memory` | MiB | `accelerator_memory_used_bytes` | ×1 MiB |
| `npu_chip_info_hbm_total_memory` | MiB | `accelerator_memory_total_bytes` | ×1 MiB |
| `npu_chip_info_temperature` | ℃ | `accelerator_temperature_celsius` | ×1 |
| `npu_chip_info_power` | W | `accelerator_power_usage_watts` | ×1 |
| `npu_chip_info_power_limit` | W | `accelerator_power_limit_watts` | ×1 |
| `npu_chip_info_health_status` | 1 = 健康 | `accelerator_health_diagnostic_info`（仅 0 产出），`category=npu`、`code=health_status`、`severity=critical` | — |

`npu_chip_info_overall_utilization` 是厂商自己的综合口径，不可跨厂商比较或聚合。

`accelerator_errors_total`（仅 physical）：

| 原生指标 | `error_type` |
| :--- | :--- |
| `npu_chip_info_hbm_ecc_total_single_bit_error_cnt` | `hbm_ecc_single_bit` |
| `npu_chip_info_hbm_ecc_total_double_bit_error_cnt` | `hbm_ecc_double_bit` |
| `npu_chip_roce_rx_err_pkt_num` | `roce_rx_bad_packet` |
| `npu_chip_roce_tx_err_pkt_num` | `roce_tx_bad_packet` |
| `npu_chip_roce_new_pkt_rty_num` | `roce_packet_retry` |
| `npu_chip_roce_unexpected_ack_num` | `roce_unexpected_ack` |
| `npu_chip_roce_out_of_order_num` | `roce_out_of_order` |
| `npu_chip_roce_verification_err_num` | `roce_verification` |
| `npu_chip_roce_qp_status_err_num` | `roce_qp_status` |
| `npu_chip_mac_tx_bad_pkt_num` | `mac_tx_bad_packet` |
| `npu_chip_mac_rx_bad_pkt_num` | `mac_rx_bad_packet` |
| `npu_chip_mac_tx_bad_oct_num` | `mac_tx_bad_octets` |
| `npu_chip_mac_rx_bad_oct_num` | `mac_rx_bad_octets` |

不映射：

- `npu_chip_info_power_usage` 与 `npu_chip_info_power` 是同一读数的两个名字。同一指标名与同一
  观测目标只能由一条映射产生，所以只留一个；留两个会让结果取决于映射顺序。
- `npu_chip_info_process_info_num` 曾经是 `accelerator_used_count` 的来源，该指标已从契约删除
  （归属改由关系层承载）。

关系层：`huawei.com/Ascend910`、`huawei.com/Ascend910B` → `id`/`device`（芯片序号），仅 physical。

### 8.2 vNPU（`vnpu_pod_*`，`virtual`）

身份：`device_id` ← `v_dev_id`；`device_type` = `npu`，`vendor` = `huawei`。该族**没有**
`parent_device_id` 的来源，因此每个 vNPU 目标都会被报成 `missing_parent_device_id`（第 10 节）。

| 原生指标 | 原生单位 | 契约指标 | 倍率 |
| :--- | :--- | :--- | :--- |
| `vnpu_pod_aicore_utilization` | 0–100 | `accelerator_core_utilization_percentage` | ×1 |
| `vnpu_pod_used_memory` | KiB | `accelerator_memory_used_bytes` | ×1 KiB |
| `vnpu_pod_total_memory` | KiB | `accelerator_memory_total_bytes`（**限额**） | ×1 KiB |

该族没有健康、温度、功耗、错误来源，所以 vNPU 不产出这些指标，也不产出 `accelerator_health_state`。
关系层：vNPU 不是按设备分配的，没有声明 `NativeResource`，见第 10 节。

## 9. Volcano vGPU（`volcano-vgpu`）

采集源：Volcano vGPU monitor 的 `/metrics`，报告软件切分出的 GPU 切片。

身份：`device_id` ← `vdeviceid`，`parent_device_id` ← `deviceuuid`（切出来的物理卡），
`device_mode` 恒为 `virtual`，`device_type` = `gpu`，`vendor` = `nvidia`。该族**不含**
`node`/`model`，因此从 `BuildInventory` 里按 `deviceuuid` 继承 DCGM 报过的整卡 `node`/`model`；
也没有静态信息。

| 原生指标 | 原生单位 | 契约指标 | 倍率 |
| :--- | :--- | :--- | :--- |
| `Device_utilization_desc_of_container` | 0–100 | `accelerator_core_utilization_percentage` | ×1 |
| `vGPU_device_memory_usage_in_bytes` | bytes | `accelerator_memory_used_bytes` | ×1 |
| `vGPU_device_memory_limit_in_bytes` | bytes | `accelerator_memory_total_bytes`（**限额**） | ×1 |

不映射：温度、功耗、降频原因、硬件错误 —— 契约把它们限制在 physical，而切片没有独立读数；
`accelerator_overall_utilization_percentage` 也没有来源。

关系层：切片按虚拟资源分配而不是按设备，`podresources` 里的资源名不是设备标识，因此没有声明
`NativeResource`，见第 10 节。

## 10. 身份前提与已知缺口

以下是**假设**而非已验证事实，代码对应位置都有 TODO：

| 假设 | 若它不成立 |
| :--- | :--- |
| DCGM 在 MIG 序列上用 `UUID` 报父卡、用 `GPU_I_ID` 报序号 | 若 DCGM 有稳定的 MIG 实例 UUID，`device_id` 应改用实例 UUID，去掉 `<父>.<序号>` 组合 |
| DCGM 用 `GPU_I_PROFILE` 报切分规格 | 缺失时只是不产生 `profile`，不臆造规格 |
| DCGM 的健康严重度用第 7.2 节那张表里的取值 | 表外取值会让该诊断被丢弃（可观测，但不是静默） |
| Ascend 的 `vdie_id` 是稳定唯一标识 | 若只有纯十进制序号，该目标被报成 `decimal_device_id` |
| Ascend 的健康值 1 = 健康 | 若相反，健康与故障会互换且不会报错 |
| Ascend 设备插件注册的资源名叫 `huawei.com/Ascend910` / `Ascend910B` | 关系层解析不到，不产出序列 |
| DCGM 每行都带 `UUID` | 退到 `device` 序号，被报成 `decimal_device_id` |

两处缺口是**结构性**的，不是假设问题：

- **vNPU 没有父级来源**：`vnpu_pod_*` 只有 `v_dev_id`，没有它被切出来的物理 NPU 标识，
  `parent_device_id` 为空。补它需要一个本仓库尚未读取的分配源。
- **MIG 与 vNPU 的分配标识未知**：设备插件按什么标识分配它们、对应哪个扩展资源名都不清楚。
  因此这两种形态不产出关系层序列，而不是把父卡的分配关系套到它们身上。

## 11. 契约之外

- `batch size`、模型延迟、推理队列、模型吞吐量属于应用或模型运行时指标，不属于加速卡指标。
- 每个指标的跨厂商可比性由 `specification/Monitoring.md` 的「可比范围」定义。本模块只负责
  按该定义产出取值，自身不做任何聚合，也不为某个厂商的细分口径另立指标。
- 归因层不属于本模块，本模块只提供 join 所需的两层。
- 不为填补空缺伪造数值：缺口用不产出表达，不用 0 或节点均值表达。
