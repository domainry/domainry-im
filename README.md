# Domainry IM

`domainry-im` 是可独立启动的轻量 IM 组件。它复用 Domainry Identity 的登录态、用户
投影和权限，不维护第二套账号、密码或会话；也不依赖 OpenIM、Kafka、MongoDB、
Redis、Etcd 或 WebSocket。

## 组件边界

组件只负责通用即时通讯能力：

- 一对一会话和普通群组，成员之外不可读写
- 会话内严格递增序号、`client_message_id` 幂等发送和断线补拉
- 文本、附件、常用语模板、调用方自定义的结构化 JSON 消息
- 任意标签及标签/正文/会话搜索
- 未读数和逐成员已读游标
- 通用外部资源上下文及具备独立权限的上下文历史只读访问
- Workspace 级常用语配置
- 通用附件上传、下载、校验，以及可选拍摄时间和定位元数据
- 可恢复 SSE 同步提示；数据库游标是事实，SSE 不是消息事实源

组件不负责调用方的业务状态机、单据、审批、通知策略或业务枚举。调用方通过下面三个
稳定扩展点组合业务功能，不修改 IM 核心：

1. `context`：把群组关联到调用方拥有的外部资源；IM 只按三元组存储和索引。
2. `tags`：调用方自定义、可精确检索的消息标签；IM 不解释标签语义。
3. `structured`：由 `content_type` 标识调用方契约、由 `data` 携带 JSON；IM 只校验、
   持久化和投递，不推进其中状态。

需要审批或单据流转时，业务服务维护自己的状态，并把状态快照作为结构化消息写入 IM。
这样业务规则可独立演进，IM 仍是可复用组件。

## 会话和扩展模型

一对一会话由 Workspace 与两个成员确定，重复创建返回同一会话。群组有两种互斥标识：

- 普通群组提供稳定的 `client_thread_id`；
- 外部资源群组提供 `context`，同一 Workspace 下同一上下文只有一个会话。

外部上下文示例：

```json
{
  "kind": "group",
  "title": "Asset discussion",
  "member_ids": ["user-b"],
  "context": {
    "namespace": "example-product",
    "resource_type": "asset",
    "resource_id": "asset-001"
  }
}
```

结构化消息示例：

```json
{
  "client_message_id": "mobile-01KM9Q6R5G",
  "kind": "structured",
  "content_type": "application/vnd.example.asset-state+json",
  "data": {
    "external_id": "asset-001",
    "state": "inspected"
  },
  "tags": ["workflow", "inspection"]
}
```

`content_type` 和 `data` 完全属于调用方契约。IM 不为某个 `content_type` 注册处理器，也
不会根据 `data.state` 自动产生新消息或修改外部状态。

## 架构

```text
cmd/im-server                         独立进程入口
internal/adapter/identitysdk          Identity → 通用 IM Actor 适配
internal/application/im              Action/Permission 唯一清单
internal/domain/conversation         通用会话、消息和回执规则
internal/infrastructure/persistence  ORM 查询、Schema 和迁移宿主
internal/adapter/knowledgefiles      Knowledge 通用文件能力 → IM 附件端口
internal/infrastructure/media/local  仅供独立测试的本地附件适配器
internal/transport/http/saas         HTTP/SSE 传输
internal/assembly/saas               独立服务装配
module                               Action、Schema、表归属声明
```

领域服务不读取 Identity 上下文，也不依赖 `net/http` 或 `database/sql`。HTTP 路由从
代码注册的 ActionDefinition 派生，每个业务路由要求同名 Permission；持久化层继续做
Workspace 和会话成员校验。

独立服务启动时向 Identity 注册 Application，并用 `module:im` 权限快照精确对账 17 个
终端用户 Permission；另有一个只接受 Identity 短期 application-service token 的业务投影
Action。对账失败时拒绝启动。IM 仍集成 Domainry 账号体系，但账号集成只存在于
adapter/assembly 边界，通用会话领域不依赖 Identity SDK。

数据库初始结构是 source-owned 的 v1 Schema，通过 `domainry-orm/schema` 生成并由
唯一 `_schema_migrations` 宿主登记。运行时查询使用 `domainry-orm/query`，Repository
没有手写业务 SQL。本地开发默认使用 SQLite；生产和多实例部署使用 PostgreSQL。多个
实例启动时以 PostgreSQL advisory lock 串行执行迁移，避免并发建表或迁移记录冲突。

## HTTP API 与权限

所有 `/im/*` 路由都必须通过 Domainry Identity。普通用户接口使用用户 access token：

```http
Authorization: Bearer <domainry-access-token>
```

浏览器原生 `EventSource` 无法自定义 Authorization 请求头，因此 Web 客户端应使用
`fetch` 可读流携带 Bearer Token。服务不签发 URL 查询参数票据，凭据不会进入 URL、
代理日志或浏览历史。

| Permission | 方法与路径 | 用途 |
|---|---|---|
| `im.threads.create` | `POST /im/threads` | 创建或取得会话 |
| `im.threads.list` | `GET /im/threads?limit=50` | 会话与未读数列表 |
| `im.threads.members.update` | `PUT /im/threads/{thread_id}/members` | 替换群成员 |
| `im.messages.send` | `POST /im/threads/{thread_id}/messages` | 幂等发送消息 |
| `im.messages.list` | `GET /im/threads/{thread_id}/messages?after_sequence=0&limit=50` | 按序补拉消息 |
| `im.messages.search` | `GET /im/messages/search?q=&tag=&thread_id=&limit=50` | 搜索有权访问的消息 |
| `im.receipts.mark` | `PUT /im/threads/{thread_id}/read` | 推进自己的已读游标 |
| `im.receipts.list` | `GET /im/threads/{thread_id}/receipts` | 查看成员已读游标 |
| `im.attachments.upload` | `POST /im/uploads` | 上传附件，multipart 字段名为 `file` |
| `im.attachments.read` | `GET /im/attachments/{attachment_id}/content` | 下载成员可访问的附件 |
| `im.contexts.history.list` | `GET /im/contexts/{namespace}/{resource_type}/{resource_id}/history` | 按外部上下文读取历史 |
| `im.contexts.history.search` | `GET /im/contexts/{namespace}/{resource_type}/{resource_id}/history/search` | 按外部上下文搜索历史 |
| `im.contexts.members.update` | `PUT /im/contexts/{namespace}/{resource_type}/{resource_id}/members` | 产品校验业务资源后同步上下文群成员 |
| application-service grant `im.contexts.messages#send` | `POST /im/contexts/{namespace}/{resource_type}/{resource_id}/messages` | 业务服务用短期精确授权 token 投影结构化状态消息；service actor 不加入群成员 |
| `im.contexts.attachments.read` | `GET /im/contexts/{namespace}/{resource_type}/{resource_id}/attachments/{attachment_id}/content` | 校验附件所属上下文后读取 |
| `im.quick_replies.list` | `GET /im/quick-replies?tag=` | 获取启用中的常用语 |
| `im.quick_replies.update` | `PUT /im/quick-replies/{key}` | 新增、修改或停用常用语 |
| `im.events.stream` | `GET /im/events` | 建立同步提示流 |

运维探针为无需业务权限的 `GET /live` 和 `GET /ready`。

上下文历史权限用于审计、客服或管理者只读访问，并不把主体伪装成群成员。普通消息、
附件和成员修改接口仍严格要求会话成员身份。

业务投影接口只接受 `kind=structured`，并要求 token 中只有目标 audience 下的
`im.contexts.messages#send` grant。它会按上下文确定性创建空会话并以
`client_message_id` 幂等追加消息；service actor 不会被写成成员。业务产品之后同步真实
成员时复用同一上下文会话，因此 IM 只保存可展示的投影，不拥有或反推业务状态机。

## 离线发送和附件元数据

客户端无网时，把消息内容、稳定的 `client_message_id` 和待上传附件写入本地队列。恢复
网络后先上传附件，再发送消息；同一发送者在同一会话内用相同
`client_message_id` 重试会得到原消息，不会重复入库。若同一幂等键对应不同内容，服务
返回冲突，防止静默覆盖。

附件消息示例：

```json
{
  "client_message_id": "mobile-01KM9Q7B2H",
  "kind": "attachment",
  "text": "optional caption",
  "tags": ["evidence"],
  "attachments": [{
    "file_id": "file_...",
    "filename": "machine-room.jpg",
    "content_type": "image/jpeg",
    "size_bytes": 125839,
    "sha256": "...",
    "taken_at": "2026-09-26T08:10:00+08:00",
    "location_status": "captured",
    "latitude": 35.6812,
    "longitude": 139.7671,
    "location_accuracy_meters": 12.5
  }]
}
```

拍摄和定位字段是可选的通用附件元数据。取得坐标时用 `captured` 并同时提交经纬度；
无法定位用 `unavailable`，用户拒绝权限用 `permission_denied`，后二者不提交坐标。普通
文档附件可完全不传这些字段。

## SSE 与恢复

连接时通过 `Last-Event-ID`，或首次连接用 `?cursor=<last_cursor>`，提交本地保存的同步
游标。服务返回：

- `im.ready`：客户端游标已经是最新状态；
- `im.sync`：数据库状态已变化，客户端应按各会话最后 `sequence` 调消息接口补拉。

事件 `id` 从持久状态计算。进程重启、连接断开或提示丢失都不会丢消息，因为客户端以
数据库消息序列补齐正式数据。SSE 只传游标和未读总数，不复制消息正文。

## 配置与启动

| 环境变量 | 说明 |
|---|---|
| `DOMAINRY_IM_LISTEN_ADDRESS` | 监听地址，默认 `:8092` |
| `DOMAINRY_IM_DATABASE_DRIVER` | 数据库驱动：本地默认 `sqlite`，生产使用 `postgres` |
| `DOMAINRY_IM_DATABASE_PATH` | SQLite 文件，仅 `sqlite` 使用，默认 `./data/domainry-im.db` |
| `DOMAINRY_IM_SQLITE_BUSY_TIMEOUT` | SQLite 写锁等待，默认 `5s` |
| `DOMAINRY_IM_SQLITE_MAX_OPEN_CONNECTIONS` | SQLite 连接数，默认 `8` |
| `DOMAINRY_IM_DATABASE_DSN` | PostgreSQL DSN；`postgres` 驱动必填 |
| `DOMAINRY_IM_DATABASE_SCHEMA` | PostgreSQL Schema，默认 `public`；建议生产使用 `domainry_im` |
| `DOMAINRY_IM_DATABASE_MAX_OPEN_CONNECTIONS` | PostgreSQL 最大连接数，默认 `16` |
| `DOMAINRY_IM_DATABASE_MAX_IDLE_CONNECTIONS` | PostgreSQL 最大空闲连接数，默认 `8` |
| `DOMAINRY_IM_DATABASE_CONNECTION_LIFETIME` | PostgreSQL 连接最长生命周期，默认 `30m` |
| `DOMAINRY_IM_DATABASE_CONNECTION_IDLE_TIME` | PostgreSQL 空闲连接最长保留时间，默认 `5m` |
| `DOMAINRY_IM_SSE_POLL_INTERVAL` | 持久状态检查周期，默认 `2s` |
| `DOMAINRY_IM_SSE_HEARTBEAT` | SSE 心跳周期，默认 `15s` |
| `DOMAINRY_IM_IDENTITY_ENDPOINT` | Domainry Identity 地址，必填 |
| `DOMAINRY_IM_IDENTITY_ISSUER` | Token issuer，必填 |
| `DOMAINRY_IM_WORKSPACE_ID` | 本服务绑定的 Workspace，必填 |
| `DOMAINRY_IM_APPLICATION_KEY` | Identity audience，默认 `domainry-im` |
| `DOMAINRY_IM_IDENTITY_SERVICE_TOKEN` | 可选服务凭据 |
| `DOMAINRY_IM_KNOWLEDGE_ENDPOINT` | 独立 Domainry Knowledge 服务地址，必填 |
| `DOMAINRY_IM_KNOWLEDGE_RUNTIME_ID` | Knowledge Runtime ID，默认与 `DOMAINRY_IM_APPLICATION_KEY` 相同 |
| `DOMAINRY_IM_KNOWLEDGE_SERVICE_TOKEN` | Knowledge 服务凭据，必填 |

```bash
go run ./cmd/im-server
```

生产数据库示例：

```bash
DOMAINRY_IM_DATABASE_DRIVER=postgres \
DOMAINRY_IM_DATABASE_DSN='postgres://domainry_im:password@postgres:5432/domainry?sslmode=require' \
DOMAINRY_IM_DATABASE_SCHEMA=domainry_im \
go run ./cmd/im-server
```

运行账号必须能连接目标数据库并对配置的 Schema 执行建表和 DML。服务启动时会创建不
存在的 Schema 并执行自身迁移；SQLite 只面向本地开发和单实例测试，不作为生产部署
方案。

IM 数据库只保存 `file_id` 和消息侧附件元数据，文件内容、哈希和对象存储引用由独立
Knowledge 服务管理。生产环境在 Knowledge 配置 AWS S3 或 MinIO；IM 不接触 bucket、
object key 或对象存储凭据。SSE 是无内存会话状态设计，不需要 Kafka 才能恢复连接。

生产镜像构建入口：

```bash
docker build -t domainry-im:local .
```
