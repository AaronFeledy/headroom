import QtQuick

Rectangle {
    id: highlight
    required property string targetKey
    property var viewport: null
    property bool presenting: false
    readonly property bool flashing: fade.running
    // The clip is checked before claiming attention, so offscreen meters keep
    // their one-time highlight until scrolled into view during this opening.
    function tryHighlight() {
        if (!presenting || !parent.visible || width <= 0 || height <= 0) return
        if (viewport) {
            const point = mapToItem(viewport, width / 2, height / 2)
            if (point.y < 0 || point.y > viewport.height) return
        }
        if (backend.notifications.claimHighlight(targetKey)) flash()
    }
    function flash() { fade.restart() }
    anchors.fill: parent; anchors.margins: -4
    radius: 6; color: Qt.alpha(Theme.cyan, 0.14)
    border.width: 2; border.color: Theme.cyan
    opacity: 0; z: 2; enabled: false
    onPresentingChanged: {
        if (presenting) Qt.callLater(tryHighlight)
        else { fade.stop(); opacity = 0 }
    }
    onVisibleChanged: if (visible) Qt.callLater(tryHighlight)
    onWidthChanged: Qt.callLater(tryHighlight)
    Connections {
        target: backend.notifications
        function onPresentationChanged() { Qt.callLater(highlight.tryHighlight) }
    }
    Connections {
        target: highlight.viewport ? highlight.viewport.contentItem : null
        function onContentYChanged() { Qt.callLater(highlight.tryHighlight) }
    }
    SequentialAnimation {
        id: fade
        NumberAnimation { target: highlight; property: "opacity"; from: 0; to: 1; duration: 220 }
        PauseAnimation { duration: 4500 }
        NumberAnimation { target: highlight; property: "opacity"; to: 0; duration: 800 }
    }
}
