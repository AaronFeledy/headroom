import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

Popup {
    id: panel
    property bool updatesRequested: false
    function focusUpdates() {
        const maxY = Math.max(0, settingsScroll.contentHeight - settingsScroll.availableHeight)
        settingsScroll.contentItem.contentY = Math.min(aboutUpdates.y, maxY)
        aboutUpdates.focusHeading()
    }
    function openUpdates() {
        if (opened) Qt.callLater(focusUpdates)
        else {
            updatesRequested = true
            open()
        }
    }
    property int selectedInterval: 60
    property var intervalValues: [15, 30, 60, 120, 300]
    function intervalLabel(seconds) {
        if (seconds === 60) return "1 minute"
        if (seconds % 60 === 0) return (seconds / 60) + " minutes"
        return seconds + " seconds"
    }
    function saveAndConnect() {
        error.text = backend.saveSettings(localMode.checked ? "local" : (sshMode.checked ? "ssh" : "remote"), url.text, token.text,
                                          panel.selectedInterval, notifications.checked,
                                          backend.settings.primary, forget.checked, sshUrl.text)
        if (!error.text) { token.text = ""; panel.close() }
    }
    signal diagnosticsRequested()
    parent: Overlay.overlay
    anchors.centerIn: parent
    width: Math.min(480, parent.width - 32)
    height: Math.min(650, parent.height - 32)
    modal: true; focus: true; padding: 28
    closePolicy: Popup.CloseOnPressOutside
    background: Rectangle { color: Theme.surface; radius: 18; border.color: Theme.selection }
    Overlay.modal: Rectangle { color: Theme.overlay; radius: Theme.windowRadius }
    onOpened: {
        startupService.refresh(); appInfo.refreshServer()
        localMode.checked = backend.settings.mode === "local"
        remoteMode.checked = backend.settings.mode === "remote"
        sshMode.checked = backend.settings.mode === "ssh"
        url.text = backend.settings.url; token.text = ""; forget.checked = false
        sshUrl.text = backend.settings.sshUrl
        selectedInterval = backend.settings.interval
        intervalValues = [15, 30, 60, 120, 300]
        if (intervalValues.indexOf(selectedInterval) < 0) intervalValues = intervalValues.concat([selectedInterval])
        interval.currentIndex = intervalValues.indexOf(selectedInterval)
        notifications.checked = backend.settings.notifications; error.text = ""
        if (updatesRequested) {
            updatesRequested = false
            Qt.callLater(focusUpdates)
        } else {
            settingsScroll.contentItem.contentY = 0
            if (remoteMode.checked) url.forceActiveFocus()
            else if (sshMode.checked) sshUrl.forceActiveFocus()
        }
    }
    component Caption: Text { color: Theme.foreground; font.pixelSize: 12; font.weight: Font.Medium }
    component ConnectionMode: RadioButton {
        id: choice
        implicitHeight: 32; font.pixelSize: 12
        indicator: Rectangle {
            x: 0; y: (choice.height - height) / 2; width: 20; height: 20; radius: 10
            color: Theme.inset
            border.color: choice.checked || choice.activeFocus ? Theme.purple : Theme.muted
            border.width: choice.activeFocus ? 2 : 1
            Rectangle { anchors.centerIn: parent; width: 10; height: 10; radius: 5; color: Theme.purple; visible: choice.checked }
        }
        contentItem: Text { text: choice.text; font: choice.font; color: Theme.foreground; leftPadding: 28; verticalAlignment: Text.AlignVCenter }
    }
    component Check: CheckBox {
        id: check
        implicitHeight: 32; font.pixelSize: 12
        indicator: Rectangle {
            x: 0; y: (check.height - height) / 2; width: 20; height: 20; radius: 5
            color: check.checked ? Theme.purple : Theme.background
            border.color: check.activeFocus ? Theme.purple : Theme.selection
            Text { anchors.centerIn: parent; text: check.checked ? "✓" : ""; color: Theme.background; font.pixelSize: 13 }
        }
        contentItem: Text { text: check.text; font: check.font; color: Theme.foreground; leftPadding: 30; verticalAlignment: Text.AlignVCenter }
    }
    component Entry: TextField {
        Layout.fillWidth: true; implicitHeight: 44; color: Theme.foreground; font.pixelSize: 13; selectByMouse: true
        placeholderTextColor: Theme.muted; leftPadding: 12; rightPadding: 12
        background: Rectangle { radius: 8; color: Theme.inset; border.color: parent.activeFocus ? Theme.purple : Theme.selection }
    }
    ColumnLayout {
        anchors.fill: parent; spacing: 12
    ScrollView {
        id: settingsScroll; objectName: "settingsScroll"
        Layout.fillWidth: true; Layout.fillHeight: true; contentWidth: availableWidth; clip: true
        ScrollBar.vertical.policy: contentHeight > availableHeight ? ScrollBar.AlwaysOn : ScrollBar.AlwaysOff
        ColumnLayout {
            width: settingsScroll.availableWidth - 12; spacing: 17
            RowLayout {
                Layout.fillWidth: true
                Text { text: "Make yourself at home"; color: Theme.foreground; font.pixelSize: 22; font.weight: Font.DemiBold; Layout.fillWidth: true }
                ActionButton { text: "×"; quiet: true; implicitWidth: 32; onClicked: panel.close(); Accessible.name: "Close settings" }
            }
            Text { text: "Connect Headroom to your usage server."; color: Theme.muted; font.pixelSize: 13 }
            Rectangle { Layout.fillWidth: true; height: 1; color: Theme.selection; Layout.topMargin: 4; Layout.bottomMargin: 4 }
            ColumnLayout { Layout.fillWidth: true; spacing: 8
                Caption { text: "CONNECTION" }
                ButtonGroup { id: connectionModes }
                Flow {
                    id: connectionModeFlow; objectName: "connectionModeFlow"
                    Layout.fillWidth: true; spacing: 8
                    Layout.preferredHeight: childrenRect.height
                    ConnectionMode { id: localMode; objectName: "localMode"; text: "Local"; ButtonGroup.group: connectionModes; Accessible.name: "Use local server" }
                    ConnectionMode { id: sshMode; objectName: "sshMode"; text: "SSH · Recommended"; ButtonGroup.group: connectionModes; Accessible.name: "Use SSH, recommended for a remote server" }
                    ConnectionMode { id: remoteMode; objectName: "remoteMode"; text: "HTTP(S)"; ButtonGroup.group: connectionModes; Accessible.name: "Connect directly over HTTP or HTTPS" }
                }
                Text {
                    Layout.fillWidth: true; wrapMode: Text.WordWrap; color: Theme.muted; font.pixelSize: 11
                    text: localMode.checked
                        ? "Use the bundled server or an existing server on this computer."
                        : sshMode.checked
                            ? "Recommended for a remote server. Uses your OpenSSH keys or agent and trusted host keys; server SSH access must be enabled."
                            : "Connect directly to an existing usage server over HTTP or HTTPS."
                }
            }
            ColumnLayout { Layout.fillWidth: true; spacing: 8
                visible: sshMode.checked
                Caption { text: "SSH ADDRESS" }
                Entry { id: sshUrl; objectName: "sshUrl"; placeholderText: "ssh://user@server.example:2222"; Accessible.name: "SSH address" }
            }
            ColumnLayout { Layout.fillWidth: true; spacing: 8
                visible: remoteMode.checked
                Caption { text: "BACKEND ADDRESS" }
                Entry { id: url; objectName: "backendUrl"; placeholderText: "http://localhost:7823"; Accessible.name: "Backend address" }
                Text { text: "The base address of your existing usage API."; color: Theme.muted; font.pixelSize: 11 }
            }
            ColumnLayout { Layout.fillWidth: true; spacing: 8
                visible: remoteMode.checked
                Caption { text: "BEARER TOKEN" }
                Entry { id: token; objectName: "bearerToken"; echoMode: TextInput.Password; placeholderText: backend.settings.hasToken && (localMode.checked ? backend.settings.mode === "local" : backend.settings.mode === "remote" && url.text.trim() === backend.settings.url) ? "Saved token · leave empty to keep" : "Enter token, if your server requires one"; Accessible.name: "Bearer token" }
                Text { text: "Stored locally in your current-user settings file."; color: Theme.muted; font.pixelSize: 11 }
                Check { id: forget; visible: backend.settings.hasToken; text: "Remove saved token"; palette.windowText: Theme.foreground; font.pixelSize: 12 }
            }
            RowLayout {
                Layout.fillWidth: true
                ColumnLayout { Layout.fillWidth: true; spacing: 6
                    Caption { text: "Refresh interval" }
                    Text { text: "Fetch the latest backend readings"; color: Theme.muted; font.pixelSize: 11 }
                }
                ComboBox {
                    id: interval; objectName: "refreshInterval"; model: panel.intervalValues.map(value => panel.intervalLabel(value))
                    implicitWidth: 140; implicitHeight: 40; Accessible.name: "Refresh interval"
                    background: Rectangle { radius: 8; color: Theme.surface; border.color: interval.activeFocus ? Theme.purple : Theme.selection }
                    contentItem: Text { text: interval.displayText; color: Theme.foreground; font.pixelSize: 12; leftPadding: 12; verticalAlignment: Text.AlignVCenter }
                    indicator: Text { x: parent.width - 23; y: 12; text: "⌄"; color: Theme.muted }
                    delegate: ItemDelegate {
                        required property string modelData; required property int index
                        width: interval.width; text: modelData
                        contentItem: Text { text: modelData; color: Theme.foreground; font.pixelSize: 12 }
                        background: Rectangle { color: parent.highlighted ? Theme.selection : Theme.inset }
                        highlighted: interval.highlightedIndex === index
                    }
                    popup.closePolicy: Popup.CloseOnPressOutside
                    popup.background: Rectangle { color: Theme.inset; radius: 8; border.color: Theme.selection }
                    onActivated: panel.selectedInterval = panel.intervalValues[currentIndex]
                }
            }
            Check { id: notifications; text: "Notify about usage, banked resets, and updates"; palette.windowText: Theme.foreground; font.pixelSize: 12 }
            Text {
                text: Qt.platform.os === "osx"
                      ? "Closing the window keeps Headroom in your menu bar.\nRight-click its icon to quit."
                      : "Closing the window keeps Headroom in your system tray.\nUse the tray menu to quit."
                color: Theme.muted; font.pixelSize: 12; lineHeight: 1.4; visible: trayAvailable
            }
            StartupSettings { Layout.fillWidth: true }
            AppInfoSettings { id: aboutUpdates; Layout.fillWidth: true }
            ActionButton { text: "Open diagnostics"; quiet: true; onClicked: { panel.close(); panel.diagnosticsRequested() } }
        }
    }
            Text { id: error; visible: text.length > 0; Layout.fillWidth: true; wrapMode: Text.WordWrap; color: Theme.red; font.pixelSize: 12 }
            RowLayout {
                Layout.fillWidth: true; Layout.topMargin: 6
                ActionButton { text: "Cancel"; quiet: true; onClicked: panel.close() }
                Item { Layout.fillWidth: true }
                ActionButton { objectName: "saveConnection"; text: "Save & connect  →"; accent: true; onClicked: panel.saveAndConnect() }
            }
    }
}
