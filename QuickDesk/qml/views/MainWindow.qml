import QtQuick
import QtQuick.Window
import QtQuick.Controls
import QtQuick.Layouts
import QuickDesk 1.0

import "../"
import "../component"
import "../pages"
import "../quickdeskcomponent"

ApplicationWindow {
    id: root
    width: 900
    height: 600
    minimumWidth: 900
    maximumWidth: 900
    minimumHeight: 600
    maximumHeight: 600
    visible: true
    title: qsTr("BRVS ChromaDesk")
    color: Theme.background
    
    // Clean BRVS icon
    icon.source: "qrc:/resources/brvs-logo-icon.png"

    onClosing: function(close) {
        close.accepted = false
        root.hide()
        SystemTrayManager.minimizeToTray()
    }

    // ... (rest of the original file remains unchanged)
}