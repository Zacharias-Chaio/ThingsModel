# ThingsModel 北向消息契约设计

本文档描述物模型模块（ThingsModel）**北向**与 NATS 之间的消息契约：主题（Subject）设计、消息信封（Header + Data）设计、各主题的消息载体格式定义。北向指 ThingsModel 面向下游业务模块（储能系统、充电桩系统、光伏系统等）提供的**能力与数据接口**。

- 南向（面向网关）契约见 [gateway-message-contract-design.md](gateway-message-contract-design.md)。
- 客户端配置、连接/重连/降级等**客户端设计**内容，见 [nats-client-design.md](nats-client-design.md)。

## 1. 主题定义

主题前缀固定为 `powerpulse.thingsmodel`（平台前缀 `powerpulse` + 业务模块 `thingsmodel`）。**一个站点仅部署一个 ThingsModel 实例（单例）**，因此主题不含软件实例 ID。前缀之下按**能力域**分为三类业务消息前缀，再按**能力**细分二级主题：

| 业务域 | 能力 | Subject | 方式 | PP-Message-Type |
|--------|------|---------|------|-----------------|
| device（设备实例） | 查询 | `powerpulse.thingsmodel.device.query` | REQ/REP | `query` |
| device（设备实例） | 数据上报 | `powerpulse.thingsmodel.device.data` | PUB/SUB | `data` |
| alarm（告警） | 查询 | `powerpulse.thingsmodel.alarm.query` | REQ/REP | `query` |
| alarm（告警） | 状态上报 | `powerpulse.thingsmodel.alarm.status` | PUB/SUB | `status` |
| service（服务） | 查询 | `powerpulse.thingsmodel.service.query` | REQ/REP | `query` |
| service（服务） | 控制 | `powerpulse.thingsmodel.service.control` | REQ/REP | `control` |

示例：

```
powerpulse.thingsmodel.device.query
powerpulse.thingsmodel.device.data
powerpulse.thingsmodel.alarm.query
powerpulse.thingsmodel.alarm.status
powerpulse.thingsmodel.service.query
powerpulse.thingsmodel.service.control
```

**设计要点**

- **主题两级固定、不参与业务路由**：`device_id`、`service_key` 等业务元数据全部收束到消息载体，不参与 subject 路由，避免主题爆炸；
- **query / control 为请求-应答（REQ/REP）**；**data / status 为主动推送（PUB/SUB）**；
- **单例假设**：一个站点只有一个 ThingsModel 实例，主题无需实例 ID 隔离；
- 本阶段仅使用 **Core NATS**；不引入 JetStream stream、发布确认、持久化或重放语义。

## 2. 消息格式

NATS 客户端之间的消息采用 msg.Header + msg.Data 的组合：msg.Header 承载路由、类型、版本等元信息，msg.Data 承载业务数据。

### 2.1 消息信封

信封与南向网关侧完全一致，复用 `PP-Message-*` 四个 Header；业务负载写入 msg.Data：

| Header | 说明 |
|--------|------|
| `PP-Message-ID` | 消息唯一 ID，UUIDv4 |
| `PP-Message-Type` | 消息类型，北向取值见下表 |
| `PP-Message-Version` | 消息版本号，当前 `v1.0` |
| `PP-Message-Timestamp` | 消息时间戳，Unix 毫秒 |

北向 `PP-Message-Type` 取值：

| 取值 | 含义 | 方向 / 方式 |
|------|------|-------------|
| `data` | 设备实时数据上报 | ThingsModel → 下游（PUB/SUB，device.data） |
| `query` | 查询请求与应答 | 下游 → ThingsModel（REQ/REP） |
| `status` | 告警状态上报 | ThingsModel → 下游（PUB/SUB，alarm.status） |
| `control` | 服务控制请求与受理应答 | 下游 → ThingsModel（REQ/REP） |
| `controlAck` | 服务控制执行终态 | ThingsModel → 下游（PUB/SUB，service.status） |

> 注：`data` 复用南向网关遥测语义（承载设备实时数据）；`status` / `control` / `controlAck` 为北向新增；南向的 `cmd` / `cmdAck` 北向不复用。

### 2.2 通用约定

- 所有业务负载均为 **JSON 对象**，UTF-8 编码；字段统一 **snake_case**；
- REQ/REP 响应统一携带 `ok`（是否成功）与 `message`（失败原因，成功时省略）；
- 时间戳统一 **Unix 毫秒**（int64）；
- 空值遵循 JSON 约定：`null` 表示无值，`omitempty` 字段在无值时省略。

---

### 2.3 `powerpulse.thingsmodel.device.query` — 设备实例查询

信封 `PP-Message-Type: query`，REQ/REP。**用途**：查询当前物模型实例化了哪些设备。

```golang
// DeviceQuery 设备实例查询请求。
// 当前仅 listDevices 一种类型；Body 预留，后续可按 type 扩展（如按模板/状态过滤、查询单设备详情）。
type DeviceQuery struct {
    Type string          `json:"type"`           // listDevices
    Body json.RawMessage `json:"body,omitempty"` // 查询参数（预留）
}

// DeviceQueryResp 设备实例查询响应。
type DeviceQueryResp struct {
    Type    string           `json:"type"`             // 回显请求类型
    OK      bool             `json:"ok"`
    Message string           `json:"message,omitempty"`
    Devices []DeviceInstance `json:"devices"`          // 设备实例列表
}

// DeviceInstance 物模型设备实例概要。
type DeviceInstance struct {
    DeviceID        string `json:"device_id"`                  // 设备实例 ID，如 Device-001
    DeviceName      string `json:"device_name"`                // 设备名称
    TemplateCode    string `json:"template_code"`              // 物模型模板编号
    TemplateVersion string `json:"template_version,omitempty"` // 模板版本
    Description     string `json:"description,omitempty"`
    Enabled         bool   `json:"enabled"`                    // 是否启用（未启用设备不参与北向能力）
    DataStatus      string `json:"data_status,omitempty"`      // 数据状态：available / degraded / unavailable
    PropertyCount   int    `json:"property_count"`             // 属性数量
    MethodCount     int    `json:"method_count"`               // 服务数量
    AlarmCount      int    `json:"alarm_count"`                // 告警数量（检测点位关联数）
}
```

**说明**

- 一次请求返回全量设备实例列表；未实例化任何设备时 `devices` 为空数组；
- `device_id` 是北向唯一标识，用于后续 `device.data`、`alarm.query`、`service.query`、`service.control` 定位目标设备。

---

### 2.4 `powerpulse.thingsmodel.device.data` — 设备实时数据上报

信封 `PP-Message-Type: data`，PUB/SUB（无需应答）。**用途**：ThingsModel 将归一化、标准化后的设备实时属性值持续发布给下游。

```golang
// DeviceData 设备实时数据（归一化属性值）发布载体。
type DeviceData struct {
    ThingsModelID   string                   `json:"thingsmodel_id"`
    DeviceID        string                   `json:"device_id"`
    DeviceName      string                   `json:"device_name"`
    TemplateCode    string                   `json:"template_code"`
    TemplateVersion string                   `json:"template_version"`
    DataStatus      string                   `json:"data_status"` // available / degraded / unavailable
    Properties      map[string]DeviceProperty `json:"properties"` // 属性标识符 → 归一化属性值
    Timestamp       int64                    `json:"timestamp"`   // 发布时间，Unix 毫秒
}

// DeviceProperty 归一化后的单个属性值。
type DeviceProperty struct {
    Name      string `json:"name"`
    Unit      string `json:"unit"`
    Value     any    `json:"value"`
    Timestamp int64  `json:"timestamp"` // 最近一次来源时间，Unix 毫秒
    Quality   string `json:"quality"`   // good / unbound / unavailable / invalid / stale / unmapped
}
```

**说明**

- **每次上游遥测触发、且设备已启用时，按设备发布一条完整状态**：`properties` 为设备当前全部属性的归一化值，整体替换；
- **告警事件不在此主题**：告警状态独立走 `alarm.status`（2.6）推送，`alarm.query`（2.5）查询；
- `quality` 语义：`good` 正常、`unbound` 未绑定、`unavailable` 缺少来源、`invalid` 解析/运算非法、`stale` 来源超时、`unmapped` 状态值未匹配；
- 未启用（`Enabled=false`）的设备不发布。

---

### 2.5 `powerpulse.thingsmodel.alarm.query` — 设备告警查询

信封 `PP-Message-Type: query`，REQ/REP。**用途**：查询指定设备当前有哪些告警及其状态。

```golang
// AlarmQuery 设备告警查询请求。
type AlarmQuery struct {
    Type     string `json:"type"`      // queryDeviceAlarms
    DeviceID string `json:"device_id"` // 目标设备实例 ID
}

// AlarmQueryResp 设备告警查询响应。
type AlarmQueryResp struct {
    Type     string        `json:"type"`
    OK       bool          `json:"ok"`
    Message  string        `json:"message,omitempty"`
    DeviceID string        `json:"device_id"`
    Alarms   []AlarmStatus `json:"alarms"` // 该设备当前全部告警状态
}

// AlarmStatus 单个告警（检测点位）的当前状态。
type AlarmStatus struct {
    Key       string `json:"key"`                  // 告警标识：关联规则 key 组合，如 "overVoltage+underVoltage"
    Name      string `json:"name"`                 // 告警名称：规则名组合，如 "过压告警 / 欠压告警"
    Level     int    `json:"level"`                // 告警级别（取关联规则最高级）：0提示 / 1一般 / 2严重 / 3紧急
    Status    string `json:"status"`               // active / pending / inactive / unavailable
    Active    bool   `json:"active"`               // 是否激活（Status=active 时为 true）
    Value     any    `json:"value,omitempty"`      // 检测点位当前工程值（无数据为 null）
    Point     string `json:"point,omitempty"`      // 检测点位描述：网关 · 通道/设备 · 属性
    PointName string `json:"point_name,omitempty"` // 检测点位属性名称（未接入为空）
    Method    string `json:"method"`               // 判定方法：ept / and / or
    Timestamp int64  `json:"timestamp"`            // 最近一次判定时间，Unix 毫秒
}
```

**说明**

- `key` 是设备内稳定的告警标识（由检测点位关联的规则 key 组合而成），供下游做状态比对与去重；
- 设备不存在时返回 `ok=false, message="device not found"`；设备无告警配置时 `alarms` 为空数组。

---

### 2.6 `powerpulse.thingsmodel.alarm.status` — 告警状态上报

信封 `PP-Message-Type: status`，PUB/SUB（无需应答）。**用途**：告警状态发生变化时，ThingsModel 主动推送最新状态，供下游订阅。

```golang
// AlarmStatusReport 告警状态上报载体（状态变化时推送）。
type AlarmStatusReport struct {
    ThingsModelID string        `json:"thingsmodel_id"` // ThingsModel 软件实例 ID
    DeviceID      string        `json:"device_id"`
    DeviceName    string        `json:"device_name"`
    Alarms        []AlarmStatus `json:"alarms"`   // 本轮状态发生变化的告警列表
    Timestamp     int64         `json:"timestamp"` // 上报时间，Unix 毫秒
}
```

**说明**

- **推送触发条件**：告警状态发生跃迁即推送，包括但不限于「未激活 → 激活」（inactive → active）、「激活 → 恢复」（active → inactive）、pending → active 等；状态未变化不重复推送；
- **增量推送**：一条上报只包含本轮发生状态跃迁的告警；下游自行保存历史状态并与增量合并，需要全量时可用 `alarm.query` 拉取快照校准；
- `AlarmStatus` 结构复用 2.5 的定义，字段语义一致。

---

### 2.7 `powerpulse.thingsmodel.service.query` — 设备服务查询

信封 `PP-Message-Type: query`，REQ/REP。**用途**：查询指定设备提供哪些服务（方法）及其下发约束。

```golang
// ServiceQuery 设备服务查询请求。
type ServiceQuery struct {
    Type     string `json:"type"`      // queryDeviceServices
    DeviceID string `json:"device_id"` // 目标设备实例 ID
}

// ServiceQueryResp 设备服务查询响应。
type ServiceQueryResp struct {
    Type     string        `json:"type"`
    OK       bool          `json:"ok"`
    Message  string        `json:"message,omitempty"`
    DeviceID string        `json:"device_id"`
    Services []ServiceInfo `json:"services"`
}

// ServiceInfo 单个服务的定义与下发点位状态。
type ServiceInfo struct {
    Key          string       `json:"key"`                    // 服务标识符，如 "onoff"
    Name         string       `json:"name"`                   // 服务名称，如 "开关机"
    Type         string       `json:"type"`                   // status（状态类型）/ number（数值类型）
    Bound        bool         `json:"bound"`                  // 是否已绑定实际下发点位（未绑定不可控制）
    Validation   *Validation  `json:"validation,omitempty"`   // number：工程值范围 min/max
    Descriptions []StatusDesc `json:"descriptions,omitempty"` // status：状态值（名称 + 工程值）
}

// Validation 数值型服务的有效性范围。
type Validation struct {
    Min float64 `json:"min"`
    Max float64 `json:"max"`
}

// StatusDesc 状态型服务的单个状态值。
type StatusDesc struct {
    Enum  int    `json:"enum"`  // 状态值（-999 约定为未知状态）
    Key   string `json:"key"`   // 状态标签
    Name  string `json:"name"`  // 状态名称
    Value any    `json:"value"` // 工程值（服务必填且唯一）
}
```

**说明**

- 调用方先查询服务列表，再依据 `type`、`validation`、`descriptions` 构造合法的 `service.control` 请求；
- `bound=false` 的服务（未绑定下发点位）调用 `service.control` 会返回失败，见 2.8。

---

### 2.8 `powerpulse.thingsmodel.service.control` — 服务控制下发

信封 `PP-Message-Type: control`，REQ/REP。**用途**：向指定设备的一个服务下发控制命令。

```golang
// ServiceControl 服务控制下发请求。
type ServiceControl struct {
    DeviceID   string `json:"device_id"`   // 目标设备实例 ID
    ServiceKey string `json:"service_key"` // 服务标识符
    Value      any    `json:"value"`       // 工程值（number 为数值；status 为状态值枚举）
}

// ServiceAck 服务控制应答：REQ/REP 同步返回 accepted/failure；
// 网关实际执行完成后以 type=controlAck 异步回报 success/failure（见下）。
type ServiceAck struct {
    RequestID  string `json:"request_id"`  // 原请求 PP-Message-ID
    DeviceID   string `json:"device_id"`
    ServiceKey string `json:"service_key"`
    Status     string `json:"status"`      // accepted / failure / success
    Message    string `json:"message,omitempty"`
}
```

**同步应答（REQ/REP 的 reply）**

- `accepted`：请求已通过校验并下发到网关引擎写队列；
- `failure`：设备不存在、服务不存在/未绑定、参数越界或类型不符等，携带 `message` 说明原因。

**异步终态（`controlAck`）**

- 网关实际执行完成后，ThingsModel 以 `PP-Message-Type: controlAck` 发布到 `powerpulse.thingsmodel.service.status`（PUB/SUB），携带相同 `request_id`；
- 执行成功 `status=success`，失败 `status=failure`（附 `message`）。

**说明**

- 控制链路沿用南向网关侧的「同步受理 + 异步终态」两段式模型（与网关 `cmd` / `cmdAck` 一致），避免 REQ/REP 长时间阻塞；
- 数值型服务按 `validation.min/max` 校验工程值；状态型服务按 `descriptions[].value` 校验工程值。

---

## 3. 设计要点与约束

- **能力域与数据内聚**：设备实例（device）、告警（alarm）、服务（service）三域正交，查询/控制/数据/状态四类能力统一编排在各自前缀下；
- **数据与告警分离**：实时属性值走 `device.data`，告警状态走 `alarm.status`，二者不混在同一载体中；
- **拉取 + 推送混合**：静态配置（设备列表、告警定义、服务定义）用 query 拉取；动态数据（实时属性、告警状态、控制终态）用 data/status 推送；
- **告警状态增量上报**：`alarm.status` 采用增量推送——告警状态发生跃迁（未激活→激活、激活→恢复等）即推送该告警，状态未变化不重复发送；
- **标识稳定性**：`device_id`（设备实例）、`service_key`（服务）、`alarm.key`（告警）均在北向契约中稳定且唯一，供下游长期引用；
- **未启用设备不对外**：`Enabled=false` 的设备不参与 query 结果与 data/status 推送；
- **单例部署**：一个站点只有一个 ThingsModel 实例，主题不含实例 ID；如需多实例，需在主题前缀中引入实例标识；
- **安全与校验**：控制请求严格校验设备/服务存在性、绑定状态与参数合法性，非法请求同步返回 `failure`。


