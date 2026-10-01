#include "logincopy.h"
#include "usage.h"
#include <QUrl>

namespace {
QString text(const QVariant &value, int maximum = 200) {
    if (value.metaType().id() != QMetaType::QString) return {};
    const QString result = value.toString();
    if (result.size() > maximum) return {};
    for (const QChar character : result) if (character.unicode() < 32 || character.unicode() == 127) return {};
    return result;
}
}

QVariantMap LoginCopy::compose(const QVariantMap &provider, const QString &mode, const QString &sharing,
                             const QVariantList &helperChecked) {
    if (!provider.contains(QStringLiteral("auth"))) return {};
    const auto auth = provider.value(QStringLiteral("auth")).toMap();
    const QString state = text(auth.value(QStringLiteral("state")));
    QVariantMap result{{QStringLiteral("title"), QStringLiteral("Usage is unavailable")},
        {QStringLiteral("lines"), QStringList{provider.value(QStringLiteral("error")).toString()}},
        {QStringLiteral("command"), QString()}, {QStringLiteral("url"), QString()}};
    if (state != QStringLiteral("expired") && state != QStringLiteral("signed_out")) return result;
    const bool local = mode == QStringLiteral("local");
    const QString location = local ? QStringLiteral("on this computer") : QStringLiteral("on the server");
    const auto source = auth.value(QStringLiteral("source")).toMap();
    const QString kind = text(source.value(QStringLiteral("kind")));
    const QString name = Usage::displayName(text(source.value(QStringLiteral("name")), 80));
    QStringList lines;
    result[QStringLiteral("title")] = state == QStringLiteral("expired") ? QStringLiteral("Sign-in expired") : QStringLiteral("Not signed in");
    if (state == QStringLiteral("signed_out"))
        lines.append(QStringLiteral("Headroom couldn't find a %1 sign-in.").arg(Usage::displayName(provider.value(QStringLiteral("provider_name")).toString())));
    else if (kind == QStringLiteral("cli")) lines.append(QStringLiteral("Your %1 login %2 expired.").arg(name, location));
    else if (kind == QStringLiteral("app")) lines.append(QStringLiteral("Your %1 app sign-in %2 expired.").arg(name, location));
    else if (kind == QStringLiteral("browser")) lines.append(QStringLiteral("Your %1 sign-in to cursor.com %2 expired.").arg(name, location));
    else if (kind == QStringLiteral("desktop")) lines.append(QStringLiteral("The %1 sign-in shared by Headroom expired.").arg(name));
    else if (kind == QStringLiteral("api")) lines.append(QStringLiteral("The credential sent to the server expired."));
    else lines.append(QStringLiteral("Your %1 sign-in %2 expired.").arg(Usage::displayName(provider.value(QStringLiteral("provider_name")).toString()), location));
    const QString command = text(auth.value(QStringLiteral("sign_in_command")));
    if (!command.isEmpty()) {
        result[QStringLiteral("command")] = command;
        lines.append(QStringLiteral("Run %1 %2.").arg(command, location));
    }
    const QString signInUrl = text(auth.value(QStringLiteral("sign_in_url")));
    const QUrl url(signInUrl, QUrl::StrictMode);
    const bool validUrl = url.isValid() && url.scheme() == QStringLiteral("https") && !url.host().isEmpty() && url.userInfo().isEmpty();
    const auto checked = auth.value(QStringLiteral("checked")).toList();
    bool browserOnServer = false;
    for (const auto &value : checked.mid(0, 12))
        if (value.toMap().value(QStringLiteral("kind")) == QStringLiteral("browser")) browserOnServer = true;
    if (!signInUrl.isEmpty() && validUrl) {
        bool openHere = false;
        if (sharing == QStringLiteral("available")) {
            lines.append(local ? QStringLiteral("Or sign in to cursor.com in your browser.")
                : QStringLiteral("Or sign in to cursor.com in a browser on this computer. Headroom shares it with the server."));
            openHere = true;
        } else if (local) { lines.append(QStringLiteral("Or sign in to cursor.com in Firefox.")); openHere = true; }
        else if (sharing == QStringLiteral("insecure")) lines.append(QStringLiteral("Browser sign-ins on this computer can't be shared over an unencrypted connection. Update the server to turn on HTTPS, or connect with SSH."));
        else if (sharing == QStringLiteral("disabled")) lines.append(QStringLiteral("Sharing browser sign-ins is turned off in Settings."));
        else if (browserOnServer) lines.append(QStringLiteral("Or sign in to cursor.com in Firefox on the server."));
        if (openHere) result[QStringLiteral("url")] = signInUrl;
    }
    int notes = 0;
    for (int origin = 0; origin < 2; ++origin) {
        const auto entries = origin == 0 ? checked : helperChecked;
        for (const auto &value : entries.mid(0, 12)) {
            if (notes == 2) break;
            const auto entry = value.toMap();
            const QString entryName = text(entry.value(QStringLiteral("name")), 80);
            if (entryName.isEmpty()) continue;
            if (origin == 0 && entryName == source.value(QStringLiteral("name")).toString()
                && entry.value(QStringLiteral("kind")) == source.value(QStringLiteral("kind"))) continue;
            if (origin == 1 && kind == QStringLiteral("desktop") && entryName == source.value(QStringLiteral("name")).toString()) continue;
            const QString named = Usage::displayName(entryName) + (origin == 0 && !local ? QStringLiteral(" on the server") : QString());
            const QString status = text(entry.value(QStringLiteral("status")));
            QString note;
            if (status == QStringLiteral("encrypted")) note = QStringLiteral("%1 is signed in, but Headroom can't read its cookies.").arg(named);
            else if (status == QStringLiteral("locked")) note = QStringLiteral("Close %1 so Headroom can read its sign-in, or use another browser.").arg(named);
            else if (status == QStringLiteral("expired")) note = QStringLiteral("Your %1 sign-in also expired.").arg(named);
            else if (status == QStringLiteral("unreadable")) note = QStringLiteral("Headroom couldn't read %1.").arg(named);
            if (!note.isEmpty()) { lines.append(note); ++notes; }
        }
    }
    result[QStringLiteral("lines")] = lines;
    return result;
}
