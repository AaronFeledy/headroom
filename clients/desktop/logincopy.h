#pragma once
#include <QVariantMap>
#include <QVariantList>

namespace LoginCopy {
QVariantMap compose(const QVariantMap &provider, const QString &mode, const QString &sharing,
                    const QVariantList &helperChecked = {});
}
