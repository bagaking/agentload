<!--
meta:
  目的: macOS 菜单栏工具的"舒服"质量线调研——被喜爱工具的共性、平台规范、WKWebView-in-NSPopover 已知坑、分发/能耗礼仪，并沉淀为"讨喜 table-stakes 清单"。支撑 M03_S02（托盘）、M05（onboarding/偏好/popover）、M06_S01（分发）与 M02_S02（能耗预算）。
  日期: 2026-07-26（调研与信息更新时间）
  来源: UX 基准调研；出处为 Apple HIG / 开发者论坛 / 各应用官网与评测，原始链接见各条目与文末。
-->

# DOCREF_003 · macOS 菜单栏 UX 质量线与讨喜清单

## 1. 标杆应用的共性（用起来最舒服 = 做减法）

- **iStat Menus** — 定制深度做得平易近人：拖放排列 + **实时预览菜单栏渲染效果**（其招牌交互）；单条合并模式适配刘海屏；用户自定义告警规则（Rules & Notifications）；设置导出/导入是用户挚爱功能；自我营销"最省 CPU 的监控应用"——**效率本身是卖点**。反面：模块多易挤爆菜单栏、复杂度吓人。<https://www.macworld.com/article/538718/mac-gems-istat-menus-review.html>
- **Stats (exelban)** — 免费开源统治者（~39.5k star）：每个指标是独立可开关 widget；**对自身开销激进诚实**——文档写明监控"is not a cheap task"、点名 Sensors/Bluetooth 最贵、告知关掉可省 ~50% 能耗；39 个社区语言包。反面教材：偏好面板 toggle 堆积、无组织。<https://github.com/exelban/stats>
- **Bartender→Ice 崩塌** — 信任是 UX 功能：Bartender 2024-06 被静默出售（用户从签名证书变化发现），随后默认开启配置上传，信任一夜蒸发；开源 Ice 只覆盖 ~80% 功能、打磨更差，用户照样迁移。**直接验证 agentload local-only 立场是护城河**；也警示：打磨留不住感觉被监视的用户。附：社区明确以"新 macOS 发布日兼容"给工具排名——day-one 兼容是信任信号。<https://www.macstories.net/roundups/managing-your-mac-menu-bar-a-roundup-of-my-favorite-bartender-alternatives/>
- **Raycast** — 键盘优先 onboarding 范本：首启流程内完成全局热键录制；热键录制带冲突检测；overlay 不切窗口不抢上下文；高频调用产品把速度当核心功能。<https://manual.raycast.com/settings>
- **MeetingBar + Itsycal** — glanceable 单值设计：菜单栏只放最重要的一个值 + 倒计时；用户可调字符宽度、icon-only 模式；Itsycal 评测给出品类最锋利的表述——赢在"**rigorously subtracting friction, not adding features**"，点开"无导航、无延迟、无抢焦点的模态窗"。<https://meetingbar.app/>
- **Loop / Rectangle** — 默认值即产品：用户因"记不住组合键"从 Rectangle 逃到 Loop；Loop 赢在可发现性（径向菜单展示全部动作 + **preview-before-commit**：先看结果再松手确认）和避冲突默认值（双击 Shift 触发，专为避开与其他 app 的和弦冲突）。<https://github.com/MrKai77/Loop>
- **CleanShot X** — "最舒服"的终局表述："差异看似微小，影响巨大——Details matter!"；"安静待在菜单栏，需要时就绪"；最终状态是"**quietly becomes infrastructure——卸了它一切都变慢**"。唯一被骂点：强推自家云——**云必须可选且可隐藏**。<https://cleanshot.com/testimonials>

## 2. 平台规范与技术坑

- **Apple HIG（菜单栏 extra）**：Apple 明确建议点击 status item 应"Display a menu — not a popover"；实践者共识 popover"never felt right"（微延迟、消失方式不自然、像浮窗不像系统件）。规范要点：template image（isTemplate=true）自动明暗适配；左键主 UI / 右键上下文菜单（Quit/Prefs）是预期双重可供性；动态图标颜色仅用于有意义的状态变化；空间不足时 macOS 会移除 extra、用户可移除（removalAllowed）——用 KVO 观察并优雅处置；必须设 accessibility 描述。<https://developer.apple.com/design/human-interface-guidelines/the-menu-bar>
- **WKWebView-in-NSPopover 已知坑**（直接命中 agentload 架构）：WKWebView 跨进程渲染与菜单栏窗口层级打架（字符面板压在 popover 下，需子类化 + NSTextInputClient）；`NSPopover.contentSize` 会静默裁剪溢出的 web 内容；焦点/激活怪癖；全屏 app 场景鼠标一动 popover 即消失——Apple 建议 fallback 到浮动 NSPanel；macOS 26.2 回归使 evaluateJavaScript 回调 ~3s（正常 ~200ms）；WebKit 比原生壳多 ~150MB 内存。**用户实际感知的就是：首绘延迟/白闪、非原生滚动与右键菜单、抢焦点——恰是 Itsycal/CleanShot 被夸"没有"的东西**。可行缓解：预热 web view、popover 背景色匹配主题、钉死 contentSize；每次 macOS 更新都是 JS 桥/窗口层级回归风险，需常设 QA 清单。<https://techconcepts.org/blog/macos-menu-bar-swiftui-nspopover>
- **Sparkle vs App Store**：Sparkle 2（MIT）是直接分发事实标准（EdDSA 签名、delta 更新、沙箱支持）；MAS 免托管但强制沙箱（挡掉部分系统 API）；**单 target 双 scheme 混合分发已被验证——但 MAS 构建绝不能捆 Sparkle，否则被拒**；实践者忠告：v1 就要带自动更新，否则 v1 用户永远无更新路径；10.14.5 起店外分发必须公证。<https://sparkle-project.org/>
- **Launch-at-login + 无障碍**：必须 opt-in、默认关（App Review 要求用户同意；最佳模式是欢迎页上给开关）；macOS 13+ 用 SMAppService.mainApp；Ventura 起任何后台项都会触发系统通知——静默注册显得鬼祟；已知坑 SMAppServiceStatusNotFound 需优雅处理。VoiceOver：VO-M-M 逐个报 status item 名称——未标注的 NSStatusItem 被念成泛称，**状态按钮的 accessibility label + 下拉全键盘导航是底线**。<https://nilcoalescing.com/blog/LaunchAtLoginSetting/>
- **能耗礼仪 = 卸载阈值**：真实差评："uses more energy than Activity Monitor... uninstalling"；macOS 电池菜单公开点名"Apps Using Significant Energy"（按 CPU/磁盘 I/O/网络计分）——**上榜即死**。市场规范：可配置刷新频率是预期而非可选；微脚印当营销（"2.2 MB on disk"）；昂贵轮询按模块可关；厂商以"最省 CPU"互相竞争。另一个被点名的卸载触发器：**通知设置不持久化**。<https://developer.apple.com/library/archive/documentation/Performance/Conceptual/power_efficiency_guidelines_osx/MonitoringBatteryUsage.html>

## 3. 讨喜 Table-Stakes 清单（11 条，验收时逐条对照）

1. **Glanceability**：菜单栏只显一个有意义的值/状态（如活跃会话数或状态 glyph）；template image 自动明暗；icon-only 模式 + 用户可调宽度适配刘海/拥挤栏；颜色只用于有意义的状态变化。
2. **零配置首启**：60 秒内不碰任何设置即可用；默认值专为避冲突挑选（Loop 双击 Shift 教训）；权限与 launch-at-login 在欢迎页 opt-in 并解释，绝不静默注册。
3. **Popover 礼仪**（agentload 最大架构风险）：即开无白闪（预热 WKWebView、背景色匹配主题、钉死 contentSize）；绝不从用户当前 app 抢焦点；点外部与 Esc 消失；他人全屏时可用（NSPanel fallback）；保留 web 面板则须为每个 macOS 版本预算窗口层级/JS 桥回归 QA。
4. **性能预算**：绝不出现在"Apps Using Significant Energy"；刷新节奏用户可配；昂贵轮询（进程扫描、传感器）按模块可关；像 Stats 一样发布诚实的逐功能成本说明；WebKit ~150MB 内存要有辩护、考虑懒加载面板。
5. **键盘 + 无障碍**：全局热键切换 popover（带冲突检测录制器，Raycast 式）；Esc 关闭；下拉全键盘可导航；NSStatusItem 有 accessibility label（VO-M-M 有意义播报）。
6. **通知礼仪**：通知只来自用户创建的规则（iStat 式"当 X 时提醒我"）；设置必须真持久化（不持久是被点名的卸载触发器）；永无营销/催促通知。
7. **偏好设计**：每个选项配菜单栏效果实时预览（iStat 招牌）；拖放排序；逐模块开关；设置导出/导入；避免 Stats 式无组织 toggle 堆。
8. **信任即 UX**：local-only 默认，任何云/同步严格 opt-in 且可隐藏（CleanShot 唯一被骂点）；所有权与数据流透明（Bartender 静默出售杀死了品类冠军）；开源或行为可审计；新 macOS 版本 day-one 兼容。
9. **生命周期**：v1 即自动更新（Sparkle 直发 + MAS 混合单 target 已验证；MAS 构建绝不捆 Sparkle）；公证；用户移除 status item 或 macOS 挤掉时优雅处置（removalAllowed + KVO）；右键上下文菜单含 Quit/Preferences。
10. **北极星框架**："最舒服" = CleanShot 的"quietly becomes infrastructure" + Itsycal 的"rigorously subtracting friction"。agentload 的差异化（证据中立、local-first）恰好落在翻转 Bartender/Ice 市场的信任轴上——**制胜组合是 Ice 的信任 + Bartender 的打磨，目前无人做到**。
11. **待解战略张力**：WKWebView popover 是原生感的主要威胁。选项按投入递增：激进预热+样式伪装；glance 摘要下沉为原生 NSMenu/SwiftUI 层、web 面板降为次要窗口；popover 全原生、React 只留浏览器面板。→ M05_S03 承接决策。

## 来源与准确性

- 更新时间：2026-07-26。出处类型：Apple 官方 HIG/性能指南/开发者论坛（高）、各应用 GitHub 仓库与官网（高）、媒体评测与博客（中）、App Store 用户评论（个体样本，作风向参考）。
- 风险提示：macOS 26.x 的 WKWebView 回归细节随系统更新变动，实施 M05_S03 前应复测；MAS 审核口径可能调整。
- 主要链接：<https://www.macworld.com/article/538718/mac-gems-istat-menus-review.html> · <https://bjango.com/mac/istatmenus/> · <https://github.com/exelban/stats> · <https://www.macstories.net/roundups/managing-your-mac-menu-bar-a-roundup-of-my-favorite-bartender-alternatives/> · <https://github.com/jordanbaird/Ice> · <https://manual.raycast.com/settings> · <https://meetingbar.app/> · <https://www.podfeet.com/blog/2020/06/itsycal/> · <https://github.com/MrKai77/Loop> · <https://cleanshot.com/testimonials> · <https://developer.apple.com/design/human-interface-guidelines/the-menu-bar> · <https://multi.app/blog/pushing-the-limits-nsstatusitem> · <https://techconcepts.org/blog/macos-menu-bar-swiftui-nspopover> · <https://developer.apple.com/forums/thread/733688> · <https://developer.apple.com/forums/thread/772145> · <https://developer.apple.com/forums/thread/810699> · <https://sparkle-project.org/> · <https://www.avanderlee.com/xcode/sparkle-distribution-apps-in-and-out-of-the-mac-app-store/> · <https://nilcoalescing.com/blog/LaunchAtLoginSetting/> · <https://support.apple.com/guide/voiceover/menu-bar-and-control-center-mchlp2748/mac> · <https://developer.apple.com/library/archive/documentation/Performance/Conceptual/power_efficiency_guidelines_osx/MonitoringBatteryUsage.html> · <https://apps.apple.com/us/app/menubar-stats/id714196447>
