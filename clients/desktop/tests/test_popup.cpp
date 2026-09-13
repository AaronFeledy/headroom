#include "popup.h"
#include <QtTest>
#include <QQuickWindow>
#include <QScreen>
class PopupTest : public QObject {
    Q_OBJECT
private slots:
    void toggleAndDismiss() {
        QQuickWindow window;
        window.setFlags(Qt::Tool | Qt::FramelessWindowHint);
        TrayPopup popup(&window, true);
        popup.toggle(QPoint(700,580)); QTRY_VERIFY(window.isVisible());
        popup.toggle(QPoint(700,580)); QVERIFY(!window.isVisible());
        popup.show(); QTRY_VERIFY(window.isActive());
        QWindow child; child.setTransientParent(&window); child.setFlags(Qt::Tool);
        child.show(); child.requestActivate(); QTRY_VERIFY(child.isActive());
        QTest::qWait(200); QVERIFY(window.isVisible());
        QWindow outside; outside.show(); outside.requestActivate(); QTRY_VERIFY(outside.isActive());
        // A transient child may have taken focus before an outside window did.
        QTRY_VERIFY(!window.isVisible());
    }
    void unknownAnchorStillTogglesVisibility() {
        QQuickWindow window;
        TrayPopup popup(&window, true);
        popup.toggle({}, false); QTRY_VERIFY(window.isVisible());
        popup.toggle({}, false); QVERIFY(!window.isVisible());
    }
    void reopeningTracksClickedIconAndKeepsSize() {
        QQuickWindow window;
        window.setFlags(Qt::Tool | Qt::FramelessWindowHint);
        const QRect screen = window.screen()->geometry();
        const QRect work = window.screen()->availableGeometry();
        const QSize preferred(520, 440);
        TrayPopup popup(&window, true, preferred);
        const QPoint firstAnchor(screen.left() + 50, screen.top() + 10);
        popup.toggle(firstAnchor);
        QTRY_VERIFY(window.isVisible());
        QTRY_COMPARE(window.geometry(), PopupPlacement::bounds(screen, work, firstAnchor, preferred));
        const QRect first = window.geometry();
        popup.toggle(firstAnchor);
        QVERIFY(!window.isVisible());
        const QPoint secondAnchor(screen.right() - 50, screen.bottom() - 10);
        popup.toggle(secondAnchor);
        QTRY_VERIFY(window.isVisible());
        QTRY_COMPARE(window.geometry(), PopupPlacement::bounds(screen, work, secondAnchor, preferred));
        QCOMPARE(window.size(), first.size());
        QVERIFY(window.position() != first.topLeft());
    }
    void placement_data() {
        QTest::addColumn<QRect>("screen"); QTest::addColumn<QRect>("work");
        QTest::addColumn<QPoint>("anchor"); QTest::addColumn<QString>("edge");
        const QRect screen(0, 0, 1920, 1080);
        QTest::newRow("bottom") << screen << QRect(0,0,1920,1032) << QPoint(1800,1056) << "bottom";
        QTest::newRow("top") << screen << QRect(0,48,1920,1032) << QPoint(1500,24) << "top";
        QTest::newRow("left") << screen << QRect(48,0,1872,1080) << QPoint(24,540) << "left";
        QTest::newRow("right") << screen << QRect(0,0,1872,1080) << QPoint(1896,540) << "right";
        QTest::newRow("negative-monitor") << QRect(-1920,0,1920,1080) << QRect(-1920,0,1920,1032) << QPoint(-200,1056) << "bottom";
        QTest::newRow("small-screen") << QRect(0,0,800,600) << QRect(0,0,800,560) << QPoint(700,580) << "bottom";
        QTest::newRow("scaled-logical-monitor") << QRect(0,0,1707,960) << QRect(0,32,1707,888) << QPoint(1600,944) << "bottom";
        QTest::newRow("scaled-negative-monitor") << QRect(-1707,0,1707,960) << QRect(-1707,32,1707,888) << QPoint(-1600,16) << "top";
    }
    void placement() {
        QFETCH(QRect, screen); QFETCH(QRect, work); QFETCH(QPoint, anchor); QFETCH(QString, edge);
        const auto popup = PopupPlacement::bounds(screen, work, anchor, QSize(960,820));
        QVERIFY(work.adjusted(24,24,-24,-24).contains(popup));
        if (edge == "bottom") QVERIFY(popup.bottom() < anchor.y());
        if (edge == "top") QVERIFY(popup.top() > anchor.y());
        if (edge == "left") QVERIFY(popup.left() > anchor.x());
        if (edge == "right") QVERIFY(popup.right() < anchor.x());
    }
    void clampsOversizedPopupAndUnknownAnchor() {
        const QRect screen(100, 200, 640, 480), work(100, 240, 640, 440);
        const auto popup = PopupPlacement::bounds(screen, work, QPoint(-9000, -9000), QSize(1200, 900));
        QCOMPARE(popup, work.adjusted(24, 24, -24, -24));
    }
    void constrainsRememberedSizeForCurrentScreen() {
        QCOMPARE(PopupPlacement::constrainedSize(QSize(1896, 1056), QSize(1200, 900)), QSize(1200, 900));
        QCOMPARE(PopupPlacement::constrainedSize(QSize(1896, 1056), QSize(9000, 9000)), QSize(1600, 1056));
        QCOMPARE(PopupPlacement::constrainedSize(QSize(700, 500), QSize(200, 100)), QSize(460, 420));
        QCOMPARE(PopupPlacement::constrainedSize(QSize(400, 300), QSize(1200, 900)), QSize(400, 300));
    }
    void resizeHitTestingCoversEdgesAndCorners() {
        const QSize size(960, 820);
        QCOMPARE(PopupPlacement::resizeEdges(size, QPoint(0, 0)), Qt::Edges(Qt::LeftEdge | Qt::TopEdge));
        QCOMPARE(PopupPlacement::resizeEdges(size, QPoint(959, 410)), Qt::Edges(Qt::RightEdge));
        QCOMPARE(PopupPlacement::resizeEdges(size, QPoint(480, 819)), Qt::Edges(Qt::BottomEdge));
        QCOMPARE(PopupPlacement::resizeEdges(size, QPoint(480, 410)), Qt::Edges{});
    }
    void fallbackGeometryUsesTrackedCoordinates() {
        const QRect work(-1896, 24, 1848, 1032);
        const QRect start(-1500, 100, 700, 600);
        const QRect first = PopupPlacement::resizedBounds(start, Qt::LeftEdge, QPoint(20, 0), work);
        QCOMPARE(first.left(), -1480);
        QCOMPARE(first.right(), start.right());
        QCOMPARE(first.size(), QSize(680, 600));
        // Before an asynchronous LayerShell configure, another local event is
        // still relative to the configured start rectangle, so the total delta
        // remains relative to that rectangle rather than the pending target.
        const QRect beforeConfigure = PopupPlacement::resizedBounds(start, Qt::LeftEdge, QPoint(30, 0), work);
        QCOMPARE(beforeConfigure.left(), -1470);
        QCOMPARE(beforeConfigure.right(), start.right());
        // After configure, the same pointer is 10 pixels into the moved surface.
        const QRect afterConfigure = PopupPlacement::resizedBounds(first, Qt::LeftEdge, QPoint(10, 0), work);
        QCOMPARE(afterConfigure, beforeConfigure);
    }
    void resizeFlushesOnHideAndSurvivesReopen() {
        QQuickWindow window;
        QSize saved;
        int writes = 0;
        TrayPopup popup(&window, false, QSize(700, 600), [&](QSize size) { saved = size; ++writes; });
        popup.show();
        QTRY_VERIFY(window.isVisible());
        window.resize(640, 520);
        QTRY_COMPARE(window.size(), QSize(640, 520));
        QCoreApplication::processEvents();
        window.hide();
        QCOMPARE(saved, QSize(640, 520));
        QCOMPARE(writes, 1);
        popup.show();
        QCOMPARE(window.size(), QSize(640, 520));
    }
    void hiddenScreenClampDoesNotReplacePreferredSize() {
        QQuickWindow window;
        int writes = 0;
        {
            TrayPopup popup(&window, false, QSize(1200, 900), [&](QSize) { ++writes; });
            window.resize(700, 500); // Models a programmatic clamp while hidden.
        }
        QCOMPARE(writes, 0);
    }
    void fallbackEdgeDragResizesWindow() {
        QQuickWindow window;
        TrayPopup popup(&window, false, QSize(700, 600));
        popup.show();
        QTRY_VERIFY(window.isVisible());
        const QSize before = window.size();
        const QPoint corner(before.width() - 1, before.height() - 1);
        QTest::mousePress(&window, Qt::LeftButton, Qt::NoModifier, corner);
        QTest::mouseMove(&window, corner - QPoint(80, 60), 10);
        QTest::mouseRelease(&window, Qt::LeftButton, Qt::NoModifier, corner - QPoint(80, 60));
        QTRY_COMPARE(window.size(), before - QSize(80, 60));
    }
};
QTEST_MAIN(PopupTest)
#include "test_popup.moc"
