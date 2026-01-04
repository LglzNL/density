# DENSITY

**DENSITY** increases compute density on Linux hosts by reducing RAM pressure for many identical/near-identical workloads (VMs/processes) using kernel-supported memory deduplication (KSM) and optional swap compression.

> Goal: **more instances per host** with transparent, reproducible benchmarks.

## Quickstart (2 minutes)

### 1) Install
- From release packages: `.deb` / `.rpm` (see **Releases**)
- Or build from source (WIP)

### 2) Enable
```bash
sudo densityctl enable
densityctl status
