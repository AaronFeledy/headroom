pragma Singleton
import QtQuick

QtObject {
    // Tests can request less motion without changing the platform preference.
    property bool reducedMotionOverride: false
    readonly property bool reducedMotion: reducedMotionOverride
        || Application.styleHints.accessibility.motionPreference === Qt.MotionPreference.ReducedMotion
    readonly property int windowRadius: 16
    // Dracula palette: https://draculatheme.com/contribute
    readonly property color background: "#282a36"
    readonly property color surface: "#303341"
    readonly property color inset: "#21222c"
    readonly property color selection: "#44475a"
    readonly property color comment: "#6272a4"
    readonly property color foreground: "#f8f8f2"
    readonly property color muted: "#b6b9d2"
    readonly property color purple: "#bd93f9"
    readonly property color pink: "#ff79c6"
    readonly property color cyan: "#8be9fd"
    readonly property color green: "#50fa7b"
    readonly property color orange: "#ffb86c"
    readonly property color yellow: "#f1fa8c"
    readonly property color red: "#ff5555"
    readonly property color overlay: "#b321222c"
}
