#pragma once
#include <QList>
#include <QString>

class ReminderLedger {
public:
    struct Record {
        QString scope, type, meter;
        qint64 boundary = 0; // epoch milliseconds
        int phases = 0;
    };

    explicit ReminderLedger(QString path = {});
    QString error() const { return m_error; }
    const QList<Record> &records() const { return m_records; }
    int locate(const Record &key) const;
    int add(const Record &record);
    void setPhases(int index, int phases);
    void retain(qint64 nowMs);
    void flush();

private:
    void load();
    bool save();
    QString m_path, m_error;
    QList<Record> m_records;
    bool m_readOnly = false, m_dirty = false;
};
