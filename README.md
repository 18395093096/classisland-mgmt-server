# ClassIsland 集控服务端（Cyrene_MSP）

用 Go 实现的 ClassIsland 集控服务器，单文件二进制 + SQLite，零外部依赖，双击即可运行。

> 官方 [ClassIsland/ManagementServer](https://github.com/ClassIsland/ManagementServer) 自 2026-01 起已挂出暂停开发公告，
> 本项目按客户端实际使用的 **Cyrene_MSP v2.0.0.0** 协议独立实现，代码完全可控。

## 已实现

| 能力 | 说明 |
| --- | --- |
| 实例注册 | gRPC `ClientRegister.Register`，返回服务器 PGP 公钥 |
| 服务器鉴权 | gRPC `Handshake`，客户端用服务器公钥加密挑战令牌，服务端解密回传完成双向校验 |
| 命令流 | gRPC `ClientCommandDeliver.ListenCommand` 双向流，支持心跳（Ping/Pong）与离线排队补发 |
| 配置回传 | gRPC `ConfigUpload.UploadConfig`，接收客户端上传的设置/档案/日志等 |
| 审计日志 | gRPC `Audit.LogEvent` |
| 数据分发 | HTTP `GET /api/v1/client/{cuid}/manifest` 与 `.../resource/{kind}` |
| 管理接口 | 档案增删改、客户端管理、分组分配、命令下发 |
| 网页控制台 | 内嵌在 `/ui/`，无需额外部署前端 |
| 可视化课表编辑器 | `/ui/` 首屏：时间表时间点、科目、周一至周日课表网格，所见即所得，保存即发布 |

## 发布课表（可视化，推荐）

打开 <http://<host>:50050/ui/>，首屏「课表编辑」按三步走：

1. **时间表**：新建时间表 → 添加时间点，填起止时间、选类型（上课 / 课间 / 休息 / 隐藏）。
   改某节的开始或结束时间，相邻时间点会自动衔接。
2. **科目**：添加科目，填名称、简称（显示在课表上的字）、任课教师。
3. **课表**：选时间表 → 在「节次 × 周一至周日」网格里为每个格子选科目 → 保存并发布。

保存会把档案版本号 +1，客户端下次拉取即生效（无需重启客户端）。
全空的星期不会生成课表，删除科目后请顺手检查对应格子。

> 实现要点：`ClassPlan.Classes` 只与时间点里 **TimeType=0（上课）** 的项按顺序一一对应，
> 课间不占位；时间表增删节次后，已排课表会自动对齐长度。

## 快速开始

```bash
go build -o cims-server.exe ./cmd/server

# 启动（默认 HTTP :50050 / gRPC :50051）
./cims-server.exe -org "某某学校" -public http://192.168.1.10:50050
```

常用参数：

```
-config config.json   配置文件路径（首次运行自动生成）
-http   :50050        HTTP 监听地址
-grpc   :50051        gRPC 监听地址
-db     data/cims.db  数据库路径
-public http://x:50050 对外基址（写入清单里的资源 URL，局域网部署务必设置）
-org    组织名称      显示在客户端「由 XX 管理」
```

## 客户端接入

在 ClassIsland **安装目录**放一个 `ManagementPreset.json`：

```json
{
    "ManagementServerKind": 1,
    "ManagementServer": "http://192.168.1.10:50050",
    "ManagementServerGrpc": "http://192.168.1.10:50051",
    "ClassIdentity": "高一1班"
}
```

启动 ClassIsland → 加入集控 → 自动识别预设 → 确认组织名 → 加入。

> gRPC 地址客户端用 `GrpcChannel.ForAddress`，局域网未配 TLS 时用 `http://`；
> 若服务端启用了 HTTPS，两端都改成 `https://` 即可。

## 管理接口

Base：`http://<host>:50050/api/v1/admin`，若配置了 `AdminToken`，用 `?token=xxx` 或 `Authorization: Bearer xxx`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/clients` | 客户端列表 |
| POST | `/clients/{cuid}/command` | 下发命令（101 重启 / 102 通知 / 103 数据更新 / 104 取配置） |
| POST | `/clients/{cuid}/group` | 修改分组 |
| GET | `/objects?kind=classplan` | 档案列表 |
| POST | `/objects` | 新建档案 `{kind,name,payload}` |
| PUT | `/objects/{id}` | 更新档案内容（版本自动 +1） |
| DELETE | `/objects/{id}` | 删除档案 |
| GET/POST | `/groups` | 分组列表 / 新建 |
| POST | `/groups/{id}/assign` | 把某类型档案分配给分组 |
| GET | `/audit` | 审计日志 |
| GET | `/online` | 在线实例 |

网页控制台：<http://<host>:50050/ui/>

## 重要：版本机制

客户端只在清单里的 `Version` 变大时才重新拉取对应档案。
通过管理接口 `PUT /objects/{id}` 更新内容时版本号会自动 +1；
**直接改数据库不会触发客户端更新**，请务必走接口或控制台。

## 目录结构

```
cmd/server/        入口
internal/config/   配置
internal/store/    SQLite 存储层（纯 Go，无需 CGO）
internal/crypto/   PGP 密钥与握手解密
internal/httpapi/  Client API + Management API
internal/grpcserver/ Cyrene_MSP gRPC 服务与在线 Hub
internal/webui/    内嵌网页控制台
proto/             从 ClassIsland 客户端同步的 .proto（buf 编译）
gen/go/            生成的 Go 代码
```

## 重新生成 protobuf 代码

```bash
go install github.com/bufbuild/buf/cmd/buf@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
cd proto && buf generate
```

## 注意

- 首次启动会在 `data/keys/` 生成服务器 PGP 密钥对，**请备份该目录**；
  密钥更换后客户端需要重新加入集控。
- 本项目面向校内局域网自托管，未做多租户与公网加固，公网暴露请自行加反向代理与 TLS。
