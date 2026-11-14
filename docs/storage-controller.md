# storage controller（存储控制器）

> storage controller 位于control plane(管理API客户端)与 pageserver 之间，负责把租户（tenant）映射到 pageserver 的租户分片（shard）上。它还管理 generation（代次）、高可用（通过次要位置 secondary location）以及租户在节点间的实时迁移，其内部采用"意图状态（intent）+ 协调（reconcile）"的循环模式，使外部真实状态与意图保持一致。

## 职责

- 管理pageserver集群：注册、心跳、故障检测、上线/下线/排水drain/填充fill/删除
- 管理租户分片: 创建、调度、迁移、分片分裂、删除
- generation number唯一签发者： 防止S3写冲突。pageserver会将layer文件上传到S3，如果两个pageserver同时认为自己是某租户分片的合法writer，会造成写冲突导致互相删除对方的文件进而造成数据丢失。tenant_shards.generation_pageserver列记录当前generation签发给了哪个pageserver，pageserver重启时调用`re-attach`。需要保证任何时刻S3上最多只有一个pageserver认为自己持有某分片的当前generation。
- 管理safekeeper: 时间线创建、成员变更、调度策略
- 向控制面/Compute通知attachment变化（compute hook）

## 工作原理

存储控制器storage controller，是计算层与存储层之间的沟通桥梁。因为计算节点与存储节点分离，存储层中的safekeeper以及pageserver都支持扩缩容，当pageserver的节点发生扩容或者节点发生故障，计算层是无法直接感知到存储层的变化的，此时就需要storage controller感知存储层safekeeper以及pageserver节点发生的变化，告知计算层。
为了计算层与存储层彻底解耦，需要storage controller去调度租户以及分片，由storage controller去决定租户分片被调度到哪个pageserver上。所以 ，在启动节点，pageserver启动时都会向storage controller进行注册，safekeeper启动时也会向safekeeper进行注册。

## 用法

比较重要的配置项：
-  `--control-plane-url <CONTROL_PLANE_URL>` 控制面地址，storage controller感知到存储层节点变化后，并不直接通知计算层，而是通过控制面进行通知。由控制面去通知计算节点。
- `--database-url <DATABASE_URL>` 数据库地址，存储元信息。某种程度上也可以理解为分布式存储的元信息。

```sh
storage_controller --help
Usage: storage_controller [OPTIONS] <--listen <LISTEN>|--listen-https <LISTEN_HTTPS>>

选项（Options）:
  -l, --listen <LISTEN>
          HTTP 监听的主机和端口，例如 `127.0.0.1:1234`。["listen", "listen_https"] 中至少要指定其中一项
      --listen-https <LISTEN_HTTPS>
          HTTPS 监听的主机和端口，例如 `127.0.0.1:1234`。["listen", "listen_https"] 中至少要指定其中一项
      --public-key <PUBLIC_KEY>
          用于客户端 JWT 认证的公钥
      --jwt-token <JWT_TOKEN>
          用于向本服务所管理的 pageserver 进行服务间认证的令牌
      --safekeeper-jwt-token <SAFEKEEPER_JWT_TOKEN>
          用于向本服务所管理的 safekeeper 进行服务间认证的令牌
      --control-plane-jwt-token <CONTROL_PLANE_JWT_TOKEN>
          在调用计算通知（compute notification）端点时，用于向控制面（control plane）进行服务间认证的令牌
      --peer-jwt-token <PEER_JWT_TOKEN>
          用于 storage controller 对等节点（peer，即其它 storage controller 实例）之间认证的 JWT 令牌
      --control-plane-url <CONTROL_PLANE_URL>
          控制面存储（storage）API 的前缀 URL
      --database-url <DATABASE_URL>
          连接 postgres 的 URL，例如 postgresql://localhost:1234/storage_controller
      --dev
          启用开发模式的标志，允许在无认证的情况下运行
      --max-offline-interval <MAX_OFFLINE_INTERVAL>
          将无响应的 pageserver 标记为离线（offline）之前的宽限期
      --max-warming-up-interval <MAX_WARMING_UP_INTERVAL>
          在 pageserver 重启前后，将无响应的 pageserver 标记为离线之前更宽容的宽限期
      --split-threshold <SPLIT_THRESHOLD>
          自动拆分分片（shard）的大小阈值（默认禁用）
      --max-split-shards <MAX_SPLIT_SHARDS>
          自动拆分过程中允许的最大分片数量。0 表示禁用自动拆分。默认值为 16，作为防止意外产生过多分片的安全措施 [默认值: 16]
      --initial-split-threshold <INITIAL_SPLIT_THRESHOLD>
          针对尚未分片（unsharded）的租户进行初始分片拆分的大小阈值。0 表示禁用初始拆分
      --initial-split-shards <INITIAL_SPLIT_SHARDS>
          初始拆分的目标分片数量。0 或 1 表示禁用初始拆分。默认值为 2 [默认值: 2]
      --reconciler-concurrency <RECONCILER_CONCURRENCY>
          可并行运行的普通优先级协调器（reconciler）的最大数量
      --priority-reconciler-concurrency <PRIORITY_RECONCILER_CONCURRENCY>
          可并行运行的高优先级协调器的最大数量
      --safekeeper-reconciler-concurrency <SAFEKEEPER_RECONCILER_CONCURRENCY>
          可并行运行的 safekeeper 协调任务的最大数量（按每个 safekeeper 计）
      --tenant-rate-limit <TENANT_RATE_LIMIT>
          租户 API 速率限制，以每个租户每秒的请求数计 [默认值: 10]
      --db-connect-timeout <DB_CONNECT_TIMEOUT>
          等待初始数据库连接就绪的最长时间 [默认值: 5s]
      --start-as-candidate
          以候选者（candidate）身份启动，用于高可用（HA）选主；通常配合多个 storage controller 实例部署使用
      --address-for-peers <ADDRESS_FOR_PEERS>
          向对等节点（peer）宣告自身时使用的地址，供其它 storage controller 实例与之通信
      --neon-local-repo-dir <NEON_LOCAL_REPO_DIR>
          `neon_local` 会将该项设置为 neon_local 仓库目录的路径。仅用于测试
      --chaos-interval <CHAOS_INTERVAL>
          混沌测试（chaos testing）：周期性演练租户迁移，以验证系统在异常下的健壮性
      --chaos-exit-crontab <CHAOS_EXIT_CRONTAB>
          混沌测试：按照 crontab 计划演练进程立即退出
      --max-secondary-lag-bytes <MAX_SECONDARY_LAG_BYTES>
          在排空（draining）某个 pageserver 时，次要位置（secondary location）可接受的最大滞后（lag）字节数
      --heartbeat-interval <HEARTBEAT_INTERVAL>
          向已注册节点发送心跳（heartbeat）的周期
      --long-reconcile-threshold <LONG_RECONCILE_THRESHOLD>
          协调（reconcile）耗时超过该阈值即视为"长时间协调"，通常用于告警与监控诊断
      --use-https-pageserver-api
          标志，对 pageserver API 的请求使用 https
      --timelines-onto-safekeepers
          将时间线（timeline）相关信息记录/同步到 safekeeper 的标志
      --use-https-safekeeper-api
          标志，对 safekeeper API 的请求使用 https
      --ssl-key-file <SSL_KEY_FILE>
          包含 HTTPS API 证书私钥的文件路径 [默认值: server.key]
      --ssl-cert-file <SSL_CERT_FILE>
          包含 HTTPS API 所用 X509 证书的文件路径 [默认值: server.crt]
      --ssl-cert-reload-period <SSL_CERT_RELOAD_PERIOD>
          从文件重新加载证书和私钥的周期 [默认值: 60s]
      --ssl-ca-file <SSL_CA_FILE>
          在 HTTPS API 中使用的受信任根 CA 证书
      --use-local-compute-notifications
          Neon Local 专用标志。启用时，忽略 [`Cli::control_plane_url`]，直接投递计算通知（compute notification），而不是经由控制面
      --timeline-safekeeper-count <TIMELINE_SAFEKEEPER_COUNT>
          创建时间线时为该时间线选择的 safekeeper 数量。safekeeper 会从不同的可用区（availability zone）中挑选。该选项主要用于测试目的 [默认值: 3]
      --kick-secondary-downloads <KICK_SECONDARY_DOWNLOADS>
          启用时，在协调（reconciliation）过程中主动检查并触发热图（heatmap）的下载/上传。这样可避免默认等待热图下载间隔，从而加速迁移。主要用于测试以减少测试执行时间 [默认值: false] [可选值: true, false]
      --shard-split-request-timeout <SHARD_SPLIT_REQUEST_TIMEOUT>
          分片拆分（shard split）请求的超时时间
      --handle-ps-local-disk-loss
          **功能标志（Feature Flag）** 存储控制器是否应主动纠正 pageserver 上报的本地磁盘丢失（local disk loss）
```

## 关键术语对照

| 英文术语             | 中文释义   | 说明                                                               |
| -------------------- | ---------- | ------------------------------------------------------------------ |
| storage controller   | 存储控制器 | 管理租户到 pageserver 分片映射的核心控制组件                       |
| pageserver           | 页面服务器 | 负责页面层存储与读取的节点                                         |
| safekeeper           | 安全守护者 | 负责 WAL 持久化与高可用的节点                                      |
| tenant               | 租户       | neon 中的租户（对应一个项目/数据库）                               |
| shard                | 分片       | 租户数据在多个 pageserver 上的水平分片                             |
| timeline             | 时间线     | 租户数据的某个分支/历史版本                                        |
| heatmap              | 热图       | 描述分片数据分布与访问热度的元数据，用于迁移与拆分                 |
| reconciler           | 协调器     | 负责让外部真实状态与"意图状态"保持一致的循环任务                   |
| secondary location   | 次要位置   | 租户分片的副本所在节点，用于高可用                                 |
| draining             | 排空       | 将某 pageserver 上的负载安全迁移走的过程                           |
| generation           | 代次       | 用于保证 pageserver 数据安全（防止脑裂）的版本号                   |
| compute notification | 计算通知   | 当租户 pageserver 位置变化时，通知 compute（postgres）更新连接配置 |
