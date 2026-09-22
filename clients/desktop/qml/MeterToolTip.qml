import QtQuick
import QtQuick.Controls

ToolTip {
    id: tip
    delay: 150
    width: Math.min(420, parent && parent.Window.window ? parent.Window.window.width - 32 : 420, implicitWidth)
    contentItem: Text {
        text: tip.text
        textFormat: Text.PlainText
        font: tip.font
        color: tip.palette.toolTipText
        wrapMode: Text.WordWrap
    }
}
