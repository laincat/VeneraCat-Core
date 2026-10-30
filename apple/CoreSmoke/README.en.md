# CoreSmoke

English | [简体中文](README.md)

Links `build/VeneraCatCore.xcframework` (built by `scripts/mac-verify.sh`) and calls Core from Swift: it installs a comic source, runs a search through a fake host, checks exact BigInt storage, MD5 and error handling, and measures per-source install time.

```sh
cd apple/CoreSmoke
swift test                       # macOS slice
xcodebuild test -scheme CoreSmoke \
  -destination 'platform=iOS Simulator,name=iPhone 17'   # iOS simulator slice
```

The Swift names (`MobileHostProtocol`, `MobileNew`, method labels) follow the Objective-C header gobind generates. If the compiler reports a different spelling, follow the generated `VeneraCatCore.framework/Headers/Mobile.objc.h`. A Go method returning `(string, error)` becomes a Swift method with an `error: NSErrorPointer` parameter; one returning only `error` becomes a `throws` method.
