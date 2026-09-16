# ThingsModel

ThingsModel 是一个物模型模板与设备实例配置平台。

## 启动

```powershell
go run .
```

默认服务地址为 `http://localhost:8090`，SQLite 数据库默认写入 `data/thingsmodel.db`。可用 `-db` 指定其他数据库文件，用 `-templates` 指定模板目录：

```powershell
go run . -db .\data\thingsmodel.db -templates .\templats
```

## 日志与平台配置

平台配置（含日志、NATS、设备订阅）统一存储在 SQLite 数据库的配置表中，首次启动自动写入默认配置，之后从数据库加载。日志默认输出到终端和 `logs/thingsmodel.log`；文件到达上限（MB）时自动轮转，备份数量、保留天数与压缩均可在 Web「平台设置」中调整。

## 配置层次

- **模板管理**：定义可复用的属性、服务与告警语义，模板仍保存在 `templats/` JSON 文件中。
- **设备管理**：基于模板创建独立设备实例，并在 SQLite 中保存模板快照和实际 `deviceId` / `propertyId` 绑定。
- **运行数据**：展示已热重载到运行配置的设备。当前版本未接入采集引擎，因此明确显示“未接入/暂无实时数据”，不会把模板默认值作为实时值展示。

设备实例新增、更新和删除后会立即同步至进程内运行注册表，无需重启 HTTP 服务。后续采集引擎可在该注册表基础上提供真实属性值、服务调用结果和告警状态。

## NATS 数据绑定

平台通过 NATS 消费 Gateway-Modbus 已发布的采集遥测，将设备实例绑定的数据清洗、标准化和聚合后再次扇出。订阅与发布是两个独立的 NATS 客户端，可在 Web「平台设置」中分别配置（可指向不同服务器），保存时各自热加载、互不影响，配置写回 SQLite 数据库。

- 上游订阅：`{input_subject_prefix}.{gateway_id}.data`，例如 `powerpulse.gateway.gw-001.data`。
- 下游扇出：`powerpulse.thingsmodel.{software.id}.data`（输出前缀为静态编码，不提供配置），例如 `powerpulse.thingsmodel.thingsmodel-001.data`。
- 上游消息严格兼容 Gateway-Modbus 的 `PP-Message-*` 信封和 `MessageData` JSON 契约。
- 每次上游设备遥测都会更新来源目录；设备实例配置向导可从已发现的上游设备和属性中选择绑定来源。
- 数据库默认配置中 `subscriber.enabled` 与 `publisher.enabled` 均为 `false`。启用后分别填写服务地址，并在“设备订阅”中添加上游 Gateway ID。

保存平台设置会立即分别重载日志输出与两个 NATS 客户端；“软件重启”只重建运行时消息资源、清空已接收的来源缓存，HTTP 服务保持可用。

详细的消息契约和处理规则见 [docs/nats-data-binding.md](docs/nats-data-binding.md)。
