# ptpsim — IEEE 1588 PTP 离散事件仿真栈

一套用 Go 实现的精确时间协议（PTP, IEEE 1588-2008）软件栈，运行在单线程、
确定性的离散事件网络仿真器中，并自带一个查看同步状态的 Web 面板。

面向皮划艇赛事计时这类起点/终点设备需要亚微秒级时间对齐的场景：换交换机
导致几十微秒漂移的根因往往在 BMCA 选主、透明时钟修正、链路不对称或伺服
整定上。本项目把这些机制逐一建成可观测、可复现、可与协议手算值对照的
仿真，而不是停留在方案描述。

## 快速开始

```bash
# 跑全部测试（含协议手算对照、可复现性校验）
go test ./...

# 启动某个场景的面板（默认 :8080）
go run ./cmd/ptpsim -scenario failover -http :8080
# 浏览器打开 http://localhost:8080

# 只跑仿真、打印结果，不起 Web
go run ./cmd/ptpsim -scenario ring -no-http

# 换种子：同一种子两次运行的 trace 哈希完全一致
go run ./cmd/ptpsim -scenario linear -seed 42 -no-http
```

内置场景：`linear`（OC/BC 链）、`ring`（BC 环，验证破环）、`tc`
（透明时钟链）、`p2p`（点到点延迟机制）、`failover`（主时钟断电重选）、
`asymmetry`（不对称链路补偿）、`foldup`（两步模式跟随报文丢失）。

## 面板的五个视图

1. **设备拓扑** — 节点类型（OC/BC/TC）、主时钟高亮、断电标记，链路上标注
   双向时延与不对称补偿值。
2. **主时钟选举** — 每次 BMCA 决策（M1/M2/M3/S1/P1/P2），并附独立手算的
   逐项数据集比较轨迹（priority1→class→accuracy→variance→priority2→identity）。
3. **各端口状态** — Master/Slave/Passive/Listening/PreMaster/Uncalibrated/
   Faulty 状态机，外部主时钟数、对等延迟、各类报文计数。
4. **时间偏移曲线** — 可在「真实偏移 / 协议测量偏移 / 伺服频偏」之间切换，
   直接看到 PI 收敛过程与故障切换瞬态。
5. **报文时间线 + 状态迁移** — 每条 Sync/Follow_Up/Announce/Delay/Pdelay
   报文的收发、丢弃与透明时钟修正，以及端口角色迁移原因。

## 已实现的协议能力（对应需求逐条）

| 需求 | 实现位置 |
| --- | --- |
| ① BMCA 数据集逐项比较，含步数（stepsRemoved） | `internal/ptp/dataset.go` 的 `CompareDatasets`、`bmca.go` 的状态决策算法 |
| ② 端口状态机 Master/Slave/Passive/Listening 等，Announce 超时重选 | `bmca.go`、`device.go` 的 `scheduleAnnounceTimeout`（按本地时钟计时） |
| ③ 端到端（E2E）与点到点（P2P）两种延迟测量 | `receive.go` 的 Delay_Req/Resp 与 Pdelay 四时间戳交换 |
| ④ 透明时钟把驻留时间累加进 correctionField | `receive.go` 的 `tcForward`，含两步模式 Follow_Up 修正转移 |
| ⑤ 边界时钟一侧从、一侧主逐级下传 | `scenario` 的线性/环形拓扑，BMCA 的 S1/P1 决策 |
| ⑥ 从端 PI 伺服，参数可配、收敛可见 | `internal/clock/servo.go`，增益经离散稳定性扫描整定 |
| ⑦ 链路双向不对称可配补偿 | `link.go` 方向补偿 + `receive.go` 分方向的 meanPath 计算 |
| ⑧ 两步模式 Follow_Up 丢失则该轮作废 | `receive.go` 的 `handleFollowUp`，`scenario` 可注入丢失 |

## 仿真器与时钟模型

- **离散事件引擎**（`internal/engine`）：纳秒整数仿真时间，最小堆调度，
  同刻事件按单调序号定序，保证同种子执行顺序逐字节确定。
- **本地振荡器**（`internal/clock/oscillator.go`）：
  `c(t) = 固定相位偏移 + ∫(1+f(s))ds + 低通相位噪声 + 伺服相位调整`，
  `f` 含静态频偏、线性漂移、频率随机游走。所有随机数来自每节点独立的
  PCG 源，无全局 rand 依赖。
- **PI 伺服**：输出频率校正（ppb），大偏差用硬相位步进、小偏差用比例+
  积分平滑。默认增益在 1 s Sync 周期下处于离散闭环稳定区，静态频偏可
  收敛到亚纳秒稳态残差。

## 理论对照（不是自说自话）

`internal/theory` 与运行中的协议完全独立，只按 IEEE 1588 公式手算：

- `PredictGrandmaster`：按 9.3.5 b) 字段顺序选出主时钟，并给出每一步是
  哪个字段决胜的审计轨迹；`TestTheoryVersusSimulationBMCA` 断言三种拓扑
  下手算与仿真选主一致。
- `E2EOffset` / `AsymmetryResidual`：11.3 节闭环数值算例，验证不对称
  链路的 `(dMS-dSM)/2` 系统偏差及补偿抵消。
- `P2PPeerDelay`：11.4 节四时间戳对等延迟公式。
- `FailoverTime`：超时（按本地时钟）+ 传播 + 状态机驻留的切换时间合成。
- `TestNoNoiseTheoreticalFloor`：关闭所有随机项后，稳态偏移低于 5 ns，
  证明协议方程本身无偏，残差只来自噪声与量化。

## 可复现性

每次运行对五类 trace（报文、端口状态、选举、偏移样本、通用事件）做
规范化 SHA-256。`experiment.RunReproducible` 与主程序都会用同一种子跑
两遍并比对哈希；`TestReproducibility` 强制校验。所有内置场景的
`reproducible` 均为 `true`。

## 典型结果（种子 20261119，Sync 周期 1 s）

| 场景 | 从端平均 \|偏移\| | 最大 \|偏移\| |
| --- | --- | --- |
| linear（两级 BC，E2E） | ~29 ns | ~117 ns |
| ring（5 节点 BC 环） | ~124 ns | ~573 ns |
| transparent-tc（P2P，两级 TC） | ~161 ns | ~217 ns |
| failover（GM 断电） | ~33 ns | ~93 ns |
| asymmetry（80/20 μs 链路，已补偿） | ~29 ns | ~117 ns |

## 代码结构

```
internal/
  engine/      离散事件引擎、确定性随机源
  clock/       振荡器（漂移+相位噪声）、PI 伺服
  ptp/         数据集/报文/BMCA/状态机/E2E/P2P/TC/BC、链路、拓扑
  scenario/    三种拓扑与故障、丢包、不对称事件注入
  theory/      独立于实现的 IEEE 1588 手算
  experiment/  运行、指标统计、可复现性哈希、理论对照测试
  trace/       有界环形缓冲的事件记录
  web/         JSON API + 嵌入式单页面板
cmd/ptpsim/    命令行入口
```
