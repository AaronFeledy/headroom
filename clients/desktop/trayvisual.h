#pragma once
#include "warning.h"
#include <QDateTime>
#include <QHash>
#include <QIcon>
#include <QVariantList>
#include <QVariantMap>
#include <functional>

namespace TrayVisual {
enum class Kind { Setup, Connecting, Idle, Usage, Exhausted, Offline, AuthError, ApiError, Malformed, ProviderError };
struct Model {
    Kind kind = Kind::Setup;
    QString provider;
    QString providerType;
    QString tooltip;
    bool unread = false;
    double used = -1;
    double expected = -1;
    Usage::WarningLevel level = Usage::WarningLevel::Normal;
    Usage::WarningLevel secondary = Usage::WarningLevel::Normal;
    // Whether each successfully read provider has a Critical meter, including
    // providers the tray isn't showing, so their episodes stay current.
    QHash<QString, bool> critical;
};
struct AttentionFrame {
    double fire = 0;
    double phase = 0;
    bool flash = false;
};
using Assessment = std::function<QVariantMap(const QString &, const QVariantMap &)>;
// The ring shows the shown provider's percent meter with id `meter`, or its
// first percent meter when `meter` is empty or absent.
Model build(const QVariantMap &state, const QVariantList &providers, const QString &shown, const QString &meter,
            const Assessment &assessment, const QDateTime &now = QDateTime::currentDateTimeUtc());
// Percentage points a provider in the same tier must lead by to take the tray.
constexpr double SwitchMargin = 5;
struct Selection {
    QString key;   // provider row key; empty when nothing qualifies
    QString meter; // id of that provider's most constrained meter
};
// The provider closest to a limit, ranked by its most constrained meter's
// shared warning tier and then percent used, among successful readings with a
// current percent meter. The incumbent row keeps the tray until another
// provider reaches a higher tier or leads by SwitchMargin, so close providers
// don't flap between polls. Ties go to the provider and meter listed first.
Selection constrainedProvider(const QVariantList &providers, const QString &incumbent,
                            const Assessment &assessment, const QDateTime &now = QDateTime::currentDateTimeUtc());
QIcon icon(const Model &model, const AttentionFrame &attention = {});
}
