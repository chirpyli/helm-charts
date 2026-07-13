# 离线构建（推荐）：二进制为 CGO_ENABLED=0 静态链接，可直接运行在 scratch 上，
# 无需下载任何基础镜像，产物镜像仅约 9.5MB，分发到各节点极快。
#   1) cd charts/neon-control-plane/src && go build -o neon-control-plane .
#      （go.mod 无第三方依赖，可完全离线编译）
#   2) docker build -t neon-control-plane:latest charts/neon-control-plane/src/
# 若运行环境需要系统 CA 证书（例如 control-plane 访问集群外 HTTPS 服务），
# 可将基础镜像改为 alpine 并在 Dockerfile 中 apk add ca-certificates。
FROM scratch
COPY neon-control-plane /app/neon-control-plane
ENTRYPOINT ["/app/neon-control-plane"]
