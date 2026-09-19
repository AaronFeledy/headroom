#include "trayvisual.h"
#include "usage.h"
#include <QDir>
#include <QPainter>
#include <QtTest>

class TrayTest : public QObject {
    Q_OBJECT
    const QDateTime now = QDateTime::fromString("2026-09-08T12:00:00Z", Qt::ISODate);
    QVariantMap meter(QString id, double used, int remaining = 9000) const {
        return {{"id", id}, {"label", id == "session" ? "5-Hour" : "Weekly"}, {"utilization", used},
                {"resets_at", now.addSecs(remaining).toString(Qt::ISODate)}};
    }
    QVariantMap provider(QString name, QVariantList buckets, bool success = true) const {
        return {{"provider_name", name}, {"is_success", success}, {"buckets", buckets}};
    }
    QVariantMap ready() const { return {{"status", "ready"}, {"lastGood", now.toSecsSinceEpoch()}, {"updated", "Just updated"}}; }
    TrayVisual::Assessment assess = [](const QString &, const QVariantMap &bucket) {
        // Deliberately disagree with numeric usage: this must use the state machine's cached tier.
        return QVariantMap{{"severity", bucket["id"] == "session" ? 1 : 3}};
    };
    static int flamePixels(const QImage &image) {
        int count = 0;
        for (int y = 0; y < image.height(); ++y)
            for (int x = 0; x < image.width(); ++x) {
                const QColor color = image.pixelColor(x, y);
                if (color.alpha() > 180 && color.red() > 210 && color.green() > 80
                    && color.green() < 225 && color.blue() < 70) ++count;
            }
        return count;
    }
private slots:
    void unreadBadgeSurvivesEveryTrayState() {
        for (const auto kind : {TrayVisual::Kind::Usage, TrayVisual::Kind::Exhausted,
                TrayVisual::Kind::Offline, TrayVisual::Kind::AuthError, TrayVisual::Kind::Setup}) {
            TrayVisual::Model model; model.kind = kind; model.provider = "Claude";
            model.used = 95; model.level = Usage::WarningLevel::Critical;
            model.secondary = Usage::WarningLevel::Warning;
            const auto plain = TrayVisual::icon(model).pixmap(64, 64).toImage();
            model.unread = true;
            for (const auto frame : {TrayVisual::AttentionFrame{}, TrayVisual::AttentionFrame{1, 0.2, false},
                    TrayVisual::AttentionFrame{0, 0, true}}) {
                const auto image = TrayVisual::icon(model, frame).pixmap(64, 64).toImage();
                QCOMPARE(image.pixelColor(53, 11), QColor("#8be9fd"));
                QVERIFY(image != plain);
            }
            model.unread = false;
            QCOMPARE(TrayVisual::icon(model).pixmap(64, 64).toImage(), plain);
        }
    }
    void usesSharedLevelsForBothIndicators() {
        const auto model = TrayVisual::build(ready(), {provider("Codex", {meter("session", 12), meter("weekly", 8, 86400)})}, "Codex", assess, now);
        QCOMPARE(model.kind, TrayVisual::Kind::Usage);
        QCOMPARE(model.level, Usage::WarningLevel::Watch);
        QCOMPARE(model.secondary, Usage::WarningLevel::Critical);
        QCOMPARE(model.used, 12);
        QCOMPARE(model.expected, 50);
        QVERIFY(model.tooltip.startsWith("ChatGPT · 12% used"));
        QCOMPARE(model.tooltip.count('\n'), 1);
        QVERIFY(model.tooltip.contains("Resets in 2h 30m · Critical"));
        QVERIFY(!model.tooltip.contains("Weekly"));
        const auto image = TrayVisual::icon(model).pixmap(64, 64).toImage();
        QVERIFY(!image.isNull());
        QCOMPARE(image.pixelColor(53, 52), QColor("#ff5555"));
        QCOMPARE(image.pixelColor(32, 59), QColor("#f8f8f2"));
        const QString captureDirectory = qEnvironmentVariable("HEADROOM_TEST_CAPTURE_DIR");
        if (!captureDirectory.isEmpty()) {
            QVERIFY(QDir().mkpath(captureDirectory));
            QImage sheet(440, 220, QImage::Format_ARGB32_Premultiplied);
            sheet.fill(QColor("#282a36"));
            QPainter painter(&sheet);
            painter.setPen(QColor("#f8f8f2"));
            painter.setFont(QFont("sans-serif", 10));
            int row = 0;
            for (const QString &name : {QStringLiteral("Claude"), QStringLiteral("Codex"), QStringLiteral("Cursor"), QStringLiteral("Grok")}) {
                auto sample = model; sample.provider = name;
                sample.used = 38; sample.expected = 55; sample.level = Usage::WarningLevel::Normal;
                sample.secondary = Usage::WarningLevel::Normal;
                painter.drawText(QRect(12, row * 54, 85, 50), Qt::AlignVCenter, Usage::displayName(name));
                int x = 110;
                for (const int size : {16, 20, 24, 32, 48}) {
                    painter.drawPixmap(x, row * 54 + (50 - size) / 2, TrayVisual::icon(sample).pixmap(size, size));
                    x += 62;
                }
                ++row;
            }
            painter.end();
            QVERIFY(sheet.save(QDir(captureDirectory).filePath(QStringLiteral("tray-sizes.png"))));
        }
    }
    void keepsSelectedProviderOnError() {
        const auto model = TrayVisual::build(ready(), {
            provider("Claude", {}, false), provider("Codex", {meter("session", 60), meter("weekly", 99)})}, "Claude", assess, now);
        QCOMPARE(model.kind, TrayVisual::Kind::ProviderError);
        QCOMPARE(model.provider, QString("Claude"));
        QCOMPARE(model.used, -1);
        QCOMPARE(model.secondary, Usage::WarningLevel::Normal);
        QVERIFY(model.tooltip.contains("Provider unavailable"));
        QVERIFY(!model.tooltip.contains("ChatGPT"));
    }
    void distinguishesConnectionStates_data() {
        QTest::addColumn<QString>("status"); QTest::addColumn<QString>("error"); QTest::addColumn<int>("kind");
        using K = TrayVisual::Kind;
        QTest::newRow("setup") << "setup" << "" << int(K::Setup);
        QTest::newRow("connecting") << "connecting" << "" << int(K::Connecting);
        QTest::newRow("offline") << "offline" << "network" << int(K::Offline);
        QTest::newRow("token") << "offline" << "auth" << int(K::AuthError);
        QTest::newRow("http") << "offline" << "api" << int(K::ApiError);
        QTest::newRow("malformed") << "offline" << "malformed" << int(K::Malformed);
    }
    void distinguishesConnectionStates() {
        QFETCH(QString, status); QFETCH(QString, error); QFETCH(int, kind);
        auto state = ready(); state["status"] = status; state["errorKind"] = error; state["retrySeconds"] = 30;
        const auto model = TrayVisual::build(state, {provider("Claude", {meter("session", 40)})}, "Claude", assess, now);
        QCOMPARE(int(model.kind), kind);
        QVERIFY(!TrayVisual::icon(model).isNull());
        QCOMPARE(model.tooltip.count('\n'), 1);
        if (model.kind == TrayVisual::Kind::Offline) {
            const auto disconnected = TrayVisual::icon(model).pixmap(64, 64).toImage();
            QCOMPARE(disconnected.pixelColor(32, 32), QColor("#ff5555"));
            for (const QString &name : {QStringLiteral("Claude"), QStringLiteral("Codex"), QStringLiteral("Cursor"), QStringLiteral("Grok"), QString()}) {
                auto other = model; other.provider = name;
                QCOMPARE(TrayVisual::icon(other).pixmap(64, 64).toImage(), disconnected);
            }
            auto retry = state; retry["loading"] = true; retry["lastGood"] = 0;
            QCOMPARE(TrayVisual::build(retry, {}, "Claude", assess, now).kind, TrayVisual::Kind::Offline);
            const auto recovered = TrayVisual::build(ready(), {provider("Claude", {meter("session", 40)})}, "Claude", assess, now);
            QCOMPARE(recovered.kind, TrayVisual::Kind::Usage);
            QVERIFY(TrayVisual::icon(recovered).pixmap(64, 64).toImage() != disconnected);
            const QString capture = qEnvironmentVariable("HEADROOM_TEST_CAPTURE_DIR");
            if (!capture.isEmpty()) {
                QVERIFY(QDir().mkpath(capture));
                QVERIFY(disconnected.save(QDir(capture).filePath(QStringLiteral("tray-offline.png"))));
            }
        }
    }
    void loadingWithoutGoodDataAndBackgroundRefresh() {
        auto state = ready(); state["loading"] = true; state["lastGood"] = 0;
        QCOMPARE(TrayVisual::build(state, {}, "Claude", assess, now).kind, TrayVisual::Kind::Connecting);
        state["lastGood"] = now.toSecsSinceEpoch();
        QCOMPARE(TrayVisual::build(state, {provider("Claude", {meter("session", 40)})}, "Claude", assess, now).kind, TrayVisual::Kind::Usage);
    }
    void exhaustionAndExpiredPacing() {
        const auto model = TrayVisual::build(ready(), {provider("Claude", {meter("session", 100, -60)})}, "Claude", assess, now);
        QCOMPARE(model.kind, TrayVisual::Kind::Exhausted);
        QCOMPARE(model.expected, -1);
        QVERIFY(model.tooltip.contains("awaiting reset update"));
        QVERIFY(model.tooltip.contains("100% used"));
        // The graphic uses the supplied tier even when a synthetic test's level is unusual.
        QCOMPARE(model.level, Usage::WarningLevel::Watch);
    }
    void billingStatusAndMissingSelectedProvider() {
        auto billing = meter("on_demand", 0); billing["label"] = "On-Demand"; billing["status_text"] = "$4.00 billed";
        const auto model = TrayVisual::build(ready(), {provider("Cursor", {meter("session", 40), billing})}, "Cursor", assess, now);
        QVERIFY(!model.tooltip.contains("On-Demand"));
        QVERIFY(!model.tooltip.contains("On-Demand · 0%"));
        QCOMPARE(model.secondary, Usage::WarningLevel::Normal);
        const auto missing = TrayVisual::build(ready(), {provider("Cursor", {meter("session", 40)})}, "Claude", assess, now);
        QCOMPARE(missing.kind, TrayVisual::Kind::Idle);
        QCOMPARE(missing.used, -1);
    }
    void firstMeasurableBucketDrivesTray() {
        auto billing = meter("on_demand", 0); billing["status_text"] = "On-demand enabled";
        auto measured = meter("weekly", 37, 86400);
        const auto model = TrayVisual::build(ready(), {provider("Grok", {billing, measured})}, "Grok", assess, now);
        QCOMPARE(model.kind, TrayVisual::Kind::Usage);
        QCOMPARE(model.used, 37);
        QCOMPARE(model.level, Usage::WarningLevel::Critical);
        QCOMPARE(model.secondary, Usage::WarningLevel::Normal);
        QCOMPARE(TrayVisual::build(ready(), {provider("Grok", {billing})}, "Grok", assess, now).kind,
                 TrayVisual::Kind::Idle);
    }
    void criticalAttentionRendersAProceduralFlame() {
        auto model = TrayVisual::build(ready(), {provider("Grok", {meter("weekly", 97)})}, "Grok", assess, now);
        QCOMPARE(model.level, Usage::WarningLevel::Critical);
        const TrayVisual::AttentionFrame first{1.0, 0.0, false};
        const TrayVisual::AttentionFrame bent{1.0, 0.25, false};
        for (const int size : {16, 22}) {
            const QImage image = TrayVisual::icon(model, first).pixmap(size, size).toImage();
            QVERIFY2(flamePixels(image) >= (size == 16 ? 5 : 10), "flame colors must survive native tray scaling");
            QVERIFY(image != TrayVisual::icon(model).pixmap(size, size).toImage());
            QVERIFY(image != TrayVisual::icon(model, bent).pixmap(size, size).toImage());
        }
        const auto secondaryCritical = TrayVisual::build(
            ready(), {provider("Codex", {meter("session", 30), meter("weekly", 75)})}, "Codex", assess, now);
        QCOMPARE(secondaryCritical.level, Usage::WarningLevel::Watch);
        QCOMPARE(secondaryCritical.secondary, Usage::WarningLevel::Critical);
        QVERIFY(flamePixels(TrayVisual::icon(secondaryCritical, first).pixmap(22, 22).toImage()) >= 10);

        const QString capture = qEnvironmentVariable("HEADROOM_TEST_CAPTURE_DIR");
        if (!capture.isEmpty()) {
            QVERIFY(QDir().mkpath(capture));
            QImage sheet(360, 92, QImage::Format_ARGB32_Premultiplied);
            sheet.fill(QColor("#282a36"));
            QPainter painter(&sheet);
            painter.setPen(QColor("#f8f8f2"));
            painter.drawText(QRect(8, 4, 344, 18), Qt::AlignCenter, "Critical attention · fire phases and flash");
            for (int frame = 0; frame < 4; ++frame)
                painter.drawPixmap(24 + frame * 72, 24, TrayVisual::icon(model, {1, frame / 4.0, false}).pixmap(64, 64));
            painter.drawPixmap(312, 40, TrayVisual::icon(model, {0, 0, true}).pixmap(32, 32));
            painter.end();
            QVERIFY(sheet.save(QDir(capture).filePath(QStringLiteral("tray-critical-attention.png"))));
        }
    }
    void attentionIsLimitedToCriticalMeters() {
        const TrayVisual::AttentionFrame attention{1.0, 0.3, true};
        auto normal = TrayVisual::build(ready(), {provider("Claude", {meter("session", 35)})}, "Claude",
                                        [](const QString &, const QVariantMap &) { return QVariantMap{{"severity", 0}}; }, now);
        QCOMPARE(TrayVisual::icon(normal, attention).pixmap(64, 64).toImage(),
                 TrayVisual::icon(normal).pixmap(64, 64).toImage());
        normal.kind = TrayVisual::Kind::Offline;
        normal.level = Usage::WarningLevel::Critical;
        QCOMPARE(TrayVisual::icon(normal, attention).pixmap(64, 64).toImage(),
                 TrayVisual::icon(normal).pixmap(64, 64).toImage());
    }
    void flashAddsABrightAlertFrame() {
        const auto model = TrayVisual::build(ready(), {provider("Grok", {meter("weekly", 97)})}, "Grok", assess, now);
        const QImage normal = TrayVisual::icon(model).pixmap(64, 64).toImage();
        const QImage flashed = TrayVisual::icon(model, {0, 0, true}).pixmap(64, 64).toImage();
        QVERIFY(flashed != normal);
        QCOMPARE(flashed.pixelColor(32, 3), QColor("#ffb347"));
    }
};
QTEST_MAIN(TrayTest)
#include "test_tray.moc"
