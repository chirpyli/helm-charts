# 基础镜像使用 alpine：自带 sh 与 apk 包管理器，方便开发调试时
# 进入容器（docker exec -it <容器> sh）排查问题，也可按需 apk add 安装
# curl、net-tools 等工具；同时便于安装系统 CA 证书以支持访问集群外 HTTPS 服务。
# 二进制为 CGO_ENABLED=0 静态链接，可直接运行，无需额外 C 库依赖。
#   1) cd charts/neon-control-plane/src && go build -o neon-control-plane .
#      （go.mod 无第三方依赖，可完全离线编译）
#   2) docker build -t neon-control-plane:latest charts/neon-control-plane/src/
FROM alpine:3.20
# 安装系统 CA 证书：control-plane 访问集群外 HTTPS 服务（如外部 API）时需要。
RUN apk add --no-cache ca-certificates
COPY neon-control-plane /app/neon-control-plane
ENTRYPOINT ["/app/neon-control-plane"]
