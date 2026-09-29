import QtQuick
import QtQuick.Effects
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui

// OwnGit in the Omarchy bar: the OwnGit icon, and a panel with whether
// OwnGit runs, the clone address, the three latest pushes and what needs
// doing. Everything it shows comes from "owngit tray read --json", which
// reads OwnGit's status on this computer and checks that OwnGit sent it;
// "owngit tray open" opens the dashboard after asking OwnGit again. This
// widget never reads OwnGit's files or talks to OwnGit itself, and shows
// every text as plain text.
Panel {
  id: root
  moduleName: "owngit.status"
  ipcTarget: "owngit.status"

  readonly property string program: String(setting("command", "") || "owngit")
  readonly property string stateDir: String(setting("stateDir", "") || "")
  readonly property string lang: {
    var chosen = String(setting("language", "") || "")
    if (chosen === "en" || chosen === "ko") return chosen
    return Qt.locale().name.indexOf("ko") === 0 ? "ko" : "en"
  }

  // panel is the last answer of "owngit tray read"; problem says why there
  // is none.
  property var panel: null
  property string problem: ""
  // notice says why the last Copy or Open dashboard did not work.
  property string notice: ""
  property bool reading: false
  property string copying: ""
  // The exit code and output of "owngit tray open", which arrive apart.
  property int openCode: -1
  property var openText: null
  property int cursor: 0
  property string copied: ""

  readonly property string condition: panel ? panel.condition : "unavailable"
  readonly property color foreground: root.bar ? root.bar.foreground : Color.foreground
  readonly property string fontFamily: root.bar ? root.bar.fontFamily : Style.font.family

  // The words this widget needs before owngit answers; the rest come from
  // owngit in the chosen language.
  readonly property var words: lang === "ko"
    ? { unavailable: "상태를 알 수 없음", noProgram: "owngit 명령을 실행하지 못했습니다. 이 위젯 설정에서 owngit 프로그램 경로를 지정하세요.",
        copyFailed: "복사하지 못했습니다. wl-copy(wl-clipboard 패키지)가 있는지 확인하세요.", openFailed: "대시보드를 열지 못했습니다." }
    : { unavailable: "Status unavailable", noProgram: "The owngit command could not be run. Set the owngit program in this widget's settings.",
        copyFailed: "Could not copy. Check that wl-copy (the wl-clipboard package) is installed.", openFailed: "The dashboard did not open." }

  // actions are what Enter does at each cursor position.
  readonly property var actions: {
    var list = []
    if (panel && panel.command) list.push("command")
    if (panel && panel.clone_address) list.push("clone")
    if (panel && panel.can_open) list.push("open")
    return list
  }

  function commandFor(operation) {
    var line = [program, "tray", operation, "--json", "--lang", lang]
    if (stateDir !== "") line.push("--state-dir", stateDir)
    return line
  }

  function refresh() {
    if (readProc.running) return
    readProc.command = commandFor("read")
    reading = true
    readProc.running = true
    // A program that cannot start stops at once without an answer; an
    // answer that already arrived is kept.
    Qt.callLater(function() {
      if (root.reading && !readProc.running && readProc.processId === null) {
        root.reading = false
        root.panel = null
        root.problem = root.words.noProgram
      }
    })
  }

  function accept(text) {
    reading = false
    var answer = null
    try {
      answer = JSON.parse(text)
    } catch (e) {
      answer = null
    }
    if (answer && answer.panel && typeof answer.panel === "object") {
      panel = answer.panel
      problem = ""
      return
    }
    panel = null
    problem = answer && answer.error && answer.error.message ? String(answer.error.message) : words.noProgram
  }

  function copy(value, id) {
    if (copyProc.running) return
    copying = id
    notice = ""
    copyProc.command = ["wl-copy", "--", String(value)]
    copyProc.running = true
    // wl-copy that cannot start stops at once without an exit code.
    Qt.callLater(function() {
      if (root.copying !== "" && !copyProc.running && copyProc.processId === null) copyFailed()
    })
  }

  function copyFailed() {
    copying = ""
    copied = ""
    notice = words.copyFailed
  }

  function openDashboard() {
    if (openProc.running) return
    notice = ""
    openCode = -1
    openText = null
    openProc.command = commandFor("open")
    openProc.running = true
  }

  // openDone closes the panel after the dashboard opened, and otherwise
  // shows why "owngit tray open" opened nothing and reads the status again.
  function openDone() {
    if (openCode < 0 || openText === null) return
    if (openCode === 0) {
      root.close()
      return
    }
    var answer = null
    try {
      answer = JSON.parse(openText)
    } catch (e) {
      answer = null
    }
    notice = answer && answer.error && answer.error.message ? String(answer.error.message) : words.openFailed
    refresh()
  }

  function activate(action) {
    if (action === "command") copy(panel.command, "command")
    else if (action === "clone") copy(panel.clone_address, "clone")
    else if (action === "open") openDashboard()
  }

  onOpenedChanged: {
    if (!opened) {
      notice = ""
      return
    }
    cursor = Math.max(0, actions.indexOf("open"))
    refresh()
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  Process {
    id: readProc
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: root.accept(text)
    }
  }

  Process {
    id: openProc
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        root.openText = text
        root.openDone()
      }
    }
    onExited: function(exitCode) {
      root.openCode = exitCode
      root.openDone()
    }
  }

  Process {
    id: copyProc
    onExited: function(exitCode) {
      if (exitCode !== 0) {
        root.copyFailed()
        return
      }
      root.copied = root.copying
      root.copying = ""
      copiedTimer.restart()
    }
  }

  Timer {
    interval: root.opened ? 5000 : 15000
    running: true
    repeat: true
    triggeredOnStart: true
    onTriggered: root.refresh()
  }

  Timer {
    id: copiedTimer
    interval: 2000
    onTriggered: root.copied = ""
  }

  component OwnGitIcon: Item {
    property string name: "owngit-unavailable-symbolic"
    property color color: root.foreground
    property real size: Style.space(14)
    width: size
    height: size

    Image {
      id: image
      anchors.fill: parent
      source: Qt.resolvedUrl("assets/" + parent.name + ".svg")
      sourceSize.width: Math.round(parent.size * Screen.devicePixelRatio)
      sourceSize.height: Math.round(parent.size * Screen.devicePixelRatio)
      visible: false
      layer.enabled: true
    }

    MultiEffect {
      anchors.fill: image
      source: image
      colorization: 1.0
      colorizationColor: parent.color
    }
  }

  component Line: Text {
    textFormat: Text.PlainText
    wrapMode: Text.Wrap
    width: parent ? parent.width : 0
    color: root.foreground
    font.family: root.fontFamily
    font.pixelSize: Style.font.body
  }

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    tooltipText: root.panel ? root.panel.tooltip : "OwnGit: " + root.words.unavailable
    iconComponent: Component {
      Item {
        OwnGitIcon {
          anchors.centerIn: parent
          name: "owngit-" + root.condition + "-symbolic"
          color: root.foreground
        }
      }
    }
    onPressed: function(b) { root.toggle() }
  }

  KeyboardPanel {
    id: card
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: keyCatcher
    contentWidth: card.fittedContentWidth(Style.space(340))
    contentHeight: card.fittedContentHeight(column.implicitHeight)

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      onMoveRequested: function(dx, dy) {
        if (root.actions.length === 0) return
        root.cursor = (root.cursor + (dy !== 0 ? dy : dx) + root.actions.length) % root.actions.length
      }
      onActivateRequested: root.activate(root.actions[root.cursor])
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }

      Column {
        id: column
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.top: parent.top
        spacing: Style.space(12)

        Row {
          spacing: Style.space(10)
          OwnGitIcon {
            name: "owngit-state-" + root.condition + "-symbolic"
            size: Style.font.heading
            anchors.verticalCenter: parent.verticalCenter
          }
          Column {
            anchors.verticalCenter: parent.verticalCenter
            Text {
              textFormat: Text.PlainText
              text: root.panel ? root.panel.state : root.words.unavailable
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.title
              font.bold: true
            }
            Text {
              textFormat: Text.PlainText
              visible: text !== ""
              text: root.panel ? root.panel.subtitle : ""
              color: root.foreground
              opacity: 0.6
              font.family: root.fontFamily
              font.pixelSize: Style.font.caption
            }
          }
        }

        Column {
          width: parent.width
          spacing: Style.space(6)
          visible: notices.count > 0 || root.problem !== "" || root.notice !== "" || (root.panel && root.panel.command !== "")

          Line {
            visible: root.notice !== ""
            text: root.notice
          }
          Line {
            visible: root.problem !== ""
            text: root.problem
          }
          Repeater {
            id: notices
            model: root.panel ? root.panel.notice : []
            Line {
              required property var modelData
              text: String(modelData)
            }
          }
          Line {
            visible: text !== ""
            text: root.panel ? root.panel.command_intro : ""
          }
          Field {
            visible: root.panel !== null && root.panel.command !== ""
            value: root.panel ? root.panel.command : ""
            action: "command"
          }
        }

        Column {
          width: parent.width
          spacing: Style.space(6)
          visible: root.panel !== null && root.panel.clone_address !== ""

          PanelSectionHeader {
            text: root.panel ? root.panel.labels.clone_address.toUpperCase() : ""
            foreground: root.foreground
            fontFamily: root.fontFamily
          }
          Field {
            value: root.panel ? root.panel.clone_address : ""
            action: "clone"
          }
          Line {
            text: root.panel ? root.panel.labels.clone_help : ""
            opacity: 0.6
            font.pixelSize: Style.font.caption
          }
        }

        Column {
          width: parent.width
          spacing: Style.space(4)
          visible: root.panel !== null

          PanelSectionHeader {
            text: root.panel ? root.panel.labels.recent.toUpperCase() : ""
            foreground: root.foreground
            fontFamily: root.fontFamily
          }
          Line {
            visible: root.panel !== null && root.panel.pushes.length === 0
            text: root.panel ? root.panel.no_pushes : ""
            opacity: 0.6
          }
          Repeater {
            model: root.panel ? root.panel.pushes : []
            Item {
              required property var modelData
              width: parent.width
              implicitHeight: pushWhen.implicitHeight
              Text {
                id: pushName
                textFormat: Text.PlainText
                text: String(modelData.repository)
                color: root.foreground
                font.family: root.fontFamily
                font.pixelSize: Style.font.body
                font.bold: true
                elide: Text.ElideRight
                width: Math.min(implicitWidth, parent.width * 0.4)
              }
              Text {
                textFormat: Text.PlainText
                text: String(modelData.branch)
                color: root.foreground
                opacity: 0.6
                font.family: root.fontFamily
                font.pixelSize: Style.font.body
                elide: Text.ElideMiddle
                anchors.left: pushName.right
                anchors.leftMargin: Style.space(8)
                anchors.right: pushWhen.left
                anchors.rightMargin: Style.space(8)
              }
              Text {
                id: pushWhen
                textFormat: Text.PlainText
                text: String(modelData.when)
                color: root.foreground
                opacity: 0.6
                font.family: root.fontFamily
                font.pixelSize: Style.font.body
                anchors.right: parent.right
              }
            }
          }
        }

        Button {
          visible: root.panel !== null && root.panel.can_open
          width: parent.width
          text: root.panel ? root.panel.labels.open : ""
          foreground: root.foreground
          fontFamily: root.fontFamily
          bordered: true
          hasCursor: root.actions[root.cursor] === "open"
          onClicked: root.openDashboard()
        }
      }
    }
  }

  // Field is text to copy with its Copy button.
  component Field: Item {
    property string value: ""
    property string action: ""
    width: parent ? parent.width : 0
    implicitHeight: Math.max(fieldText.implicitHeight, copyButton.implicitHeight)

    Text {
      id: fieldText
      textFormat: Text.PlainText
      text: parent.value
      color: root.foreground
      font.family: root.fontFamily
      font.pixelSize: Style.font.body
      wrapMode: Text.WrapAnywhere
      anchors.left: parent.left
      anchors.right: copyButton.left
      anchors.rightMargin: Style.space(8)
      anchors.verticalCenter: parent.verticalCenter
    }
    Button {
      id: copyButton
      anchors.right: parent.right
      anchors.verticalCenter: parent.verticalCenter
      text: root.panel ? (root.copied === parent.action ? root.panel.labels.copied : root.panel.labels.copy) : ""
      foreground: root.foreground
      fontFamily: root.fontFamily
      fontSize: Style.font.bodySmall
      bordered: true
      hasCursor: root.actions[root.cursor] === parent.action
      onClicked: root.activate(parent.action)
    }
  }
}
