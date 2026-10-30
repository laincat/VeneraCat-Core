# CoreSmoke

[English](README.en.md) | 简体中文

链接 `build/VeneraCatCore.xcframework`(由 `scripts/mac-verify.sh` 生成),从 Swift 调用 Core:安装一个漫画源,通过伪造的宿主执行搜索,检查大整数存储是否精确、MD5 和错误处理,并测量每个源的安装耗时。

```sh
cd apple/CoreSmoke
swift test                       # macOS 版本
xcodebuild test -scheme CoreSmoke \
  -destination 'platform=iOS Simulator,name=iPhone 17'   # iOS 模拟器版本
```

Swift 中的名称(`MobileHostProtocol`、`MobileNew`、方法参数标签)按 gobind 生成的 Objective-C 头文件书写。如果编译器报出不同的拼写,以生成的 `VeneraCatCore.framework/Headers/Mobile.objc.h` 为准。Go 方法返回 `(string, error)` 时,Swift 中是带 `error: NSErrorPointer` 参数的方法;只返回 `error` 时是 `throws` 方法。
