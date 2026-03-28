# Example — polyglot web service

## Build

```bash
docker buildx build -f Daegfile -t myapp .
```

## Run

```bash
docker run --rm -p 8000:8000 myapp
```

## Test branches in isolation

```bash
docker buildx build -f Daegfile --target python-app -t myapp-python .
docker buildx build -f Daegfile --target node-app   -t myapp-node .
```
