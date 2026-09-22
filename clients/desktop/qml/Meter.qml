import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

ColumnLayout {
    id: meter
    objectName: "meter_" + providerName + "_" + bucket.id
    required property var bucket
    required property string providerName
    property bool compact: false
    property var notificationViewport: null
    property bool presentingNotifications: false
    property color accent: Theme.purple
    property Component footerAccessory: null
    property bool footerAccessoryVisible: false
    property var concern: { meter.clock; return backend.concern(providerName, bucket) }
    property bool warning: concern.severity > 0
    property color usageColor: concern.color || Theme.purple
    property var clock: backend.state
    property var pace: { meter.clock; return backend.pacing(providerName, bucket) }
    property bool statusOnly: bucket.id === "on_demand" && bucket.utilization <= 0 && !!bucket.status_text && bucket.status_text.indexOf(" / ") < 0
    readonly property string providerDetails: bucket.detail_text || ""
    readonly property string usageDetails: concern.detail + (providerDetails ? "\n\n" + providerDetails : "")
    readonly property string billingDetails: {
        meter.clock
        const reset = backend.resetTimeLabel(bucket.resets_at || "")
        return [providerDetails, reset ? "Resets: " + reset : ""].filter(line => line.length > 0).join("\n\n")
    }
    spacing: 6
    GridLayout {
        id: meterHeader
        Layout.fillWidth: true
        // A full-width meter can share one line; smaller grid cells retain two.
        readonly property bool inlineValue: meter.compact && width >= titleLabel.implicitWidth
            + (severityLabel.visible ? severityLabel.implicitWidth + 8 : 0)
            + usageValue.implicitWidth + columnSpacing
        columns: inlineValue ? 2 : 1
        columnSpacing: 12; rowSpacing: 6
        RowLayout {
            Layout.fillWidth: true; spacing: 8
            Text {
                id: titleLabel
                objectName: "meterLabel_" + meter.providerName + "_" + meter.bucket.id
                textFormat: Text.PlainText; text: meter.bucket.label
                color: Theme.foreground; font.pixelSize: 13
                Layout.fillWidth: true; Layout.maximumWidth: Math.ceil(implicitWidth)
                elide: Text.ElideRight
                HoverHandler { id: titleHover }
                MeterToolTip {
                    visible: titleHover.hovered && meter.billingDetails.length > 0
                    text: meter.billingDetails
                }
            }
            Text {
                id: severityLabel
                objectName: "meterSeverity_" + meter.providerName + "_" + meter.bucket.id
                visible: meter.warning && !meter.statusOnly
                text: meter.concern.level || ""
                color: meter.usageColor; font.pixelSize: 10; font.weight: Font.Medium
            }
            Item { Layout.fillWidth: true }
        }
        RowLayout {
            id: usageValue
            visible: !meter.statusOnly
            Layout.alignment: meterHeader.inlineValue ? Qt.AlignRight : Qt.AlignLeft
            spacing: 5
            Text { text: Math.round(meter.bucket.utilization) + "%"; color: meter.warning ? meter.usageColor : Theme.foreground; font.pixelSize: meter.compact ? 20 : 24; font.weight: Font.Medium; font.letterSpacing: -0.7 }
            Text { text: "used"; color: Theme.muted; font.pixelSize: 11; Layout.alignment: Qt.AlignBottom; Layout.bottomMargin: 4 }
        }
    }
    Item {
        id: graph
        property string notchLabel: ""
        objectName: "usageGraph_" + meter.providerName + "_" + meter.bucket.id
        visible: !meter.statusOnly
        Layout.fillWidth: true; implicitHeight: 12
        Accessible.role: Accessible.ProgressBar
        Accessible.name: backend.displayName(meter.providerName) + " " + meter.bucket.label
        Accessible.description: meter.usageDetails
        Rectangle {
            id: track
            objectName: "meterTrack_" + meter.providerName + "_" + meter.bucket.id
            anchors.verticalCenter: parent.verticalCenter
            width: parent.width; height: 6; radius: 3; color: Theme.selection
            Rectangle {
                objectName: "meterFill_" + meter.providerName + "_" + meter.bucket.id
                width: Math.max(0, parent.width * meter.bucket.utilization / 100)
                height: parent.height; radius: 3
                color: meter.usageColor
                Behavior on width { enabled: !captureMode; NumberAnimation { duration: 350; easing.type: Easing.OutCubic } }
            }
            Repeater {
                objectName: "meterNotches_" + meter.providerName + "_" + meter.bucket.id
                model: backend.notches(meter.providerName, meter.bucket)
                Item {
                    required property var modelData
                    required property int index
                    objectName: "meterNotch_" + meter.providerName + "_" + meter.bucket.id + "_" + index
                    x: modelData.fraction * track.width - width / 2
                    width: 12; height: 12; y: -3
                    Rectangle { anchors.centerIn: parent; width: 1; height: 6; color: Theme.foreground; opacity: notchHover.hovered ? 0.9 : 0.30 }
                    HoverHandler {
                        id: notchHover
                        onHoveredChanged: {
                            if (hovered) graph.notchLabel = modelData.label
                            else if (graph.notchLabel === modelData.label) graph.notchLabel = ""
                        }
                    }
                }
            }
        }
        Rectangle {
            objectName: "paceMarker_" + meter.providerName + "_" + meter.bucket.id
            visible: meter.pace.available
            x: Math.max(0, Math.min(parent.width - width, parent.width * (meter.pace.expected || 0) / 100 - width / 2))
            width: 4; height: 12; radius: 1
            color: Theme.foreground; border.width: 1; border.color: Theme.background
        }
        NotificationHighlight {
            objectName: "notificationHighlight_" + meter.objectName
            targetKey: meter.objectName
            viewport: meter.notificationViewport
            presenting: meter.presentingNotifications
        }
        HoverHandler { id: graphHover }
        MeterToolTip {
            visible: graphHover.hovered
            text: graph.notchLabel || meter.usageDetails
        }
    }
    GridLayout {
        id: meterFooter
        Layout.fillWidth: true
        // Keep countdowns or status beside pace when the complete footer fits, including accessories.
        readonly property bool inlineReset: !meter.statusOnly && (resetLabel.visible || statusLabel.visible)
            && width >= Math.ceil(paceLabel.implicitWidth) + Math.ceil(resetDetails.implicitWidth) + columnSpacing
        columns: inlineReset ? 2 : 1
        columnSpacing: 12; rowSpacing: 5
        Text {
            id: paceLabel
            objectName: "paceLabel_" + meter.providerName + "_" + meter.bucket.id
            visible: !meter.statusOnly
            text: meter.pace.label
            Layout.fillWidth: true; elide: Text.ElideRight
            color: !meter.pace.available ? Theme.muted : meter.warning ? meter.usageColor
                : meter.pace.onPace ? Theme.muted : meter.pace.over ? Theme.orange : Theme.cyan
            font.pixelSize: 11
            HoverHandler { id: paceHover }
            ToolTip.visible: paceHover.hovered
            ToolTip.text: meter.concern.detail
        }
        Item {
            id: resetDetailsHost
            Layout.fillWidth: !meterFooter.inlineReset
            Layout.alignment: Qt.AlignRight | Qt.AlignVCenter
            implicitWidth: resetDetails.implicitWidth
            implicitHeight: resetDetails.implicitHeight
            RowLayout {
                id: resetDetails
                objectName: "meterResetDetails_" + meter.providerName + "_" + meter.bucket.id
                anchors.fill: parent
                spacing: 8
                Text {
                    id: resetLabel
                    objectName: "meterReset_" + meter.providerName + "_" + meter.bucket.id
                    visible: !meter.bucket.status_text || !meter.bucket.status_text.trim()
                    text: { meter.clock; return backend.countdown(meter.bucket.resets_at || "") }
                    color: Theme.muted; font.pixelSize: 11; Layout.fillWidth: true; elide: Text.ElideRight
                    HoverHandler { id: resetHover }
                    MeterToolTip {
                        visible: resetHover.hovered && meter.billingDetails.length > 0
                        text: meter.billingDetails
                    }
                }
                Text {
                    id: statusLabel
                    visible: !!meter.bucket.status_text && !!meter.bucket.status_text.trim()
                    objectName: "meterStatus_" + meter.providerName + "_" + meter.bucket.id
                    textFormat: Text.PlainText
                    text: meter.bucket.status_text || ""
                    Accessible.description: meter.billingDetails
                    HoverHandler { id: statusHover }
                    MeterToolTip {
                        objectName: "meterBillingTooltip_" + meter.providerName + "_" + meter.bucket.id
                        visible: statusHover.hovered && meter.billingDetails.length > 0
                        text: meter.billingDetails
                    }
                    color: Theme.muted; font.pixelSize: 11; Layout.fillWidth: true; wrapMode: Text.WordWrap
                }
                Item {
                    visible: meter.footerAccessoryVisible
                    implicitWidth: accessoryOverlay.implicitWidth
                    implicitHeight: accessoryOverlay.implicitHeight
                    Layout.alignment: Qt.AlignRight | Qt.AlignVCenter
                }
            }
            // Only the visible slot above participates in layout. Keep the
            // accessory loaded here so its transient delta can float at zero.
            Loader {
                id: accessoryOverlay
                sourceComponent: meter.footerAccessory
                anchors.right: parent.right
                anchors.verticalCenter: parent.verticalCenter
            }
        }
    }
}
