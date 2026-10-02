// Documentation only: production QML, frozen synthetic data, temporary settings.
// No live backend, polling, browser credentials, updater requests or reset actions.
#include "controller.h"
#include "usage.h"
#include "trayvisual.h"
#include "startup.h"
#include "appinfo.h"
#include "updateservice.h"
#include "remoteupdate.h"
#include "palette.h"
#include <QApplication>
#include <QDir>
#include <QFile>
#include <QFontDatabase>
#include <QFontInfo>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQmlExpression>
#include <QQuickItem>
#include <QQuickStyle>
#include <QQuickWindow>
#include <QSvgRenderer>
#include <QPainter>
#include <QPainterPath>
#include <QFontMetricsF>
#include <QTemporaryDir>
#include <QElapsedTimer>
#include <QThread>
#include <cmath>

static QByteArray read(const QString &path) {
    QFile file(path);
    if (!file.open(QIODevice::ReadOnly)) qFatal("Cannot read %s", qPrintable(path));
    return file.readAll();
}
static void write(const QString &path, const QByteArray &bytes) {
    QFile file(path);
    if (!file.open(QIODevice::WriteOnly) || file.write(bytes) != bytes.size())
        qFatal("Cannot write %s", qPrintable(path));
}
static QQuickItem *findItem(QQuickItem *root, const QString &name) {
    if (root->objectName() == name) return root;
    for (auto child : root->childItems()) if (auto found = findItem(child, name)) return found;
    return nullptr;
}
static void settle(int milliseconds) {
    QElapsedTimer timer; timer.start();
    while (timer.elapsed() < milliseconds) {
        QCoreApplication::processEvents(QEventLoop::AllEvents, 20);
        QThread::msleep(5);
    }
}
class RenderBackend final : public Controller {
    Q_OBJECT
public:
    RenderBackend(const QString &settings, const QJsonObject &scene)
        : Controller(settings, nullptr, false, {}, disabledCredentials(), {}, false),
          m_now(QDateTime::fromString(scene["now"].toString(), Qt::ISODate)) {
        if (!m_now.isValid()) qFatal("Invalid frozen scene clock");
        QJsonArray providers;
        for (const auto &entry : scene["providers"].toArray()) {
            auto provider = entry.toObject();
            provider["error"] = QJsonValue::Null;
            provider["is_success"] = true;
            provider["needs_reauth"] = false;
            QJsonArray buckets;
            for (const auto &value : provider["buckets"].toArray()) {
                auto bucket = value.toObject();
                const auto seconds = bucket.take("reset_in_seconds");
                bucket["resets_at"] = seconds.isDouble()
                    ? QJsonValue(m_now.addSecs(seconds.toInteger()).toString(Qt::ISODate))
                    : QJsonValue(QJsonValue::Null);
                if (!bucket.contains("status_text")) bucket["status_text"] = QJsonValue::Null;
                buckets.append(bucket);
            }
            provider["buckets"] = buckets;
            providers.append(provider);
        }
        if (!Usage::parse(QJsonDocument(providers).toJson(), m_samples)) qFatal("Invalid sample fixture");
        refresh();
    }
    void refresh() override { acceptSnapshot(m_samples); }
    Q_INVOKABLE QVariantMap concern(const QString &provider, const QVariantMap &bucket) const {
        return Usage::concern(provider, bucket, m_now);
    }
    Q_INVOKABLE QVariantMap pacing(const QString &provider, const QVariantMap &bucket) const {
        return Usage::pacing(provider, bucket, m_now);
    }
    Q_INVOKABLE QString countdown(const QString &timestamp) const {
        const auto reset = QDateTime::fromString(timestamp, Qt::ISODateWithMs);
        if (!reset.isValid()) return Usage::countdown(timestamp);
        // Keep the production formatter and shift its wall clock to the frozen scene.
        const auto shifted = QDateTime::currentDateTimeUtc().addSecs(m_now.secsTo(reset));
        return Usage::countdown(shifted.toString(Qt::ISODateWithMs));
    }
    Q_INVOKABLE QString resetTimeLabel(const QString &timestamp) const {
        return Usage::resetTimeLabel(timestamp, m_now);
    }
    QDateTime now() const { return m_now; }
private:
    static CredentialServiceOptions disabledCredentials() {
        CredentialServiceOptions options; options.enabled = false; return options;
    }
    QDateTime m_now;
    QVariantList m_samples;
};


static QString textPath(const QString &text, const QString &family, int size, qreal center, qreal baseline) {
    QFont font(family); font.setPixelSize(size); font.setWeight(QFont::Medium);
    QPainterPath path;
    path.addText(center - QFontMetricsF(font).horizontalAdvance(text) / 2, baseline, font, text);
    QString d;
    for (int i = 0; i < path.elementCount(); ++i) {
        const auto element = path.elementAt(i);
        if (element.isMoveTo()) d += QString("M%1 %2 ").arg(element.x).arg(element.y);
        else if (element.isLineTo()) d += QString("L%1 %2 ").arg(element.x).arg(element.y);
        else if (element.type == QPainterPath::CurveToElement) {
            const auto control = path.elementAt(++i), end = path.elementAt(++i);
            d += QString("C%1 %2 %3 %4 %5 %6 ").arg(element.x).arg(element.y)
                .arg(control.x).arg(control.y).arg(end.x).arg(end.y);
        }
    }
    return d.trimmed();
}

int main(int argc, char **argv) {
    qputenv("QT_QPA_PLATFORM", "offscreen");
    qputenv("QT_QUICK_BACKEND", "software");
    QQuickStyle::setStyle("Basic");
    QQuickWindow::setDefaultAlphaBuffer(true);
    QApplication app(argc, argv);
    app.setPalette(headroomPalette());
    app.setApplicationVersion(HEADROOM_VERSION);
    QLocale::setDefault(QLocale(QLocale::English, QLocale::UnitedStates));
    // Reuse Qt's SVG renderer for the vector tray and combined composition.
    if (argc == 5 && QString::fromLocal8Bit(argv[1]) == "--svg") {
        QSvgRenderer svg(QString::fromLocal8Bit(argv[2]));
        if (!svg.isValid()) qFatal("Invalid generated SVG");
        bool valid = false;
        const double scale = QString::fromLocal8Bit(argv[4]).toDouble(&valid);
        if (!valid || scale < 1 || scale > 4) qFatal("Invalid SVG export scale");
        const auto size = svg.defaultSize();
        QImage image(QSize(qRound(size.width() * scale), qRound(size.height() * scale)), QImage::Format_ARGB32_Premultiplied);
        image.fill(Qt::transparent);
        QPainter painter(&image); svg.render(&painter); painter.end();
        if (!image.save(QString::fromLocal8Bit(argv[3]))) qFatal("SVG export failed");
        return 0;
    }
    if (argc != 3) qFatal("Usage: headroom-doc-render SCENE_JSON OUTPUT_DIRECTORY");
    const auto scene = QJsonDocument::fromJson(read(QString::fromLocal8Bit(argv[1]))).object();
    const auto output = QDir(QString::fromLocal8Bit(argv[2]));
    QDir().mkpath(output.absolutePath());
    const auto fonts = QDir(QString(HEADROOM_ROOT) + "/docs/renderings/fonts");
    for (const auto &font : fonts.entryList({"*.ttf"}))
        if (QFontDatabase::addApplicationFont(fonts.filePath(font)) < 0) qFatal("Cannot load rendering font");
    const QFont renderFont(scene["font_family"].toString());
    if (QFontInfo(renderFont).family() != renderFont.family()) qFatal("Rendering font unavailable");
    QTemporaryDir temporary;
    if (!temporary.isValid()) qFatal("Cannot create temporary settings");
    const QString settings = temporary.filePath("settings.json");
    QJsonArray order; for (const auto &entry : scene["providers"].toArray()) order.append(entry.toObject()["provider_name"]);
    write(settings, QJsonDocument(QJsonObject{{"order", order}, {"schemaVersion", 1}, {"connectionMode", "remote"},
        {"url", ""}, {"token", ""}, {"primary", scene["primary"]}, {"notifications", false}}).toJson());
    RenderBackend backend(settings, scene);
    StartupService startup(temporary.path(), QCoreApplication::applicationFilePath(), false);
    AppInfo appInfo; UpdateService updateService(false); RemoteUpdateService remoteUpdate;
    QQmlApplicationEngine engine;
    auto context = engine.rootContext();
    context->setContextProperty("backend", &backend);
    context->setContextProperty("startupService", &startup);
    context->setContextProperty("appInfo", &appInfo);
    context->setContextProperty("updateService", &updateService);
    context->setContextProperty("remoteUpdateService", &remoteUpdate);
    context->setContextProperty("trayAvailable", true);
    context->setContextProperty("startHidden", true);
    context->setContextProperty("captureMode", true);
    engine.load(QUrl::fromLocalFile(QString(HEADROOM_ROOT) + "/clients/desktop/qml/Main.qml"));
    if (engine.rootObjects().isEmpty()) qFatal("Production QML did not load");
    auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
    if (!window) qFatal("Expected application window");
    window->setProperty("font", renderFont);
    auto state = backend.state(); state["updated"] = "Just updated"; window->setProperty("state", state);
    auto theme = QQmlExpression(qmlContext(window), window, "Theme").evaluate().value<QObject *>();
    if (!theme) qFatal("Production theme missing");
    theme->setProperty("reducedMotionOverride", true);
    const int width = scene["width"].toInt();
    if (width < window->minimumWidth() || width >= 700) qFatal("Scene width must select the compact layout");
    window->resize(width, 1400); window->show(); settle(350);
    auto rows = findItem(window->contentItem(), "providerRows");
    auto footer = findItem(window->contentItem(), "stickyFooter");
    if (!rows || !footer) qFatal("Production layout anchors missing");
    const int height = int(std::ceil(rows->height() + footer->height() + 32));
    window->resize(width, height); settle(350);
    QJsonArray geometry;
    for (const auto &entry : backend.providers()) {
        const auto provider = entry.toMap();
        auto card = findItem(window->contentItem(), "providerCard_" + provider["provider_name"].toString());
        if (!card) qFatal("Provider card missing");
        const auto point = card->mapToScene(QPointF());
        if (point.x() < 0 || point.y() < 0 || point.x() + card->width() > width + 1
            || point.y() + card->height() > footer->y() + 1) qFatal("Provider card clipped");
        for (const auto &value : provider["buckets"].toList()) {
            const auto bucket = value.toMap();
            auto meter = findItem(window->contentItem(), "meter_" + provider["provider_name"].toString() + "_" + bucket["id"].toString());
            if (!meter) qFatal("Usage meter missing");
            const auto at = meter->mapToItem(card, QPointF());
            if (at.x() < 0 || at.y() < 0 || at.x() + meter->width() > card->width() + 1
                || at.y() + meter->height() > card->height() + 1) qFatal("Usage meter clipped");
        }
        geometry.append(QJsonObject{{"provider", provider["provider_name"].toString()},
            {"x", point.x()}, {"y", point.y()}, {"width", card->width()}, {"height", card->height()}});
    }
    const auto desktop = window->grabWindow();
    if (desktop.isNull() || !desktop.save(output.filePath("headroom-desktop.png"))) qFatal("UI render failed");
    const auto model = TrayVisual::build(backend.state(), backend.providers(), scene["primary"].toString(),
        [&](const QString &provider, const QVariantMap &bucket) { return backend.concern(provider, bucket); }, backend.now());
    if (model.kind != TrayVisual::Kind::Usage) qFatal("Tray scene requires a usage meter");
    // Match the visible-artwork crop used by providerPixmap() in trayvisual.cpp.
    QImage logo(128, 128, QImage::Format_ARGB32_Premultiplied); logo.fill(Qt::transparent);
    QPainter painter(&logo);
    QSvgRenderer provider(QString(":/provider-icons/%1.svg").arg(model.provider.toLower()));
    provider.render(&painter); painter.end();
    QRect bounds;
    for (int y = 0; y < logo.height(); ++y) for (int x = 0; x < logo.width(); ++x)
        if (logo.pixelColor(x, y).alpha() > 0) bounds |= QRect(x, y, 1, 1);
    if (bounds.isEmpty()) qFatal("Provider artwork missing");
    const auto box = provider.viewBoxF();
    QJsonObject tray{{"provider", model.provider}, {"used", model.used}, {"expected", model.expected},
        {"color", Usage::warningColor(model.level)}, {"secondary", int(model.secondary)},
        {"secondary_color", Usage::warningColor(model.secondary)},
        {"logo_bounds", QJsonArray{box.x() + bounds.x() * box.width() / 128,
            box.y() + bounds.y() * box.height() / 128, bounds.width() * box.width() / 128,
            bounds.height() * box.height() / 128}}};
    QJsonObject colors;
    for (const auto name : {"background", "surface", "inset", "selection", "foreground", "muted"})
        colors[name] = theme->property(name).value<QColor>().name();
    write(output.filePath("render-metadata.json"), QJsonDocument(QJsonObject{
        {"qt_version", qVersion()}, {"font", QFontInfo(renderFont).family()},
        {"width", width}, {"height", height}, {"pixel_width", desktop.width()},
        {"pixel_height", desktop.height()}, {"providers", geometry}, {"tray", tray}, {"colors", colors},
        {"clock_path", textPath(scene["tray_clock"].toString("10:24"), renderFont.family(), 42, 778, 101)},
        {"tooltip_path", textPath("Headroom", renderFont.family(), 20, 200, 203)}}).toJson());
    qInfo("Rendered production QML: %dx%d logical, %dx%d pixels", width, height, desktop.width(), desktop.height());
    return 0;
}
#include "render.moc"
