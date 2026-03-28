# Example — embedded Linux gateway

## Build

```bash
docker buildx build -f Daegfile -t gateway .
```

## Build component images separately

Each component image is exactly two layers: a stable `rootfs` base + one component layer.
Push them independently; the orchestrator pulls only what changed.

```bash
docker buildx build -f Daegfile --target kernel-image    -t registry/gw-kernel:v1 .
docker buildx build -f Daegfile --target rootfs-assembled -t registry/gw-rootfs:v1 .
```

## OTA update — enable I2C

`config-i2c` is not in `KERNEL_CONFIGS` by default. To enable I2C in a kernel OTA update:

```bash
# v1: I2C disabled (defconfig default)
docker buildx build --target kernel-image -t registry/gw-kernel:v1 .

# v2: I2C enabled — rebuild kernel-image only, rootfs-assembled unchanged
docker buildx build --target kernel-image -t registry/gw-kernel:v2 \
  --build-arg KERNEL_CONFIGS="config-net, config-security, config-drivers, config-i2c" .
```

The orchestrator pulls `gw-kernel:v2` (one new layer) and leaves `gw-rootfs` untouched.

## Select kernel version and config fragments

```bash
# Different kernel version
docker buildx build -f Daegfile -t gateway \
  --build-arg KERNEL_VERSION=6.6.45 .

# Without driver support (e.g. for a VM target)
docker buildx build -f Daegfile -t gateway \
  --build-arg KERNEL_CONFIGS="config-net, config-security" .

# Minimal network-only rootfs
docker buildx build -f Daegfile -t gateway \
  --build-arg ROOTFS_COMPONENTS="rootfs-net, rootfs-init" .
```

## Test stages in isolation

```bash
# Inspect the merged kernel config
docker buildx build -f Daegfile --target kernel-configured -t kconfig .
docker run --rm kconfig cat /kernel/.config | grep CONFIG_NF_TABLES

# Verify the kernel build
docker buildx build -f Daegfile --target kernel-image -t kimage .
docker run --rm kimage ls /boot/bzImage /rootfs-modules/lib/modules

# Inspect the assembled rootfs
docker buildx build -f Daegfile --target rootfs-assembled -t rootfs .
docker run --rm rootfs which swupdate
docker run --rm rootfs sshd -T 2>/dev/null | head

# Verify no build tooling leaked into the final image
docker run --rm gateway which cmake  # must fail
docker run --rm gateway which gcc    # must fail
```

## Adding a kernel feature group

Create a new stage that writes a fragment to `/kernel/config.d/`:

```dockerfile
FROM kernel-src AS config-cgroups
RUN mkdir -p /kernel/config.d && cat > /kernel/config.d/40-cgroups.config <<'EOF'
CONFIG_CGROUPS=y
CONFIG_CGROUP_SCHED=y
CONFIG_MEMCG=y
EOF
```

Then include it in the build:

```bash
docker buildx build -f Daegfile -t gateway \
  --build-arg KERNEL_CONFIGS="config-net, config-security, config-drivers, config-cgroups" .
```

No other changes required. Fragments are merged in filename order by
`merge_config.sh`, so prefix numbering controls precedence when options overlap.
