#pragma once
#include "warning.h"
#include <QDateTime>
#include <QIcon>
#include <QVariantList>
#include <QVariantMap>
#include <functional>

namespace TrayVisual {
enum class Kind { Setup, Connecting, Idle, Usage, Exhausted, Offline, AuthError, ApiError, Malformed, ProviderError };
struct Model {
    Kind kind = Kind::Setup;
    QString provider;
    QString tooltip;
    bool unread = false;
    double used = -1;
    double expected = -1;
    Usage::WarningLevel level = Usage::WarningLevel::Normal;
    Usage::WarningLevel secondary = Usage::WarningLevel::Normal;
};
struct AttentionFrame {
    double fire = 0;
    double phase = 0;
    bool flash = false;
};
using Assessment = std::function<QVariantMap(const QString &, const QVariantMap &)>;
Model build(const QVariantMap &state, const QVariantList &providers, const QString &primary,
            const Assessment &assessment, const QDateTime &now = QDateTime::currentDateTimeUtc());
QIcon icon(const Model &model, const AttentionFrame &attention = {});
}
