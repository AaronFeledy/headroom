import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

Rectangle {
    id: card
    required property var provider
    signal resetRequested()
    objectName: "providerCard_" + name
    property string name: provider.provider_name
    readonly property string providerType: provider.provider_type || name
    property string displayName: backend.displayName(name)
    property color accent: Theme.purple
    property var notificationViewport: null
    property bool presentingNotifications: false
    property bool offline: false
    property bool failed: provider.error !== null && provider.error !== undefined
    // A failed provider may carry its last successful reading as stale meters.
    readonly property bool stale: failed && provider.stale === true
    readonly property string readingAge: { card.clock; return backend.readingAgeLabel(provider.last_reading_at || 0) }
    readonly property var loginCopy: { backend.settings; return backend.loginCopy(provider) }
    readonly property bool hasAuthCopy: !!loginCopy.title
    readonly property bool cursorLoginRequired: failed && providerType === "Cursor"
        && !hasAuthCopy
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
    readonly property bool resetProvider: name === "Claude" || name === "Codex"
    readonly property bool countsResets: providerType === "Claude" || providerType === "Codex"
    property bool hasResetCount: countsResets && !failed && resetCredits !== null
        && resetCredits !== undefined && resetCredits.available_count >= 0
    property bool hasBankedResets: hasResetCount && resetCredits.available_count > 0
    // The server's additive kind breakdown describes the count when it is a real list;
    // null, absent, or an empty list keeps the legacy total-count text.
    readonly property var resetKinds: (resetCredits && resetCredits.kinds) || []
    readonly property bool hasKindBreakdown: hasBankedResets && resetKinds.length > 0
    // Kind wording is composed only by the C++ reset policy. The Controller
    // builds it from this row's own reading and meters, so read-only
    // multi-account rows show the same text without a second implementation.
    readonly property var kindContent: {
        card.provider; card.resetCredits
        return backend.resetKindContent(card.name)
    }
    readonly property string resetCountText: {
        if (hasKindBreakdown) return kindContent.countText
        if (!hasResetCount) return ""
        return resetCredits.available_count === 1 ? "1 banked reset"
            : resetCredits.available_count + " banked resets"
    }
    readonly property string resetKindDetails: kindContent.details
    readonly property var resetState: { backend.resetAction; card.provider; return backend.resetState(card.name) }
    readonly property bool managingReset: resetState.managing === true
    readonly property string usageUrl: name === "Claude" ? "https://claude.ai/settings/usage" : name === "Codex" ? "https://chatgpt.com/codex/settings/usage" : ""
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
        id: bankedResetsControl
        RowLayout {
            spacing: 5
            Item {
                // Keep the transient delta anchored even when the last reset
                // disappears. Only positive counts have a visible/focusable label.
                implicitWidth: resetLabel.implicitWidth
                implicitHeight: resetLabel.implicitHeight
                Text {
                    id: resetLabel
                    anchors.fill: parent
                    function openUsage() { if (card.resetProvider) Qt.openUrlExternally(card.usageUrl) }
                    objectName: "bankedResets_" + card.name
                    visible: card.hasBankedResets
                    textFormat: Text.PlainText
                    readonly property string detailText: card.resetKindDetails
                    text: card.hasResetCount ? card.resetCredits.available_count + (card.resetCredits.available_count === 1 ? " reset" : " resets") : ""
                    color: card.weeklyConcern.severity === 3 ? Theme.red : Theme.muted
                    font.pixelSize: 11; elide: Text.ElideRight
                    horizontalAlignment: Text.AlignRight
                    font.underline: card.resetProvider && (bankedResetHover.hovered || activeFocus)
                    activeFocusOnTab: visible && card.resetProvider
                    Accessible.role: card.resetProvider ? Accessible.Link : Accessible.StaticText
                    Accessible.name: card.resetCountText
                    Accessible.description: card.resetProvider ? "Open " + card.displayName + " usage and reset controls" : ""
                    Accessible.onPressAction: openUsage()
                    Keys.onReturnPressed: openUsage()
                    Keys.onEnterPressed: openUsage()
                    Keys.onSpacePressed: openUsage()
                    TapHandler { enabled: card.resetProvider; onTapped: parent.openUsage() }
                    // Hover works for read-only account rows too, so their kind
                    // breakdown remains explainable without adding actions.
                    HoverHandler {
                        id: bankedResetHover; enabled: card.countsResets
                        cursorShape: card.resetProvider ? Qt.PointingHandCursor : Qt.ArrowCursor
                    }
                    ToolTip.visible: card.countsResets && bankedResetHover.hovered
                    ToolTip.text: {
                        const lines = []
                        if (card.resetProvider) lines.push("Open " + card.displayName + " usage and reset controls")
                        // Kind details already start with each kind's count.
                        if (card.resetKindDetails.length) lines.push(card.resetKindDetails)
                        else if (card.resetCountText.length) lines.push(card.resetCountText)
                        return lines.join("\n")
                    }
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
                    property real riseLimit: 0
                    readonly property bool running: floatAway.running
                    function play() {
                        const event = backend.notifications.presented.find(event => event.target === "bankedResets_" + card.name)
                        if (!event || !event.delta) return
                        delta = event.delta
                        // The count sits in the header with little room above it, so
                        // start on the count and rise no further than the card's top.
                        rise = 0
                        riseLimit = Math.min(32, Math.max(0, parent.mapToItem(card, 0, y).y))
                        floatAway.restart()
                    }
                    anchors.right: parent.right; anchors.verticalCenter: parent.verticalCenter
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
                        NumberAnimation { target: resetDelta; property: "rise"; from: 0; to: Theme.reducedMotion ? 0 : resetDelta.riseLimit; duration: Theme.reducedMotion ? 0 : 1100; easing.type: Easing.OutCubic }
                        SequentialAnimation {
                            NumberAnimation { target: resetDelta; property: "opacity"; from: 0; to: 1; duration: 80 }
                            PauseAnimation { duration: 250 }
                            NumberAnimation { target: resetDelta; property: "opacity"; to: 0; duration: 770 }
                        }
                        SequentialAnimation {
                            NumberAnimation { target: resetDelta; property: "scale"; from: Theme.reducedMotion ? 1 : 0.7; to: Theme.reducedMotion ? 1 : 1.2; duration: Theme.reducedMotion ? 0 : 140; easing.type: Easing.OutBack }
                            NumberAnimation { target: resetDelta; property: "scale"; to: 1; duration: Theme.reducedMotion ? 0 : 160 }
                        }
                    }
                }
            }
            // IMPORTANT: DO NOT test this button, the endpoint, or any code
            // that might trigger a reset. It can burn a very valuable reset.
            // The intentionally skipped tests must not be filled in or enabled.
            ActionButton {
                id: resetButton
                objectName: "useBankedReset_" + card.name
                // A failed or stale card keeps only the management of an owned
                // arm, submission, or pending request; fresh choices wait.
                visible: card.resetProvider && card.resetState.visible && (!card.failed || card.managingReset)
                enabled: card.resetState.enabled
                text: {
                    switch (card.resetState.phase) {
                    case "submitting": return "Resetting…"
                    case "armed": return "Auto…"
                    case "paused": return "Paused…"
                    case "reconciling": return "Waiting…"
                    default: return "Options…"
                    }
                }
                implicitHeight: 22; implicitWidth: contentItem.implicitWidth + 12
                padding: 4; font.pixelSize: 10
                Layout.alignment: Qt.AlignRight | Qt.AlignVCenter
                Accessible.name: {
                    switch (card.resetState.phase) {
                    case "armed": return "Automatic reset at limit options"
                    case "paused": return "Automatic reset paused options"
                    case "submitting": case "reconciling": return text
                    default: return "Reset options"
                    }
                }
                Accessible.description: card.resetState.openReason || ""
                ToolTip.visible: hovered && !enabled && ToolTip.text.length > 0
                ToolTip.text: card.resetState.openReason || ""
                onClicked: card.resetRequested()
            }
        }
    }
    implicitHeight: body.implicitHeight + (card.compact ? 28 : 36)
    radius: 12
    color: Theme.surface
    border.color: card.offline ? Theme.red : drop.containsDrag ? card.accent : card.pinned ? Qt.alpha(card.accent, 0.55) : hover.hovered ? Theme.comment : Theme.selection
    // Layered outlines give the selected tray meter a soft halo without an
    // expensive blur effect or changes to the card's layout.
    Repeater {
        model: 4
        Rectangle {
            required property int index
            objectName: "trayMeterGlow_" + card.name + "_" + index
            anchors.fill: parent; anchors.margins: -(index + 1)
            z: -1; radius: card.radius + index + 1
            color: "transparent"; border.width: 1
            border.color: Qt.alpha(card.accent, 0.12 - index * 0.025)
            visible: card.pinned
        }
    }
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
        GridLayout {
            id: identityGroup
            objectName: "providerIdentityGroup_" + card.name
            Layout.fillWidth: card.stacked
            Layout.preferredWidth: card.stacked ? -1 : 138
            Layout.maximumWidth: card.stacked ? Infinity : 138
            Layout.minimumWidth: 0
            Layout.alignment: Qt.AlignTop
            // The header group is full-width above meters and narrow beside
            // them. Reflow its contents according to its own available width.
            readonly property real identityWidth: 160
            readonly property bool inlineNotice: staleNotice.visible
                && width >= identityWidth + columnSpacing + 160
            columns: inlineNotice ? 2 : 1
            columnSpacing: 14; rowSpacing: 10
            GridLayout {
                Layout.fillWidth: !identityGroup.inlineNotice
                Layout.preferredWidth: identityGroup.inlineNotice ? identityGroup.identityWidth : -1
                Layout.maximumWidth: identityGroup.inlineNotice ? identityGroup.identityWidth : Infinity
                Layout.minimumWidth: 0
                Layout.alignment: Qt.AlignTop
                columns: card.stacked ? 2 : 1
                // A zero-size slot still receives spacing, so drop it with the control.
                columnSpacing: resetSlot.shown ? 8 : 0; rowSpacing: resetSlot.shown ? 6 : 0
                RowLayout {
                    id: providerHeader
                    objectName: "providerHeader_" + card.name
                    Layout.fillWidth: true
                    Layout.minimumWidth: 0
                    Layout.alignment: Qt.AlignTop
                    spacing: 10
                    activeFocusOnTab: true
                    Accessible.description: card.pinned ? "Current tray meter" : ""
                    Rectangle {
                        width: 35; height: 35; radius: 10; color: Qt.alpha(card.accent, 0.10); border.color: Qt.alpha(card.accent, 0.17)
                        Image { objectName: "providerIcon_" + card.name; anchors.centerIn: parent; width: 26; height: 26; source: "qrc:/provider-icons/" + card.providerType.toLowerCase() + ".svg"; sourceSize.width: 52; sourceSize.height: 52; visible: ["Claude", "Codex", "Cursor", "Grok", "OpenCodeGo"].indexOf(card.providerType) >= 0 }
                    }
                    ColumnLayout {
                        Layout.fillWidth: true; spacing: 1
                        Text { textFormat: Text.PlainText; objectName: "providerLabel_" + card.name; text: card.displayName; Layout.fillWidth: true; elide: Text.ElideRight; color: Theme.foreground; font.pixelSize: 16; font.weight: Font.DemiBold }
                        Text { textFormat: Text.PlainText; text: card.provider.subtitle || "Usage overview"; color: Theme.muted; font.pixelSize: 11; Layout.fillWidth: true; elide: Text.ElideRight }
                    }
                    ContextMenu.menu: orderMenu
                    Menu {
                        id: orderMenu; objectName: "providerMenu_" + card.name
                        palette.window: Theme.surface; palette.windowText: Theme.foreground
                        palette.light: Theme.selection; palette.midlight: Theme.selection
                        palette.mid: Theme.selection; palette.dark: Theme.comment
                        separatorsCollapsible: true
                        onClosed: card.Window.window.restoreEscapeFocus()
                        MenuItem { text: "Move to top / use in tray"; onTriggered: backend.setPrimary(card.name) }
                    }
                }
                Item {
                    id: resetSlot
                    // Keep the loader alive at zero so its notification delta can float.
                    // An empty slot stays on the right so that delta remains inside the card.
                    readonly property bool shown: card.hasBankedResets || card.managingReset
                    Layout.alignment: card.stacked || !shown ? Qt.AlignRight | Qt.AlignVCenter : Qt.AlignLeft | Qt.AlignTop
                    implicitWidth: shown ? resetControl.implicitWidth : 0
                    implicitHeight: shown ? resetControl.implicitHeight : 0
                    Loader {
                        id: resetControl
                        objectName: "bankedResetControl_" + card.name
                        anchors.right: parent.right; anchors.verticalCenter: parent.verticalCenter
                        sourceComponent: bankedResetsControl
                    }
                }
            }
            ColumnLayout {
                id: staleNotice
                objectName: "providerStaleNotice_" + card.name
                visible: card.stale
                Layout.fillWidth: true; Layout.minimumWidth: 0; Layout.alignment: Qt.AlignTop; spacing: 3
                Text {
                    objectName: "providerStaleAge_" + card.name
                    textFormat: Text.PlainText
                    text: "Last successful reading " + card.readingAge
                    color: Theme.orange; font.pixelSize: 11; font.weight: Font.Medium
                    Layout.fillWidth: true; wrapMode: Text.Wrap
                }
                Text {
                    objectName: "providerStaleError_" + card.name
                    textFormat: Text.PlainText
                    text: card.provider.error || "The provider could not return usage."
                    color: Theme.muted; font.pixelSize: 11
                    Layout.fillWidth: true; wrapMode: Text.Wrap
                }
            }
        }
        ColumnLayout {
            id: meterColumn
            visible: !card.failed || card.stale
            Layout.fillWidth: true
            spacing: card.compact ? 12 : 14
            GridLayout {
                id: meters
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
                        stale: card.stale
                        retainedPace: card.provider.last_known_pacing
                            ? card.provider.last_known_pacing[modelData.id] || null : null
                        notificationViewport: card.notificationViewport
                        presentingNotifications: card.presentingNotifications
                        Layout.fillWidth: true; Layout.alignment: Qt.AlignTop
                        Layout.columnSpan: {
                            if (card.stacked && index === 0) return meters.columns
                            const position = index - (card.stacked ? 1 : 0)
                            return index === card.provider.buckets.length - 1 ? meters.columns - position % meters.columns : 1
                        }
                    }
                }
            }
            UsageDetails {
                Layout.fillWidth: true
                provider: card.provider
                clock: card.clock
            }
        }
        ColumnLayout {
            visible: card.failed && !card.stale; spacing: 12; Layout.fillWidth: true
            Text { objectName: "providerErrorTitle_" + card.name; textFormat: Text.PlainText; text: card.hasAuthCopy ? card.loginCopy.title : card.provider.needs_reauth || card.cursorLoginRequired ? "Reconnect your account" : "Usage is unavailable"; color: Theme.red; font.pixelSize: 14; font.weight: Font.Medium }
            Repeater {
                model: card.hasAuthCopy ? card.loginCopy.lines : []
                Text {
                    required property string modelData
                    required property int index
                    objectName: "providerLoginLine_" + card.name + "_" + index
                    text: modelData; textFormat: Text.PlainText; color: Theme.muted
                    font.pixelSize: 12; wrapMode: Text.WordWrap; Layout.fillWidth: true
                }
            }
            Text {
                id: errorMessage; objectName: "providerError_" + card.name
                visible: !card.hasAuthCopy
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
            ActionButton { visible: !card.hasAuthCopy && !!card.provider.reauth_command; text: "Copy sign-in command"; onClicked: { backend.copyText(card.provider.reauth_command); text = "Copied" } }
            Flow {
                Layout.fillWidth: true; Layout.preferredHeight: childrenRect.height; spacing: 8
                visible: card.hasAuthCopy && (!!card.loginCopy.command || !!card.loginCopy.url)
                ActionButton { objectName: "providerCopyCommand_" + card.name; visible: !!card.loginCopy.command; text: "Copy command"; Accessible.name: "Copy command"; onClicked: backend.copyText(card.loginCopy.command) }
                ActionButton { objectName: "providerOpenLogin_" + card.name; visible: !!card.loginCopy.url; text: "Open cursor.com"; Accessible.name: "Open cursor.com"; onClicked: Qt.openUrlExternally(card.loginCopy.url) }
            }
        }
    }
}
