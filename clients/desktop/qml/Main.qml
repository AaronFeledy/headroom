import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

ApplicationWindow {
    id: window
    width: 1180; height: 940; minimumWidth: 460; minimumHeight: 420
    visible: !startHidden
    title: "Headroom"
    flags: Qt.FramelessWindowHint | (trayAvailable ? Qt.Tool | Qt.WindowStaysOnTopHint : Qt.Window)
    color: "transparent"
    background: Rectangle { color: Theme.background; radius: Theme.windowRadius; antialiasing: true }
    font.family: "Inter"
    property bool compact: width < 700
    property string filter: "All providers"
    property var state: backend.state
    property bool serverOffline: state.status === "offline"
    property var providers: backend.providers
    property var shownProviders: providers.filter(p => filter === "All providers" || p.provider_name === filter)
    property int healthy: providers.filter(p => p.is_success).length
    property int concernLevel: {
        window.state // Update severity as time advances, even between polls.
        let level = 0
        for (const p of providers) {
            if (!p.is_success) level = 3
            else for (const b of p.buckets) level = Math.max(level, backend.concern(p.provider_name, b).severity)
        }
        return level
    }
    property int attention: {
        window.state
        return providers.filter(p => !p.is_success || p.buckets.some(b => backend.concern(p.provider_name, b).severity > 0)).length
    }
    property string nextReset: {
        let values = []
        for (const p of providers) for (const b of p.buckets) if (b.resets_at && new Date(b.resets_at).getTime() > Date.now()) values.push(b.resets_at)
        values.sort(); return values.length ? backend.countdown(values[0]).replace("Resets in ", "") : "—"
    }
    onClosing: function(close) { if (trayAvailable) { close.accepted = false; hide() } }
    onProvidersChanged: { if (filter !== "All providers" && !providers.some(p => p.provider_name === filter)) filter = "All providers" }
    Shortcut { sequence: "Ctrl+R"; onActivated: backend.refresh() }
    Shortcut { sequence: "Ctrl+,"; onActivated: settings.open() }
    Shortcut { sequence: "Ctrl+Q"; onActivated: Qt.quit() }
    Shortcut { sequence: "Escape"; onActivated: { if (settings.opened) settings.close(); else if (trayAvailable) window.hide() } }
    SettingsPanel { id: settings; objectName: "settingsPanel"; onDiagnosticsRequested: diagnostics.open() }
    DiagnosticsPanel { id: diagnostics; objectName: "diagnosticsPanel" }

    // IMPORTANT: DO NOT test this button/confirmation, its endpoint, or any
    // code that might trigger a reset: doing so can burn a very valuable reset.
    // This restriction belongs in source and skip-only tests, never in UI copy.
    Popup {
        id: resetConfirmation
        objectName: "resetConfirmation"
        property var action: backend.resetAction
        parent: Overlay.overlay; anchors.centerIn: parent
        width: Math.min(440, parent.width - 32)
        modal: true; focus: true; padding: 24
        closePolicy: Popup.CloseOnEscape
        onOpened: resetCancel.forceActiveFocus()
        onClosed: backend.cancelChatGptResetConfirmation()
        background: Rectangle { color: Theme.surface; radius: 16; border.color: Theme.selection }
        Overlay.modal: Rectangle { color: Theme.overlay; radius: Theme.windowRadius }
        contentItem: ColumnLayout {
            spacing: 18
            Text { text: "Use a banked reset?"; color: Theme.foreground; font.pixelSize: 20; font.weight: Font.DemiBold; Layout.fillWidth: true; wrapMode: Text.WordWrap }
            Text {
                visible: resetConfirmation.action.canConfirm
                text: "Use one banked reset for the ChatGPT account connected to your usage server? This spends a reset and cannot be undone."
                color: Theme.muted; font.pixelSize: 13; wrapMode: Text.WordWrap; Layout.fillWidth: true
            }
            Text {
                visible: text.length > 0
                text: resetConfirmation.action.message
                textFormat: Text.PlainText; color: Theme.foreground; font.pixelSize: 13
                wrapMode: Text.WordWrap; Layout.fillWidth: true
            }
            RowLayout {
                Layout.fillWidth: true
                ActionButton { text: "Usage page"; quiet: true; onClicked: Qt.openUrlExternally("https://chatgpt.com/codex/settings/usage") }
                Item { Layout.fillWidth: true }
                ActionButton { id: resetCancel; text: resetConfirmation.action.canConfirm ? "Cancel" : "Close"; onClicked: resetConfirmation.close() }
            }
            // DO NOT activate for testing. This is the only UI call site that
            // can spend a banked reset; skipped tests are intentional safeguards.
            ActionButton {
                objectName: "confirmBankedReset"
                visible: resetConfirmation.action.canConfirm || resetConfirmation.action.busy
                enabled: resetConfirmation.action.canConfirm
                text: resetConfirmation.action.busy ? "Using reset…" : "Use one reset"
                accent: true; Layout.fillWidth: true
                onClicked: backend.consumeChatGptReset()
            }
        }
    }

    ColumnLayout {
        anchors.fill: parent; spacing: 0
        ScrollView {
            id: scroll; objectName: "meterScroll"
            Layout.fillWidth: true; Layout.fillHeight: true; contentWidth: availableWidth; clip: true
            // Keep scrolling content inside the rounded top edge without a full-window texture mask.
            Layout.topMargin: Theme.windowRadius
            ScrollBar.horizontal.policy: ScrollBar.AlwaysOff
            ColumnLayout {
                width: scroll.availableWidth; spacing: 0
                ColumnLayout {
                    Layout.fillWidth: true; Layout.margins: window.compact ? 16 : 24
                    Layout.topMargin: (window.compact ? 16 : 24) - Theme.windowRadius; spacing: 12
                    ColumnLayout {
                        id: providerRows; objectName: "providerRows"
                        visible: window.providers.length > 0
                        Layout.fillWidth: true; spacing: 12
                        Repeater {
                            model: window.shownProviders
                            ProviderCard {
                                required property var modelData
                                provider: modelData; offline: window.serverOffline; Layout.fillWidth: true
                                onResetRequested: { backend.prepareChatGptReset(); resetConfirmation.open() }
                            }
                        }
                    }
                    Rectangle {
                        objectName: "connectionPlaceholder"
                        visible: window.providers.length === 0
                        Layout.fillWidth: true; implicitHeight: 300; radius: 12; color: Theme.surface
                        border.color: window.serverOffline ? Theme.red : Theme.selection
                        ColumnLayout {
                            anchors.centerIn: parent; width: parent.width - 50; spacing: 18
                            Text { text: window.state.status === "ready" ? "No providers enabled" : window.state.status === "connecting" ? (backend.settings.mode === "local" ? "Preparing your local server" : "Connecting to your server") : "Connect your usage server"; color: Theme.foreground; font.pixelSize: 24; font.weight: Font.Medium; Layout.alignment: Qt.AlignHCenter }
                            Text { text: window.state.status === "ready" ? "Enable providers on your backend to see their usage here." : window.state.status === "connecting" || window.state.status === "offline" ? window.state.message : "Bring Claude, ChatGPT, Cursor, and Grok into view."; color: Theme.muted; font.pixelSize: 13; horizontalAlignment: Text.AlignHCenter; wrapMode: Text.WordWrap; Layout.fillWidth: true }
                            ActionButton { text: window.state.status === "connecting" ? (backend.settings.mode === "local" ? "Preparing…" : "Connecting…") : window.state.status === "offline" ? "Connection settings →" : "Connect your backend →"; accent: true; Layout.alignment: Qt.AlignHCenter; onClicked: settings.open() }
                        }
                    }
                }
            }
        }
        Rectangle {
            objectName: "stickyFooter"
            Layout.fillWidth: true; implicitHeight: footerBody.implicitHeight + 24
            color: Theme.inset; radius: Theme.windowRadius; antialiasing: true
            Rectangle { anchors.top: parent.top; width: parent.width; height: parent.radius; color: parent.color }
            Rectangle { objectName: "footerBorder"; anchors.top: parent.top; width: parent.width; height: 1; color: window.serverOffline ? Theme.red : Theme.selection }
            ColumnLayout {
                id: footerBody
                anchors { left: parent.left; right: parent.right; top: parent.top; margins: window.compact ? 16 : 24; topMargin: 12 }
                spacing: 10
                Text {
                    objectName: "serverUpdateNotice"
                    visible: appInfo.serverUpdateNotice.length > 0
                    Layout.fillWidth: true; wrapMode: Text.WordWrap
                    text: appInfo.serverUpdateNotice
                    textFormat: Text.PlainText; color: Theme.orange; font.pixelSize: 11
                }
                Text {
                    visible: window.state.status === "offline"
                    Layout.fillWidth: true; wrapMode: Text.WordWrap
                    text: window.state.message + (window.state.lastGood ? " Last readings are retained." : "")
                    textFormat: Text.PlainText; color: Theme.red; font.pixelSize: 11
                }
                RowLayout {
                    Layout.fillWidth: true; spacing: window.compact ? 8 : 12
                    Image { source: "../headroom.svg"; sourceSize.width: 26; sourceSize.height: 26; Layout.preferredWidth: 26; Layout.preferredHeight: 26 }
                    ColumnLayout {
                        Layout.fillWidth: true; spacing: 3
                        Text { text: "headroom"; font.pixelSize: 14; font.weight: Font.DemiBold; color: Theme.foreground }
                        Text {
                            Layout.fillWidth: true; elide: Text.ElideRight; font.pixelSize: 10
                            text: window.state.status === "offline" ? (window.state.retrySeconds > 0 ? "Offline · retry in " + window.state.retrySeconds + "s" : "Offline · use Refresh to retry") : window.state.loading ? "Refreshing…" : window.state.status === "connecting" ? window.state.message : window.state.status === "ready" ? window.state.updated : "Not connected"
                            color: window.state.status === "offline" ? Theme.red : Theme.muted
                            HoverHandler { id: statusHover }
                            ToolTip.visible: statusHover.hovered
                            ToolTip.text: window.healthy + " / " + window.providers.length + " providers online · Next reset in " + window.nextReset
                        }
                    }
                    Text {
                        visible: !window.compact && window.attention > 0
                        text: window.attention + (window.attention === 1 ? " needs attention" : " need attention"); color: backend.warningColor(window.concernLevel); font.pixelSize: 11
                    }
                    ActionButton {
                        objectName: "providerFilter"
                        visible: window.providers.length > 0
                        text: backend.displayName(window.filter) + " ⌄"; quiet: true; font.pixelSize: 11
                        Accessible.name: "Filter providers"
                        onClicked: filterMenu.open()
                        Menu {
                            id: filterMenu; objectName: "providerFilterMenu"
                            y: -height - 8
                            Instantiator {
                                model: ["All providers"].concat(window.providers.map(p => p.provider_name))
                                delegate: MenuItem {
                                    required property string modelData
                                    text: backend.displayName(modelData); checkable: true; checked: window.filter === modelData
                                    onTriggered: { window.filter = modelData; scroll.contentItem.contentY = 0 }
                                }
                                onObjectAdded: (index, object) => filterMenu.insertItem(index, object)
                                onObjectRemoved: (index, object) => filterMenu.removeItem(object)
                            }
                        }
                    }
                    ActionButton { text: "↻"; implicitWidth: 34; implicitHeight: 34; quiet: true; enabled: !window.state.loading; Accessible.name: "Refresh usage"; onClicked: backend.refresh() }
                    ActionButton { text: "⚙"; implicitWidth: 34; implicitHeight: 34; quiet: true; Accessible.name: "Connection settings"; onClicked: settings.open() }
                }
            }
        }
    }
}
