import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

ColumnLayout {
    spacing: 5
    CheckBox {
        id: startup
        objectName: "startAtLogin"
        text: "Start Headroom when I sign in"
        checked: startupService.enabled
        enabled: startupService.available
        font.pixelSize: 12
        implicitHeight: 32
        onClicked: {
            startupService.setEnabled(checked)
            // A failed write must return the checkbox to the persisted state.
            checked = Qt.binding(function() { return startupService.enabled })
        }
        indicator: Rectangle {
            x: 0; y: (startup.height - height) / 2; width: 20; height: 20; radius: 5
            color: startup.checked ? Theme.purple : Theme.background
            border.color: startup.activeFocus ? Theme.purple : Theme.selection
            opacity: startup.enabled ? 1 : 0.5
            Text { anchors.centerIn: parent; text: startup.checked ? "✓" : ""; color: Theme.background; font.pixelSize: 13 }
        }
        contentItem: Text {
            text: startup.text; font: startup.font
            color: startup.enabled ? Theme.foreground : Theme.muted
            leftPadding: 30; verticalAlignment: Text.AlignVCenter
        }
    }
    Text {
        Layout.fillWidth: true
        text: !startupService.available ? "Start at login is disabled for this session."
              : Qt.platform.os === "osx" ? "Opens in the menu bar at your next login."
              : "Opens in the system tray. This setting applies immediately."
        wrapMode: Text.WordWrap; color: Theme.muted; font.pixelSize: 11
    }
    Text {
        Layout.fillWidth: true; visible: text.length > 0
        text: startupService.error
        wrapMode: Text.WordWrap; color: Theme.red; font.pixelSize: 12
    }
    Component.onCompleted: startupService.refresh()
}
