# GB28181 设备模拟器（带界面）

这是一个可双击运行的 Windows 桌面工具，基于旁边的 `gb28181-go` 设备侧（UAC）库，用来向 GB/T 28181 平台（例如 `sdsipsignal`）做 SIP 注册、心跳，并在收到 INVITE 后推送媒体。

界面是原生窗口（[Gio](https://gioui.org)，交叉编译不需要 CGO），包含参数表单、启动/停止、状态栏和滚动日志，用途类似 EasyGBD_Demo，但本程序**不是**网页产品。

## 依赖

本仓库通过 `go.mod` 里的 `replace` 引用同级目录中的上游库，**不要修改** `gb28181-go`：

```
replace github.com/mickeyzzc/gb28181-go => ../gb28181-go
```

拷贝到 Windows 后，目录关系应是：

```
E:\src\contribs\gb28181-go
E:\src\contribs\gb28181-device-ui
```

## 重要说明

- **视频是合成的。** 程序按 `gb28181-go` 自带 `device-register` 示例的方式，每秒写入一帧占位 H.264 NALU（每 5 帧一个 IDR）。这不是摄像头/RTSP 实况，平台上最多只能看到“有流”，不要指望看到真实画面。
- **本机出发地址是路由探测出来的，不是绑定设置。** 库本身没有 `-local-ip` 参数；界面会对「平台地址:端口」做一次 UDP connect（不真正发报文），再用 getsockname 显示内核会选用的源 IP，并在平台地址变化或点击「启动」时刷新。这正是为了核对流量是否从 `192.168.10.0/24` 一侧出发，而不是像 EasyGBD 那样自动选到别的网卡（例如 `192.168.4.170`）。
- **密码不会随仓库分发。** 源码默认值和本 README 都不含 SIP 密码。启动前必须在界面填写与平台约定的认证密码，否则会拒绝启动。密码只会写进可执行文件旁边的本地文件 `gb28181-device-ui.json`（已列入 `.gitignore`），请勿把该文件提交或拷贝到仓库里。

## 在 WSL 里交叉编译

本机若没有 Windows 版 Go，可在 WSL2（如 `utu2404`，Go 1.26 或更高，已验证 1.27.4）中编译：

```bash
cd /mnt/e/src/contribs/gb28181-device-ui
export GOTOOLCHAIN=local
export GOPROXY=https://goproxy.cn,direct
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-H windowsgui -s -w" -o gb28181-device-ui.exe .
```

得到单个文件 `gb28181-device-ui.exe`。`-H windowsgui` 避免双击时弹出黑色控制台。

## 运行

双击 `gb28181-device-ui.exe`，或在 PowerShell / cmd 中运行。首次打开会填入与当前联调一致的非敏感默认值（平台 `192.168.10.170:5060`、设备编码、通道编码、本地 SIP 端口 `15091` 等），密码为空。

1. 填写密码，核对平台地址、编码和本地 SIP 端口（默认 15091，避免和仍在运行的 EasyGBD_Demo 占用的 15090 冲突）。
2. 查看右侧「本机出发地址」是否为到达 `192.168.10.170` 时应走的网卡地址。
3. 点「启动」。日志会显示注册、心跳、INVITE 和 RTP 相关信息；右侧状态栏会尽量从日志和库的导出接口（平台协议版本、平台时间、已绑定端口）汇总。
4. 点「停止」或关闭窗口 /「退出」时，会先尝试注销再停止服务，并自动把当前表单保存到 `gb28181-device-ui.json`。也可随时用「保存配置」「加载配置」。

若提示本地 SIP 端口被占用，换一个端口再启动。初始注册失败时，库会进入仅监听并定期重试，不一定立刻退出。
