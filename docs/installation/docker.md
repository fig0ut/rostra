---
icon: material/docker
---

# Docker

The examples use Rostra images published by this fork. Until an image is published, build one locally with `docker build -t rostra .` and use `rostra` as the image name.

## :material-console: Command

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
