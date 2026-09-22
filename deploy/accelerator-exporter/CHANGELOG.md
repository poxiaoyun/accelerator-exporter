# Changelog

## [Unreleased]

### Fixed

- The observation target inventory is deduplicated by `(node, device_id)` instead of by label set.
  `accelerator_info` carries the static information that only the source reading the driver version
  and the PCI position can fill, so one card reported by both DCGM and the vGPU monitor used to leave
  two inventory series behind while its value metrics were collapsed to one. The inventory now holds
  one series per target and keeps the source configured first; the losing inventory is counted in
  `accelerator_exporter_duplicate_samples_total` like any other duplicate. A target whose `device_id`
  did not resolve still falls back to the label set, so incomplete targets are never merged.

## [1.0.4] - 2026-09-22

### Breaking

- `config.sources` is no longer a list of preset names. It is now the list of
  source definitions itself, each with `name`, `rule` and exactly one of
  `podMonitor` and `serviceMonitor`, and `config.sourceDefinitions` is gone.
  Existing releases that only listed presets — `sources: [nvidia-dcgm]` — must be
  replaced, otherwise the chart fails schema validation.
- `config.customSources` is gone. A non-standard installation is now expressed by
  overriding the entry in `config.sources`; `config.sources` itself is hidden and
  every supported source is enabled by default, so a deployment that installs
  nothing unusual still needs no configuration at all.
- `config.sources` no longer accepts the `ascend-vnpu` preset. The `ascend-npu`
  preset now covers the whole npu-exporter endpoint, which exposes both the
  physical NPU and the vNPU families, so the second preset only re-scraped the
  same endpoint. **Existing releases must remove `ascend-vnpu` from
  `config.sources` before upgrading**, otherwise the chart fails schema
  validation.
- A custom source in `config.customSources` now declares `rule` instead of
  `acceleratorType`. The rule decides `device_type`, `device_mode`, `vendor` and
  the identity of every observation target, so a source no longer states them.
- Device metrics no longer carry `namespace`, `pod`, `container`, `instance`,
  `uuid`, `partition` or `accelerator_type`. Attribution moved to the new
  relation-layer metric `accelerator_pod_info`, and identity to `device_id`,
  `parent_device_id`, `device_type`, `device_mode` and `device_index`.
- Several metrics were renamed or removed. See DESIGN.md for the current contract; the removed
  names are `accelerator_total_count`,
  `accelerator_partition_total_count`, `accelerator_used_count`,
  `accelerator_share_percentage`, `accelerator_operations_*`,
  `accelerator_batch_size_count`, `accelerator_latency_seconds` and
  `accelerator_queue_depth_count`.

### Changed

- The order of `config.sources` is now the precedence between sources. When two of
  them cover the same metric on the same observation target, the source listed
  first wins and the other reading is counted in
  `accelerator_exporter_duplicate_samples_total`. Discovery used to order targets
  by name, so which reading survived depended on what the sources happened to be
  called. Put the more authoritative source earlier; the chart already lists
  `nvidia-dcgm` before `volcano-vgpu`.

### Added

- `volcano-vgpu` also reports the physical cards its slices run on, through
  `HostCoreUtilization` and `HostGPUMemoryUsage`. On a node that runs no DCGM
  this is the only source of the whole-card readings a consumer reaches through
  `parent_device_id`. It carries no whole-card memory total, because the monitor
  does not report one.
- `podResources.hostPath` mounts kubelet's pod-resources directory read-only so
  the exporter can produce `accelerator_pod_info`. Set it to `""` on a cluster
  that does not expose the socket; `accelerator_exporter_allocation_source_configured`
  then reports 0, and every source whose identity comes from the samples is still
  produced. The exception is `volcano-vgpu`, whose slice identifier exists only in
  the allocation fact.
- NVIDIA DCGM is discovered in both `rune-system` and `gpu-operator`, including GPU
  Operator installations managed by NVIDIA Support (the fix released as chart 1.0.3).
## [1.0.3] - 2026-09-21

### Fixed

- Discover NVIDIA DCGM metrics in both `rune-system` and `gpu-operator`, including GPU Operator installations managed by NVIDIA Support.

## [1.0.2] - 2026-09-07

### Fixed

- Preserve GPU, NPU, vGPU and vNPU workload labels during Prometheus scraping.
- Move scrape-target namespace, Pod and container labels to the exporter prefix so physical-device samples are not incorrectly attributed to the exporter workload.
