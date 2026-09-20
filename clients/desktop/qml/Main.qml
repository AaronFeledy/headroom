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
    readonly property string desktopUpdateLabel: {
        if (updateService.updateMethod !== "automatic") return ""
        switch (updateService.state) {
        case "available": return "↑ Update available"
        case "downloading": return "Downloading…"
        case "staged": return updateService.latestVersion === appInfo.applicationVersion
            ? "↑ Restart to apply" : "↑ Restart to update"
        case "applying": return "Applying update…"
        case "failed": return "Update needs attention"
        default: return ""
        }
    }
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
    readonly property bool presentingNotifications: visible && active && visibility !== Window.Minimized
        && !settings.visible && !diagnostics.visible && !filterMenu.visible && !resetConfirmation.visible
        && !notificationPopup.visible
    onPresentingNotificationsChanged: if (presentingNotifications) Qt.callLater(presentNotifications)
    onVisibleChanged: {
        if (!visible) {
            notificationReveal.stop(); notificationReveal.requestedEvent = null
            notificationPopup.close()
            backend.notifications.endPresentation()
        }
        else if (!settings.opened && !diagnostics.opened && !filterMenu.opened && !resetConfirmation.opened)
            restoreEscapeFocus()
    }
    onVisibilityChanged: if (window.visibility === Window.Minimized) {
        notificationPopup.close()
        backend.notifications.endPresentation()
    }
    function findNotificationItem(root, name) {
        if (root.objectName === name) return root
        for (let child of root.children) {
            const found = findNotificationItem(child, name)
            if (found) return found
        }
        return null
    }
    function revealNotification(event, explicit) {
        if (event.target === "desktopUpdate") {
            if (explicit) settings.openUpdates()
            return
        }
        if (filter !== "All providers") {
            filter = "All providers"
            notificationReveal.requestedEvent = event
            notificationReveal.restart()
            return
        }
        const item = findNotificationItem(providerRows, event.target)
        if (!item || !item.visible) return // Activity details remain available when a meter disappears.
        const point = item.mapToItem(scroll.contentItem, 0, 0)
        // Leave room above the reset counter for its rising delta animation.
        const topSpace = event.target.startsWith("bankedResets_") ? 88 : 18
        scroll.contentItem.contentY = Math.max(0, Math.min(point.y + scroll.contentItem.contentY - topSpace,
            scroll.contentHeight - scroll.availableHeight))
    }
    function presentNotifications() {
        if (!presentingNotifications || backend.notifications.unreadCount === 0) return
        backend.notifications.present()
    }
    Timer {
        id: notificationReveal
        property var requestedEvent: null
        // Explicit activity navigation may change the provider filter.
        // Reveal after card geometry settles, especially in compact windows.
        interval: 50
        onTriggered: {
            const event = requestedEvent
            requestedEvent = null
            if (window.presentingNotifications && event)
                window.revealNotification(event, false)
        }
    }
    Connections {
        target: backend.notifications
        function onPendingChanged() { Qt.callLater(window.presentNotifications) }
    }
    function restoreEscapeFocus() {
        window.requestActivate()
        escapeFocus.forceActiveFocus()
    }
    function dismissOverlayOrHide() {
        if (notificationPopup.opened) notificationPopup.close()
        else if (resetConfirmation.opened) resetConfirmation.close()
        else if (settings.opened) settings.close()
        else if (diagnostics.opened) diagnostics.close()
        else if (filterMenu.opened) filterMenu.close()
        else if (trayAvailable) window.hide()
    }
    Shortcut { sequence: "Ctrl+R"; onActivated: backend.refresh() }
    Shortcut { sequence: "Ctrl+,"; onActivated: settings.open() }
    Shortcut { sequence: "Ctrl+Q"; onActivated: Qt.quit() }
    Item { id: escapeFocus; objectName: "escapeFocus"; width: 0; height: 0; focus: true; activeFocusOnTab: false }
    SettingsPanel { id: settings; objectName: "settingsPanel"; onDiagnosticsRequested: diagnostics.open(); onClosed: restoreEscapeFocus() }
    DiagnosticsPanel { id: diagnostics; objectName: "diagnosticsPanel"; onClosed: restoreEscapeFocus() }

    Popup {
        id: notificationPopup; objectName: "notificationPopup"
        parent: Overlay.overlay
        x: window.compact ? 16 : 24
        y: Math.max(12, window.height - stickyFooter.height - height - 8)
        width: Math.min(380, parent.width - 32)
        height: Math.min(activityBody.implicitHeight + padding * 2, Math.max(80, window.height - stickyFooter.height - 24))
        padding: 16; focus: true
        closePolicy: Popup.CloseOnEscape | Popup.CloseOnPressOutside
        onClosed: restoreEscapeFocus()
        background: Rectangle { color: Theme.surface; radius: 12; border.color: Theme.comment }
        contentItem: ScrollView {
            id: activityScroll
            clip: true; contentWidth: availableWidth
            ScrollBar.horizontal.policy: ScrollBar.AlwaysOff
            ColumnLayout {
                id: activityBody
                width: activityScroll.availableWidth; spacing: 8
                Text { text: "Recent activity"; color: Theme.foreground; font.pixelSize: 14; font.weight: Font.DemiBold }
                Repeater {
                    model: backend.notifications.presented
                    ActionButton {
                        required property var modelData
                        objectName: "notificationEvent_" + modelData.target
                        Layout.fillWidth: true; quiet: true
                        implicitHeight: contentItem.implicitHeight + 16
                        Accessible.name: modelData.title
                        Accessible.description: modelData.message
                        contentItem: ColumnLayout {
                            spacing: 4
                            Text {
                                text: modelData.title + " →"; textFormat: Text.PlainText
                                Layout.fillWidth: true; wrapMode: Text.WordWrap
                                color: Theme.foreground; font.pixelSize: 12
                            }
                            Text {
                                text: modelData.message; textFormat: Text.PlainText
                                Layout.fillWidth: true; wrapMode: Text.WordWrap
                                color: Theme.muted; font.pixelSize: 11
                            }
                        }
                        onClicked: {
                            const event = modelData
                            notificationPopup.close()
                            Qt.callLater(function() { window.revealNotification(event, true) })
                        }
                    }
                }
            }
        }
    }

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
        closePolicy: Popup.NoAutoClose
        onOpened: resetCancel.forceActiveFocus()
        onClosed: { backend.cancelChatGptResetConfirmation(); restoreEscapeFocus() }
        background: Rectangle { color: Theme.surface; radius: 16; border.color: Theme.selection }
        Overlay.modal: Rectangle { color: Theme.overlay; radius: Theme.windowRadius }
        contentItem: ColumnLayout {
            spacing: 12
            Text { text: resetConfirmation.action.automatic ? "Automatic reset scheduled" : "Use a banked reset?"; color: Theme.foreground; font.pixelSize: 20; font.weight: Font.DemiBold; Layout.fillWidth: true; wrapMode: Text.WordWrap }
            Text {
                visible: resetConfirmation.action.canConfirm || resetConfirmation.action.automatic
                text: resetConfirmation.action.automatic
                    ? "One reset will be used when a usage update reports 100% weekly usage. Keep Headroom running; quitting cancels this choice."
                    : "Use one reset now, or once a usage update reports 100% weekly usage. Keep Headroom running for automatic use. Spending a reset cannot be undone."
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
                ActionButton { id: resetCancel; text: resetConfirmation.action.canConfirm && !resetConfirmation.action.automatic ? "Cancel" : "Close"; onClicked: resetConfirmation.close() }
            }
            // DO NOT activate either choice for testing. Both can spend a
            // banked reset; skipped tests are intentional safeguards.
            ActionButton {
                objectName: "confirmBankedReset"
                visible: resetConfirmation.action.canConfirm || resetConfirmation.action.busy
                enabled: resetConfirmation.action.canConfirm
                text: resetConfirmation.action.busy ? "Using reset…" : "Use now"
                accent: true; Layout.fillWidth: true
                onClicked: backend.consumeChatGptReset()
            }
            ActionButton {
                objectName: "scheduleBankedReset"
                visible: resetConfirmation.action.canConfirm && !resetConfirmation.action.automatic
                enabled: resetConfirmation.action.canSchedule
                text: "Use automatically at 100%"
                Layout.fillWidth: true
                onClicked: if (backend.scheduleChatGptReset()) resetConfirmation.close()
            }
            ActionButton {
                objectName: "cancelScheduledBankedReset"
                visible: resetConfirmation.action.automatic
                text: "Cancel automatic reset"
                Layout.fillWidth: true
                onClicked: { backend.cancelScheduledChatGptReset(); resetConfirmation.close() }
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
                                notificationViewport: scroll
                                presentingNotifications: window.presentingNotifications && !notificationReveal.running
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
            id: stickyFooter; objectName: "stickyFooter"
            Layout.fillWidth: true; implicitHeight: footerBody.implicitHeight + 24
            color: Theme.inset; radius: Theme.windowRadius; antialiasing: true
            Rectangle { anchors.top: parent.top; width: parent.width; height: parent.radius; color: parent.color }
            Rectangle { objectName: "footerBorder"; anchors.top: parent.top; width: parent.width; height: 1; color: window.serverOffline ? Theme.red : Theme.selection }
            ColumnLayout {
                id: footerBody
                anchors { left: parent.left; right: parent.right; top: parent.top; margins: window.compact ? 16 : 24; topMargin: 12 }
                spacing: 10
                Flow {
                    Layout.fillWidth: true; spacing: 8
                    visible: (window.compact && window.desktopUpdateLabel.length > 0)
                        || appInfo.serverUpdateNotice.length > 0
                    UpdateIndicator {
                        id: compactUpdateIndicator; objectName: "compactUpdateIndicator"
                        notificationTarget: "desktopUpdate"
                        presentingNotifications: window.presentingNotifications && !notificationReveal.running
                        visible: window.compact && window.desktopUpdateLabel.length > 0
                        text: window.desktopUpdateLabel
                        needsAttention: updateService.state === "failed"
                        detail: updateService.statusText
                        Accessible.name: "Headroom: " + text
                        onClicked: settings.openUpdates()
                    }
                    UpdateIndicator {
                        id: serverUpdateIndicator; objectName: "serverUpdateIndicator"
                        visible: appInfo.serverUpdateNotice.length > 0
                        text: remoteUpdateService.busy ? "Updating server…"
                            : remoteUpdateService.state === "failed" ? "Server update needs attention"
                            : "↑ Server update available"
                        needsAttention: remoteUpdateService.state === "failed"
                        detail: appInfo.serverUpdateNotice
                        Accessible.name: text
                        onClicked: settings.openUpdates()
                    }
                }
                Text {
                    visible: window.state.status === "offline"
                    Layout.fillWidth: true; wrapMode: Text.WordWrap
                    text: window.state.message + (window.state.lastGood ? " Last readings are retained." : "")
                    textFormat: Text.PlainText; color: Theme.red; font.pixelSize: 11
                }
                RowLayout {
                    Layout.fillWidth: true; spacing: window.compact ? 8 : 12
                    ToolButton {
                        id: notificationButton; objectName: "notificationButton"
                        readonly property bool hasActivity: backend.notifications.presented.length > 0
                        Layout.preferredWidth: 26; Layout.preferredHeight: 26
                        padding: 0; enabled: hasActivity; activeFocusOnTab: enabled
                        Accessible.name: "Recent activity"
                        Accessible.description: "Open notification details without changing the dashboard"
                        contentItem: Image { source: "../headroom.svg"; sourceSize.width: 26; sourceSize.height: 26 }
                        background: Rectangle { color: notificationButton.hovered ? Theme.selection : "transparent"; radius: 5 }
                        Rectangle {
                            objectName: "notificationActivityDot"
                            visible: notificationButton.hasActivity
                            anchors { right: parent.right; top: parent.top }
                            width: 7; height: 7; radius: 3.5
                            color: Theme.cyan; border.width: 1; border.color: Theme.inset
                        }
                        ToolTip.visible: hovered || activeFocus
                        ToolTip.text: "Recent activity"
                        onClicked: notificationPopup.open()
                    }
                    ColumnLayout {
                        Layout.fillWidth: true; spacing: 3
                        RowLayout {
                            spacing: 10
                            Text { text: "headroom"; font.pixelSize: 14; font.weight: Font.DemiBold; color: Theme.foreground }
                            UpdateIndicator {
                                objectName: "desktopUpdateIndicator"
                                notificationTarget: "desktopUpdate"
                                presentingNotifications: window.presentingNotifications && !notificationReveal.running
                                visible: !window.compact && window.desktopUpdateLabel.length > 0
                                text: window.desktopUpdateLabel
                                needsAttention: updateService.state === "failed"
                                detail: updateService.statusText
                                Accessible.name: "Headroom: " + text
                                onClicked: settings.openUpdates()
                            }
                        }
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
                            closePolicy: Popup.CloseOnPressOutside
                            onClosed: restoreEscapeFocus()
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
    // Draw above the footer so the outline follows the entire rounded window.
    Rectangle {
        anchors.fill: parent
        z: 1
        color: "transparent"
        radius: Theme.windowRadius
        border.width: 1
        border.color: Qt.alpha(Theme.muted, 0.18)
        antialiasing: true
        enabled: false
    }

}
