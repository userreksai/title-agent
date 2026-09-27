# Title Agent

独立 Go 标题抓取服务，默认监听 **8003**。接收 master 的域名任务，返回标题、HTTP 状态或超时/失败结果。

## 下载最新代码并编译

要求 Git、Go 1.23+、基本 coreutils；下载脚本需要 curl。不要求 root，不安装 systemd，也不自动启动或重启程序。

```sh
mkdir -p /user/local/title_agent
cd /user/local/title_agent
curl -fsSL https://raw.githubusercontent.com/userreksai/title-agent/main/build.sh -o build.sh
sh build.sh
```

脚本每次下载默认分支最新代码，在临时目录执行 `go build`，把程序放到**执行命令时的当前目录**：

```text
/user/local/title_agent/
  build.sh
  title-agent
  agent.json
  title-agent.version
  title-agent.previous  # 更新已有程序后才生成
```

`agent.json` 只在不存在时生成，后续不会覆盖。更新前的程序保留为 `title-agent.previous`，下载/编译失败不会替换现有程序。`title-agent.version` 记录 Git commit，临时源码结束后清理。

## 配置与运行

编辑 `agent.json`：

- `listen`：默认 `:8003`。
- `name`：分别配置 `agent-1`、`agent-2`、`agent-3`，必须与 master 对应名称一致。
- `token`：至少 16 字符的随机令牌，推荐 32 字符以上；master 中填同一个 token。
- `max_concurrent`：默认 20，不能小于 master 的每台并发数。
- `max_timeout_seconds`：默认 15，最大 15 秒。
- `health_probe_urls`：外网探针，任一正常即可；配置本机能稳定访问的地址。全部失败时 master 将本机当作机器故障剔除，发到独立机器群。空数组仅检查 agent 服务本身。

```sh
./title-agent -config ./agent.json -check-config
./title-agent -config ./agent.json
```

手动后台运行示例：

```sh
nohup ./title-agent -config ./agent.json >agent.log 2>&1 &
```

master 的 URL 填 `http://此机器IP:8003`。防火墙只允许主控访问；跨公网时使用 VPN 或 HTTPS 反向代理。

## 更新

```sh
cd /user/local/title_agent
curl -fsSL https://raw.githubusercontent.com/userreksai/title-agent/main/build.sh -o build.sh
sh build.sh
```

编译后由你停止旧进程、重新启动新程序。运行中的旧进程不会自动更新。使用 `TITLE_AGENT_REF=分支名 sh build.sh` 可指定分支。

## 抓取行为

每域名 DNS、连接、TLS、重定向及标题读取共用最多 15 秒。裸域名先 HTTPS，连接/读取失败后在同一总预算内尝试 HTTP；返回真实错误页标题，不会通过寻找正常页面掩盖 500 页面。只解析 HTML，不执行 JavaScript；支持 UTF-8 和声明为 GBK/GB18030 等编码的标题。

默认阻止目标及重定向访问内网/本机 IP；需要监控内网网站时设置 `allow_private_targets=true`。网站请求支持 80/443 端口。所有 agent 接口要求 `Authorization: Bearer <token>`。

```sh
curl -H 'Authorization: Bearer 实际token' http://127.0.0.1:8003/healthz
```

## 手动编译与验证

```sh
go mod download
go test ./...
go vet ./...
go build -trimpath -o title-agent ./cmd/title-agent
sh tests/script-smoke.sh agent build.sh
```

最后一项使用模拟 Git/Go 检查编译失败保护、更新与配置保留，不请求真实仓库。
