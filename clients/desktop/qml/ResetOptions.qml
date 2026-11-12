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
    signal kindSelected(string kind)
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
    // What Use now would spend, when the chosen kind names a grant.
    readonly property string useNowGrant: action.useNowGrant || ""
    // The additive breakdown. Null or an empty list keeps the legacy dialog.
    readonly property var kinds: action.kinds || []
    readonly property bool kindAware: claude && kinds.length > 0
    readonly property string selectedKindId: action.selectedKind || ""
    readonly property var selectedKindInfo: {
        for (const kind of kinds) if (kind.id === selectedKindId) return kind
        return null
    }
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
        case "armed":
            if (kindAware && selectedKindInfo)
                return "One " + selectedKindInfo.label + " will be used when a usage update reports the limit below and Anthropic allows it. The choice ends with this weekly window; quitting Headroom cancels it."
            return claude
                ? "One Claude reset will be used when a usage update reports a session or weekly limit and Anthropic allows a reset. The choice ends with this weekly window; quitting Headroom cancels it."
                : "One reset will be used when a usage update reports 100% weekly usage. The choice ends with this weekly window; quitting Headroom cancels it."
        case "paused": return "Usage is unavailable, so monitoring is paused. The choice still ends with this weekly window; quitting Headroom cancels it."
        case "reconciling": return "A previous request is still being reconciled with updated usage. Nothing can be used until then."
        // The irreversible-spend warning sits under Use now wherever it can be chosen.
        default:
            if (kindAware) return selectedKindInfo
                ? "Use one " + selectedKindInfo.label + " now when Anthropic allows it, or automatically at its limit."
                : "Choose which banked reset to use now, or which one the automatic choice may use."
            return claude
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
            visible: options.kindAware || options.availableCount > 0
            text: options.kindAware
                ? (options.selectedKindInfo ? "Banked: " + options.selectedKindInfo.countText
                    : "Choose which banked reset to use")
                : "Banked resets: " + options.availableCount
            textFormat: Text.PlainText; color: Theme.foreground; font.pixelSize: 12
            Layout.fillWidth: true; wrapMode: Text.WordWrap
            Accessible.name: text
        }
        // One row per listed kind: capitalized label, count, refills, and
        // whether Anthropic permits it now. Selecting a row records a choice;
        // it never spends or authorizes anything by itself.
        ColumnLayout {
            objectName: "resetKindChooser"
            visible: options.kindAware && !options.armed && !options.busy
            Layout.fillWidth: true; spacing: 6
            Text {
                objectName: "resetKindHeading"
                text: "Choose a banked reset"
                textFormat: Text.PlainText; color: Theme.foreground; font.pixelSize: 12; font.weight: Font.Medium
                Layout.fillWidth: true
            }
            Repeater {
                model: options.kinds
                delegate: ColumnLayout {
                    required property var modelData
                    Layout.fillWidth: true; spacing: 2
                    ActionButton {
                        objectName: "resetKind_" + modelData.id
                        text: modelData.title
                        accent: options.selectedKindId === modelData.id
                        implicitHeight: 32
                        Layout.fillWidth: true
                        Accessible.role: Accessible.RadioButton
                        Accessible.checked: options.selectedKindId === modelData.id
                        Accessible.description: modelData.countText
                            + (modelData.refillText.length ? ". " + modelData.refillText : "")
                        onClicked: options.kindSelected(modelData.id)
                    }
                    Text {
                        objectName: "resetKindDetails_" + modelData.id
                        text: modelData.countText
                            + (modelData.refillText.length ? " · " + modelData.refillText : "")
                            + " · " + (modelData.usableNow ? "Can be used now" : "Anthropic cannot apply it right now")
                        textFormat: Text.PlainText; color: Theme.muted; font.pixelSize: 11
                        Layout.fillWidth: true; wrapMode: Text.WordWrap
                    }
                    Text {
                        objectName: "resetKindNext_" + modelData.id
                        visible: text.length > 0
                        text: modelData.nextText || ""
                        textFormat: Text.PlainText; color: Theme.muted; font.pixelSize: 11
                        Layout.fillWidth: true; wrapMode: Text.WordWrap
                    }
                }
            }
        }
        Text {
            objectName: "resetOptionsKindTrigger"
            visible: options.kindAware && options.selectedKindInfo !== null
                && options.selectedKindInfo.triggerText.length > 0
            text: options.selectedKindInfo ? options.selectedKindInfo.triggerText : ""
            textFormat: Text.PlainText; color: Theme.muted; font.pixelSize: 12
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
                objectName: "useNowGrant"
                visible: options.useNowGrant.length > 0
                text: options.useNowGrant
                textFormat: Text.PlainText; color: Theme.muted; font.pixelSize: 11
                Layout.fillWidth: true; wrapMode: Text.WordWrap
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
