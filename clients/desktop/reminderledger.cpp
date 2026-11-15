#include "reminderledger.h"
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QRegularExpression>
#include <QSaveFile>
#include <algorithm>
#include <cmath>
#include <utility>

namespace {
constexpr qint64 MatchMs = 60000;
constexpr qint64 RetentionMs = 2 * 86400000;
constexpr int MaxRecords = 512;
const QString ReadError = QStringLiteral("Reminder history could not be read and will be replaced.");
const QString NewerError = QStringLiteral("Reminder history was written by a newer Headroom version and is not saved this session.");
const QString SaveError = QStringLiteral("Reminder history could not be saved. Reminders may repeat after a restart.");

bool knownRecord(const QString &type, const QString &meter) {
    if (type == "expiry") return meter.isEmpty();
    if (type == "unused") return meter == "weekly";
    return type == "session" && meter == "session";
}
}

ReminderLedger::ReminderLedger(QString path) : m_path(std::move(path)) {
    load();
}

void ReminderLedger::load() {
    if (m_path.isEmpty()) return;
    QFile file(m_path);
    if (!file.exists()) return;
    if (!file.open(QIODevice::ReadOnly) || file.size() > 4 * 1024 * 1024) {
        m_error = ReadError;
        return;
    }
    const QByteArray data = file.readAll();
    if (file.error() != QFileDevice::NoError) {
        m_error = ReadError;
        return;
    }
    QJsonParseError parseError;
    const QJsonDocument document = QJsonDocument::fromJson(data, &parseError);
    if (parseError.error != QJsonParseError::NoError || !document.isObject()) {
        m_error = ReadError;
        return;
    }
    const auto root = document.object();
    const auto version = root.value("schemaVersion");
    if (!version.isDouble() || !std::isfinite(version.toDouble())
        || std::floor(version.toDouble()) != version.toDouble()) {
        m_error = ReadError;
        return;
    }
    if (version.toDouble() > 1) {
        m_readOnly = true;
        m_error = NewerError;
        return;
    }
    if (version.toDouble() != 1) {
        m_error = ReadError;
        return;
    }
    const auto records = root.value("records");
    if (!records.isArray()) {
        m_error = ReadError;
        return;
    }
    static const QRegularExpression scopePattern(QStringLiteral("\\A[0-9a-f]{32}\\z"));
    for (const auto &entry : records.toArray()) {
        if (!entry.isObject()) continue;
        const auto object = entry.toObject();
        const QString scope = object.value("scope").toString();
        const QString type = object.value("type").toString();
        const QString meter = object.value("meter").toString();
        const auto boundary = object.value("boundary");
        const auto phases = object.value("phases");
        if (!scopePattern.match(scope).hasMatch() || !knownRecord(type, meter)) continue;
        if (!boundary.isDouble() || !phases.isDouble()) continue;
        const double b = boundary.toDouble(), p = phases.toDouble();
        if (!std::isfinite(b) || b <= 0 || b > 9007199254740991.0 || std::floor(b) != b
            || !std::isfinite(p) || p < 0 || p > 3 || std::floor(p) != p) continue;
        m_records.append({scope, type, meter, static_cast<qint64>(b), static_cast<int>(p)});
    }
}

bool ReminderLedger::save() {
    if (!QDir().mkpath(QFileInfo(m_path).absolutePath())) {
        m_error = SaveError;
        return false;
    }
    QJsonArray records;
    for (const auto &record : m_records) {
        records.append(QJsonObject{{"scope", record.scope}, {"type", record.type}, {"meter", record.meter},
            {"boundary", record.boundary}, {"phases", record.phases}});
    }
    QSaveFile file(m_path);
    if (!file.open(QIODevice::WriteOnly)) {
        m_error = SaveError;
        return false;
    }
#ifndef Q_OS_WIN
    if (!file.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner)) {
        file.cancelWriting();
        m_error = SaveError;
        return false;
    }
#endif
    const QByteArray data = QJsonDocument(QJsonObject{{"schemaVersion", 1}, {"records", records}})
        .toJson(QJsonDocument::Compact);
    if (file.write(data) != data.size() || !file.commit()) {
        m_error = SaveError;
        return false;
    }
    m_error.clear();
    return true;
}

int ReminderLedger::locate(const Record &key) const {
    for (int i = 0; i < m_records.size(); ++i) {
        const auto &record = m_records[i];
        if (record.scope == key.scope && record.type == key.type && record.meter == key.meter
            && qAbs(record.boundary - key.boundary) <= MatchMs) return i;
    }
    return -1;
}

int ReminderLedger::add(const Record &record) {
    m_records.append(record);
    m_dirty = true;
    return m_records.size() - 1;
}

void ReminderLedger::setPhases(int index, int phases) {
    if (m_records[index].phases == phases) return;
    m_records[index].phases = phases;
    m_dirty = true;
}

void ReminderLedger::retain(qint64 nowMs) {
    for (int i = m_records.size() - 1; i >= 0; --i) {
        if (m_records[i].boundary < nowMs - RetentionMs) {
            m_records.removeAt(i);
            m_dirty = true;
        }
    }
}

void ReminderLedger::flush() {
    if (m_records.size() > MaxRecords) {
        std::stable_sort(m_records.begin(), m_records.end(), [](const Record &a, const Record &b) {
            return a.boundary < b.boundary;
        });
        while (m_records.size() > MaxRecords) {
            m_records.removeFirst();
            m_dirty = true;
        }
    }
    // A failed write stays dirty, so the next evaluation retries it.
    if (m_dirty) m_dirty = !m_path.isEmpty() && !m_readOnly && !save();
}
