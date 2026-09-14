import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Window

Popup {
    id: panel
    parent: Overlay.overlay
    anchors.centerIn: parent
    width: Math.min(840, parent.width - 32)
    height: Math.min(640, parent.height - 32)
    modal: true; focus: true; padding: 24
    closePolicy: Popup.CloseOnPressOutside
    background: Rectangle { color: Theme.surface; radius: 18; border.color: Theme.selection }
    Overlay.modal: Rectangle { color: Theme.overlay; radius: Theme.windowRadius }
    ColumnLayout {
        anchors.fill: parent; spacing: 14
        RowLayout {
            Layout.fillWidth: true
            Text { text: "Diagnostics"; font.pixelSize: 22; color: Theme.foreground; Layout.fillWidth: true }
            ActionButton { text: "×"; quiet: true; implicitWidth: 32; Accessible.name: "Close diagnostics"; onClicked: panel.close() }
        }
        Text { text: "Last 500 events · this session only · times in UTC"; color: Theme.muted; font.pixelSize: 12; Layout.fillWidth: true; wrapMode: Text.WordWrap }
        Text { text: "Connection and warning events are recorded without tokens, account details, or response bodies."; color: Theme.muted; font.pixelSize: 11; Layout.fillWidth: true; wrapMode: Text.WordWrap }
        Rectangle {
            Layout.fillWidth: true; Layout.fillHeight: true; color: Theme.inset; radius: 8
            ScrollView {
                anchors.fill: parent; anchors.margins: 10; clip: true
                TextArea {
                    objectName: "diagnosticLog"
                    text: backend.diagnosticText()
                    property var entries: backend.diagnostics
                    onEntriesChanged: text = backend.diagnosticText()
                    readOnly: true; selectByMouse: true; wrapMode: TextEdit.Wrap
                    color: Theme.foreground; font.family: "monospace"; font.pixelSize: 11
                    background: null
                    placeholderText: "No events recorded."; placeholderTextColor: Theme.muted
                    Accessible.name: "Diagnostic events"
                    Keys.priority: Keys.BeforeItem
                    Keys.onShortcutOverride: (event) => { if (event.key === Qt.Key_Escape) event.accepted = true }
                    Keys.onPressed: (event) => {
                        if (event.key !== Qt.Key_Escape) return
                        const host = Window.window
                        if (host && typeof host.dismissOverlayOrHide === "function")
                            host.dismissOverlayOrHide()
                        else
                            panel.close()
                        event.accepted = true
                    }
                }
            }
        }
        RowLayout {
            Layout.fillWidth: true
            Text { text: backend.state.status === "offline" ? "Next retry in " + backend.state.retrySeconds + "s" : "Connection: " + backend.state.status; color: Theme.muted; font.pixelSize: 11; Layout.fillWidth: true }
            ActionButton { text: "Clear"; quiet: true; onClicked: backend.clearDiagnostics() }
            ActionButton { text: "Copy log"; onClicked: backend.copyText(backend.diagnosticText()) }
        }
    }
}
