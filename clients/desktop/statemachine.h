#pragma once

#include <QLoggingCategory>
#include <QString>

inline const QLoggingCategory &headroomStateMachine() {
    static const QLoggingCategory category("headroom.statemachine");
    return category;
}

inline void reportStateMachineViolation(const char *machine, const QString &from, const QString &to) {
    qCWarning(headroomStateMachine).noquote() << "headroom.statemachine" << machine << from << "->" << to;
}
