# 打包教训：go build ≠ wails build

## 现象
- `go build -o build/bin/hiq.exe .` 构建成功，但 exe 双击后**窗口不出现、
  进程静默退出**（控制台子系统 + 无 wails 构建链处理），且体积 ~99MB。
- `wails build`（项目正规方式，见 desktop/README.md）产物 ~81.5MB，
  双击正常打开，页签恢复/模型回退全部正常。

## 规则
桌面端打包一律用：
```bash
cd desktop && wails build   # → build/bin/hiq.exe
```
体积差（~17MB）= wails build 的符号剥离等 ldflags；行为差 = 子系统/加载链。
裸 go build 只允许用于 `go build ./...` 编译自检，产物不可分发。

## 本次连带修复（70f4303）
陈旧模型（desktop-tabs.json 里活得比 provider 久的引用）在 boot.Build 咽喉处
回退到可用 provider（slog 警告并继续），任何调用路径都不再砖死启动。
