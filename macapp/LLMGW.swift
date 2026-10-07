// LLMGW menu bar app: starts and stops the gateway, shows its status, opens the dashboard, chat and docs.
// Build with macapp/build.sh (Command Line Tools are enough, no Xcode project).
import AppKit
import ServiceManagement
import SwiftUI

// ---------- languages (EN default, FR, NL) ----------

let strings: [String: [String: String]] = [
    "running": ["en": "Running", "fr": "En marche", "nl": "Actief"],
    "external": ["en": "Running (started elsewhere)", "fr": "En marche (lancée ailleurs)", "nl": "Actief (elders gestart)"],
    "starting": ["en": "Starting…", "fr": "Démarrage…", "nl": "Starten…"],
    "stopped": ["en": "Stopped", "fr": "Arrêtée", "nl": "Gestopt"],
    "nofolder": ["en": "Gateway folder not found", "fr": "Dossier de la passerelle introuvable", "nl": "Gatewaymap niet gevonden"],
    "start": ["en": "Start", "fr": "Démarrer", "nl": "Starten"],
    "stop": ["en": "Stop", "fr": "Arrêter", "nl": "Stoppen"],
    "restart": ["en": "Restart", "fr": "Redémarrer", "nl": "Herstarten"],
    "dashboard": ["en": "Open the dashboard", "fr": "Ouvrir le dashboard", "nl": "Dashboard openen"],
    "chat": ["en": "Open the chat", "fr": "Ouvrir le chat", "nl": "Chat openen"],
    "docs": ["en": "Documentation", "fr": "Documentation", "nl": "Documentatie"],
    "logs": ["en": "Show the log", "fr": "Afficher le journal", "nl": "Logboek tonen"],
    "folder": ["en": "Show the gateway folder", "fr": "Afficher le dossier de la passerelle", "nl": "Gatewaymap tonen"],
    "choose": ["en": "Choose the gateway folder…", "fr": "Choisir le dossier de la passerelle…", "nl": "Gatewaymap kiezen…"],
    "login": ["en": "Open at login", "fr": "Ouvrir à l'ouverture de session", "nl": "Openen bij aanmelden"],
    "language": ["en": "Language", "fr": "Langue", "nl": "Taal"],
    "quit": ["en": "Quit (stops the gateway)", "fr": "Quitter (arrête la passerelle)", "nl": "Stoppen (stopt de gateway)"],
    "pick": ["en": "Choose the folder that contains llmgw and config.toml", "fr": "Choisissez le dossier qui contient llmgw et config.toml", "nl": "Kies de map met llmgw en config.toml"],
    "missing": ["en": "llmgw or config.toml is missing in this folder.", "fr": "llmgw ou config.toml manque dans ce dossier.", "nl": "llmgw of config.toml ontbreekt in deze map."],
    "loginerr": ["en": "The login item could not be changed: ", "fr": "Impossible de modifier l'ouverture à la session : ", "nl": "Het aanmeldonderdeel kon niet worden gewijzigd: "],
    "exited": ["en": "The gateway stopped. See the log for the reason.", "fr": "La passerelle s'est arrêtée. Le motif est dans le journal.", "nl": "De gateway is gestopt. Zie het logboek voor de reden."],
]

// ---------- gateway process ----------

@MainActor
final class Gateway: ObservableObject {
    enum State { case stopped, starting, running, external }

    @Published var state: State = .stopped
    @Published var version = ""
    @Published var folder: URL?
    @Published var lang: String
    @Published var loginItem = SMAppService.mainApp.status == .enabled
    @Published var lastError = ""

    private var proc: Process?
    private var listen = "127.0.0.1:8088"
    private var timer: Timer?
    private var stopping = false

    init() {
        let saved = UserDefaults.standard.string(forKey: "lang")
        let sys = Locale.preferredLanguages.first?.prefix(2).lowercased() ?? "en"
        lang = saved ?? (["fr", "nl"].contains(sys) ? String(sys) : "en")
        folder = Gateway.findFolder()
        readConfig()
        timer = Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { _ in Task { @MainActor in await self.poll() } }
        Task {
            await poll()
            if state == .stopped { start() }
        }
    }

    func t(_ k: String) -> String { strings[k]?[lang] ?? strings[k]?["en"] ?? k }

    func setLang(_ l: String) {
        lang = l
        UserDefaults.standard.set(l, forKey: "lang")
    }

    // The app next to config.toml (repository folder), otherwise the folder chosen earlier.
    static func findFolder() -> URL? {
        let fm = FileManager.default
        let ok = { (u: URL) in fm.fileExists(atPath: u.appendingPathComponent("config.toml").path) && fm.fileExists(atPath: u.appendingPathComponent("llmgw").path) }
        let beside = Bundle.main.bundleURL.deletingLastPathComponent()
        if ok(beside) {
            UserDefaults.standard.set(beside.path, forKey: "folder")
            return beside
        }
        if let p = UserDefaults.standard.string(forKey: "folder"), ok(URL(fileURLWithPath: p)) { return URL(fileURLWithPath: p) }
        return nil
    }

    func readConfig() {
        guard let f = folder, let txt = try? String(contentsOf: f.appendingPathComponent("config.toml"), encoding: .utf8) else { return }
        if let r = txt.range(of: #"(?m)^\s*listen\s*=\s*"([^"]+)""#, options: .regularExpression) {
            let line = String(txt[r])
            if let q1 = line.firstIndex(of: "\""), let q2 = line.lastIndex(of: "\""), q1 < q2 {
                listen = String(line[line.index(after: q1)..<q2]).replacingOccurrences(of: "0.0.0.0", with: "127.0.0.1")
            }
        }
    }

    var base: String { "http://" + (listen.hasPrefix(":") ? "127.0.0.1" + listen : listen) }
    var port: String { String(listen.split(separator: ":").last ?? "8088") }
    var ours: Bool { proc?.isRunning ?? false }

    func poll() async {
        var ok = false
        if let u = URL(string: base + "/version") {
            var req = URLRequest(url: u)
            req.timeoutInterval = 1.5
            if let (d, r) = try? await URLSession.shared.data(for: req), (r as? HTTPURLResponse)?.statusCode == 200,
               let j = try? JSONSerialization.jsonObject(with: d) as? [String: Any] {
                version = j["version"] as? String ?? ""
                ok = true
            }
        }
        state = ok ? (ours ? .running : .external) : (ours ? .starting : .stopped)
    }

    func start() {
        guard let f = folder else { chooseFolder(); return }
        guard state == .stopped else { return }
        readConfig()
        lastError = ""
        let logURL = f.appendingPathComponent("llmgw.log")
        if !FileManager.default.fileExists(atPath: logURL.path) { FileManager.default.createFile(atPath: logURL.path, contents: nil) }
        guard let log = try? FileHandle(forWritingTo: logURL) else { return }
        log.seekToEndOfFile()
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/zsh")
        // secrets.env holds the provider keys and the SSO secrets (never in config.toml)
        p.arguments = ["-c", "set -a; [ -f secrets.env ] && . ./secrets.env; set +a; exec ./llmgw -config config.toml"]
        p.currentDirectoryURL = f
        p.standardOutput = log
        p.standardError = log
        p.terminationHandler = { _ in
            Task { @MainActor in
                if !self.stopping { self.lastError = self.t("exited") }
                self.stopping = false
                await self.poll()
            }
        }
        do {
            try p.run()
            proc = p
            state = .starting
        } catch {
            lastError = error.localizedDescription
        }
    }

    func stop() {
        stopping = true
        if let p = proc, p.isRunning {
            p.terminate() // SIGTERM: the gateway flushes its metrics before exiting
        } else {
            for pid in listeningGatewayPIDs() { kill(pid, SIGTERM) }
        }
        state = .stopped
    }

    func restart() {
        stop()
        Task {
            for _ in 0..<25 { // wait for the port to be released (5 s)
                try? await Task.sleep(nanoseconds: 200_000_000)
                if listeningGatewayPIDs().isEmpty && !ours { break }
            }
            state = .stopped
            start()
        }
    }

    // PIDs listening on the gateway port whose command is llmgw (never another program).
    func listeningGatewayPIDs() -> [pid_t] {
        let out = run("/usr/sbin/lsof", ["-tiTCP:" + port, "-sTCP:LISTEN"])
        return out.split(separator: "\n").compactMap { pid_t($0) }.filter {
            run("/bin/ps", ["-o", "comm=", "-p", String($0)]).contains("llmgw")
        }
    }

    private func run(_ exe: String, _ args: [String]) -> String {
        let p = Process(), pipe = Pipe()
        p.executableURL = URL(fileURLWithPath: exe)
        p.arguments = args
        p.standardOutput = pipe
        p.standardError = FileHandle.nullDevice
        guard (try? p.run()) != nil else { return "" }
        p.waitUntilExit()
        return String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
    }

    func open(_ path: String) { if let u = URL(string: base + path) { NSWorkspace.shared.open(u) } }

    func openLog() {
        guard let f = folder else { return }
        let u = f.appendingPathComponent("llmgw.log")
        NSWorkspace.shared.open([u], withApplicationAt: URL(fileURLWithPath: "/System/Applications/Utilities/Console.app"), configuration: NSWorkspace.OpenConfiguration())
    }

    func showFolder() { if let f = folder { NSWorkspace.shared.activateFileViewerSelecting([f.appendingPathComponent("config.toml")]) } }

    func chooseFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.message = t("pick")
        NSApp.activate(ignoringOtherApps: true)
        guard panel.runModal() == .OK, let u = panel.url else { return }
        let fm = FileManager.default
        guard fm.fileExists(atPath: u.appendingPathComponent("config.toml").path), fm.fileExists(atPath: u.appendingPathComponent("llmgw").path) else {
            lastError = t("missing")
            return
        }
        UserDefaults.standard.set(u.path, forKey: "folder")
        folder = u
        readConfig()
        Task { await poll() }
    }

    func toggleLogin() {
        do {
            if SMAppService.mainApp.status == .enabled { try SMAppService.mainApp.unregister() } else { try SMAppService.mainApp.register() }
        } catch {
            lastError = t("loginerr") + error.localizedDescription
        }
        loginItem = SMAppService.mainApp.status == .enabled
    }

    func quit() {
        if ours {
            stopping = true
            proc?.terminate()
            proc?.waitUntilExit()
        }
        NSApp.terminate(nil)
    }

    // Menu bar icon: two arrows (the gateway) with a status dot, redrawn for light and dark menu bars.
    var icon: NSImage {
        let dot: NSColor = switch state {
        case .running, .external: .systemGreen
        case .starting: .systemOrange
        case .stopped: folder == nil ? .systemGray : .systemRed
        }
        let img = NSImage(size: NSSize(width: 22, height: 18), flipped: false) { r in
            let cfg = NSImage.SymbolConfiguration(pointSize: 14, weight: .semibold)
            if let sym = NSImage(systemSymbolName: "arrow.left.arrow.right.circle", accessibilityDescription: "LLMGW")?.withSymbolConfiguration(cfg) {
                let s = sym.size
                let box = NSRect(x: 1, y: (r.height - s.height) / 2, width: s.width, height: s.height)
                let tinted = NSImage(size: s, flipped: false) { rr in
                    sym.draw(in: rr)
                    NSColor.labelColor.set()
                    rr.fill(using: .sourceAtop)
                    return true
                }
                tinted.draw(in: box)
            }
            let d = NSRect(x: r.width - 8, y: 1, width: 7, height: 7)
            NSColor.windowBackgroundColor.setFill()
            NSBezierPath(ovalIn: d.insetBy(dx: -1, dy: -1)).fill()
            dot.setFill()
            NSBezierPath(ovalIn: d).fill()
            return true
        }
        img.isTemplate = false
        return img
    }

    var statusLine: String {
        guard folder != nil else { return t("nofolder") }
        let s: String = switch state {
        case .running: t("running")
        case .external: t("external")
        case .starting: t("starting")
        case .stopped: t("stopped")
        }
        return "LLMGW \(version.isEmpty ? "" : version + " · ")\(s) · :\(port)"
    }
}

// ---------- menu ----------

struct MenuContent: View {
    @ObservedObject var gw: Gateway

    var body: some View {
        let up = gw.state == .running || gw.state == .external
        Text(gw.statusLine)
        if !gw.lastError.isEmpty { Text("⚠ " + gw.lastError) }
        Divider()
        if gw.state == .stopped {
            Button(gw.t("start")) { gw.start() }.keyboardShortcut("s")
        } else {
            Button(gw.t("stop")) { gw.stop() }
        }
        Button(gw.t("restart")) { gw.restart() }.keyboardShortcut("r").disabled(gw.folder == nil)
        Divider()
        Button(gw.t("dashboard")) { gw.open("/dashboard") }.keyboardShortcut("d").disabled(!up)
        Button(gw.t("chat")) { gw.open("/chat") }.disabled(!up)
        Button(gw.t("docs")) { gw.open("/docs/") }.disabled(!up)
        Divider()
        Button(gw.t("logs")) { gw.openLog() }.disabled(gw.folder == nil)
        Button(gw.t("folder")) { gw.showFolder() }.disabled(gw.folder == nil)
        Button(gw.t("choose")) { gw.chooseFolder() }
        Toggle(gw.t("login"), isOn: Binding(get: { gw.loginItem }, set: { _ in gw.toggleLogin() }))
        Menu(gw.t("language")) {
            ForEach([("en", "English"), ("fr", "Français"), ("nl", "Nederlands")], id: \.0) { code, name in
                Button((gw.lang == code ? "✓ " : "    ") + name) { gw.setLang(code) }
            }
        }
        Divider()
        Text("by Vulpoint.com")
        Button(gw.t("quit")) { gw.quit() }.keyboardShortcut("q")
    }
}

@main
struct LLMGWApp: App {
    @StateObject private var gw = Gateway()

    var body: some Scene {
        MenuBarExtra {
            MenuContent(gw: gw)
        } label: {
            Image(nsImage: gw.icon)
        }
    }
}
