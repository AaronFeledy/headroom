#include "credentialservice.h"
#include "http_assertions.h"
#include "tls_fixture.h"
#include <QtTest>
#include <QSslServer>
#include <QTcpSocket>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSignalSpy>
#include <QTemporaryDir>

class CredentialHttpFixture : public QSslServer {
public:
    int status = 200;
    QByteArray headers;
    QList<QByteArray> requests;
    QByteArray body = response(QStringLiteral("Cursor"));
    bool holdResponse = false;
    QPointer<QTcpSocket> heldSocket;

    explicit CredentialHttpFixture(bool replacement = false) {
        TlsFixture::configure(*this, replacement);
        connect(this, &QTcpServer::pendingConnectionAvailable, this, [this] {
            while (hasPendingConnections()) {
                auto socket = nextPendingConnection();
                connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
                const auto readRequest = [this, socket] {
                    QByteArray bytes = socket->property("bytes").toByteArray() + socket->readAll();
                    socket->setProperty("bytes", bytes);
                    const qsizetype headersEnd = bytes.indexOf("\r\n\r\n");
                    if (headersEnd < 0 || socket->property("handled").toBool()) return;
                    qsizetype contentLength = 0;
                    for (const auto &line : bytes.first(headersEnd).split('\n'))
                        if (line.trimmed().toLower().startsWith("content-length:"))
                            contentLength = line.mid(line.indexOf(':') + 1).trimmed().toLongLong();
                    if (bytes.size() < headersEnd + 4 + contentLength) return;
                    socket->setProperty("handled", true);
                    requests.append(bytes);
                    if (holdResponse) { heldSocket = socket; return; }
                    respond(socket);
                };
                connect(socket, &QTcpSocket::readyRead, socket, readRequest);
                QMetaObject::invokeMethod(socket, readRequest, Qt::QueuedConnection);
            }
        });
    }
    void respond(QTcpSocket *socket) {
                    socket->write("HTTP/1.1 " + QByteArray::number(status) + " Result\r\nContent-Type: application/json\r\nContent-Length: "
                        + QByteArray::number(body.size()) + "\r\nConnection: close\r\n" + headers + "\r\n" + body);
                    socket->disconnectFromHost();
    }
    QString url(const QString &host = QStringLiteral("127.0.0.1")) const { return QString("https://%1:%2").arg(host).arg(serverPort()); }
    static QByteArray response(const QString &provider) {
        QJsonObject usage{{"provider_name", provider}, {"primary_label", "Current"}, {"secondary_label", "Weekly"},
            {"show_secondary", true}, {"subtitle", QJsonValue::Null}, {"primary_status_text", QJsonValue::Null},
            {"secondary_status_text", QJsonValue::Null}, {"reauth_command", QJsonValue::Null},
            {"current", QJsonObject{{"utilization", 7}, {"resets_at", QJsonValue::Null}}},
            {"weekly", QJsonObject{{"utilization", 2}, {"resets_at", QJsonValue::Null}}},
            {"buckets", QJsonArray{QJsonObject{{"id", "weekly"}, {"label", "Weekly"}, {"utilization", 2},
                {"resets_at", QJsonValue::Null}, {"status_text", QJsonValue::Null}}}},
            {"error", QJsonValue::Null}, {"needs_reauth", false}, {"is_success", true}};
        return QJsonDocument(QJsonObject{{"provider", provider}, {"refetched", true}, {"usage", usage}}).toJson(QJsonDocument::Compact);
    }
};

class CredentialServiceTest : public QObject {
    Q_OBJECT
    QTemporaryDir m_dir;
    QString m_record;
private:
    CredentialServiceOptions options(int cooldown = 40, int timeout = 1000) const {
        CredentialServiceOptions result;
        result.helperPath = QStringLiteral(CREDENTIAL_FIXTURE_PATH);
        result.enabled = true;
        result.retryCooldownMs = cooldown;
        result.helperTimeoutMs = timeout;
        result.requestTimeoutMs = timeout;
        return result;
    }
    static QVariantMap cursorFailure() {
        return {{"provider_name", "Cursor"}, {"is_success", false}, {"needs_reauth", true},
            {"error", "Cursor session expired. Log in to cursor.com again."}, {"buckets", QVariantList{}}};
    }
    static QVariantMap grokWithoutWeekly() {
        return {{"provider_name", "Grok"}, {"is_success", true}, {"needs_reauth", false}, {"error", QVariant()},
            {"buckets", QVariantList{QVariantMap{{"id", "credits"}, {"label", "Credits"}, {"utilization", 5}}}}};
    }
private slots:
    void initTestCase() {
        QVERIFY2(TlsFixture::selectNativeTestBackend(), "SecureTransport is unavailable");
    }
    void init() {
        m_record = m_dir.filePath(QStringLiteral("record"));
        QFile::remove(m_record);
        qputenv("HEADROOM_CREDENTIAL_FIXTURE_RECORD", m_record.toUtf8());
        qputenv("HEADROOM_CREDENTIAL_FIXTURE_MODE", "valid");
    }
    void cleanup() {
        qunsetenv("HEADROOM_CREDENTIAL_FIXTURE_RECORD");
        qunsetenv("HEADROOM_CREDENTIAL_FIXTURE_MODE");
    }
    void deniesRemotePlainHttpBeforeReadingBrowser() {
        CredentialService service(options());
        service.configure("remote", "http://192.0.2.1:7823", "fixture-token");
        service.consider({cursorFailure()});
        QTest::qWait(80);
        QVERIFY(!QFileInfo::exists(m_record));
        QVERIFY(!service.busy());
    }
    void authControlsRecoveryAndSourceCompatibility_data() {
        QTest::addColumn<bool>("auth"); QTest::addColumn<bool>("newHelper");
        QTest::newRow("new-server-new-helper") << true << true;
        QTest::newRow("legacy-server-new-helper") << false << true;
        QTest::newRow("new-server-legacy-helper") << true << false;
        QTest::newRow("legacy-server-legacy-helper") << false << false;
    }
    void authControlsRecoveryAndSourceCompatibility() {
        QFETCH(bool, auth); QFETCH(bool, newHelper);
        qputenv("HEADROOM_CREDENTIAL_FIXTURE_MODE", newHelper ? "sources" : "valid");
        CredentialHttpFixture server; QVERIFY(server.listen(QHostAddress::LocalHost));
        CredentialService service(options()); QSignalSpy recovered(&service, &CredentialService::providerRecovered);
        service.configure("remote", server.url(), "fixture-token", TlsFixture::certificate());
        auto provider = cursorFailure();
        if (auth) {
            provider["needs_reauth"] = false;
            provider["auth"] = QVariantMap{{"state", "expired"}, {"accepts_browser_credentials", true}};
        }
        service.consider({provider}); QTRY_COMPARE(recovered.size(), 1);
        const auto payload = QJsonDocument::fromJson(server.requests.first().mid(server.requests.first().indexOf("\r\n\r\n") + 4)).object();
        QCOMPARE(payload.contains("source_name"), auth && newHelper);
        if (auth && newHelper) QCOMPARE(payload["source_name"].toString(), QStringLiteral("Firefox"));
        QCOMPARE(service.checked("Cursor").size(), newHelper ? 2 : 0);
        QCOMPARE(service.sharingStatus(), QStringLiteral("available"));
    }
    void authSignedInOrDisabledNeverRunsHelper() {
        CredentialService service(options());
        service.configure("remote", "https://127.0.0.1:65530", "", TlsFixture::certificate());
        auto provider = cursorFailure(); provider["auth"] = QVariantMap{{"state", "signed_in"}, {"accepts_browser_credentials", true}};
        service.consider({provider}); QTest::qWait(60); QVERIFY(!QFileInfo::exists(m_record));
        provider["auth"] = QVariantMap{{"state", "expired"}, {"accepts_browser_credentials", false}};
        service.consider({provider}); QTest::qWait(60); QVERIFY(!QFileInfo::exists(m_record));
        service.configure("remote", "https://127.0.0.1:65530", "", TlsFixture::certificate(), false);
        service.consider({cursorFailure()}); QTest::qWait(60); QVERIFY(!QFileInfo::exists(m_record));
        QCOMPARE(service.sharingStatus(), QStringLiteral("disabled"));
    }
    void sharingStatusReportsPolicyAndAvailability() {
        auto missing = options(); missing.helperPath = m_dir.filePath("missing");
        CredentialService service(missing); service.configure("remote", "https://example.test", "");
        QCOMPARE(service.sharingStatus(), QStringLiteral("unavailable"));
        service.configure("remote", "http://example.test", "");
        QCOMPARE(service.sharingStatus(), QStringLiteral("insecure"));
        missing.enabled = false; CredentialService unsupported(missing);
        unsupported.configure("ssh", "ssh://example.test", "");
        QCOMPARE(unsupported.sharingStatus(), QStringLiteral("unsupported"));
    }
    void unownedLocalServerNeverReadsBrowserOrSendsSecrets() {
        CredentialHttpFixture impostor; QVERIFY(impostor.listen(QHostAddress::LocalHost));
        CredentialService service(options()); QSignalSpy events(&service, &CredentialService::event);
        service.configure("local", QString("http://127.0.0.1:%1").arg(impostor.serverPort()), "fixture-bearer");
        service.consider({cursorFailure()});
        QTRY_VERIFY(!service.busy());
        QVERIFY(!QFileInfo::exists(m_record));
        QCOMPARE(impostor.requests.size(), 0);
        QCOMPARE(events.size(), 1);
        QVERIFY(events.first().first().toString().contains("verified private local connection"));
    }
    void losingLocalOwnershipStopsAnInFlightAttempt() {
        CredentialHttpFixture server; QVERIFY(server.listen(QHostAddress::LocalHost)); server.holdResponse = true;
        CredentialService service(options()); QSignalSpy recovered(&service, &CredentialService::providerRecovered);
        service.configure("local", server.url(), "fixture-bearer", TlsFixture::certificate());
        service.consider({cursorFailure()});
        QTRY_COMPARE(server.requests.size(), 1); QVERIFY(service.busy());
        service.configure("local", server.url(), "", QSslCertificate());
        QTRY_VERIFY(!service.busy());
        if (server.heldSocket) server.respond(server.heldSocket);
        QTest::qWait(80); QCOMPARE(recovered.size(), 0); QCOMPARE(server.requests.size(), 1);
    }
    void loopbackRecoversAndKeepsSecretsOutOfEvents() {
        CredentialHttpFixture server; QVERIFY(server.listen(QHostAddress::LocalHost));
        CredentialService service(options()); QSignalSpy recovered(&service, &CredentialService::providerRecovered);
        QSignalSpy events(&service, &CredentialService::event);
        service.configure("local", server.url(), "fixture-bearer", TlsFixture::certificate());
        service.consider({cursorFailure()});
        QTRY_COMPARE(recovered.size(), 1); QCOMPARE(server.requests.size(), 1);
        QVERIFY(server.requests.first().startsWith("PUT /api/v1/providers/cursor/credentials "));
        QVERIFY(HttpAssertions::hasHeader(server.requests.first(), "Authorization", "Bearer fixture-bearer"));
        QVERIFY(server.requests.first().contains("synthetic-cursor"));
        QCOMPARE(recovered.first().first().toMap()["provider_name"].toString(), QString("Cursor"));
        for (const auto &row : events) {
            const QString text = row.first().toString();
            QVERIFY(!text.contains("synthetic-cursor")); QVERIFY(!text.contains("fixture-bearer"));
        }
        QTest::qWait(60); service.consider({cursorFailure()});
        QTRY_VERIFY(QFile(m_record).exists()); QTest::qWait(100);
        QCOMPARE(server.requests.size(), 1); // Same successful credential is not replayed.
    }
    void grokOnlyRunsWithoutWeekly() {
        CredentialHttpFixture server; QVERIFY(server.listen(QHostAddress::LocalHost)); server.body = CredentialHttpFixture::response("Grok");
        CredentialService service(options()); QSignalSpy recovered(&service, &CredentialService::providerRecovered);
        service.configure("local", server.url(), "", TlsFixture::certificate());
        QVariantMap withWeekly = grokWithoutWeekly();
        withWeekly["buckets"] = QVariantList{QVariantMap{{"id", "weekly"}}};
        service.consider({withWeekly}); QTest::qWait(80); QVERIFY(!QFileInfo::exists(m_record));
        service.consider({grokWithoutWeekly()}); QTRY_COMPARE(recovered.size(), 1);
        QVERIFY(server.requests.first().contains("sso=synthetic-grok"));
    }
    void refusesRedirectsWithoutForwardingToDestination() {
        CredentialHttpFixture origin, destination;
        QVERIFY(origin.listen(QHostAddress::LocalHost)); QVERIFY(destination.listen(QHostAddress::LocalHost));
        origin.status = 302; origin.headers = "Location: " + destination.url().toUtf8() + "/target\r\n";
        CredentialService service(options()); QSignalSpy recovered(&service, &CredentialService::providerRecovered);
        service.configure("local", origin.url(), "fixture-bearer", TlsFixture::certificate()); service.consider({cursorFailure()});
        QTRY_VERIFY(!service.busy()); QCOMPARE(recovered.size(), 0); QCOMPARE(origin.requests.size(), 1); QCOMPARE(destination.requests.size(), 0);
    }
    void refusesSameOriginRedirect() {
        CredentialHttpFixture origin; QVERIFY(origin.listen(QHostAddress::LocalHost));
        origin.status = 302; origin.headers = "Location: /target\r\n";
        CredentialService service(options()); QSignalSpy recovered(&service, &CredentialService::providerRecovered);
        service.configure("local", origin.url(), "fixture-bearer", TlsFixture::certificate()); service.consider({cursorFailure()});
        QTRY_VERIFY(!service.busy()); QCOMPARE(recovered.size(), 0); QCOMPARE(origin.requests.size(), 1);
        QVERIFY(origin.requests.first().startsWith("PUT /api/v1/providers/cursor/credentials "));
    }
    void endpointChangeKillsHelperAndRemovesOwnedSnapshots() {
        qputenv("HEADROOM_CREDENTIAL_FIXTURE_MODE", "hang");
        CredentialService service(options(40, 5000));
        service.configure("local", "https://127.0.0.1:65530", "", TlsFixture::certificate()); service.consider({cursorFailure()});
        QTRY_VERIFY(QFileInfo::exists(m_record));
        QFile record(m_record); QVERIFY(record.open(QIODevice::ReadOnly));
        QTRY_VERIFY(record.size() > 8); record.seek(0);
        const auto lines = record.readAll().split('\n'); QVERIFY(lines.size() >= 2);
        const QString snapshotRoot = QString::fromUtf8(lines[1]); QVERIFY(QFileInfo::exists(snapshotRoot));
        service.configure("remote", "https://example.test", "");
        QTRY_VERIFY(!service.busy()); QVERIFY(!QFileInfo::exists(snapshotRoot));
    }
    void oversizedHelperOutputIsBounded() {
        qputenv("HEADROOM_CREDENTIAL_FIXTURE_MODE", "oversized");
        CredentialService service(options());
        service.configure("local", "https://127.0.0.1:65530", "", TlsFixture::certificate()); service.consider({cursorFailure()});
        QTRY_VERIFY(!service.busy()); QVERIFY(QFileInfo::exists(m_record));
    }
    void localTlsRequiresPinnedCertificate() {
        CredentialHttpFixture server; QVERIFY(server.listen(QHostAddress::Any));
        CredentialService service(options()); QSignalSpy events(&service, &CredentialService::event);
        service.configure("local", server.url("localhost"), ""); service.consider({cursorFailure()});
        QTRY_COMPARE(events.size(), 1); QCOMPARE(server.requests.size(), 0); QVERIFY(!QFileInfo::exists(m_record));
        QVERIFY(events.first().first().toString().contains("verified private local connection"));
    }
    void replacementCertificateReceivesNoCredentialOrBearer() {
        CredentialHttpFixture attacker(true); QVERIFY(attacker.listen(QHostAddress::LocalHost));
        CredentialService service(options()); QSignalSpy recovered(&service, &CredentialService::providerRecovered);
        service.configure("local", attacker.url(), "fixture-bearer", TlsFixture::certificate());
        service.consider({cursorFailure()});
        QTRY_VERIFY(!service.busy());
        QVERIFY(QFileInfo::exists(m_record));
        QCOMPARE(attacker.requests.size(), 0);
        QCOMPARE(recovered.size(), 0);
    }
    void newerHealthySnapshotCancelsHeldRecovery() {
        CredentialHttpFixture server; QVERIFY(server.listen(QHostAddress::LocalHost)); server.holdResponse = true;
        CredentialService service(options()); QSignalSpy recovered(&service, &CredentialService::providerRecovered);
        service.configure("local", server.url(), "", TlsFixture::certificate()); service.consider({cursorFailure()});
        QTRY_COMPARE(server.requests.size(), 1); QVERIFY(service.busy());
        QVariantMap healthy = cursorFailure(); healthy["is_success"] = true; healthy["needs_reauth"] = false;
        healthy["error"] = QVariant(); healthy["buckets"] = QVariantList{QVariantMap{{"id", "weekly"}}};
        service.consider({healthy}); QTRY_VERIFY(!service.busy());
        if (server.heldSocket) server.respond(server.heldSocket);
        QTest::qWait(80); QCOMPARE(recovered.size(), 0);
    }
    void grokResponseMustActuallyRestoreWeekly() {
        CredentialHttpFixture server; QVERIFY(server.listen(QHostAddress::LocalHost));
        server.body = CredentialHttpFixture::response("Grok");
        auto object = QJsonDocument::fromJson(server.body).object();
        auto usage = object["usage"].toObject(); usage["buckets"] = QJsonArray{QJsonObject{{"id", "credits"}, {"label", "Credits"},
            {"utilization", 5}, {"resets_at", QJsonValue::Null}, {"status_text", QJsonValue::Null}}};
        object["usage"] = usage; server.body = QJsonDocument(object).toJson(QJsonDocument::Compact);
        CredentialService service(options()); QSignalSpy recovered(&service, &CredentialService::providerRecovered);
        service.configure("local", server.url(), "", TlsFixture::certificate()); service.consider({grokWithoutWeekly()});
        QTRY_VERIFY(!service.busy()); QCOMPARE(recovered.size(), 0); QCOMPARE(server.requests.size(), 1);
        QTest::qWait(60); service.consider({grokWithoutWeekly()}); QTRY_COMPARE(server.requests.size(), 2);
    }
    void sshRecoversWithoutBearerOrHttpFallback()
    {
        auto sshOptions = options();
        sshOptions.sshOptions = SshOptions{QStringLiteral(SSH_FIXTURE_PATH), 1000};
        CredentialService service(sshOptions); QSignalSpy recovered(&service, &CredentialService::providerRecovered);
        service.configure(QStringLiteral("ssh"), QStringLiteral("ssh://valid"), QStringLiteral("must-not-be-sent"));
        service.consider({cursorFailure()});
        QTRY_COMPARE(recovered.size(), 1);
        QCOMPARE(recovered.first().first().toMap()["provider_name"].toString(), QStringLiteral("Cursor"));
    }
};
QTEST_GUILESS_MAIN(CredentialServiceTest)
#include "test_credentials.moc"
