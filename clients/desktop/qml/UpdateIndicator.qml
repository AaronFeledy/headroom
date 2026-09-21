import QtQuick
import QtQuick.Controls

ActionButton {
    id: control
    property string notificationTarget: ""
    property bool presentingNotifications: false
    NotificationHighlight {
        objectName: "notificationHighlight_" + parent.objectName
        targetKey: parent.notificationTarget
        presenting: parent.presentingNotifications
    }
    property bool needsAttention: false
    property string detail: ""
    readonly property color indicatorColor: needsAttention ? Theme.orange : Theme.purple
    implicitHeight: 28
    implicitWidth: contentItem.implicitWidth + 20
    padding: 8
    font.pixelSize: 11
    Accessible.description: "Open About & Updates. " + detail
    ToolTip.visible: hovered || activeFocus
    ToolTip.text: detail
    ToolTip.delay: 500
    contentItem: Text {
        text: control.text
        elide: Text.ElideRight
        clip: true
        font: control.font
        color: control.indicatorColor
        horizontalAlignment: Text.AlignHCenter
        verticalAlignment: Text.AlignVCenter
    }
    background: Rectangle {
        radius: height / 2
        color: Qt.alpha(control.indicatorColor, control.down ? 0.24 : control.hovered ? 0.18 : 0.10)
        border.width: control.visualFocus ? 2 : 1
        border.color: Qt.alpha(control.indicatorColor, control.visualFocus ? 1 : 0.3)
    }
}
