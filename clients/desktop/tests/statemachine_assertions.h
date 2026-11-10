#pragma once

#include <QRegularExpression>
#include <QTest>

inline void failOnStateMachineWarning() {
    QTest::failOnWarning(QRegularExpression(QStringLiteral("^headroom\\.statemachine ")));
}
