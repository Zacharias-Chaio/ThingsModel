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

## 日志

日志由根目录的 [config.yaml](config.yaml) 管理。默认输出到终端和 `logs/thingsmodel.log`；文件到达 `max_size`（MB）时自动轮转，`max_backups`、`max_age` 与 `compress` 分别控制备份数量、保留天数和压缩。

```yaml
logger:
	level: "debug" # debug、info、warn 或 error
	console: true
	file: true
	file_config:
		filename: "logs/thingsmodel.log"
		max_size: 10
		max_backups: 5
		max_age: 30
		compress: true
```

可使用 `-config` 指定其他配置文件：

```powershell
go run . -config .\config.yaml
```

## 配置层次

- **模板管理**：定义可复用的属性、服务与告警语义，模板仍保存在 `templats/` JSON 文件中。
- **设备管理**：基于模板创建独立设备实例，并在 SQLite 中保存模板快照和实际 `deviceId` / `propertyId` 绑定。
- **运行数据**：展示已热重载到运行配置的设备。当前版本未接入采集引擎，因此明确显示“未接入/暂无实时数据”，不会把模板默认值作为实时值展示。

设备实例新增、更新和删除后会立即同步至进程内运行注册表，无需重启 HTTP 服务。后续采集引擎可在该注册表基础上提供真实属性值、服务调用结果和告警状态。
