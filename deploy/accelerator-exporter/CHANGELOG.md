# Changelog

## [2.0.0] - 2026-09-22

### Breaking

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

### Added

- `podResources.hostPath` mounts kubelet's pod-resources directory read-only so
  the exporter can produce `accelerator_pod_info`. Set it to `""` on a cluster
  that does not expose the socket; the device layer is still produced and
  `accelerator_exporter_allocation_source_configured` reports 0.
- NVIDIA DCGM is discovered in both `rune-system` and `gpu-operator`, including GPU
  Operator installations managed by NVIDIA Support (the fix released as chart 1.0.3).
## [1.0.3] - 2026-09-21

### Fixed

- Discover NVIDIA DCGM metrics in both `rune-system` and `gpu-operator`, including GPU Operator installations managed by NVIDIA Support.

## [1.0.2] - 2026-09-07

### Fixed

- Preserve GPU, NPU, vGPU and vNPU workload labels during Prometheus scraping.
- Move scrape-target namespace, Pod and container labels to the exporter prefix so physical-device samples are not incorrectly attributed to the exporter workload.
