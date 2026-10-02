import QtQuick
import QtQuick.Controls

Button {
    id: control
    property bool accent: false
    property bool quiet: false
    implicitHeight: 40
    implicitWidth: contentItem.implicitWidth + 30
    hoverEnabled: true
    font.family: "Inter"
    font.pixelSize: 13
    font.weight: Font.Medium
    padding: 14
    contentItem: Text {
        text: control.text
        font: control.font
        color: !control.enabled ? Theme.comment : control.accent ? Theme.background : Theme.foreground
        horizontalAlignment: Text.AlignHCenter
        verticalAlignment: Text.AlignVCenter
    }
    background: Rectangle {
        radius: 9
        color: control.accent ? (control.down ? Theme.purple : control.hovered ? Theme.pink : Theme.purple) : control.down ? Theme.selection : control.hovered ? Theme.selection : control.quiet ? "transparent" : Theme.surface
        border.width: control.visualFocus ? 2 : control.quiet || control.accent ? 0 : 1
        border.color: control.visualFocus ? Theme.purple : Theme.selection
        Behavior on color { objectName: "buttonColorBehavior"; enabled: !captureMode && !Theme.reducedMotion; ColorAnimation { duration: 120 } }
    }
    HoverHandler { cursorShape: Qt.PointingHandCursor }
}
