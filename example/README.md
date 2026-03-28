# Example — polyglot data science image

This Daegfile builds a production image containing CUDA, OpenCV, FFmpeg, a Python FastAPI server, and a React frontend — all in one image, built efficiently.

## The graph

```
ubuntu:24.04
├── cuda-tools   (nvidia-cuda-toolkit)
├── opencv       (libopencv-dev)
└── ffmpeg       (ffmpeg + libavcodec)
      ↓
  MERGE → system-deps   ← ldconfig repair, apt cleanup once
  ├── python-app         (torch, numpy, fastapi)
  └── node-app           (react, vite)
        ↓
    MERGE → final        ← no conflicts, assertion verified at build time
```

## Build it

```bash
# Standard build
docker buildx build -f Daegfile .

# Build only the system layer (for testing or publishing as a base)
docker buildx build --target system-deps -f Daegfile .

# Override the base image
docker buildx build --build-arg BASE=nvidia/cuda:12.3-base -f Daegfile .
```

## What this demonstrates

**Without Daeg**, installing CUDA + OpenCV + FFmpeg + Python + Node in one image requires a single long `RUN` chain where every layer depends on every previous one. A change to the Node version rebuilds everything including the 4-minute `pip install torch`.

**With Daeg**, the five installation branches run in parallel. The caches for `cuda-tools`, `opencv`, and `ffmpeg` are independent — changing one does not invalidate the others. `ldconfig` and the apt cleanup run once at the merge point rather than three times across branches.

The second merge has no `RESOLVE` lines, which is a machine-verified claim: at build time, BuildKit confirms that the Python and Node installations do not write to any of the same paths.
