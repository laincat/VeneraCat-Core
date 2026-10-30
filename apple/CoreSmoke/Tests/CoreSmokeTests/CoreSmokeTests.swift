import Foundation
import VeneraCatCore
import XCTest

/// Plays the client: answers HTTP from a table, keeps data in memory.
final class FakeHost: NSObject, MobileHostProtocol {
    var routes: [String: String] = [:]
    var data: [String: String] = [:]
    private let lock = NSLock()

    func http(_ requestJSON: String?, error: NSErrorPointer) -> String {
        guard let json = requestJSON?.data(using: .utf8),
              let req = try? JSONSerialization.jsonObject(with: json) as? [String: Any],
              let url = req["url"] as? String
        else { return "" }
        lock.lock(); defer { lock.unlock() }
        let body = routes.first { url.hasPrefix($0.key) }?.value ?? ""
        let resp: [String: Any] = [
            "status": body.isEmpty ? 404 : 200,
            "headers": ["Content-Type": ["text/html"]],
            "bodyBase64": Data(body.utf8).base64EncodedString(),
            "finalUrl": url,
        ]
        return String(data: try! JSONSerialization.data(withJSONObject: resp), encoding: .utf8)!
    }

    func cancelHTTP(_ id: Int64) {}
    func verify(_ challengeJSON: String?, error: NSErrorPointer) -> String { #"{"userAgent":"Smoke"}"# }
    func loadData(_ key: String?, dataKey: String?, error: NSErrorPointer) -> String {
        lock.lock(); defer { lock.unlock() }
        return data["\(key ?? "")/\(dataKey ?? "")"] ?? ""
    }
    func saveData(_ key: String?, dataKey: String?, valueJSON: String?) throws {
        lock.lock(); defer { lock.unlock() }
        data["\(key ?? "")/\(dataKey ?? "")"] = valueJSON
    }
    func deleteData(_ key: String?, dataKey: String?) throws {
        lock.lock(); defer { lock.unlock() }
        data["\(key ?? "")/\(dataKey ?? "")"] = nil
    }
    func loadSetting(_ key: String?, settingKey: String?, error: NSErrorPointer) -> String { "" }
    func isLogged(_ key: String?) -> Bool { false }
    func log(_ level: String?, title: String?, content: String?) {}

    // Cookies: one jar per partition, keyed by cookie name.
    var jars: [String: [String: [String: Any]]] = [:]
    func loadCookies(_ partition: String?, url: String?, error: NSErrorPointer) -> String {
        lock.lock(); defer { lock.unlock() }
        let list = Array((jars[partition ?? ""] ?? [:]).values)
        return String(data: try! JSONSerialization.data(withJSONObject: list), encoding: .utf8)!
    }
    func saveCookies(_ partition: String?, url: String?, cookiesJSON: String?) throws {
        let list = try JSONSerialization.jsonObject(with: Data((cookiesJSON ?? "[]").utf8)) as? [[String: Any]] ?? []
        lock.lock(); defer { lock.unlock() }
        for c in list { if let name = c["name"] as? String { jars[partition ?? "", default: [:]][name] = c } }
    }
    func deleteCookies(_ partition: String?, url: String?) throws {
        lock.lock(); defer { lock.unlock() }
        jars[partition ?? ""] = nil
    }

    // UI: answers are taken in order; every call is recorded.
    var answers: [String] = []
    var events: [String] = []
    private func answer(_ event: String) -> String {
        lock.lock(); defer { lock.unlock() }
        events.append(event)
        return answers.isEmpty ? "" : answers.removeFirst()
    }
    func showMessage(_ message: String?) { _ = answer("message \(message ?? "")") }
    func showDialog(_ title: String?, content: String?, actionsJSON: String?, error: NSErrorPointer) -> String {
        answer("dialog \(title ?? "")")
    }
    func showInputDialog(_ title: String?, image: String?, errorMessage: String?, error: NSErrorPointer) -> String {
        answer("input [\(errorMessage ?? "")]")
    }
    func showSelectDialog(_ title: String?, optionsJSON: String?, initialIndex: Int64, error: NSErrorPointer) -> String {
        answer("select \(initialIndex)")
    }
    func launchURL(_ url: String?) { _ = answer("launch \(url ?? "")") }
    func showLoading(_ id_: Int64) { _ = answer("loading \(id_)") }
    func cancelLoading(_ id_: Int64) { _ = answer("cancelLoading \(id_)") }

    var clipboard = ""
    func writeClipboard(_ text: String?) throws {
        lock.lock(); defer { lock.unlock() }
        clipboard = text ?? ""
    }
    func readClipboard(_ error: NSErrorPointer) -> String {
        lock.lock(); defer { lock.unlock() }
        return clipboard
    }

    func recorded() -> [String] {
        lock.lock(); defer { lock.unlock() }
        return events
    }
}

/// gomobile exports Go methods returning (string, error) as
/// `-> String` with an `error: NSErrorPointer` parameter rather than `throws`.
/// `call` turns that shape into a throwing call.
func call<T>(_ body: (NSErrorPointer) -> T) throws -> T {
    var err: NSError?
    let out = body(&err)
    if let err { throw err }
    return out
}

final class CoreSmokeTests: XCTestCase {
    func makeCore(_ host: FakeHost) throws -> MobileCore {
        var err: NSError?
        let config = #"{"appVersion":"1.6.0","locale":"zh_CN","platform":"ios","defaultUserAgent":"Smoke/1"}"#
        guard let core = MobileNew(config, host, &err) else { throw err ?? NSError(domain: "smoke", code: 1) }
        return core
    }

    func testComicSourceEndToEnd() throws {
        let host = FakeHost()
        host.routes["https://mini.test/s"] = #"<ul><li><a href="c1">One</a></li><li><a href="c2">Two</a></li></ul>"#
        let core = try makeCore(host)
        defer { try? core.close() }

        let src = """
        class Mini extends ComicSource {
            name = "Mini"; key = "mini"; version = "1.0.0"
            author = {name: "Smoke", url: "https://smoke.test"}
            search = { load: async (k, o, p) => {
                const res = await Network.get("https://mini.test/s?q=" + k, {})
                const doc = new HtmlDocument(res.body)
                const comics = doc.querySelectorAll("a").map(a => new Comic({id: a.attributes.href, title: a.text}))
                doc.dispose()
                this.saveData("big", {n: 12345678901234567890n})
                return {comics, maxPage: 1}
            } }
        }
        """
        let rec = try call { core.install(0, typ: "comic", body: src, origin: "smoke", indexURL: "", sha256: "", error: $0) }
        XCTAssertTrue(rec.contains(#""key":"mini""#), rec)
        XCTAssertTrue(rec.contains(#""name":"Smoke""#), rec)

        let start = Date()
        let out = try call { core.callComic(core.newCall(), key: "mini", path: "search.load", argsJSON: #"["kw", [], 1]"#, error: $0) }
        let elapsed = Date().timeIntervalSince(start)
        XCTAssertTrue(out.contains(#""title":"Two""#), out)
        XCTAssertEqual(host.data["mini/big"], #"{"n":12345678901234567890}"#)
        print("SMOKE search.load took \(Int(elapsed * 1000)) ms")
    }

    func testCryptoAndHashFromCore() throws {
        let core = try makeCore(FakeHost())
        defer { try? core.close() }
        let src = """
        class H extends ComicSource { name = "H"; key = "h"; version = "1"
          go() { return Convert.hexEncode(Convert.md5(Convert.encodeUtf8("abc"))) } }
        """
        _ = try call { core.install(0, typ: "comic", body: src, origin: "", indexURL: "", sha256: "", error: $0) }
        XCTAssertEqual(try call { core.callComic(0, key: "h", path: "go", argsJSON: "[]", error: $0) }, #""900150983cd24fb0d6963f7d28e17f72""#)
    }

    func testBadInputIsAnErrorNotACrash() throws {
        let core = try makeCore(FakeHost())
        defer { try? core.close() }
        XCTAssertThrowsError(try call { core.callComic(0, key: "missing", path: "x", argsJSON: "not json", error: $0) })
        XCTAssertThrowsError(try call { core.restore("{", error: $0) })
    }

    func testCookiesUIAndClipboardReachTheHost() throws {
        let host = FakeHost()
        let core = try makeCore(host)
        defer { try? core.close() }
        let src = """
        class U extends ComicSource { name = "U"; key = "u"; version = "1"
          cookies = async () => {
            Network.setCookies("https://u.test/", [{name: "sid", value: "abc"}])
            return (await Network.getCookies("https://u.test/")).map(c => c.name + "=" + c.value)
          }
          dialog = async () => { let hit = "none"
            await UI.showDialog("T", "C", [{text: "No", callback: () => { hit = "no" }}, {text: "Yes", callback: () => { hit = "yes" }}])
            return hit }
          input = async () => UI.showInputDialog("Code", (t) => t.length < 3 ? "short" : null)
          select = async () => UI.showSelectDialog("Pick", ["a", "b"], 1)
          clip = async () => { await setClipboard("copied"); UI.showMessage("hi"); return getClipboard() }
        }
        """
        _ = try call { core.install(0, typ: "comic", body: src, origin: "", indexURL: "", sha256: "", error: $0) }
        func run(_ path: String) throws -> String {
            try call { core.callComic(0, key: "u", path: path, argsJSON: "[]", error: $0) }
        }

        XCTAssertEqual(try run("cookies"), #"["sid=abc"]"#)
        XCTAssertNotNil(host.jars["comic:u"]?["sid"])

        host.answers = [#"{"index":1}"#]
        XCTAssertEqual(try run("dialog"), #""yes""#)

        host.answers = [#"{"text":"ab"}"#, #"{"text":"abcd"}"#]
        XCTAssertEqual(try run("input"), #""abcd""#)
        XCTAssertTrue(host.recorded().contains("input [short]"), "\(host.recorded())")

        host.answers = [#"{"index":0}"#]
        XCTAssertEqual(try run("select"), "0")
        XCTAssertTrue(host.recorded().contains("select 1"), "\(host.recorded())")

        XCTAssertEqual(try run("clip"), #""copied""#)
        core.loadingDismissed(42) // unknown ids are ignored
    }

    func testHasComicAndIndexStatus() throws {
        let core = try makeCore(FakeHost())
        defer { try? core.close() }
        let src = "class I extends ComicSource { name = 'I'; key = 'idx'; version = '1.0.0'; search = { load: async () => ({comics: []}) } }"
        _ = try call { core.install(0, typ: "comic", body: src, origin: "", indexURL: "https://h.test/index.json", sha256: "", error: $0) }
        XCTAssertTrue(core.hasComic("idx", path: "search.load"))
        XCTAssertFalse(core.hasComic("idx", path: "comic.onImageLoad"))
        XCTAssertFalse(core.hasComic("missing", path: "search.load"))

        let index = #"[{"name":"I","key":"idx","version":"1.1.0","fileName":"i.js"},{"name":"N","key":"n","version":"1","fileName":"n.js"}]"#
        let out = try call { core.compareComicIndex(index, indexURL: "https://h.test/index.json", error: $0) }
        XCTAssertTrue(out.contains(#""key":"idx""#) && out.contains(#""status":"update""#), out)
        XCTAssertTrue(out.contains(#""status":"new""#), out)
    }

    func testSearchOptionsReachSwift() throws {
        let core = try makeCore(FakeHost())
        defer { try? core.close() }
        let src = """
        class S extends ComicSource { name = "S"; key = "s"; version = "1"
          search = {
            optionList: [{label: "排序", options: ["any-任意", "hot-热门"], default: "hot"}],
            load: async () => ({comics: []}) } }
        """
        _ = try call { core.install(0, typ: "comic", body: src, origin: "", indexURL: "", sha256: "", error: $0) }
        let out = try call { core.searchOptions("s", error: $0) }
        XCTAssertTrue(out.contains(#""default":"hot""#), out)
        XCTAssertTrue(out.contains(#""labels":["任意","热门"]"#), out)
        // A source without optionList answers [], so clients can always parse.
        _ = try call { core.install(0, typ: "comic", body: "class T extends ComicSource { name = 'T'; key = 't'; version = '1' }",
                                    origin: "", indexURL: "", sha256: "", error: $0) }
        XCTAssertEqual(try call { core.searchOptions("t", error: $0) }, "[]")
    }

    func testEngineStartupCost() throws {
        let core = try makeCore(FakeHost())
        defer { try? core.close() }
        let n = 20
        let start = Date()
        for i in 0..<n {
            _ = try call {
                core.install(0, typ: "comic",
                             body: "class S\(i) extends ComicSource { name = 'S'; key = 's\(i)'; version = '1' }",
                             origin: "", indexURL: "", sha256: "", error: $0)
            }
        }
        let perSource = Date().timeIntervalSince(start) / Double(n) * 1000
        print("SMOKE install per source \(String(format: "%.1f", perSource)) ms")
        XCTAssertLessThan(perSource, 200)
    }
}
