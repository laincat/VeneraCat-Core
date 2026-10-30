// swift-tools-version:5.9
// Smoke test that links build/VeneraCatCore.xcframework and calls Core from
// Swift. Run on macOS after scripts/mac-verify.sh:
//
//   cd apple/CoreSmoke
//   swift test                                   # macOS slice
//   xcodebuild test -scheme CoreSmoke \
//     -destination 'platform=iOS Simulator,name=iPhone 17'   # iOS simulator slice
import PackageDescription

let package = Package(
    name: "CoreSmoke",
    platforms: [.iOS(.v16), .macOS(.v13)],
    targets: [
        .binaryTarget(name: "VeneraCatCore", path: "../../build/VeneraCatCore.xcframework"),
        .testTarget(name: "CoreSmokeTests", dependencies: ["VeneraCatCore"]),
    ]
)
