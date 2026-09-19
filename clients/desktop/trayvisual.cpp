#include "trayvisual.h"
#include "usage.h"
#include <QPainter>
#include <QPainterPath>
#include <QPixmapCache>
#include <cmath>

namespace {
QString resetDescription(const QVariantMap &bucket, const QDateTime &now) {
    const auto reset = QDateTime::fromString(bucket["resets_at"].toString(), Qt::ISODateWithMs).toUTC();
    if (!reset.isValid()) return "reset time unavailable";
    const qint64 seconds = now.secsTo(reset);
    if (seconds <= 0) return "awaiting reset update";
    const qint64 minutes = (seconds + 59) / 60;
    QString remaining;
    if (minutes >= 1440) remaining = QString("%1d %2h").arg(minutes / 1440).arg(minutes % 1440 / 60);
    else if (minutes >= 60) remaining = QString("%1h %2m").arg(minutes / 60).arg(minutes % 60);
    else remaining = QString("%1m").arg(minutes);
    return "Resets in " + remaining;
}
Usage::WarningLevel level(const QVariantMap &assessment) {
    return Usage::WarningLevel(qBound(0, assessment["severity"].toInt(), 3));
}
bool isMeter(TrayVisual::Kind kind) {
    return kind == TrayVisual::Kind::Usage || kind == TrayVisual::Kind::Exhausted;
}
QPixmap providerPixmap(const QString &provider) {
    if (provider.isEmpty()) return {};
    const QString key = QStringLiteral("Headroom/tray-provider/") + provider.toLower();
    QPixmap result;
    if (QPixmapCache::find(key, &result)) return result;
    const QIcon icon(QString(":/provider-icons/%1.svg").arg(provider.toLower()));
    if (icon.isNull()) return {};
    const QImage image = icon.pixmap(128, 128).toImage();
    // Favicons include different transparent margins. Fit the actual artwork
    // consistently inside the meter, without changing the shared brand assets.
    QRect bounds;
    for (int y = 0; y < image.height(); ++y)
        for (int x = 0; x < image.width(); ++x)
            if (image.pixelColor(x, y).alpha() > 0) bounds |= QRect(x, y, 1, 1);
    if (bounds.isEmpty()) return {};
    result = QPixmap::fromImage(image.copy(bounds));
    QPixmapCache::insert(key, result);
    return result;
}
QString statusText(TrayVisual::Kind kind) {
    using K = TrayVisual::Kind;
    switch (kind) {
    case K::Setup: return "Connect a backend in Settings";
    case K::Connecting: return "Connecting to backend";
    case K::Idle: return "No usage meter available for the selected provider";
    case K::Offline: return "Backend offline";
    case K::AuthError: return "Update the backend token in Settings";
    case K::ApiError: return "Backend HTTP error";
    case K::Malformed: return "Unexpected backend response";
    case K::ProviderError: return "Provider unavailable · open for details";
    case K::Exhausted: return "Selected meter exhausted";
    case K::Usage: return {};
    }
    return {};
}
}

TrayVisual::Model TrayVisual::build(const QVariantMap &state, const QVariantList &providers,
                                  const QString &primary, const Assessment &assessment, const QDateTime &now) {
    Model result;
    result.provider = primary;
    result.kind = Kind::Idle;
    QString resetText;
    for (const auto &entry : providers) {
        const auto provider = entry.toMap();
        if (provider["provider_name"].toString() != primary) continue;
        if (!provider["is_success"].toBool()) { result.kind = Kind::ProviderError; break; }
        const auto buckets = provider["buckets"].toList();
        bool primaryAssigned = false;
        for (const auto &entry : buckets) {
            const auto bucket = entry.toMap();
            const double used = bucket["utilization"].toDouble();
            const QString status = bucket["status_text"].toString();
            const bool statusOnly = bucket["id"] == "on_demand" && used <= 0 && !status.isEmpty() && !status.contains(" / ");
            if (statusOnly) continue;
            const auto severity = level(assessment(primary, bucket));
            if (!primaryAssigned) {
                primaryAssigned = true;
                result.used = used; result.level = severity;
                const auto pace = Usage::pacing(primary, bucket, now);
                result.expected = pace["available"].toBool() ? pace["expected"].toDouble() : -1;
                result.kind = used >= 100 ? Kind::Exhausted : Kind::Usage;
                resetText = resetDescription(bucket, now);
            } else if (int(severity) > int(result.secondary)) result.secondary = severity;
        }
        break;
    }
    const auto status = state["status"].toString();
    if (status == "offline") {
        const auto error = state["errorKind"].toString();
        result.kind = error == "auth" ? Kind::AuthError : error == "malformed" ? Kind::Malformed
            : error == "api" ? Kind::ApiError : Kind::Offline;
    } else if (status == "connecting" || (state["loading"].toBool() && !state["lastGood"].toLongLong())) result.kind = Kind::Connecting;
    else if (status == "setup") result.kind = Kind::Setup;
    const QString name = primary.isEmpty() ? "Headroom" : Usage::displayName(primary);
    if (isMeter(result.kind)) {
        result.tooltip = QString("%1 · %2% used\n%3").arg(name).arg(qRound(result.used)).arg(resetText);
        const auto highest = Usage::WarningLevel(qMax(int(result.level), int(result.secondary)));
        if (highest != Usage::WarningLevel::Normal) result.tooltip += " · " + Usage::warningName(highest);
    } else result.tooltip = name + "\n" + statusText(result.kind);
    return result;
}

namespace {
void drawFire(QPainter &p, double intensity, double phase) {
    constexpr double tau = 6.28318530717958647692;
    const double flicker = std::sin(phase * tau);
    const double sway = std::sin(phase * tau + 0.8) * 2.2;
    const double scale = 0.72 + 0.28 * intensity;

    p.save();
    p.setOpacity(qBound(0.0, intensity * 1.35, 1.0));
    p.translate(32, 52);
    p.scale(scale, scale * (1.0 + 0.035 * flicker));
    p.translate(-32, -52);

    // Three nested, asymmetric teardrops remain recognizable as a flame at
    // the smallest tray sizes. Phase gently bends the tips instead of adding
    // random noise, so consecutive frames form a calm loop.
    QPainterPath outer;
    outer.moveTo(32, 54);
    outer.cubicTo(20, 54, 13, 46, 17, 36);
    outer.cubicTo(19, 31, 24 + sway, 27, 23 + sway, 18);
    outer.cubicTo(32 + sway, 23, 34 + sway, 29, 35 + sway, 34);
    outer.cubicTo(40 - sway, 29, 42 - sway, 25, 41 - sway, 20);
    outer.cubicTo(51, 29, 54, 39, 49, 47);
    outer.cubicTo(45, 53, 39, 54, 32, 54);
    outer.closeSubpath();
    p.setPen(QPen(QColor("#b8202e"), 1.2, Qt::SolidLine, Qt::RoundCap, Qt::RoundJoin));
    p.setBrush(QColor("#ff3b30"));
    p.drawPath(outer);

    QPainterPath middle;
    middle.moveTo(32, 52);
    middle.cubicTo(24, 52, 20, 47, 22, 40);
    middle.cubicTo(23, 36, 28 + sway * 0.5, 32, 28 + sway * 0.5, 27);
    middle.cubicTo(35 + sway * 0.4, 32, 37, 37, 36, 40);
    middle.cubicTo(40, 37, 42, 34, 42, 31);
    middle.cubicTo(47, 39, 44, 49, 37, 52);
    middle.closeSubpath();
    p.setPen(Qt::NoPen);
    p.setBrush(QColor("#ff9500"));
    p.drawPath(middle);

    QPainterPath core;
    core.moveTo(33, 51);
    core.cubicTo(27, 51, 25, 47, 27, 43);
    core.cubicTo(28, 40, 32 + sway * 0.25, 37, 32 + sway * 0.25, 34);
    core.cubicTo(39, 40, 40, 47, 35, 51);
    core.closeSubpath();
    p.setBrush(QColor("#ffd60a"));
    p.drawPath(core);
    p.restore();
}

void drawUnreadBadge(QPainter &p, bool unread) {
    if (!unread) return;
    p.setOpacity(1.0);
    p.setPen(QPen(QColor("#282a36"), 3));
    p.setBrush(QColor("#8be9fd"));
    p.drawEllipse(QPointF(53, 11), 9, 9);
}

QPixmap renderIcon(const TrayVisual::Model &model, int size, const TrayVisual::AttentionFrame &frame) {
    using TrayVisual::Kind;
    QPixmap pixmap(size, size); pixmap.fill(Qt::transparent);
    QPainter p(&pixmap); p.setRenderHint(QPainter::Antialiasing);
    p.setRenderHint(QPainter::SmoothPixmapTransform);
    p.scale(size / 64.0, size / 64.0);
    const bool meter = isMeter(model.kind);
    const bool critical = meter && (model.level == Usage::WarningLevel::Critical
                                    || model.secondary == Usage::WarningLevel::Critical);
    const double fire = critical ? qBound(0.0, frame.fire, 1.0) : 0.0;
    const bool flash = critical && frame.flash;
    const QColor foreground("#f8f8f2"), track("#44475a"), background("#282a36");
    if (flash) {
        p.setPen(Qt::NoPen);
        p.setBrush(QColor("#671923"));
        p.drawEllipse(QRectF(2.5, 2.5, 59, 59));
    }
    p.setPen(QPen(track, 8, Qt::SolidLine, Qt::RoundCap));
    const QRectF ring(5.5, 5.5, 53, 53);
    p.drawEllipse(ring);
    if (meter) {
        p.setPen(QPen(QColor(Usage::warningColor(model.level)), 8, Qt::SolidLine, Qt::RoundCap));
        // Zero means an empty meter; don't invent visible usage for an empty allowance.
        if (model.used > 0) p.drawArc(ring, 90 * 16, -qRound(qBound(0.0, model.used, 100.0) / 100 * 5760));
        if (model.expected >= 0) {
            const double angle = (model.expected / 100 * 360 - 90) * 3.14159265358979323846 / 180;
            const QPointF unit(std::cos(angle), std::sin(angle));
            const QPointF center(32, 32);
            // Extend equally beyond both edges of the 8px ring. Flat caps let
            // the tick reach the canvas edge without a projecting cap.
            const QPointF inner = center + unit * 21, outer = center + unit * 32;
            p.setPen(QPen(background, 5, Qt::SolidLine, Qt::FlatCap)); p.drawLine(inner, outer);
            p.setPen(QPen(foreground, 3, Qt::SolidLine, Qt::FlatCap)); p.drawLine(inner, outer);
        }
    }
    if (flash) {
        p.setPen(QPen(QColor("#ffb347"), 3.5, Qt::SolidLine, Qt::RoundCap));
        p.setBrush(Qt::NoBrush);
        p.drawEllipse(QRectF(2.5, 2.5, 59, 59));
    }
    if (model.kind == Kind::Offline) {
        p.setPen(QPen(QColor("#ff5555"), 6, Qt::SolidLine, Qt::RoundCap));
        p.drawLine(QPointF(21, 21), QPointF(43, 43));
        p.drawLine(QPointF(43, 21), QPointF(21, 43));
        drawUnreadBadge(p, model.unread);
        return pixmap;
    }
    const QPixmap provider = providerPixmap(model.provider);
    if (fire > 0) p.setOpacity(1.0 - fire);
    if (!provider.isNull()) {
        const QSizeF logoSize = QSizeF(provider.size()).scaled(QSizeF(32, 32), Qt::KeepAspectRatio);
        const QRectF logo(QPointF(32 - logoSize.width() / 2, 32 - logoSize.height() / 2), logoSize);
        p.drawPixmap(logo, provider, QRectF(provider.rect()));
    } else {
        p.setPen(foreground);
        QFont font("sans-serif"); font.setPixelSize(27); font.setWeight(QFont::DemiBold); p.setFont(font);
        p.drawText(QRect(16, 16, 32, 32), Qt::AlignCenter, "H");
    }
    p.setOpacity(1.0);
    if (fire > 0) drawFire(p, fire, frame.phase - std::floor(frame.phase));
    p.setPen(foreground); p.setFont(QFont("sans-serif", 10, QFont::DemiBold));
    if (!meter) {
        QString glyph; QColor color("#6272a4");
        switch (model.kind) {
        case Kind::Setup: glyph = "+"; color = QColor("#bd93f9"); break;
        case Kind::Connecting: glyph = "…"; color = QColor("#8be9fd"); break;
        case Kind::Idle: glyph = "–"; break;
        case Kind::AuthError: color = QColor("#ff5555"); break;
        case Kind::ApiError: glyph = "!"; color = QColor("#ff5555"); break;
        case Kind::Malformed: glyph = "?"; color = QColor("#ff5555"); break;
        case Kind::ProviderError: glyph = "!"; color = QColor("#ffb86c"); break;
        default: break;
        }
        p.setPen(QPen(background, 3)); p.setBrush(color); p.drawEllipse(QPointF(53, 52), 9, 9);
        p.setPen(QPen(background, 2)); p.setBrush(Qt::NoBrush);
        if (model.kind == Kind::AuthError) {
            p.drawRoundedRect(QRectF(49.5, 51, 7, 6), 1, 1);
            p.drawArc(QRectF(50.5, 46.5, 5, 8), 0, 180 * 16);
        } else {
            p.setFont(QFont("sans-serif", 12, QFont::Bold));
            p.drawText(QRect(44, 43, 18, 18), Qt::AlignCenter, glyph);
        }
    }
    if (meter && model.secondary != Usage::WarningLevel::Normal) {
        p.setPen(QPen(background, 3)); p.setBrush(QColor(Usage::warningColor(model.secondary)));
        p.drawEllipse(QPointF(53, 52), 8, 8);
    }
    if (model.kind == Kind::Exhausted) {
        p.setPen(QPen(background, 3)); p.setBrush(QColor(Usage::warningColor(model.level)));
        p.drawEllipse(QPointF(11, 12), 8, 8);
        p.setPen(foreground); p.drawLine(QPointF(7, 12), QPointF(15, 12));
    }
    drawUnreadBadge(p, model.unread);
    return pixmap;
}
}

QIcon TrayVisual::icon(const Model &model, const AttentionFrame &attention) {
    QIcon result;
    // Supply native tray sizes so the shell does not have to reduce a single
    // large bitmap, particularly at Windows fractional display scales.
    for (const int size : {16, 20, 22, 24, 32, 40, 48, 64}) result.addPixmap(renderIcon(model, size, attention));
    return result;
}
