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
    property bool offline: false
    property bool failed: provider.error !== null && provider.error !== undefined
    property bool pinned: backend.settings.primary === name
    property bool stacked: width < 780
    property var resetCredits: provider.rate_limit_reset_credits
    property string resetAccountFingerprint: resetCredits && resetCredits.account_fingerprint
        ? resetCredits.account_fingerprint : ""
    property bool hasResetAccountBinding: resetAccountFingerprint.length === 64
        && /^[0-9a-f]{64}$/.test(resetAccountFingerprint)
    property bool hasBankedResets: name === "Codex" && !failed && resetCredits !== null
        && resetCredits !== undefined && resetCredits.available_count > 0
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
            Text {
                function openUsage() { Qt.openUrlExternally("https://chatgpt.com/codex/settings/usage") }
                objectName: "bankedResets_" + card.name
                visible: card.hasBankedResets
                textFormat: Text.PlainText
                text: !card.hasBankedResets ? "" : card.resetCredits.available_count === 1 ? "1 banked reset"
                    : card.resetCredits.available_count + " banked resets"
                color: card.weeklyConcern.severity === 3 ? Theme.red : Theme.muted
                font.pixelSize: 11; Layout.fillWidth: true; elide: Text.ElideRight
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
            // IMPORTANT: DO NOT test this button, the endpoint, or any code
            // that might trigger a reset. It can burn a very valuable reset.
            // The intentionally skipped tests must not be filled in or enabled.
            ActionButton {
                objectName: "useBankedReset_" + card.name
                visible: card.hasBankedResets && card.hasResetAccountBinding
                    && !!card.weeklyBucket && card.weeklyBucket.utilization >= 95
                enabled: backend.resetAction.enabled
                text: backend.resetAction.busy ? "Resetting…" : backend.resetAction.awaitingUsage ? "Waiting…" : "Use reset…"
                implicitHeight: 24; implicitWidth: contentItem.implicitWidth + 16
                padding: 6; font.pixelSize: 11
                Layout.alignment: Qt.AlignRight | Qt.AlignVCenter
                onClicked: card.resetRequested()
            }
        }
    }
    implicitHeight: body.implicitHeight + 36
    radius: 12
    color: Theme.surface
    border.color: card.offline ? Theme.red : drop.containsDrag ? card.accent : hover.hovered ? Theme.comment : Theme.selection
    HoverHandler { id: hover; objectName: "providerHover_" + card.name }
    Rectangle {
        id: dragGhost; parent: Overlay.overlay; z: 1000
        property string providerName: card.name
        width: 160; height: 44; radius: 9; color: Theme.selection; border.color: card.accent
        visible: dragMouse.drag.active
        Drag.active: dragMouse.drag.active; Drag.source: dragGhost; Drag.keys: ["headroom-provider"]
        Drag.hotSpot.x: 16; Drag.hotSpot.y: 16
        Text { anchors.centerIn: parent; text: "⠿  " + card.displayName; color: Theme.foreground; font.pixelSize: 14 }
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
        anchors { left: parent.left; right: parent.right; top: parent.top; margins: 18 }
        columns: card.stacked ? 1 : 2
        columnSpacing: 20; rowSpacing: 20
        ColumnLayout {
            Layout.fillWidth: card.stacked
            Layout.fillHeight: true
            Layout.preferredWidth: card.stacked ? -1 : 138
            Layout.maximumWidth: card.stacked ? Infinity : 138
            Layout.alignment: Qt.AlignTop
            spacing: 6
            RowLayout {
                Layout.fillWidth: true; spacing: 10
                Rectangle {
                    width: 35; height: 35; radius: 10; color: Qt.alpha(card.accent, 0.10); border.color: Qt.alpha(card.accent, 0.17)
                    Image { objectName: "providerIcon_" + card.name; anchors.centerIn: parent; width: 26; height: 26; source: "qrc:/provider-icons/" + card.name.toLowerCase() + ".svg"; sourceSize.width: 52; sourceSize.height: 52; visible: ["Claude", "Codex", "Cursor", "Grok"].indexOf(card.name) >= 0 }
                }
                ColumnLayout {
                    Layout.fillWidth: true; spacing: 4
                    Text { textFormat: Text.PlainText; objectName: "providerLabel_" + card.name; text: card.displayName; Layout.fillWidth: true; elide: Text.ElideRight; color: Theme.foreground; font.pixelSize: 16; font.weight: Font.DemiBold }
                    Text { textFormat: Text.PlainText; text: card.provider.subtitle || "Usage overview"; color: Theme.muted; font.pixelSize: 11; Layout.fillWidth: true; elide: Text.ElideRight }
                    Text { visible: card.pinned; text: "TRAY METER"; font.pixelSize: 8; font.letterSpacing: 0.9; color: card.accent }
                }
            }
            Item {
                Layout.preferredWidth: 35; Layout.minimumHeight: 28
                Layout.fillHeight: true
                Layout.alignment: Qt.AlignLeft
                Text { anchors.centerIn: parent; text: "⠿"; font.pixelSize: 22; color: Theme.muted }
                MouseArea {
                    id: dragMouse; objectName: "dragHandle_" + card.name
                    width: 35; height: 28; anchors.centerIn: parent
                    hoverEnabled: true
                    cursorShape: pressed ? Qt.ClosedHandCursor : Qt.OpenHandCursor
                    drag.target: dragGhost
                    onPressed: function(mouse) { let p = mapToItem(Overlay.overlay, mouse.x, mouse.y); dragGhost.x = p.x - 16; dragGhost.y = p.y - 16 }
                    onReleased: dragGhost.Drag.drop()
                    onCanceled: dragGhost.Drag.cancel()
                    acceptedButtons: Qt.LeftButton | Qt.RightButton
                    onClicked: function(mouse) { if (mouse.button === Qt.RightButton) orderMenu.popup() }
                }
                ToolTip.visible: dragMouse.containsMouse && !dragMouse.pressed
                ToolTip.text: "Drag to reorder · right-click for more options"
                Menu { id: orderMenu; MenuItem { text: "Move to top / use in tray"; onTriggered: backend.setPrimary(card.name) } }
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
            columnSpacing: 24; rowSpacing: 22; uniformCellWidths: true
            Repeater {
                model: card.provider.buckets
                Meter {
                    required property var modelData
                    required property int index
                    bucket: modelData; providerName: card.name; accent: card.accent
                    footerAccessory: card.name === "Codex" && modelData.id === "weekly" ? bankedResetsFooter : null
                    footerAccessoryVisible: card.hasBankedResets && modelData.id === "weekly"
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
            Text { text: card.provider.needs_reauth ? "Reconnect your account" : "Usage is unavailable"; color: Theme.red; font.pixelSize: 14; font.weight: Font.Medium }
            Text { textFormat: Text.PlainText; text: card.provider.error || "The provider could not return usage. Headroom will check again automatically."; color: Theme.muted; font.pixelSize: 12; wrapMode: Text.WordWrap; Layout.fillWidth: true }
            ActionButton { visible: !!card.provider.reauth_command; text: "Copy sign-in command"; onClicked: { backend.copyText(card.provider.reauth_command); text = "Copied" } }
        }
    }
}
