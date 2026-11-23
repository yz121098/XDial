import XCTest
@testable import XDial

@MainActor
final class CloudHostSetupTests: XCTestCase {
    func testDecodesWhatTheGoEngineReturns() throws {
        let report = CloudHostSetupModel.previewDecode(CloudHostReport.self, """
        {"host_key":{"fingerprint":"SHA256:abc","algorithm":"ssh-ed25519","file":"/etc/ssh/ssh_host_ed25519_key.pub"},
         "system":"Ubuntu 24.04","architecture":"aarch64","privilege":"sudo-password","ufw_active":false,
         "listening_ports":[22],"suggested_port":43111,
         "existing":{"port":43111,"method":"2022-blake3-aes-128-gcm","active":true},
         "blocking":[],"notes":[{"code":"security_group","message":"中文","message_en":"English"}]}
        """)
        XCTAssertEqual(report.hostKey.file, "/etc/ssh/ssh_host_ed25519_key.pub")
        XCTAssertEqual(report.existing?.port, 43111)
        XCTAssertEqual(report.notes.first?.text(false), "English")

        let plan = CloudHostSetupModel.previewDecode(CloudHostPlan.self,
            #"{"port":43111,"method":"2022-blake3-aes-128-gcm","reuse":true,"steps":null}"#)
        XCTAssertTrue(plan.reuse)
        XCTAssertTrue(plan.steps.isEmpty, "a reused proxy has no steps; Go encodes an empty list as null")

        let measurement = CloudHostSetupModel.previewDecode(CloudHostMeasurement.self,
            #"{"latency_ms":189,"exit_ip":"203.0.113.10","download_bytes":10000000,"download_ms":250,"mbps":320.5,"observed_at":1}"#)
        XCTAssertEqual(measurement.latencyMS, 189)
    }

    func testProgressShowsOneRowPerStepWithItsLatestStatus() {
        let model = CloudHostSetupModel()
        model.preview(stage: .deploying, events: [
            CloudHostEvent(step: "prepare", status: "running", message: nil),
            CloudHostEvent(step: "prepare", status: "done", message: nil),
            CloudHostEvent(step: "install", status: "running", message: nil),
            CloudHostEvent(step: "install", status: "failed", message: "x"),
            CloudHostEvent(step: "prepare", status: "rolled_back", message: nil),
        ])
        XCTAssertEqual(model.stepProgress.map(\.step), ["prepare", "install"])
        XCTAssertEqual(model.stepProgress.map(\.status), ["rolled_back", "failed"])
    }

    func testOnlyAVerifiedProxyCanBeSaved() {
        let model = CloudHostSetupModel()
        model.host = "203.0.113.10"
        let deployed = CloudHostResult(server: "203.0.113.10", port: 43111, method: "2022-blake3-aes-128-gcm",
                                       password: "AAAAAAAAAAAAAAAAAAAAAA==", reused: false)
        model.preview(stage: .unverified(deployed, CloudHostMessage(code: "proxy_unreachable", message: "", messageEN: "")))
        XCTAssertNil(model.draft(), "a proxy that HTTPS did not pass through must not be saved")

        let measurement = CloudHostSetupModel.previewDecode(CloudHostMeasurement.self,
            #"{"latency_ms":1,"download_bytes":0,"download_ms":0,"mbps":0}"#)
        model.preview(stage: .verified(deployed, measurement))
        let draft = try? XCTUnwrap(model.draft())
        XCTAssertEqual(draft?.name, "云主机 · 203.0.113.10")

        let line = Line(cloudHost: draft!)
        XCTAssertEqual(line.type, "shadowsocks")
        XCTAssertEqual(line.ssServer, "203.0.113.10")
        XCTAssertEqual(line.ssPort, 43111)
        XCTAssertEqual(line.ssMethod, "2022-blake3-aes-128-gcm")
        XCTAssertEqual(line.ssPassword, "AAAAAAAAAAAAAAAAAAAAAA==")
        XCTAssertTrue(line.enabled)
        XCTAssertTrue(line.verified)

        let realityDeployed = CloudHostSetupModel.previewDecode(CloudHostResult.self, """
        {"protocol":"vless-reality","server":"203.0.113.10","port":443,"method":"vless-reality",
         "uuid":"b831381d-6324-4d53-ad4f-8cda48b30811","flow":"xtls-rprx-vision",
         "server_name":"www.microsoft.com","public_key":"cHVibGljLWtleS10aGF0LWlzLTMyLWJ5dGVzLWxvbmc",
         "short_id":"0123456789abcdef","fingerprint":"chrome","reused":false}
        """)
        model.preview(stage: .verified(realityDeployed, measurement))
        let realityDraft = try? XCTUnwrap(model.draft())
        let realityLine = Line(cloudHost: realityDraft!)
        XCTAssertEqual(realityLine.type, "vless")
        XCTAssertEqual(realityLine.vlessServer, "203.0.113.10")
        XCTAssertEqual(realityLine.vlessPort, 443)
        XCTAssertEqual(realityLine.vlessUUID, "b831381d-6324-4d53-ad4f-8cda48b30811")
        XCTAssertEqual(realityLine.vlessFlow, "xtls-rprx-vision")
        XCTAssertEqual(realityLine.vlessSNI, "www.microsoft.com")
        XCTAssertEqual(realityLine.vlessRealityPublicKey, "cHVibGljLWtleS10aGF0LWlzLTMyLWJ5dGVzLWxvbmc")
        XCTAssertEqual(realityLine.vlessRealityShortID, "0123456789abcdef")
        XCTAssertEqual(realityLine.vlessClientFingerprint, "chrome")
        XCTAssertTrue(realityLine.udp)
        XCTAssertTrue(realityLine.verified)
    }

    func testEditingThePortInvalidatesThePlan() async {
        let model = CloudHostSetupModel()
        let report = CloudHostSetupModel.previewDecode(CloudHostReport.self, """
        {"host_key":{"fingerprint":"SHA256:abc","algorithm":"ssh-ed25519","file":"/etc/ssh/ssh_host_ed25519_key.pub"},
         "system":"Ubuntu 24.04","architecture":"aarch64","privilege":"root","ufw_active":false,
         "listening_ports":[22],"suggested_port":43111,"blocking":[],"notes":[]}
        """)
        let plan = CloudHostSetupModel.previewDecode(CloudHostPlan.self,
            #"{"port":43111,"method":"2022-blake3-aes-128-gcm","reuse":false,"steps":[]}"#)
        model.proxyPort = "43111"
        model.preview(stage: .review(report), plan: plan)
        XCTAssertTrue(model.canDeploy)

        model.proxyPort = "43112"
        XCTAssertNil(model.plan, "a plan for the old port must not survive a port edit")
        XCTAssertFalse(model.canDeploy)

        model.proxyPort = "43111"
        XCTAssertNil(model.plan, "returning to the old port still needs a fresh plan")
        model.proxyPort = "80"
        await model.refreshPlan()
        XCTAssertEqual(model.planError?.code, "port_invalid")
        XCTAssertFalse(model.planning)
        XCTAssertFalse(model.canDeploy)
    }

    func testScanNeedsAddressUserAndOneCredential() {
        let model = CloudHostSetupModel()
        XCTAssertFalse(model.canScan)
        model.host = "203.0.113.10"
        XCTAssertFalse(model.canScan, "no credential yet")
        model.password = "secret"
        XCTAssertTrue(model.canScan)
        model.sshPort = "70000"
        XCTAssertFalse(model.canScan)
        model.sshPort = "22"
        model.authentication = .privateKey
        XCTAssertFalse(model.canScan, "a private key is required in key mode")
        model.privateKey = "-----BEGIN OPENSSH PRIVATE KEY-----"
        XCTAssertTrue(model.canScan)
    }

    func testClosingForgetsTheCredentials() {
        let model = CloudHostSetupModel()
        model.password = "secret"
        model.privateKey = "key"
        model.passphrase = "phrase"
        model.close()
        XCTAssertEqual(model.password, "")
        XCTAssertEqual(model.privateKey, "")
        XCTAssertEqual(model.passphrase, "")
    }

    func testDisclosureNamesTheRealTestEndpointsAndVolume() {
        for zh in [true, false] {
            let test = CloudHostDisclosure.test(zh)
            for endpoint in ["www.gstatic.com", "1.1.1.1", "speed.cloudflare.com", "10 MB", "12"] {
                XCTAssertTrue(test.contains(endpoint), endpoint)
            }
            XCTAssertTrue(CloudHostDisclosure.sshAndInstall(zh).contains("deb.sagernet.org"))
            let notice = DataUseNotice.sections(isChinese: zh, companyPreset: false).flatMap(\.points).joined()
            XCTAssertTrue(notice.contains("speed.cloudflare.com"), "the data use notice covers My Server")
        }
    }
}
