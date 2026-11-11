#include <QGuiApplication>
#include <QQmlComponent>
#include <QQmlContext>
#include <QQmlEngine>
#include <QQuickItem>
#include <QQuickStyle>
#include <QQuickWindow>
#include <QTemporaryDir>
#include <QtTest>
#include "statemachine_assertions.h"
#include <optional>

// Renders qml/ResetOptions.qml from synthetic policy output inside an isolated
// window. There is no Controller, transport, receipt, or action connection
// here, and no choice is ever activated: the popup is opened, inspected,
// captured, and closed. DO NOT add clicks, confirmations, or intent emission;
// the reset-prohibited suite stays skip-only for a reason.
struct Scene {
    QByteArray name;
    QString provider;
    QString phase;
    bool canConfirm = false, canSchedule = false, canCancel = false, busy = false;
    QString useNowReason, automaticReason, message, deadline;
    std::optional<qint64> availableCount = 3;
    QVariantList kinds;
    QString selectedKind;
};
Q_DECLARE_METATYPE(Scene)

namespace {
const char harness[] = R"(
import QtQuick
import QtQuick.Controls
Window {
    width: 460; height: 420; visible: true
    color: Theme.background
    ResetOptions { objectName: "resetOptions"; parent: Overlay.overlay }
}
)";

QQuickItem *findItem(QQuickItem *root, const QString &name) {
    if (root->objectName() == name) return root;
    for (auto child : root->childItems()) if (auto found = findItem(child, name)) return found;
    return nullptr;
}

QVariantMap syntheticKind(const QString &id, const QString &label, int count, bool usable,
                          const QString &refillText, const QString &triggerText) {
    return {{"id", id}, {"label", label}, {"availableCount", count}, {"usableNow", usable},
        {"countText", QString("%1 %2%3").arg(count).arg(label).arg(count == 1 ? QString() : QStringLiteral("s"))},
        {"title", label.left(1).toUpper() + label.mid(1)},
        {"refillText", refillText}, {"triggerText", triggerText}};
}

QVariantMap synthetic(const Scene &scene) {
    // The same keys the Controller publishes; the values are invented for rendering.
    const bool armed = scene.phase == "armed" || scene.phase == "paused";
    return {{"provider", scene.provider}, {"busy", scene.busy}, {"message", scene.message},
        {"availableCount", scene.availableCount ? QVariant::fromValue(*scene.availableCount) : QVariant()},
        {"automatic", armed}, {"awaitingUsage", scene.phase == "reconciling"},
        {"phase", scene.phase}, {"canConfirm", scene.canConfirm}, {"canSchedule", scene.canSchedule},
        {"canCancel", scene.canCancel}, {"useNowReason", scene.useNowReason},
        {"automaticReason", scene.automaticReason}, {"windowEndsAt", QString()},
        {"kinds", scene.kinds}, {"kindsKnown", !scene.kinds.isEmpty()},
        {"selectedKind", scene.selectedKind}};
}
}

class ResetOptionsTest : public QObject {
    Q_OBJECT
private slots:
    void init() {
        failOnStateMachineWarning();
    }
    void rendersEveryStateWithoutActions_data() {
        QTest::addColumn<Scene>("scene");
        const QString unknownWindow = "The weekly reset time is unknown.";
        const QString awaiting = "Waiting for updated usage after the previous request.";
        const QString inProgress = "A reset request is in progress.";
        const QString alreadyArmed = "Automatic use is already set.";
        const QString deadline = "Tomorrow at 3:00 PM";
        for (const QString provider : {QStringLiteral("Codex"), QStringLiteral("Claude")}) {
            const bool claude = provider == "Claude";
            const QByteArray prefix = provider.toLower().toUtf8() + "-";
            QTest::newRow(prefix + "ready") << Scene{prefix + "ready", provider, "choices", true, true, false, false, "", "", "", ""};
            // ChatGPT: a stale reading blocks both choices. Claude at 90% without
            // usable_now: automatic use may be armed while immediate use may not.
            QTest::newRow(prefix + "unusable") << Scene{prefix + "unusable", provider, "choices", false, claude, false, false,
                claude ? "Anthropic has not reported whether a reset can be used." : "Refresh usage first.",
                claude ? "" : "Refresh usage first.", "", ""};
            QTest::newRow(prefix + "deadline-unknown") << Scene{prefix + "deadline-unknown", provider, "choices", true, false, false, false,
                "", unknownWindow, "", ""};
            QTest::newRow(prefix + "armed") << Scene{prefix + "armed", provider, "armed", true, false, true, false,
                "", alreadyArmed, "", deadline};
            // Paused has no usage reading, so the banked count is unknown rather than zero.
            QTest::newRow(prefix + "paused") << Scene{prefix + "paused", provider, "paused", false, false, true, false,
                "Usage is unavailable right now.", alreadyArmed, "", deadline, std::nullopt};
            QTest::newRow(prefix + "reconciling") << Scene{prefix + "reconciling", provider, "reconciling", false, false, false, false,
                awaiting, awaiting, claude ? "A reset has already been requested. Waiting for updated Claude usage."
                    : "A reset has already been requested. Waiting for weekly usage to drop below 90%.", ""};
            QTest::newRow(prefix + "submitting") << Scene{prefix + "submitting", provider, "submitting", false, false, false, true,
                inProgress, inProgress, "Using one banked reset…", ""};
            QTest::newRow(prefix + "zero-balance") << Scene{prefix + "zero-balance", provider, "reconciling", false, false, false, false,
                awaiting, awaiting, "", "", qint64(0)};
            QTest::newRow(prefix + "large-balance") << Scene{prefix + "large-balance", provider, "choices", true, true, false, false,
                "", "", "", "", qint64(4294967296LL)};
        }
        const QVariantMap weeklyKind = syntheticKind("weekly", "weekly reset", 1, true,
            "Refills Weekly and Fable", "Uses it when Weekly reaches 100%");
        const QVariantMap fableKind = syntheticKind("fable", "Fable reset", 2, true,
            "Refills Fable", "Uses it when Fable reaches 100%");
        QTest::newRow("claude-kind-one") << Scene{"claude-kind-one", "Claude", "choices", true, true, false, false,
            "", "", "", "", qint64(3), QVariantList{weeklyKind}, "weekly"};
        QTest::newRow("claude-kind-many-unselected") << Scene{"claude-kind-many-unselected", "Claude", "choices", false, false, false, false,
            "Choose which banked reset to use.", "Choose which banked reset to use.", "", "", qint64(3),
            QVariantList{weeklyKind, fableKind}, ""};
        QTest::newRow("claude-kind-many-selected") << Scene{"claude-kind-many-selected", "Claude", "choices", true, true, false, false,
            "", "", "", "", qint64(3), QVariantList{weeklyKind, fableKind}, "fable"};
        QTest::newRow("claude-kind-armed") << Scene{"claude-kind-armed", "Claude", "armed", true, false, true, false,
            "", "Automatic use is already set.", "", "Sunday at 10:00 PM", qint64(3), QVariantList{weeklyKind, fableKind}, "fable"};
    }
    void rendersEveryStateWithoutActions() {
        QFETCH(Scene, scene);
        QQmlEngine engine;
        engine.rootContext()->setContextProperty("captureMode", true);
        QQmlComponent component(&engine);
        component.setData(harness, QUrl::fromLocalFile(QString(SOURCE_DIR) + "/qml/ResetOptionsHarness.qml"));
        QVERIFY2(component.isReady(), qPrintable(component.errorString()));
        QScopedPointer<QObject> root(component.create());
        auto window = qobject_cast<QQuickWindow *>(root.data()); QVERIFY(window);
        QVERIFY(QTest::qWaitForWindowExposed(window));
        window->requestActivate();
        auto options = window->findChild<QObject *>("resetOptions"); QVERIFY(options);
        QSignalSpy useNow(options, SIGNAL(useNowRequested()));
        QSignalSpy automatic(options, SIGNAL(automaticRequested()));
        QSignalSpy cancelAutomatic(options, SIGNAL(cancelAutomaticRequested()));
        QVERIFY(options->setProperty("action", synthetic(scene)));
        QVERIFY(options->setProperty("deadlineLabel", scene.deadline));
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString requested = qEnvironmentVariable("HEADROOM_TEST_CAPTURE_DIR");
        const QString captureDir = requested.isEmpty() ? dir.path() : requested;
        QVERIFY(QDir().mkpath(captureDir));
        const bool armed = scene.phase == "armed" || scene.phase == "paused";
        auto overlay = options->property("parent").value<QQuickItem *>(); QVERIFY(overlay);
        const QList<QSize> sizes = scene.kinds.isEmpty()
            ? QList<QSize>{QSize(460, 420), QSize(1000, 700)}
            : QList<QSize>{QSize(460, 620), QSize(1000, 700)};
        for (const QSize size : sizes) {
            window->resize(size);
            // window->size() changes at once; the content item and overlay only follow
            // the delivered resize event, so wait for the scene before opening.
            QTRY_COMPARE(overlay->size(), QSizeF(size));
            QVERIFY(QMetaObject::invokeMethod(options, "open"));
            QTRY_VERIFY(options->property("opened").toBool());
            auto popup = [&] { return QRectF(options->property("x").toReal(), options->property("y").toReal(),
                options->property("width").toReal(), options->property("height").toReal()); };
            auto offCentre = [&] { return popup().center() - QRectF(QPointF(), QSizeF(size)).center(); };
            // Short bounded copy: the options fit the smallest window without scrolling,
            // at the width the popup asks for, centred in the overlay before any capture.
            QTRY_COMPARE(popup().width(), qMin<qreal>(440, size.width() - 32));
            QTRY_VERIFY2(popup().height() <= size.height(),
                qPrintable(QString("popup %1 tall in a %2 window").arg(popup().height()).arg(size.height())));
            QTRY_VERIFY2(qAbs(offCentre().x()) <= 1 && qAbs(offCentre().y()) <= 1,
                qPrintable(QString("popup centre off by %1,%2 in a %3x%4 window")
                    .arg(offCentre().x()).arg(offCentre().y()).arg(size.width()).arg(size.height())));
            // Close keeps the initial focus; a spending choice is never the default.
            QTRY_VERIFY(window->activeFocusItem());
            QCOMPARE(window->activeFocusItem()->objectName(), QString("resetOptionsClose"));
            auto title = findItem(window->contentItem(), "resetOptionsTitle"); QVERIFY(title);
            // The view names the provider people see, never the API key.
            QVERIFY(title->property("text").toString().contains(scene.provider == "Claude" ? "Claude" : "ChatGPT"));
            QVERIFY(!title->property("text").toString().contains("Codex"));
            auto summary = findItem(window->contentItem(), "resetOptionsSummary"); QVERIFY(summary);
            auto balance = findItem(window->contentItem(), "resetOptionsBalance"); QVERIFY(balance);
            auto warning = findItem(window->contentItem(), "useNowWarning"); QVERIFY(warning);
            auto confirm = findItem(window->contentItem(), "confirmBankedReset"); QVERIFY(confirm);
            auto schedule = findItem(window->contentItem(), "scheduleBankedReset"); QVERIFY(schedule);
            auto cancel = findItem(window->contentItem(), "cancelScheduledBankedReset"); QVERIFY(cancel);
            auto useNowReason = findItem(window->contentItem(), "useNowReason"); QVERIFY(useNowReason);
            auto automaticReason = findItem(window->contentItem(), "automaticReason"); QVERIFY(automaticReason);
            auto deadline = findItem(window->contentItem(), "resetOptionsDeadline"); QVERIFY(deadline);
            auto message = findItem(window->contentItem(), "resetOptionsMessage"); QVERIFY(message);
            auto chooser = findItem(window->contentItem(), "resetKindChooser"); QVERIFY(chooser);
            auto kindTrigger = findItem(window->contentItem(), "resetOptionsKindTrigger"); QVERIFY(kindTrigger);
            QCOMPARE(confirm->isEnabled(), scene.canConfirm);
            QCOMPARE(confirm->property("text").toString(), scene.busy ? QString("Using reset…") : QString("Use now"));
            QCOMPARE(schedule->isVisible(), !armed);
            if (!armed) QCOMPARE(schedule->isEnabled(), scene.canSchedule);
            QCOMPARE(cancel->isVisible(), armed);
            if (armed) QCOMPARE(cancel->isEnabled(), scene.canCancel);
            // A known positive balance is shown; an unknown one is never invented.
            // With kinds, the line names the chosen kind or asks for a choice.
            const bool kindAware = !scene.kinds.isEmpty();
            QCOMPARE(balance->isVisible(), kindAware || (scene.availableCount.has_value() && *scene.availableCount > 0));
            if (kindAware) {
                QString chosen, chosenLabel;
                for (const auto &value : scene.kinds) {
                    if (value.toMap()["id"].toString() != scene.selectedKind) continue;
                    chosen = value.toMap()["countText"].toString();
                    chosenLabel = value.toMap()["label"].toString();
                }
                QCOMPARE(balance->property("text").toString(),
                    chosen.isEmpty() ? QString("Choose which banked reset to use") : "Banked: " + chosen);
                // One reset is spent, so the summary names one reset of the chosen
                // kind and never quotes the whole balance as the amount used.
                if (!chosen.isEmpty()) {
                    const QString text = summary->property("text").toString();
                    QVERIFY2(text.contains("ne " + chosenLabel), qPrintable(text));
                    QVERIFY2(!text.contains(chosen) || chosen.startsWith("1 "), qPrintable(text));
                }
            } else if (balance->isVisible()) {
                QVERIFY(balance->property("text").toString().contains(QString::number(*scene.availableCount)));
            }
            // The kind chooser only appears before a spend and is never preselected
            // when several kinds are listed; rendering never activates a row.
            QCOMPARE(chooser->isVisible(), kindAware && !armed && !scene.busy);
            QCOMPARE(kindTrigger->isVisible(), kindAware && !scene.selectedKind.isEmpty());
            if (kindAware) {
                if (!scene.selectedKind.isEmpty())
                    QVERIFY(kindTrigger->property("text").toString().contains("100%"));
                for (const auto &value : scene.kinds) {
                    const auto kind = value.toMap();
                    auto row = findItem(window->contentItem(), "resetKind_" + kind["id"].toString()); QVERIFY(row);
                    // The chooser is offered only before a spend or an arm.
                    QCOMPARE(row->isVisible(), !armed && !scene.busy);
                    QCOMPARE(row->property("text").toString(), kind["title"].toString());
                    QCOMPARE(row->property("accent").toBool(), scene.selectedKind == kind["id"].toString());
                    auto details = findItem(window->contentItem(), "resetKindDetails_" + kind["id"].toString()); QVERIFY(details);
                    QVERIFY(details->property("text").toString().contains(kind["countText"].toString()));
                    QVERIFY(details->property("text").toString().contains(kind["refillText"].toString()));
                    QVERIFY(details->property("text").toString().contains(
                        kind["usableNow"].toBool() ? "Can be used now" : "cannot"));
                }
            } else {
                QVERIFY(!chooser->isVisible());
                QVERIFY(!kindTrigger->isVisible());
            }
            // Wherever Use now can be chosen, armed included, the spend is marked
            // irreversible once: beside the choice, not again in the summary.
            QCOMPARE(warning->isVisible(), scene.canConfirm);
            QVERIFY(!warning->property("text").toString().isEmpty());
            QVERIFY(!summary->property("text").toString().contains(warning->property("text").toString()));
            // A disabled choice always explains itself; an enabled one needs no excuse.
            QCOMPARE(useNowReason->isVisible(), !scene.canConfirm && !scene.busy);
            if (useNowReason->isVisible()) QCOMPARE(useNowReason->property("text").toString(), scene.useNowReason);
            QCOMPARE(automaticReason->isVisible(), !armed && !scene.canSchedule);
            if (automaticReason->isVisible()) QCOMPARE(automaticReason->property("text").toString(), scene.automaticReason);
            QCOMPARE(deadline->isVisible(), armed && !scene.deadline.isEmpty());
            QCOMPARE(message->isVisible(), !scene.message.isEmpty());
            QVERIFY(window->grabWindow().save(QDir(captureDir).filePath(
                QString("headroom-reset-options-%1-%2.png").arg(QString::fromUtf8(scene.name)).arg(size.width()))));
            QVERIFY(QMetaObject::invokeMethod(options, "close"));
            QTRY_VERIFY(!options->property("visible").toBool());
        }
        // Rendering never emitted an intent; nothing here can spend a reset.
        QCOMPARE(useNow.size(), 0); QCOMPARE(automatic.size(), 0); QCOMPARE(cancelAutomatic.size(), 0);
    }
};

int main(int argc, char **argv) {
    QQuickStyle::setStyle("Basic");
    QGuiApplication app(argc, argv);
    ResetOptionsTest test;
    return QTest::qExec(&test, argc, argv);
}
#include "test_resetoptions.moc"
