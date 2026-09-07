# Changelog

## [1.0.2] - 2026-09-07

### Fixed

- Preserve GPU, NPU, vGPU and vNPU workload labels during Prometheus scraping.
- Move scrape-target namespace, Pod and container labels to the exporter prefix so physical-device samples are not incorrectly attributed to the exporter workload.
