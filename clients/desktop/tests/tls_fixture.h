#pragma once

#include <QSslCertificate>
#include <QSslConfiguration>
#include <QSslKey>
#include <QSslServer>
#include <QSslError>
#include <QDebug>

namespace TlsFixture {
inline bool selectNativeTestBackend()
{
#ifdef Q_OS_MACOS
    static const bool selected = [] {
        if (qEnvironmentVariable("HEADROOM_TLS_FIXTURE_EMPTY_KEYCHAIN_PROBE") != "1")
            qputenv("QT_SSL_USE_TEMPORARY_KEYCHAIN", "1");
        const QString requested = QStringLiteral("securetransport");
        const bool activated = QSslSocket::setActiveBackend(requested);
        const QString active = QSslSocket::activeBackend();
        qInfo().noquote() << "Headroom TLS fixture backend:" << active;
        return activated && active == requested;
    }();
    return selected;
#else
    return true;
#endif
}

// Synthetic public test identities only; production generates a fresh key in memory.
#ifdef Q_OS_MACOS
#include "headroom_macos_tls_fixture.h"
#else
inline constexpr char certificatePem[] = R"PEM(-----BEGIN CERTIFICATE-----
MIIDdjCCAl6gAwIBAgIUepEJ6PKGRzL2DA/s7L+hPPw+FVQwDQYJKoZIhvcNAQEL
BQAwIDEeMBwGA1UEAwwVSGVhZHJvb20gVGVzdCBTZXNzaW9uMB4XDTI2MDkwOTIz
NTQwNFoXDTM2MDkwNjIzNTQwNFowIDEeMBwGA1UEAwwVSGVhZHJvb20gVGVzdCBT
ZXNzaW9uMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAtLerZ9DOsSVr
aQVG5HTIFAqtmQ67oB7viVGw4P5Ta3aMXT1ug7pCkWYImPPbQH9vMCOOPDT9LZZ/
gvqoAmvjE5NDnzl7a7zQ1IubFhG8cYOxtCvTAqWfYc6Aa8Ku9TtQpP5QlgUn6/6F
yb9ZEdoYYNYHdlev0IgVIofX6ik9ipA5vwcQHFKkG+f2AhjZHceGW/b2z1c4bfYl
2wAjbiuc0VylnwhuGEwYWRgrAEvVbMMPgk68WIO25diCeh/RCR9NqZ+7nVzEdIYx
5ukLpovDgOv1Gm8OkXUEi3PkkDcWcCxEtOXtXaVI4SGMTB/J6UwfZ8LP7vA1soQH
NQW1qNNsbQIDAQABo4GnMIGkMB0GA1UdDgQWBBS9TAsIyq0P1oVxLrEjJiI8pISo
XTAfBgNVHSMEGDAWgBS9TAsIyq0P1oVxLrEjJiI8pISoXTAsBgNVHREEJTAjggls
b2NhbGhvc3SHBH8AAAGHEAAAAAAAAAAAAAAAAAAAAAEwDwYDVR0TAQH/BAUwAwEB
/zAOBgNVHQ8BAf8EBAMCAqQwEwYDVR0lBAwwCgYIKwYBBQUHAwEwDQYJKoZIhvcN
AQELBQADggEBAHm22e5SJT3cxVrk/0rxUKOUfLE36VYSqK7iSfALXE6aGcx/oCsS
/fPbc5Rr2iXagWcDjKZGmqUw3iYOfBqbI3xjt1CM48PSnHvM2ynIIwiIH+CfnzYA
7mFGSjyWG9EIrZkyKjdkgc9WPzEcuIDO9BziOmF4s5UJyuyLfZEfWZbKh49Ei1X9
M/eZyfJwwW0pjVN0m8OMFN9BCrwmebu94YQk0RlBDNOC6vICuQKSUcR5/9nuxAKn
EBGQv1tFcN3Q/LBgXq1jVa4RCyVfGaLG+zGN6hzxsNBEvf1JL22wJFu5N7KMGIKa
AX2YY4sU4e5pofqi5ZhRvloIotw1OSc3mHQ=
-----END CERTIFICATE-----
)PEM";

inline constexpr char privateKeyPem[] = R"PEM(-----BEGIN PRIVATE KEY-----
MIIEvAIBADANBgkqhkiG9w0BAQEFAASCBKYwggSiAgEAAoIBAQC0t6tn0M6xJWtp
BUbkdMgUCq2ZDrugHu+JUbDg/lNrdoxdPW6DukKRZgiY89tAf28wI448NP0tln+C
+qgCa+MTk0OfOXtrvNDUi5sWEbxxg7G0K9MCpZ9hzoBrwq71O1Ck/lCWBSfr/oXJ
v1kR2hhg1gd2V6/QiBUih9fqKT2KkDm/BxAcUqQb5/YCGNkdx4Zb9vbPVzht9iXb
ACNuK5zRXKWfCG4YTBhZGCsAS9Vsww+CTrxYg7bl2IJ6H9EJH02pn7udXMR0hjHm
6Qumi8OA6/Uabw6RdQSLc+SQNxZwLES05e1dpUjhIYxMH8npTB9nws/u8DWyhAc1
BbWo02xtAgMBAAECggEACZTiaOT0jIZmIUGXHmZFtHBTf3W9Q3JYc3CqHufFHwHA
U/I9oYzW2jQteQSW557KE6SS9kS99rSPyq9FCKnVNpS8hVzsGwx+CwLi5GLo2aDo
o1ueermyz2H+Q5hlI9ny/uh/99BKq1PJnqZ/T7e/SqUWbLMd/oG0kyneLAtnOKkx
jCRzlJdq+ofdmtaIsoziTbS3fCf+P+WI1qqIstJBVyHOXn/4oZ+t97qSRP3uAZaS
aGwmdIWOxLeS4SoQU9t2kWOJjltrLGVMFfHVal+hXuj+7DqKnj2++8ssSqIqjai6
Js9QYzmdfLeDvSVXVv2wRz0V0DAq551H71/SMmY+oQKBgQDrSXwzxrgx67s41En2
MxjGbJDKp7U9yoFFk/ZG88OKW3+HfXlxE+qGAp1c9Dpo5H+Ey0vEcxgx86kLW+ms
brA9utY8ZVgdmD59FiqnsbcUWJYONRY+BOYNkSsHsFV/VfoN8l+6qfSrdriZu9qX
COkn7ybKwXxEhGaHQTK0FbSITQKBgQDEoGJXfxBNK6x2QJpyMFE7HLBd7mOtB9qQ
88cHt51rOlesx2lLU3KaIrh4btBZ6OaChN81XWEcH78H9nMuy2fnJYBAEbpP5syd
wYR5asxqKJW3XACLrfYcWJg9mtVBIyB9ocKT/Gy6DO967EigKpfO5nyFRvIVjfOv
m7pIfQqEoQKBgEGaRrAlCzyYDqakwk6MJq9EnAZ1RlqJOQwOWk+kLfRI2MENYiv+
MpjU582AJz0R4OBE0sZelPNwjcSmBvxWDHvZuVwzoaL2zQ2RfakuKu7YWFZzxNTc
BsznmgjVyLDtZwA2gSyJsLeSLZxnvWX5TREeZEGKOzzbcb7qldmSZ7jNAoGAZ8Kb
IsESwSaC5ARDfG6y7xa8m1TplK3IUSXTqebjU2p+SdcRYYavu1cHMcaIfGnC+q1T
fL3COS0nifQr5E3Cjt/jRewU/YU2BSnR1qDdLkgseY47W3p5nYvDCv9h+fWnd6Sd
IarbVXLuDVgTGzJe2fgXTd2acnVuWwU60HXrzwECgYBukLfJkzmgbkIX1SDn+6to
6b/osMZCSaz5sxONF98DpLjCgQnRNjUCrWF++t1BG51ndpY45GGOaZ7oqEfpDUek
85GFdZXNaXV8nWiojtsH3GTOB1X+WIgsl+LPB3nS7YfnRD5M5BZjT+YgQ/uGWOVy
DOSgolam5d/yWtmVCN2Img==
-----END PRIVATE KEY-----
)PEM";

inline constexpr char replacementCertificatePem[] = R"PEM(-----BEGIN CERTIFICATE-----
MIIDjTCCAnWgAwIBAgITXnmU+jWc7oQjAqwS197FKE6hUTANBgkqhkiG9w0BAQsF
ADAsMSowKAYDVQQDDCFIZWFkcm9vbSBSZXBsYWNlbWVudCBUZXN0IFNlc3Npb24w
HhcNMjYwOTEwMDAwMzI4WhcNMzYwOTA3MDAwMzI4WjAsMSowKAYDVQQDDCFIZWFk
cm9vbSBSZXBsYWNlbWVudCBUZXN0IFNlc3Npb24wggEiMA0GCSqGSIb3DQEBAQUA
A4IBDwAwggEKAoIBAQCsrq/piMJeHo0B5FhnIKjqlrNlN7DW1Kqn+6f1KwHGQ4jw
qcn98vVOGy6Rb9uffD7n7s7M7bFSZ9OTow+eTuBXXbqs1g61qQ009NNMg6ldHAmQ
n4PvKdPoyrJUC8ewfcfcVQ1ysVicqNHTnHV4dkKykgqCHRumujwY2udeF7P5jvtF
PFk30Gds+uz24jiHo1C30E8MrMdzz3IFM1xpWkTw3Wu+rGpO9a3RNfVMno4qU9/O
duOXFB4t1cOKp/6W09pFA01GkuEW0y1YaCd8hL2kBSDA83hGHYXjSS/8dCPoL+Tv
7d/dVourv/QrI1EcXeKZgPCsipQamS/h8DnH7pFTAgMBAAGjgacwgaQwHQYDVR0O
BBYEFDtcYtu/iMqQKZqhJDBpbgjASkT8MB8GA1UdIwQYMBaAFDtcYtu/iMqQKZqh
JDBpbgjASkT8MCwGA1UdEQQlMCOCCWxvY2FsaG9zdIcEfwAAAYcQAAAAAAAAAAAA
AAAAAAAAATAPBgNVHRMBAf8EBTADAQH/MA4GA1UdDwEB/wQEAwICpDATBgNVHSUE
DDAKBggrBgEFBQcDATANBgkqhkiG9w0BAQsFAAOCAQEAiXcsjFD3yxdIhVTHvV+q
XsWW93g9KEEqtnmPuapUdUuzAtgO2BQEcMYbUeRROl6zy9TC5hui9+x5SI/EuUU4
GThmG6ComSXxwbKvK3iWGqd+vlcFDQcRjhKdz+C8qTZMB1iefTfd8ioLjT0HmSKS
SFqLiFlJmHWFgQS4wZ1ICgPqGBxnyFDdLjcGFaaDyBEWhfYQ1o+wjHPa3K6YqBK7
MG2Jgw1QNh2mwd+uU7vx20471AWdukrg4UKyCilF6fpk6PYKiPEtd+6Fx5xXgTLQ
fiO4SaX5SzqDo35ZFFNRrCzgEZcM5VUHWUV1gnmgwDLstFnBhhmMjEduDeqcm1X8
wQ==
-----END CERTIFICATE-----
)PEM";

inline constexpr char replacementPrivateKeyPem[] = R"PEM(-----BEGIN PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQCsrq/piMJeHo0B
5FhnIKjqlrNlN7DW1Kqn+6f1KwHGQ4jwqcn98vVOGy6Rb9uffD7n7s7M7bFSZ9OT
ow+eTuBXXbqs1g61qQ009NNMg6ldHAmQn4PvKdPoyrJUC8ewfcfcVQ1ysVicqNHT
nHV4dkKykgqCHRumujwY2udeF7P5jvtFPFk30Gds+uz24jiHo1C30E8MrMdzz3IF
M1xpWkTw3Wu+rGpO9a3RNfVMno4qU9/OduOXFB4t1cOKp/6W09pFA01GkuEW0y1Y
aCd8hL2kBSDA83hGHYXjSS/8dCPoL+Tv7d/dVourv/QrI1EcXeKZgPCsipQamS/h
8DnH7pFTAgMBAAECggEATqS0GuCc3K5/O4haNoVqSIRfqU5GrGBoZF65LG/su/XP
7QL2qkA3Nd4tRDE0TZsRLyHUbwIliskcU5WkrE1x4HggZ0yJoHQgPqjtfmBMBsqZ
FJOBS6+6AcAmnR5MUHXISvE1UbGxEI/WF9Ces0DwuVDHlmBZUoJokk1Hp9OVtKaJ
CBPC9HXtdNZop8qHVfAYPyOdA2C9ic5YLwPVpQlXIn1aIzO+dBdnQdpBtO1arCan
vCw6iDUB4d4yvDQE+jqQJxdTFymG2t7pLcwVLessU0Q7ZjcPSTMu8qm8mkWJuwhC
fOdYE/kzKGFqznlG4LGVbKQj/STGXoka6AbLAXqBoQKBgQDShdi1ZP6MBmFPhOI5
46Nzo6LHciDI91AxUhZphsZDd2KseCIIu4f6GkDBoZTLlGL4ryvLiWyPxh3qZ4Nx
bIFNh/rRvljyyYpLSDXrrM6R73udaVXXTNHlPVjEOy+SMWF2++vP1va56Hl5YzCi
V2yeMufRTIIbC/3IzS7WHXXZsQKBgQDR/Dg49bZZ3y2BXLxfNumawBJ+UsjZYsIe
pUT/gMhOq2/QiVTNmNVTxyNrPMd/PiSszuRi0OPPOSKnQhUYD3RU4zA+i4GKbVBC
SI3PsFEE6EcjTzkyDYZT6RDomlFxgJUJK82qV0eLjfAohx8pD79svtNfZouNS9oU
hFq2fg4YQwKBgAit+AdSiKQhRzXOFr2oQv7rijSK4gsnFylRZxcmtVnmvCy8LzsI
8ExBdNYG4WmbTpFON5IGsnEjC+y0I9kzTglyR8cKAATShl8gNQjocQPXSdNtmlps
EmNPKi+8+PMgEhKjxE8nX7i+evOkQUcQL6w+EQyyCNMwpV/4BaxSPp+hAoGBALdy
+Q9SItAbOR8mDG/Ggomuublgy5WXQO4TBteNA/Iutg3inJLNrbl2ZUwfmk5g1Ruf
2Ek4I337dXnYMqOFhOVQYeqSaEauP4rY5gOHDxEReGyxns9Kn4sZN6tufi6B3jJX
8WKaw6d5BlHx0lhGKL9xayqSZOK5XLnF85YxRFEPAoGADjlVYQGjyBI/vYbXXn2F
1X1IKsOuGH5/ce4pzhtzx0gzLuJhkhxizp1kZf8sQFnjZLxzCOAtkSmTrRisLtxd
q7V7Aa42tudKIJd7aYOfuEgCgx6L7Z+g4rC56featl/o5Jhkhw6owbXnPGQTvWeN
P7n4ksEb18Eu1o9165o6pPA=
-----END PRIVATE KEY-----
)PEM";
#endif

inline QSslCertificate certificate()
{
    return QSslCertificate(QByteArray(certificatePem), QSsl::Pem);
}

inline QSslConfiguration serverConfiguration()
{
    QSslConfiguration configuration = QSslConfiguration::defaultConfiguration();
    configuration.setLocalCertificate(certificate());
    configuration.setPrivateKey(QSslKey(QByteArray(privateKeyPem), QSsl::Rsa, QSsl::Pem));
    configuration.setProtocol(QSsl::TlsV1_2OrLater);
    configuration.setPeerVerifyMode(QSslSocket::VerifyNone);
    return configuration;
}

inline QSslCertificate replacementCertificate()
{
    return QSslCertificate(QByteArray(replacementCertificatePem), QSsl::Pem);
}

inline QSslConfiguration replacementServerConfiguration()
{
    QSslConfiguration configuration = QSslConfiguration::defaultConfiguration();
    configuration.setLocalCertificate(replacementCertificate());
    configuration.setPrivateKey(QSslKey(QByteArray(replacementPrivateKeyPem), QSsl::Rsa, QSsl::Pem));
    configuration.setProtocol(QSsl::TlsV1_2OrLater);
    configuration.setPeerVerifyMode(QSslSocket::VerifyNone);
    return configuration;
}

inline void installDiagnostics(QSslServer &server)
{
    server.setHandshakeTimeout(3000);
    QObject::connect(&server, &QSslServer::startedEncryptionHandshake, &server,
                     [](QSslSocket *) { qInfo("Headroom TLS fixture handshake started"); });
    QObject::connect(&server, &QSslServer::errorOccurred, &server,
                     [](QSslSocket *socket, QAbstractSocket::SocketError error) {
        qWarning().noquote() << "Headroom TLS fixture socket error" << int(error)
                             << (socket ? socket->errorString() : QStringLiteral("missing socket"));
    });
    QObject::connect(&server, &QSslServer::sslErrors, &server,
                     [](QSslSocket *, const QList<QSslError> &errors) {
        QStringList codes;
        for (const QSslError &error : errors) codes.append(QString::number(int(error.error())));
        qWarning().noquote() << "Headroom TLS fixture SSL errors" << codes.join(QLatin1Char(','));
    });
    QObject::connect(&server, &QSslServer::handshakeInterruptedOnError, &server,
                     [](QSslSocket *, const QSslError &error) {
        qWarning() << "Headroom TLS fixture handshake error" << int(error.error());
    });
    QObject::connect(&server, &QSslServer::alertReceived, &server,
                     [](QSslSocket *, QSsl::AlertLevel level, QSsl::AlertType type, const QString &) {
        qWarning() << "Headroom TLS fixture received alert" << int(level) << int(type);
    });
    QObject::connect(&server, &QSslServer::alertSent, &server,
                     [](QSslSocket *, QSsl::AlertLevel level, QSsl::AlertType type, const QString &) {
        qWarning() << "Headroom TLS fixture sent alert" << int(level) << int(type);
    });
}

inline void configure(QSslServer &server, bool replacement = false)
{
    installDiagnostics(server);
    server.setSslConfiguration(replacement ? replacementServerConfiguration()
                                           : serverConfiguration());
}
}
