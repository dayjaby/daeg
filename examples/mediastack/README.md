# Example — mediastack

## Build

```bash
docker buildx build -f Daegfile -t mediastack .
```

## Select codecs

```bash
# Without SVT-AV1
docker buildx build -f Daegfile -t mediastack --build-arg CODECS="x264, x265" .

# x264 only
docker buildx build -f Daegfile -t mediastack --build-arg CODECS="x264" .
```

## Test branches in isolation

```bash
docker buildx build -f Daegfile --target medialibs    -t medialibs .
docker buildx build -f Daegfile --target x265         -t x265-test .
docker buildx build -f Daegfile --target svtav1       -t svtav1-test .
```

## Verify the final image

```bash
# Codecs enabled in FFmpeg
docker run --rm mediastack ffmpeg -buildconf | grep -E 'libx264|libx265|libsvtav1|libheif'

# No build tooling in the image
docker run --rm mediastack which cmake  # must fail
docker run --rm mediastack which gcc    # must fail

# Shared library linkage resolves cleanly
docker run --rm mediastack ldd /usr/bin/ffmpeg
```
