import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

ColumnLayout {
    id: section
    readonly property bool hasUpdateVersion: updateService.updateMethod === "automatic"
        && updateService.latestVersion.length > 0
        && updateService.latestVersion !== appInfo.applicationVersion
        && ["available", "downloading", "staged", "applying", "failed"].indexOf(updateService.state) >= 0
    function focusHeading() { updatesHeading.forceActiveFocus() }
    spacing: 10
    RowLayout {
        Layout.fillWidth: true
        Text { id: updatesHeading; objectName: "updatesHeading"; text: "ABOUT & UPDATES"; color: Theme.foreground; font.pixelSize: 12; font.weight: Font.Medium; Layout.fillWidth: true }
        Text { text: "Headroom " + appInfo.applicationVersion; color: Theme.purple; font.pixelSize: 12 }
    }
    Text {
        objectName: "availableUpdateVersion"
        visible: section.hasUpdateVersion
        text: "Available: Headroom " + updateService.latestVersion
        Layout.fillWidth: true; wrapMode: Text.WrapAnywhere
        color: Theme.purple; font.pixelSize: 12; textFormat: Text.PlainText
    }
    Text { text: updateService.statusText; Layout.fillWidth: true; wrapMode: Text.WordWrap; color: updateService.state === "failed" ? Theme.red : Theme.muted; font.pixelSize: 12; textFormat: Text.PlainText }
    Flow {
        Layout.fillWidth: true; spacing: 8; enabled: !remoteUpdateService.busy
        ActionButton { visible: updateService.canCheck || (updateService.busy && updateService.state !== "applying"); text: updateService.busy ? (updateService.state === "downloading" ? "Working…" : "Checking…") : "Check for updates"; enabled: updateService.canCheck; onClicked: updateService.checkForUpdates() }
        ActionButton { visible: updateService.canStage; text: "Download update"; onClicked: updateService.stageUpdate() }
        ActionButton { visible: updateService.canRepair; text: "Repair installation"; onClicked: updateService.repairInstallation() }
        ActionButton { visible: updateService.canCancel; text: "Cancel"; quiet: true; onClicked: updateService.cancel() }
        ActionButton { visible: updateService.restartAvailable; objectName: "restartToApply"; text: "Restart to apply"; onClicked: updateService.restartToApply(); accent: true }
        ActionButton { visible: updateService.updateMethod !== "automatic"; text: updateService.updateMethod === "system" ? "System update instructions" : "Source update guide"; quiet: true; onClicked: updateService.openUpdateMethod() }
        ActionButton {
            objectName: "updateReleaseNotes"
            visible: section.hasUpdateVersion
            text: "Release notes"; quiet: true
            onClicked: Qt.openUrlExternally("https://github.com/AaronFeledy/headroom/releases/tag/v"
                                            + encodeURIComponent(updateService.latestVersion))
        }
    }
    RowLayout {
        Layout.fillWidth: true
        ColumnLayout {
            Layout.fillWidth: true; spacing: 4
            Text { text: appInfo.serverVersion ? "Usage server " + appInfo.serverVersion : "Usage server"; color: Theme.foreground; font.pixelSize: 12 }
            Text { text: appInfo.serverStatus; Layout.fillWidth: true; wrapMode: Text.WordWrap; color: Theme.muted; font.pixelSize: 11; textFormat: Text.PlainText }
        }
        ActionButton { text: appInfo.checkingServer ? "Checking…" : "Check server"; enabled: !appInfo.checkingServer; quiet: true; onClicked: appInfo.refreshServer() }
    }
    Text {
        visible: appInfo.serverUpdateNotice.length > 0
        text: appInfo.serverUpdateNotice; Layout.fillWidth: true; wrapMode: Text.WordWrap
        color: Theme.orange; font.pixelSize: 12; textFormat: Text.PlainText
    }
    ColumnLayout {
        visible: remoteUpdateService.available
        Layout.fillWidth: true; spacing: 8
        Text {
            text: remoteUpdateService.statusText; Layout.fillWidth: true; wrapMode: Text.WordWrap
            color: remoteUpdateService.state === "failed" ? Theme.red : Theme.muted
            font.pixelSize: 12; textFormat: Text.PlainText
        }
        ActionButton {
            objectName: "remoteServerUpdate"
            text: remoteUpdateService.busy ? "Updating server…" : "Update server"
            enabled: remoteUpdateService.canStart
            onClicked: remoteUpdateService.start()
        }
    }
}
