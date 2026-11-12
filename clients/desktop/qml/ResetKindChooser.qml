import QtQuick
import QtQuick.Layouts

// One row per listed kind, after "Any matching reset" when several are
// listed. Selecting a row records a choice; it never spends or
// authorizes anything by itself.
ColumnLayout {
    id: chooser
    required property var kinds
    required property string selectedKindId
    // True when the stored choice is absent from this reading: no row is
    // selected, including "Any matching reset", until the person chooses again.
    required property bool selectionMissing
    // "Any matching reset" only means something for the automatic choice.
    property bool allowAny: true
    signal kindSelected(string kind)

    objectName: "resetKindChooser"
    Layout.fillWidth: true; spacing: 6
    Text {
        objectName: "resetKindHeading"
        text: "Choose a banked reset"
        textFormat: Text.PlainText; color: Theme.foreground; font.pixelSize: 12; font.weight: Font.Medium
        Layout.fillWidth: true
    }
    ActionButton {
        objectName: "resetKindAny"
        // A sole kind is already selected below; no extra row.
        visible: chooser.allowAny && chooser.kinds.length > 1
        text: "Any matching reset"
        accent: visible && !chooser.selectionMissing && chooser.selectedKindId === ""
        implicitHeight: 32
        Layout.fillWidth: true
        Accessible.role: Accessible.RadioButton
        Accessible.checked: visible && !chooser.selectionMissing && chooser.selectedKindId === ""
        Accessible.description: "Uses whichever matching reset reaches its limit first"
        onClicked: chooser.kindSelected("")
    }
    Repeater {
        model: chooser.kinds
        delegate: ColumnLayout {
            required property var modelData
            Layout.fillWidth: true; spacing: 2
            ActionButton {
                objectName: "resetKind_" + modelData.id
                text: modelData.title
                accent: chooser.selectedKindId === modelData.id
                implicitHeight: 32
                Layout.fillWidth: true
                Accessible.role: Accessible.RadioButton
                Accessible.checked: chooser.selectedKindId === modelData.id
                Accessible.description: modelData.countText
                    + (modelData.refillText.length ? ". " + modelData.refillText : "")
                onClicked: chooser.kindSelected(modelData.id)
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
