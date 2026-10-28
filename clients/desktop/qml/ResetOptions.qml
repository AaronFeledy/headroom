import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

// Presentational banked-reset options. The parent supplies the policy output
// (the Controller's resetAction map) and a deadline label, and owns every
// action: this popup only emits intents and never reads a backend.
// IMPORTANT: DO NOT test these choices by activating them, their endpoint, or
// any code that might trigger a reset: doing so can burn a very valuable reset.
// Rendering synthetic states is fine; the skip-only tests are intentional.
Popup {
    id: options
    property var action: ({})
    property string deadlineLabel: ""
    signal useNowRequested()
    signal automaticRequested()
    signal cancelAutomaticRequested()
    signal usagePageRequested()
    readonly property bool claude: action.provider === "Claude"
    // The API provider key stays Codex; people know it as ChatGPT.
    readonly property string providerName: claude ? "Claude" : "ChatGPT"
    // A balance is shown only when the reading knows a positive count.
    readonly property real availableCount: action.availableCount > 0 ? action.availableCount : 0
    readonly property string phase: action.phase || "choices"
    readonly property bool armed: phase === "armed" || phase === "paused"
    readonly property bool busy: action.busy === true
    readonly property bool canConfirm: action.canConfirm === true
    readonly property bool canSchedule: action.canSchedule === true
    readonly property bool canCancel: action.canCancel === true
    readonly property string useNowReason: action.useNowReason || ""
    readonly property string automaticReason: action.automaticReason || ""
    readonly property string message: action.message || ""
    readonly property string title: {
        switch (phase) {
        case "submitting": return "Using a banked " + providerName + " reset"
        case "armed": return "Automatic " + providerName + " reset scheduled"
        case "paused": return "Automatic " + providerName + " reset paused"
        case "reconciling": return providerName + " reset request pending"
        default: return "Use a banked " + providerName + " reset?"
        }
    }
    readonly property string summary: {
        switch (phase) {
        case "submitting": return "Waiting for the server's answer. Keep Headroom running."
        case "armed": return claude
            ? "One Claude reset will be used when a usage update reports a session or weekly limit and Anthropic allows a reset. The choice ends with this weekly window; quitting Headroom cancels it."
            : "One reset will be used when a usage update reports 100% weekly usage. The choice ends with this weekly window; quitting Headroom cancels it."
        case "paused": return "Usage is unavailable, so monitoring is paused. The choice still ends with this weekly window; quitting Headroom cancels it."
        case "reconciling": return "A previous request is still being reconciled with updated usage. Nothing can be used until then."
        // The irreversible-spend warning sits under Use now wherever it can be chosen.
        default: return claude
            ? "Use one Claude reset now when Anthropic allows it, or automatically once usage reports a session or weekly limit."
            : "Use one reset now, or automatically once a usage update reports 100% weekly usage."
        }
    }
    anchors.centerIn: parent
    width: Math.min(440, (parent ? parent.width : 440) - 32)
    modal: true; focus: true; padding: 24
    closePolicy: Popup.NoAutoClose
    onOpened: closeButton.forceActiveFocus()
    background: Rectangle { color: Theme.surface; radius: 16; border.color: Theme.selection }
    Overlay.modal: Rectangle { color: Theme.overlay; radius: Theme.windowRadius }
    contentItem: ColumnLayout {
        spacing: 12
        Text {
            objectName: "resetOptionsTitle"
            text: options.title; color: Theme.foreground; font.pixelSize: 20; font.weight: Font.DemiBold
            Layout.fillWidth: true; wrapMode: Text.WordWrap
            Accessible.role: Accessible.Heading; Accessible.name: text
        }
        Text {
            objectName: "resetOptionsSummary"
            text: options.summary; color: Theme.muted; font.pixelSize: 13
            Layout.fillWidth: true; wrapMode: Text.WordWrap
        }
        Text {
            objectName: "resetOptionsBalance"
            visible: options.availableCount > 0
            text: "Banked resets: " + options.availableCount
            textFormat: Text.PlainText; color: Theme.foreground; font.pixelSize: 12
            Layout.fillWidth: true; wrapMode: Text.WordWrap
            Accessible.name: text
        }
        Text {
            objectName: "resetOptionsDeadline"
            visible: options.armed && options.deadlineLabel.length > 0
            text: "Weekly reset: " + options.deadlineLabel
            textFormat: Text.PlainText; color: Theme.foreground; font.pixelSize: 12
            Layout.fillWidth: true; wrapMode: Text.WordWrap
            Accessible.name: text
        }
        Text {
            objectName: "resetOptionsMessage"
            visible: text.length > 0
            text: options.message
            textFormat: Text.PlainText; color: Theme.foreground; font.pixelSize: 13
            Layout.fillWidth: true; wrapMode: Text.WordWrap
            Accessible.name: text
        }
        RowLayout {
            Layout.fillWidth: true
            ActionButton { text: "Usage page"; quiet: true; onClicked: options.usagePageRequested() }
            Item { Layout.fillWidth: true }
            ActionButton {
                id: closeButton; objectName: "resetOptionsClose"
                text: options.canConfirm && !options.armed ? "Cancel" : "Close"
                onClicked: options.close()
            }
        }
        // DO NOT activate either choice for testing. Both can spend a
        // banked reset; skipped tests are intentional safeguards.
        ColumnLayout {
            Layout.fillWidth: true; spacing: 4
            ActionButton {
                objectName: "confirmBankedReset"
                enabled: options.canConfirm
                text: options.busy ? "Using reset…" : "Use now"
                // The primary style belongs to a permitted spend only.
                accent: options.canConfirm; Layout.fillWidth: true
                Accessible.description: options.canConfirm ? useNowWarning.text : options.useNowReason
                onClicked: options.useNowRequested()
            }
            Text {
                id: useNowWarning; objectName: "useNowWarning"
                // A permitted spend is irreversible, armed or not: say so once, beside the choice.
                visible: options.canConfirm
                text: "Spending a reset cannot be undone."
                textFormat: Text.PlainText; color: Theme.muted; font.pixelSize: 11
                Layout.fillWidth: true; wrapMode: Text.WordWrap
            }
            Text {
                objectName: "useNowReason"
                visible: !options.busy && text.length > 0
                text: options.useNowReason
                textFormat: Text.PlainText; color: Theme.muted; font.pixelSize: 11
                Layout.fillWidth: true; wrapMode: Text.WordWrap
            }
        }
        ColumnLayout {
            visible: !options.armed
            Layout.fillWidth: true; spacing: 4
            ActionButton {
                objectName: "scheduleBankedReset"
                enabled: options.canSchedule
                text: options.claude ? "Use automatically at limit" : "Use automatically at 100%"
                Layout.fillWidth: true
                Accessible.description: options.automaticReason
                onClicked: options.automaticRequested()
            }
            Text {
                objectName: "automaticReason"
                visible: text.length > 0
                text: options.automaticReason
                textFormat: Text.PlainText; color: Theme.muted; font.pixelSize: 11
                Layout.fillWidth: true; wrapMode: Text.WordWrap
            }
        }
        ActionButton {
            objectName: "cancelScheduledBankedReset"
            visible: options.armed
            enabled: options.canCancel
            text: "Cancel automatic reset"
            Layout.fillWidth: true
            Accessible.description: options.phase === "paused" ? "Available while monitoring is paused" : ""
            onClicked: options.cancelAutomaticRequested()
        }
    }
}
