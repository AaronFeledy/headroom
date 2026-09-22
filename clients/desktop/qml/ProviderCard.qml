import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

Rectangle {
    id: card
    required property var provider
    signal resetRequested()
    objectName: "providerCard_" + name
    property string name: provider.provider_name
    property string displayName: backend.displayName(name)
    property color accent: Theme.purple
    property var notificationViewport: null
    property bool presentingNotifications: false
    property bool offline: false
    property bool failed: provider.error !== null && provider.error !== undefined
    readonly property bool cursorLoginRequired: failed && name === "Cursor"
        && (provider.needs_reauth || (provider.error || "").startsWith("Log in to cursor.com"))
    property bool pinned: backend.settings.primary === name
    property bool stacked: width < 780
    // Tighten both narrow and medium cards while the provider header is above the meters.
    readonly property bool compact: stacked
    property var resetCredits: provider.rate_limit_reset_credits
    property string resetAccountFingerprint: resetCredits && resetCredits.account_fingerprint
        ? resetCredits.account_fingerprint : ""
    property bool hasResetAccountBinding: resetAccountFingerprint.length === 64
        && /^[0-9a-f]{64}$/.test(resetAccountFingerprint)
    property bool hasResetCount: name === "Codex" && !failed && resetCredits !== null
        && resetCredits !== undefined && resetCredits.available_count >= 0
    property bool hasBankedResets: hasResetCount && resetCredits.available_count > 0
    readonly property bool automaticReset: name === "Codex" && backend.resetAction.automatic
    property var clock: backend.state
    property var weeklyBucket: {
        for (let bucket of provider.buckets || []) if (bucket.id === "weekly") return bucket
        return null
    }
    property var weeklyConcern: {
        card.clock
        return card.weeklyBucket ? backend.concern(card.name, card.weeklyBucket) : ({})
    }
    Component {
        id: bankedResetsFooter
        RowLayout {
            spacing: 8
            Item {
                // Keep the transient delta anchored even when the last reset
                // disappears. Only positive counts have a visible/focusable label.
                Layout.fillWidth: true
                implicitWidth: resetLabel.implicitWidth
                implicitHeight: resetLabel.implicitHeight
                Text {
                    id: resetLabel
                    anchors.fill: parent
                    function openUsage() { Qt.openUrlExternally("https://chatgpt.com/codex/settings/usage") }
                    objectName: "bankedResets_" + card.name
                    visible: card.hasBankedResets
                    textFormat: Text.PlainText
                    text: !card.hasResetCount ? "" : card.resetCredits.available_count === 1 ? "1 banked reset"
                        : card.resetCredits.available_count + " banked resets"
                    color: card.weeklyConcern.severity === 3 ? Theme.red : Theme.muted
                    font.pixelSize: 11; elide: Text.ElideRight
                    horizontalAlignment: Text.AlignRight
                    font.underline: bankedResetHover.hovered || activeFocus
                    activeFocusOnTab: visible
                    Accessible.role: Accessible.Link
                    Accessible.name: text
                    Accessible.description: "Open ChatGPT usage and reset controls"
                    Accessible.onPressAction: openUsage()
                    Keys.onReturnPressed: openUsage()
                    Keys.onEnterPressed: openUsage()
                    Keys.onSpacePressed: openUsage()
                    TapHandler { onTapped: parent.openUsage() }
                    HoverHandler { id: bankedResetHover; cursorShape: Qt.PointingHandCursor }
                    ToolTip.visible: bankedResetHover.hovered
                    ToolTip.text: "Open ChatGPT usage and reset controls"
                }
                NotificationHighlight {
                    visible: card.hasBankedResets
                    targetKey: "bankedResets_" + card.name
                    viewport: card.notificationViewport
                    presenting: card.presentingNotifications
                    onActivated: resetDelta.play()
                }
                Text {
                    id: resetDelta
                    objectName: "bankedResetDelta_" + card.name
                    property real delta: 0
                    property real rise: 0
                    readonly property bool running: floatAway.running
                    function play() {
                        const event = backend.notifications.presented.find(event => event.target === "bankedResets_" + card.name)
                        if (!event || !event.delta) return
                        delta = event.delta
                        floatAway.restart()
                    }
                    anchors.right: parent.right; anchors.bottom: parent.top; anchors.bottomMargin: 2
                    text: delta > 0 ? "+" + delta : "−" + Math.abs(delta)
                    textFormat: Text.PlainText; color: delta > 0 ? Theme.green : Theme.orange
                    font.pixelSize: 24; font.weight: Font.Bold
                    style: Text.Outline; styleColor: Theme.surface
                    opacity: 0; z: 10; enabled: false
                    transform: Translate { y: -resetDelta.rise }
                    Connections {
                        target: card
                        function onPresentingNotificationsChanged() {
                            if (!card.presentingNotifications) { floatAway.stop(); resetDelta.opacity = 0 }
                        }
                    }
                    ParallelAnimation {
                        id: floatAway
                        NumberAnimation { target: resetDelta; property: "rise"; from: 0; to: 32; duration: 1100; easing.type: Easing.OutCubic }
                        SequentialAnimation {
                            NumberAnimation { target: resetDelta; property: "opacity"; from: 0; to: 1; duration: 80 }
                            PauseAnimation { duration: 250 }
                            NumberAnimation { target: resetDelta; property: "opacity"; to: 0; duration: 770 }
                        }
                        SequentialAnimation {
                            NumberAnimation { target: resetDelta; property: "scale"; from: 0.7; to: 1.2; duration: 140; easing.type: Easing.OutBack }
                            NumberAnimation { target: resetDelta; property: "scale"; to: 1; duration: 160 }
                        }
                    }
                }
            }
            // IMPORTANT: DO NOT test this button, the endpoint, or any code
            // that might trigger a reset. It can burn a very valuable reset.
            // The intentionally skipped tests must not be filled in or enabled.
            ActionButton {
                objectName: "useBankedReset_" + card.name
                visible: card.automaticReset
                    || (card.hasBankedResets && card.hasResetAccountBinding
                        && !!card.weeklyBucket && card.weeklyBucket.utilization >= 95)
                enabled: backend.resetAction.enabled
                text: backend.resetAction.busy ? "Resetting…" : backend.resetAction.automatic ? "Auto at 100%…"
                    : backend.resetAction.awaitingUsage ? "Waiting…" : "Use reset…"
                implicitHeight: 24; implicitWidth: contentItem.implicitWidth + 16
                padding: 6; font.pixelSize: 11
                Layout.alignment: Qt.AlignRight | Qt.AlignVCenter
                onClicked: card.resetRequested()
            }
        }
    }
    implicitHeight: body.implicitHeight + (card.compact ? 28 : 36)
    radius: 12
    color: Theme.surface
    border.color: card.offline ? Theme.red : drop.containsDrag ? card.accent : hover.hovered ? Theme.comment : Theme.selection
    NotificationHighlight {
        targetKey: card.objectName
        viewport: card.notificationViewport
        presenting: card.presentingNotifications
    }
    HoverHandler { id: hover; objectName: "providerHover_" + card.name }
    // Behind the content so links and buttons keep their own click behavior.
    MouseArea {
        id: dragMouse; objectName: "panelDrag_" + card.name
        anchors.fill: parent
        hoverEnabled: true
        acceptedButtons: Qt.LeftButton
        preventStealing: true
        cursorShape: pressed ? Qt.ClosedHandCursor : Qt.OpenHandCursor
        drag.target: dragGhost
        onPressed: function(mouse) { let p = mapToItem(Overlay.overlay, mouse.x, mouse.y); dragGhost.x = p.x - 16; dragGhost.y = p.y - 16 }
        onReleased: dragGhost.Drag.drop()
        onCanceled: dragGhost.Drag.cancel()
    }
    Rectangle {
        id: dragGhost; parent: Overlay.overlay; z: 1000
        property string providerName: card.name
        width: 160; height: 44; radius: 9; color: Theme.selection; border.color: card.accent
        visible: dragMouse.drag.active
        Drag.active: dragMouse.drag.active; Drag.source: dragGhost; Drag.keys: ["headroom-provider"]
        Drag.hotSpot.x: 16; Drag.hotSpot.y: 16
        Text { anchors.centerIn: parent; text: card.displayName; color: Theme.foreground; font.pixelSize: 14 }
    }
    Rectangle {
        visible: drop.containsDrag
        x: 8; y: drop.after ? card.height + 4 : -7
        width: card.width - 16; height: 3; radius: 1; color: card.accent
    }
    DropArea {
        id: drop; anchors.fill: parent; keys: ["headroom-provider"]
        property bool after: false
        onPositionChanged: function(event) { after = event.y > height / 2 }
        onEntered: function(event) { after = event.y > height / 2 }
        onDropped: function(event) {
            const sourceName = event.source.providerName, targetName = card.name, after = event.y > height / 2
            event.acceptProposedAction()
            Qt.callLater(function() { backend.moveProvider(sourceName, targetName, after) })
        }
    }
    GridLayout {
        id: body
        anchors { left: parent.left; right: parent.right; top: parent.top; margins: 18; topMargin: card.compact ? 14 : 18 }
        columns: card.stacked ? 1 : 2
        columnSpacing: 20; rowSpacing: card.compact ? 14 : 20
        RowLayout {
            id: providerHeader
            objectName: "providerHeader_" + card.name
            Layout.fillWidth: card.stacked
            Layout.preferredWidth: card.stacked ? -1 : 138
            Layout.maximumWidth: card.stacked ? Infinity : 138
            Layout.alignment: Qt.AlignTop
            spacing: 10
            Rectangle {
                width: 35; height: 35; radius: 10; color: Qt.alpha(card.accent, 0.10); border.color: Qt.alpha(card.accent, 0.17)
                Image { objectName: "providerIcon_" + card.name; anchors.centerIn: parent; width: 26; height: 26; source: "qrc:/provider-icons/" + card.name.toLowerCase() + ".svg"; sourceSize.width: 52; sourceSize.height: 52; visible: ["Claude", "Codex", "Cursor", "Grok"].indexOf(card.name) >= 0 }
            }
            ColumnLayout {
                Layout.fillWidth: true; spacing: 4
                Text { textFormat: Text.PlainText; objectName: "providerLabel_" + card.name; text: card.displayName; Layout.fillWidth: true; elide: Text.ElideRight; color: Theme.foreground; font.pixelSize: 16; font.weight: Font.DemiBold }
                Item {
                    Layout.fillWidth: true
                    implicitHeight: headerMetadata.implicitHeight
                        + (headerCounterSlot.visible ? 4 + headerCounterSlot.implicitHeight : 0)
                    GridLayout {
                        id: headerMetadata
                        width: parent.width
                        columns: card.compact ? 2 : 1
                        columnSpacing: 8; rowSpacing: 4
                        Text { textFormat: Text.PlainText; text: card.provider.subtitle || "Usage overview"; color: Theme.muted; font.pixelSize: 11; Layout.fillWidth: true; elide: Text.ElideRight }
                        Text { visible: card.pinned; text: "TRAY METER"; font.pixelSize: 8; font.letterSpacing: 0.9; color: card.accent }
                    }
                    Item {
                        id: headerCounterSlot
                        visible: (card.hasBankedResets || card.automaticReset) && (!card.weeklyBucket || card.failed)
                        implicitHeight: headerCounterOverlay.implicitHeight
                    }
                    Loader {
                        id: headerCounterOverlay
                        sourceComponent: !card.weeklyBucket || card.failed ? bankedResetsFooter : null
                        visible: (card.hasResetCount || card.automaticReset) && (!card.weeklyBucket || card.failed)
                        anchors.top: headerMetadata.bottom; anchors.topMargin: 4
                        width: parent.width
                    }
                }
            }
            TapHandler {
                acceptedButtons: Qt.RightButton
                onTapped: orderMenu.popup()
            }
            Menu {
                id: orderMenu; objectName: "providerMenu_" + card.name
                MenuItem { text: "Move to top / use in tray"; onTriggered: backend.setPrimary(card.name) }
            }
        }
        GridLayout {
            id: meters
            visible: !card.failed
            Layout.fillWidth: true
            // Use the space the card supplies, not this grid's minimum width:
            // its old column count can otherwise prevent it from shrinking.
            readonly property real availableWidth: body.width - (card.stacked ? 0 : 138 + body.columnSpacing)
            columns: Math.max(1, Math.min(card.provider.buckets.length - (card.stacked ? 1 : 0), Math.floor((availableWidth + columnSpacing) / (190 + columnSpacing))))
            columnSpacing: 24; rowSpacing: card.compact ? 16 : 22; uniformCellWidths: true
            Repeater {
                model: card.provider.buckets
                Meter {
                    required property var modelData
                    required property int index
                    bucket: modelData; providerName: card.name; accent: card.accent
                    compact: card.compact
                    notificationViewport: card.notificationViewport
                    presentingNotifications: card.presentingNotifications
                    footerAccessory: card.name === "Codex" && modelData.id === "weekly" ? bankedResetsFooter : null
                    footerAccessoryVisible: (card.hasBankedResets || card.automaticReset) && modelData.id === "weekly"
                    Layout.fillWidth: true; Layout.alignment: Qt.AlignTop
                    Layout.columnSpan: {
                        if (card.stacked && index === 0) return meters.columns
                        const position = index - (card.stacked ? 1 : 0)
                        return index === card.provider.buckets.length - 1 ? meters.columns - position % meters.columns : 1
                    }
                }
            }
        }
        ColumnLayout {
            visible: card.failed; spacing: 12; Layout.fillWidth: true
            Text { text: card.provider.needs_reauth || card.cursorLoginRequired ? "Reconnect your account" : "Usage is unavailable"; color: Theme.red; font.pixelSize: 14; font.weight: Font.Medium }
            Text {
                id: errorMessage; objectName: "providerError_" + card.name
                textFormat: Text.PlainText
                text: card.provider.error || "The provider could not return usage. Headroom will check again automatically."
                color: card.cursorLoginRequired ? Theme.cyan : Theme.muted
                font.pixelSize: 12; wrapMode: Text.WordWrap; Layout.fillWidth: true
                font.underline: loginLink.item ? (loginLink.item.hovered || loginLink.item.activeFocus) : false
                // Ordinary errors stay plain static text. Accessible actions are advertised from the
                // handlers that are connected, not from the current role, so the link affordances live
                // on an overlay that only exists while the Cursor sign-in link applies.
                Accessible.ignored: loginLink.active
                Accessible.role: Accessible.StaticText
                Accessible.name: text
                Loader {
                    id: loginLink
                    anchors.fill: parent
                    active: card.cursorLoginRequired
                    sourceComponent: Item {
                        objectName: "providerErrorLink_" + card.name
                        readonly property bool hovered: loginHover.hovered
                        function openLogin() { Qt.openUrlExternally("https://cursor.com/login") }
                        activeFocusOnTab: true
                        Accessible.role: Accessible.Link
                        Accessible.name: errorMessage.text
                        Accessible.description: "Open Cursor sign-in in your browser"
                        Accessible.onPressAction: openLogin()
                        Keys.onReturnPressed: openLogin()
                        Keys.onEnterPressed: openLogin()
                        Keys.onSpacePressed: openLogin()
                        TapHandler { onTapped: parent.openLogin() }
                        HoverHandler { id: loginHover; cursorShape: Qt.PointingHandCursor }
                        ToolTip.visible: loginHover.hovered || activeFocus
                        ToolTip.text: "Open Cursor sign-in in your browser"
                    }
                }
            }
            ActionButton { visible: !!card.provider.reauth_command; text: "Copy sign-in command"; onClicked: { backend.copyText(card.provider.reauth_command); text = "Copied" } }
        }
    }
}
