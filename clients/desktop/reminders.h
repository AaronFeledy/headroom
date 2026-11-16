#pragma once
#include "reminderledger.h"
#include <QDateTime>
#include <QList>
#include <QString>
#include <QVariantList>

struct Reminder {
    enum class Kind { BankedExpiry, UnusedAllowance, SessionDepleted, SessionRestored };
    Kind kind = Kind::BankedExpiry;
    QString target, title, message;
    int severity = 0; // Notifications::post convention: 0 information, 2 warning, 3 critical.
};

// Durable once-per-boundary reminders for value that would otherwise be lost.
// Only a hash of the account scope, the meter, the boundary, and phase bits are
// stored; no provider names, labels, fingerprints, or usage values are written.
class Reminders {
public:
    explicit Reminders(QString path = {}); // Empty path keeps the ledger in memory only.
    // Returns due reminders after recording them as handled. Disabled reminders
    // are recorded silently so enabling them later never replays an old boundary.
    QList<Reminder> evaluate(const QVariantList &providers, const QDateTime &now, bool notify, bool unused);
    QString error() const { return m_ledger.error(); }
private:
    // 0 not due, 1 hour lead, 2 day lead. Marks the phase bits before returning.
    int takeLead(int index, qint64 nowMs);
    void closeOpenSessions(const QString &scope, int except);
    ReminderLedger m_ledger;
};
