---
icon: material/docker
---

# Docker

以下示例使用本分支发布的 Rostra 镜像。镜像发布前，可用 `docker build -t rostra .` 在本地构建，并将示例中的镜像名改为 `rostra`。

## :material-console: 命令

```bash
docker run -d \
  -v /etc/rostra:/etc/rostra/ \
  --name=rostra \
  --restart=always \
  ghcr.io/cybervacation/rostra \
  -D /var/lib/rostra \
  -C /etc/rostra/ run
```

## :material-box-shadow: Compose

```yaml
version: "3.8"
services:
  rostra:
    image: ghcr.io/cybervacation/rostra
    container_name: rostra
    restart: always
    volumes:
      - /etc/rostra:/etc/rostra/
    command: -D /var/lib/rostra -C /etc/rostra/ run
```
