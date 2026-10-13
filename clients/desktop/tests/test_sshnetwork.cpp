#include "sshnetwork.h"

#include <QNetworkReply>
#include <QSignalSpy>
#include <QTest>

class SshNetworkTest final : public QObject {
    Q_OBJECT
private slots:
    void validatesAddressesAndArguments()
    {
        QUrl address;
        QVERIFY(SshTransport::parseAddress(QStringLiteral("ssh://alice@example.test:2222"), &address));
        const auto args = SshTransport::arguments(address);
        QCOMPARE(args.last(), QStringLiteral("usage-server --ssh-stdio"));
        QCOMPARE(args.at(args.size() - 2), QStringLiteral("example.test"));
        QVERIFY(args.contains(QStringLiteral("BatchMode=yes")));
        QVERIFY(args.contains(QStringLiteral("StrictHostKeyChecking=yes")));
        QVERIFY(args.contains(QStringLiteral("ClearAllForwardings=yes")));
        QVERIFY(args.contains(QStringLiteral("ControlPath=none")));
        for (const QString &invalid : {QStringLiteral("ssh://-oProxyCommand=bad"), QStringLiteral("ssh://u:p@host"),
                 QStringLiteral("ssh://host/path"), QStringLiteral("ssh://host?"), QStringLiteral("ssh://host#"),
                 QStringLiteral("ssh://host name"), QStringLiteral("http://host")})
            QVERIFY2(!SshTransport::parseAddress(invalid), qPrintable(invalid));
    }

    void acceptsGoFieldOrderAfterEof()
    {
        SshNetworkAccessManager network(SshOptions{QStringLiteral(SSH_FIXTURE_PATH), 1000});
        auto reply = network.get(QNetworkRequest(QUrl(QStringLiteral("ssh://valid/api/v1/usage"))));
        QSignalSpy finished(reply, &QNetworkReply::finished);
        QTRY_COMPARE(finished.size(), 1);
        QCOMPARE(reply->error(), QNetworkReply::NoError);
        QCOMPARE(reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt(), 200);
        QCOMPARE(reply->readAll(), QByteArrayLiteral("[]"));
        auto escaped = network.get(QNetworkRequest(QUrl(QStringLiteral("ssh://escaped/api/v1/usage"))));
        QSignalSpy escapedFinished(escaped, &QNetworkReply::finished);
        QTRY_COMPARE(escapedFinished.size(), 1);
        QCOMPARE(escaped->error(), QNetworkReply::NoError);
    }

    void rejectsMalformedAndFailedResponses_data()
    {
        QTest::addColumn<QString>("host");
        for (const QString &host : {QStringLiteral("duplicate"), QStringLiteral("badbase64"),
                 QStringLiteral("extra"), QStringLiteral("failure"), QStringLiteral("oversized")})
            QTest::newRow(qPrintable(host)) << host;
    }
    void rejectsMalformedAndFailedResponses()
    {
        QFETCH(QString, host);
        SshNetworkAccessManager network(SshOptions{QStringLiteral(SSH_FIXTURE_PATH), 1000});
        auto reply = network.get(QNetworkRequest(QUrl(QStringLiteral("ssh://") + host + QStringLiteral("/api/v1/usage"))));
        QSignalSpy finished(reply, &QNetworkReply::finished);
        QTRY_COMPARE(finished.size(), 1);
        QVERIFY(reply->error() != QNetworkReply::NoError);
    }

    void timeoutCancellationAndMissingExecutable()
    {
        SshNetworkAccessManager timeoutNetwork(SshOptions{QStringLiteral(SSH_FIXTURE_PATH), 30});
        auto timed = timeoutNetwork.get(QNetworkRequest(QUrl(QStringLiteral("ssh://delay/api/v1/usage"))));
        QSignalSpy timedFinished(timed, &QNetworkReply::finished);
        QTRY_COMPARE(timedFinished.size(), 1);
        QCOMPARE(timed->error(), QNetworkReply::TimeoutError);

        SshNetworkAccessManager cancelNetwork(SshOptions{QStringLiteral(SSH_FIXTURE_PATH), 1000});
        auto cancelled = cancelNetwork.get(QNetworkRequest(QUrl(QStringLiteral("ssh://delay/api/v1/usage"))));
        QSignalSpy cancelledFinished(cancelled, &QNetworkReply::finished);
        cancelled->abort();
        QTRY_COMPARE(cancelledFinished.size(), 1);
        QCOMPARE(cancelled->error(), QNetworkReply::OperationCanceledError);

        SshNetworkAccessManager missing(SshOptions{QStringLiteral("/definitely/missing/headroom-ssh"), 1000});
        auto absent = missing.get(QNetworkRequest(QUrl(QStringLiteral("ssh://valid/api/v1/usage"))));
        QSignalSpy absentFinished(absent, &QNetworkReply::finished);
        QTRY_COMPARE(absentFinished.size(), 1);
        QCOMPARE(absent->error(), QNetworkReply::ConnectionRefusedError);
    }

    void rejectsDisallowedPathAndOversizedBody()
    {
        SshNetworkAccessManager network(SshOptions{QStringLiteral(SSH_FIXTURE_PATH), 1000});
        auto disallowed = network.get(QNetworkRequest(QUrl(QStringLiteral("ssh://valid/api/v1/other"))));
        QSignalSpy disallowedFinished(disallowed, &QNetworkReply::finished); QTRY_COMPARE(disallowedFinished.size(), 1);
        QVERIFY(disallowed->error() != QNetworkReply::NoError);
        QNetworkRequest put(QUrl(QStringLiteral("ssh://valid/api/v1/providers/grok/credentials")));
        auto oversized = network.put(put, QByteArray(1024 * 1024 + 1, 'x'));
        QSignalSpy oversizedFinished(oversized, &QNetworkReply::finished); QTRY_COMPARE(oversizedFinished.size(), 1);
        QVERIFY(oversized->error() != QNetworkReply::NoError);
    }

    void allowsOnlyTheBodilessRefreshPost()
    {
        SshNetworkAccessManager network(SshOptions{QStringLiteral(SSH_FIXTURE_PATH), 1000});
        const QNetworkRequest refresh(QUrl(QStringLiteral("ssh://valid/api/v1/usage/refresh")));
        auto accepted = network.post(refresh, QByteArray());
        QSignalSpy acceptedFinished(accepted, &QNetworkReply::finished); QTRY_COMPARE(acceptedFinished.size(), 1);
        QCOMPARE(accepted->error(), QNetworkReply::NoError);
        QCOMPARE(accepted->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt(), 202);
        QVERIFY(accepted->readAll().contains("\"accepted\""));
        // The receiver contract is an exact empty-body frame: payloads and other methods never leave the desktop.
        auto withBody = network.post(refresh, QByteArrayLiteral("{}"));
        QSignalSpy withBodyFinished(withBody, &QNetworkReply::finished); QTRY_COMPARE(withBodyFinished.size(), 1);
        QCOMPARE(withBody->error(), QNetworkReply::ContentOperationNotPermittedError);
        auto asGet = network.get(refresh);
        QSignalSpy asGetFinished(asGet, &QNetworkReply::finished); QTRY_COMPARE(asGetFinished.size(), 1);
        QCOMPARE(asGet->error(), QNetworkReply::ProtocolInvalidOperationError);
        auto asPut = network.put(refresh, QByteArray());
        QSignalSpy asPutFinished(asPut, &QNetworkReply::finished); QTRY_COMPARE(asPutFinished.size(), 1);
        QCOMPARE(asPut->error(), QNetworkReply::ProtocolInvalidOperationError);
        // A receiver that predates the endpoint answers 400 "invalid request"; that is a response, not a transport error.
        auto legacy = network.post(QNetworkRequest(QUrl(QStringLiteral("ssh://legacy/api/v1/usage/refresh"))), QByteArray());
        QSignalSpy legacyFinished(legacy, &QNetworkReply::finished); QTRY_COMPARE(legacyFinished.size(), 1);
        QCOMPARE(legacy->error(), QNetworkReply::NoError);
        QCOMPARE(legacy->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt(), 400);
        QCOMPARE(legacy->readAll(), QByteArrayLiteral("{\"error\":\"invalid request\"}"));
    }
};

QTEST_GUILESS_MAIN(SshNetworkTest)
#include "test_sshnetwork.moc"
