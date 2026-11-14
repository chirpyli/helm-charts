# storage broker

Storage Broker 是 Neon 架构中的一个**轻量级发布-订阅 (Pub-Sub) 消息中间件**，作为 safekeeper 与 pageserver 之间的**服务发现与状态协调层**。它不存储任何持久化数据，只负责在组件之间实时转发消息。

**核心定位：** Safekeeper 和 pageserver **从不直接互相发现对方**，而是全部通过 Storage Broker 进行状态交换。Neon 的存储层是多 safekeeper（WAL 法定多数副本）+ 多 pageserver（拉取 WAL）。若让每个 pageserver 轮询所有 safekeeper、每个 safekeeper 与其他所有投票互通，会产生 O(n²) 连接。Storage broker 就是为了避免这种网状拓扑而存在的无状态 pub/sub 中继。

它解决两件事：

- 发现：节点获知"还有谁也持有我的 timeline"。
- 状态同步：推进 commit_lsn / remote_consistent_lsn / backup_lsn，决定谁负责把 WAL 上传到 S3；pageserver 据此挑选"最新且存活"的 safekeeper 去拉 WAL。

> pageserver以及safekeeper都支持扩缩容，当pageserver以及safekeeper的节点非常多以后，如果不通过一个中间消息队列进行节点发现，则可能需要进行大量的N x M交互，通过storage broker，safekeeper定时向storage broker发布消息，报告自己的连接信息以及WAL等情况，而pageserver则只需订阅storage broker，即可获知所有的safekeeper节点以及哪个safekeeper当前的WAL最新，以便可以连接最佳的safekeeper拉取WAL日志。

## safekeeper发布`SafekeeperTimelineInfo`消息

safekeeper周期性通过`PublishSafekeeperInfo(stream SafekeeperTimelineInfo)`向storage broker发布所有活跃 timeline 状态，核心信息包括：

```go
message SafekeeperTimelineInfo {
    uint64 safekeeper_id = 1;       // 指明是哪个safekeeper
    TenantTimelineId tenant_timeline_id = 2;  // 租户/分支
    uint64 term = 12;        // Safekeeper term   当前租约任期
    uint64 last_log_term = 3;    // Term of the last entry. 最后一条日志的任期，决定哪个副本拥有"最新且已被多数派确认"的 WAL。
    uint64 flush_lsn = 4; // 已落盘的本地 WAL； LSN of the last record.
    uint64 commit_lsn = 5;  // 该 safekeeper 认定已提交的 WAL 位置，pageserver 选主的主要依据
    // LSN up to which safekeeper has backed WAL.
    uint64 backup_lsn = 6;  // 已卸载到 S3 的位置（决定谁负责 offload）；
    // LSN of last checkpoint uploaded by pageserver.
    uint64 remote_consistent_lsn = 7; // pageserver 最后完成 ingest 的检查点（remote_consistent_lsn 用于安全 GC）；
    uint64 peer_horizon_lsn = 8; 
    uint64 local_start_lsn = 9;
    uint64 standby_horizon = 14;  // 背压，
    string safekeeper_connstr = 10;     // A connection string to use for WAL receiving. Postgres流复制协议，用于WAL接收
    string http_connstr = 13;          // HTTP endpoint connection string. 连接字符串，访问safekeeper API用
    optional string https_connstr = 15;    // HTTPS endpoint connection string. 连接字符串 TLS
    // Availability zone of a safekeeper.
    optional string availability_zone = 11;  // pageserver 优先连同 AZ 的 safekeeper 以避免跨 AZ 流量。
}
```

safekeeper发布的信息，就是pageserver连接与访问safekeeper所需的信息，例如接收WAL日志的连接串，判断应该连接哪个safekeeper的信息。

pageserver通过`SubscribeSafekeeperInfo(SubscribeSafekeeperInfoRequest)`去storage broker订阅消息。

需要注意的是，除了pageserver，safekeeper自身也会从storage broker订阅消息，用来获知其他safekeeper节点的状态。

## proto

```go
service BrokerService {
    // Subscribe to safekeeper updates.
    rpc SubscribeSafekeeperInfo(SubscribeSafekeeperInfoRequest) returns (stream SafekeeperTimelineInfo) {};

    // Publish safekeeper updates.
    rpc PublishSafekeeperInfo(stream SafekeeperTimelineInfo) returns (google.protobuf.Empty) {};

    // Subscribe to all messages, limited by a filter.
    rpc SubscribeByFilter(SubscribeByFilterRequest) returns (stream TypedMessage) {};

    // Publish one message.
    rpc PublishOne(TypedMessage) returns (google.protobuf.Empty) {};
}

message SubscribeSafekeeperInfoRequest {
    oneof subscription_key {
        google.protobuf.Empty all = 1; // subscribe to everything
        TenantTimelineId tenant_timeline_id = 2; // subscribe to specific timeline
    }
}


message TenantTimelineId {
    bytes tenant_id = 1;
    bytes timeline_id = 2;
}

message FilterTenantTimelineId {
    // If true, only messages related to `tenant_timeline_id` will be emitted.
    // Otherwise, messages for all timelines will be emitted.
    bool enabled = 1;
    TenantTimelineId tenant_timeline_id = 2;
}

message TypeSubscription {
    MessageType type = 1;
}

message SubscribeByFilterRequest {
    // Subscription will emit messages only of the specified types. You need to specify
    // at least one type to receive any messages.
    repeated TypeSubscription types = 1;

    // If set and enabled, subscription will emit messages only for the specified tenant/timeline.
    optional FilterTenantTimelineId tenant_timeline_id = 2;
}

enum MessageType {
    UNKNOWN = 0;
    SAFEKEEPER_TIMELINE_INFO = 2;
    SAFEKEEPER_DISCOVERY_REQUEST = 3;
    SAFEKEEPER_DISCOVERY_RESPONSE = 4;
}

// A message with a type.
message TypedMessage {
    MessageType type = 1;

    optional SafekeeperTimelineInfo safekeeper_timeline_info = 2;
    optional SafekeeperDiscoveryRequest safekeeper_discovery_request = 3;
    optional SafekeeperDiscoveryResponse safekeeper_discovery_response = 4;
}

message SafekeeperDiscoveryRequest {
    TenantTimelineId tenant_timeline_id = 1;
}

// Shorter version of SafekeeperTimelineInfo, contains only necessary fields.
message SafekeeperDiscoveryResponse {
    uint64 safekeeper_id = 1;
    TenantTimelineId tenant_timeline_id = 2;
    // WAL available to download.
    uint64 commit_lsn = 3;
    // A connection string to use for WAL downloading.
    string safekeeper_connstr = 4;
    // Availability zone of a safekeeper.
    optional string availability_zone = 5;
    // Replica apply LSN
    uint64 standby_horizon = 6;
}
```

## storage_broker

storage_broker为无状态服务，需要配置监听地址以及端口，对外需要提供Service，以供pageserver，safekeeper组件访问。

```sh
storage_broker --help
Broker for neon storage nodes communication

Usage: storage_broker [OPTIONS] <--listen-addr <LISTEN_ADDR>|--listen-https-addr <LISTEN_HTTPS_ADDR>>

Options:
  -l, --listen-addr <LISTEN_ADDR>
          Endpoint to listen HTTP on
      --listen-https-addr <LISTEN_HTTPS_ADDR>
          Endpoint to listen HTTPS on
      --timeline-chan-size <TIMELINE_CHAN_SIZE>
          Size of the queue to the per timeline subscriber [default: 32]
      --all-keys-chan-size <ALL_KEYS_CHAN_SIZE>
          Size of the queue to the all keys subscriber [default: 16384]
      --http2-keepalive-interval <HTTP2_KEEPALIVE_INTERVAL>
          HTTP/2 keepalive interval [default: "5000 ms"]
      --log-format <LOG_FORMAT>
          Format for logging, either 'plain' or 'json' [default: plain]
      --ssl-key-file <SSL_KEY_FILE>
          Path to a file with certificate's private key for https API [default: server.key]
      --ssl-cert-file <SSL_CERT_FILE>
          Path to a file with a X509 certificate for https API [default: server.crt]
      --ssl-cert-reload-period <SSL_CERT_RELOAD_PERIOD>
          Period to reload certificate and private key from files [default: 60s]
  -h, --help
          Print help
  -V, --version
```

## 运行日志

```log
(base) postgres@slpc:~/works/opensource$ kubectl logs -n neon neon-broker-svc-6946cfc7cb-hk2ct 
2026-09-09T08:53:32.384459Z  INFO not initializing Sentry, no SENTRY_DSN given
2026-09-09T08:53:32.384895Z  INFO version: git-env:77e22e4bf09d88b70b4a83a38c2f6de6301816b4 build_tag: build_tag-env:8464
2026-09-09T08:53:32.387268Z  INFO listening HTTP on 0.0.0.0:50051
```
