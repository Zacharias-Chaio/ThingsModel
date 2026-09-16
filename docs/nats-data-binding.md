# NATS 数据绑定设计

## 数据流

```mermaid
flowchart LR
    Gateway[Gateway-Modbus] -->|{inputPrefix}.{gatewayId}.data| Consumer[ThingsModel NATS Client]
    Consumer --> Sources[来源目录]
    Sources --> Processor[标准化与聚合运行时]
    Bindings[设备实例绑定] --> Processor
    Processor --> Runtime[运行数据 API]
    Processor -->|powerpulse.thingsmodel.{softwareId}.data| Consumers[下游订阅者]
```

## 配置

平台设置统一持久化在 SQLite 数据库的配置表中（首次启动写入默认配置）。Web 的 `GET/POST /api/settings` 直接读取和写回数据库。数据订阅与内容发布是两个独立的 NATS 客户端，各自持有连接与重连参数，可指向不同的 NATS 服务器。

```json
{
  "software": { "id": "thingsmodel-001", "name": "ThingsModel" },
  "subscriber": {
    "enabled": true,
    "url": "nats://127.0.0.1:4222",
    "inputSubjectPrefix": "powerpulse.gateway"
  },
  "publisher": {
    "enabled": true,
    "url": "nats://127.0.0.1:4222",
    "queueSize": 4096
  },
  "staleAfter": 60000,
  "deviceSubscriptions": [{ "gatewayId": "gw-001" }]
}
```

每一条 `device_subscriptions.gateway_id` 都订阅该 Gateway 的全部采集设备。NATS 的 Gateway 数据主题不支持按采集设备订阅；设备级筛选和绑定由 ThingsModel 在消息体解析后完成。

## 输入契约

输入严格使用 Gateway-Modbus 已发布的 Core NATS 主题和信封：

- Subject：`{input_subject_prefix}.{gateway_id}.data`
- Headers：`PP-Message-ID`、`PP-Message-Type=data`、`PP-Message-Version=v1.0`、`PP-Message-Timestamp`
- Payload：Gateway 的 `MessageData` JSON，其中属性位于 `properties[property_id]`。

来源目录 API `GET /api/runtime/sources` 返回每个已发现来源及其属性。当前 Gateway v1 没有稳定的采集设备实例 ID，因此来源以 `gateway_id/channel_index/device_index` 组合键保存到既有绑定中的 `deviceId` 字段，属性 map key 保存到 `propertyId`。

**约束**：Gateway 修改某通道内设备的挂载顺序后，`device_index` 可能变化，需要重新检查相应绑定。后续 Gateway 契约应增加稳定 `device_id`，届时 ThingsModel 可无损迁移来源键。

## 标准化与清洗

设备实例是绑定和标准化的唯一配置源；模板只提供可复用语义。

- 未启用的物模型设备不扇出数据。
- 未绑定属性标记为 `unbound`；缺少来源为 `unavailable`；`null`、非数值聚合输入或非法运算标记为 `invalid`；超过 `nats.stale_after` 的来源标记为 `stale`。
- 枚举属性仅支持 `EPT`，用模板 `description[].value` 匹配上游原始值，输出对应 `description[].enum`。未匹配时输出 `unmapped`，有 `enum=-1` 定义时输出 `-1`。
- 数值属性支持 `EPT`、`SUM`、`AVG`、`MIN`、`MAX`、`AND`、`OR`、`NOT`。逻辑运算把零视为 false、非零视为 true，输出 `0` 或 `1`。
- 不自动做单位换算；上游单位只用于来源信息，物模型输出单位由模板属性定义。
- 一个事件的任一绑定来源满足 `equal` / `upper` / `lower` 阈值即进入触发计时；持续 `event.time` 毫秒后标记 `active`，否则为 `pending`。不满足阈值时为 `inactive`。

## 输出契约

输出 subject 为 `powerpulse.thingsmodel.{software.id}.data`（输出前缀静态编码在 ThingsModel 代码中，不对外配置），同样使用 `PP-Message-*` 信封，`PP-Message-Type=data`、版本 `v1.0`。每个受本次上游消息影响且已启用的物模型设备发布一条完整状态：

```json
{
  "thingsmodel_id": "thingsmodel-001",
  "device_id": "PCS-A01",
  "device_name": "A区1号 PCS",
  "template_code": "PCS-DEVICE-001",
  "template_version": "1.0.0",
  "data_status": "available",
  "properties": {
    "power": {
      "name": "功率",
      "unit": "kW",
      "value": 42.5,
      "timestamp": 1789344000000,
      "quality": "good"
    }
  },
  "events": {
    "overPower": {
      "name": "功率过高",
      "level": 2,
      "status": "active",
      "active": true,
      "timestamp": 1789344000000
    }
  },
  "timestamp": 1789344000000
}
```

## 热加载

- `POST /api/settings`：先校验并写入数据库配置表，再立即替换日志配置和 NATS 客户端。新 NATS 连接或订阅无法初始化时，接口返回 `503`，配置仍保留，旧客户端继续运行。
- `POST /api/restart`：异步清空来源缓存和事件状态，并以当前配置重建 NATS 客户端。HTTP 服务保持可用；重复重启返回 `409`。
- 运行期 NATS 断线由 `nats.go` 自动按配置重连；扇出队列满时丢弃最旧的标准化遥测，优先保留最新状态。