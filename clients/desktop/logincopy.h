#pragma once
#include <QVariantMap>
#include <QVariantList>

namespace LoginCopy {
QVariantMap compose(const QVariantMap &provider, const QString &mode, const QString &sharing,
                    const QVariantList &helperChecked = {});
// Bounded sign-in command a provider advertises, or empty.
QString command(const QVariantMap &provider);
// Strict https sign-in URL a provider advertises, or empty.
QString signInUrl(const QVariantMap &provider);
}
