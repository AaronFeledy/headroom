import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

ColumnLayout {
    id: details
    required property var provider
    property var clock
    property bool expanded: false
    readonly property var rows: { details.clock; return backend.detailRows(provider) }
    readonly property var summary: { details.clock; return backend.detailSummary(provider) }
    objectName: "usageDetails_" + provider.provider_name
    visible: rows.length > 0
    spacing: 6
    Repeater {
        model: details.summary
        TextEdit {
            required property string modelData
            Layout.fillWidth: true
            Layout.minimumWidth: 0
            text: modelData
            textFormat: TextEdit.PlainText
            readOnly: true
            selectByMouse: true
            wrapMode: TextEdit.Wrap
            color: Theme.muted
            font.pixelSize: 12
            Accessible.role: Accessible.StaticText
            Accessible.name: text
        }
    }
    ActionButton {
        objectName: "usageDetailsToggle_" + details.provider.provider_name
        visible: details.rows.length > 0
        quiet: true
        checkable: true
        checked: details.expanded
        text: checked ? "Hide details" : "Show details"
        implicitHeight: 30
        padding: 6
        font.pixelSize: 12
        Layout.alignment: Qt.AlignLeft
        Accessible.name: text
        Accessible.description: "Read-only usage information"
        onClicked: details.expanded = checked
    }
    Repeater {
        model: details.expanded ? details.rows : []
        TextEdit {
            required property string modelData
            required property int index
            objectName: "usageDetail_" + details.provider.provider_name + "_" + index
            Layout.fillWidth: true
            Layout.minimumWidth: 0
            text: modelData
            textFormat: TextEdit.PlainText
            readOnly: true
            selectByMouse: true
            wrapMode: TextEdit.Wrap
            color: Theme.muted
            font.pixelSize: 11
            Accessible.role: Accessible.StaticText
            Accessible.name: text
        }
    }
}
