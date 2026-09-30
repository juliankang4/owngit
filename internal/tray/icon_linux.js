// The OwnGit icon on a Linux desktop: a StatusNotifierItem with its menu,
// and the panel drawn with GTK 4. "owngit tray icon" runs this program with
// gjs and owns everything else: it reads the server's status, proves it,
// opens the dashboard and keeps the owner's choices. This program only
// shows what owngit sends and tells owngit what the owner chose.
//
// Messages are JSON, one per line: owngit writes to standard input, this
// program writes to standard output. Every message is data; nothing in a
// message is run, and every text is shown as plain text.
//
// From owngit:
//   {"type":"init","name":BUS NAME,"icons":DIR}
//   {"type":"state","icon":ICON NAME,"symbol":ICON NAME,"panel":PANEL,"open":BOOL}
//                                     open: show the panel too
//   {"type":"opened"}                 the dashboard opened; close the panel
//   {"type":"notice","text":TEXT}     something failed; show it in the panel
//   {"type":"notify","notification":{"id":ID,"title":TEXT,"subtitle":TEXT,"body":TEXT,"action":TEXT}}
//                                     show a desktop notification
// The state message also carries "notifications": the notification
// settings, {"heading","hint","error","settings":[{"setting","label","on","enabled"}]}.
// To owngit:
//   {"type":"ready"}
//   {"type":"error","code":"already_running"|"no_display"|"toolkit","message":TEXT}
//   {"type":"open"} {"type":"hide"} {"type":"quit"}
//   {"type":"panel","open":BOOL}
//   {"type":"notified","id":ID,"message":TEXT}  shown, or why not
//   {"type":"notification_clicked","id":ID}
//   {"type":"notification_setting","setting":NAME,"on":BOOL}

let Gtk, Gdk, Pango, Adw = null;
const {Gio, GLib} = imports.gi;
const GioUnix = (() => {
    try {
        return imports.gi.GioUnix;
    } catch (e) {
        return {InputStream: Gio.UnixInputStream, OutputStream: Gio.UnixOutputStream};
    }
})();
const output = new GioUnix.OutputStream({fd: 1, close_fd: false});
const encoder = new TextEncoder();

function send(message) {
    output.write_all(encoder.encode(JSON.stringify(message) + '\n'), null);
    output.flush(null);
}

try {
    imports.gi.versions.Gtk = '4.0';
    imports.gi.versions.Gdk = '4.0';
    Gtk = imports.gi.Gtk;
    Gdk = imports.gi.Gdk;
    Pango = imports.gi.Pango;
} catch (e) {
    send({type: 'error', code: 'toolkit', message: String(e)});
    imports.system.exit(3);
}

// The panel's window, and so the notifications the desktop files under it,
// carry the name OwnGit rather than the name of gjs.
GLib.set_prgname('OwnGit');
GLib.set_application_name('OwnGit');

const loop = new GLib.MainLoop(null, false);
const bus = Gio.DBus.session;
let icons = '';
let iconName = 'owngit-unavailable-symbolic';
let symbolName = 'owngit-state-unavailable-symbolic';
let panel = null;
let notifications = null;
let notice = '';
let activationToken = '';
let menuRevision = 1;
let window = null;

// The item: its icon, its name and its menu.

const itemXML = `<node><interface name="org.kde.StatusNotifierItem">
<property name="Category" type="s" access="read"/>
<property name="Id" type="s" access="read"/>
<property name="Title" type="s" access="read"/>
<property name="Status" type="s" access="read"/>
<property name="WindowId" type="i" access="read"/>
<property name="IconThemePath" type="s" access="read"/>
<property name="IconName" type="s" access="read"/>
<property name="IconPixmap" type="a(iiay)" access="read"/>
<property name="AttentionIconName" type="s" access="read"/>
<property name="AttentionIconPixmap" type="a(iiay)" access="read"/>
<property name="OverlayIconName" type="s" access="read"/>
<property name="OverlayIconPixmap" type="a(iiay)" access="read"/>
<property name="ToolTip" type="(sa(iiay)ss)" access="read"/>
<property name="ItemIsMenu" type="b" access="read"/>
<property name="Menu" type="o" access="read"/>
<method name="ContextMenu"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
<method name="Activate"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
<method name="SecondaryActivate"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
<method name="Scroll"><arg name="delta" type="i" direction="in"/><arg name="orientation" type="s" direction="in"/></method>
<method name="ProvideXdgActivationToken"><arg name="token" type="s" direction="in"/></method>
<signal name="NewTitle"/>
<signal name="NewIcon"/>
<signal name="NewAttentionIcon"/>
<signal name="NewOverlayIcon"/>
<signal name="NewToolTip"/>
<signal name="NewStatus"><arg name="status" type="s"/></signal>
</interface></node>`;

const item = Gio.DBusExportedObject.wrapJSObject(itemXML, {
    Category: 'ApplicationStatus',
    Id: 'owngit',
    Title: 'OwnGit',
    Status: 'Active',
    WindowId: 0,
    get IconThemePath() {
        return icons;
    },
    get IconName() {
        return iconName;
    },
    IconPixmap: [],
    AttentionIconName: '',
    AttentionIconPixmap: [],
    OverlayIconName: '',
    OverlayIconPixmap: [],
    get ToolTip() {
        return ['', [], panel ? panel.tooltip : 'OwnGit', ''];
    },
    ItemIsMenu: false,
    Menu: '/MenuBar',
    ContextMenu() {},
    Activate() {
        togglePanel();
    },
    SecondaryActivate() {
        togglePanel();
    },
    Scroll() {},
    ProvideXdgActivationToken(token) {
        activationToken = token;
    },
});

const menuXML = `<node><interface name="com.canonical.dbusmenu">
<property name="Version" type="u" access="read"/>
<property name="TextDirection" type="s" access="read"/>
<property name="Status" type="s" access="read"/>
<property name="IconThemePath" type="as" access="read"/>
<method name="GetLayout"><arg type="i" direction="in"/><arg type="i" direction="in"/><arg type="as" direction="in"/><arg type="u" direction="out"/><arg type="(ia{sv}av)" direction="out"/></method>
<method name="GetGroupProperties"><arg type="ai" direction="in"/><arg type="as" direction="in"/><arg type="a(ia{sv})" direction="out"/></method>
<method name="GetProperty"><arg type="i" direction="in"/><arg type="s" direction="in"/><arg type="v" direction="out"/></method>
<method name="Event"><arg type="i" direction="in"/><arg type="s" direction="in"/><arg type="v" direction="in"/><arg type="u" direction="in"/></method>
<method name="EventGroup"><arg type="a(isvu)" direction="in"/><arg type="ai" direction="out"/></method>
<method name="AboutToShow"><arg type="i" direction="in"/><arg type="b" direction="out"/></method>
<method name="AboutToShowGroup"><arg type="ai" direction="in"/><arg type="ai" direction="out"/><arg type="ai" direction="out"/></method>
<signal name="ItemsPropertiesUpdated"><arg type="a(ia{sv})"/><arg type="a(ias)"/></signal>
<signal name="LayoutUpdated"><arg type="u"/><arg type="i"/></signal>
<signal name="ItemActivationRequested"><arg type="i"/><arg type="u"/></signal>
</interface></node>`;

// menuItems are the menu's entries: the state, then what the owner can do.
function menuItems() {
    if (!panel)
        return [];
    const labels = panel.labels;
    return [
        {id: 1, label: panel.tooltip, enabled: false},
        {id: 2, type: 'separator'},
        {id: 3, label: labels.show_panel, run: () => showPanel()},
        {id: 4, label: labels.open, visible: panel.can_open, run: () => send({type: 'open'})},
        {id: 5, type: 'separator'},
        {id: 6, label: labels.hide, run: () => send({type: 'hide'})},
        {id: 7, label: labels.quit, run: () => send({type: 'quit'})},
    ];
}

function menuProperties(entry) {
    const properties = {};
    if (entry.type)
        properties.type = new GLib.Variant('s', entry.type);
    if (entry.label !== undefined)
        properties.label = new GLib.Variant('s', entry.label.replace(/_/g, '__'));
    // Always sent: a desktop that keeps an entry's properties between
    // layouts would otherwise keep an earlier false.
    properties.enabled = new GLib.Variant('b', entry.enabled !== false);
    properties.visible = new GLib.Variant('b', entry.visible !== false);
    return properties;
}

function menuEntry(id) {
    return menuItems().find(entry => entry.id === id);
}

const menu = Gio.DBusExportedObject.wrapJSObject(menuXML, {
    Version: 3,
    TextDirection: 'ltr',
    Status: 'normal',
    IconThemePath: [],
    GetLayout(parent) {
        if (parent !== 0) {
            const entry = menuEntry(parent);
            return [menuRevision, [parent, entry ? menuProperties(entry) : {}, []]];
        }
        const children = menuItems().map(entry =>
            new GLib.Variant('(ia{sv}av)', [entry.id, menuProperties(entry), []]));
        return [menuRevision, [0, {'children-display': new GLib.Variant('s', 'submenu')}, children]];
    },
    GetGroupProperties(ids) {
        return menuItems().filter(entry => ids.length === 0 || ids.includes(entry.id))
            .map(entry => [entry.id, menuProperties(entry)]);
    },
    GetProperty(id, name) {
        const entry = menuEntry(id);
        const value = entry ? menuProperties(entry)[name] : undefined;
        return value ?? new GLib.Variant('s', '');
    },
    Event(id, event) {
        menuEvent(id, event);
    },
    EventGroup(events) {
        for (const [id, event] of events)
            menuEvent(id, event);
        return [];
    },
    AboutToShow() {
        return false;
    },
    AboutToShowGroup() {
        return [[], []];
    },
});

function menuEvent(id, event) {
    const entry = menuEntry(id);
    if (event === 'clicked' && entry && entry.run)
        entry.run();
}

function announce() {
    item.emit_signal('NewIcon', null);
    item.emit_signal('NewToolTip', null);
    item.emit_property_changed('IconName', new GLib.Variant('s', iconName));
    menuRevision++;
    menu.emit_signal('LayoutUpdated', new GLib.Variant('(ui)', [menuRevision, 0]));
}

// The item registers with the desktop's host of these items, and again
// whenever the host starts again.
function register(itemName) {
    Gio.bus_watch_name_on_connection(bus, 'org.kde.StatusNotifierWatcher', Gio.BusNameWatcherFlags.NONE,
        () => {
            bus.call('org.kde.StatusNotifierWatcher', '/StatusNotifierWatcher', 'org.kde.StatusNotifierWatcher',
                'RegisterStatusNotifierItem', new GLib.Variant('(s)', [itemName]), null,
                Gio.DBusCallFlags.NONE, -1, null, (connection, result) => {
                    try {
                        connection.call_finish(result);
                    } catch (e) {
                        printerr(`The desktop did not take the OwnGit icon: ${e.message}`);
                    }
                });
        },
        () => printerr('The desktop shows no StatusNotifierItem icons now. On GNOME, turn on the AppIndicator extension.'));
}

// Desktop notifications, through the desktop's notification service. Each
// notification's text is shown as text: where the service reads markup, it
// is escaped.

const notifyService = 'org.freedesktop.Notifications';
const notifyPath = '/org/freedesktop/Notifications';
let bodyMarkup = null;
// shownIDs maps the service's notification IDs to owngit's.
const shownIDs = new Map();

function escapeMarkup(value) {
    return value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function withCapabilities(next) {
    if (bodyMarkup !== null) {
        next();
        return;
    }
    bus.call(notifyService, notifyPath, notifyService, 'GetCapabilities', null, new GLib.VariantType('(as)'),
        Gio.DBusCallFlags.NONE, 10000, null, (connection, result) => {
            try {
                const [capabilities] = connection.call_finish(result).deepUnpack();
                bodyMarkup = capabilities.includes('body-markup');
            } catch (e) {
                // The service is away; Notify says so for the notification.
            }
            next();
        });
}

function notify(notification) {
    const id = String(notification.id);
    withCapabilities(() => {
        let body = String(notification.body);
        if (notification.subtitle)
            body = `${notification.subtitle}\n${body}`;
        if (bodyMarkup)
            body = escapeMarkup(body);
        const parameters = new GLib.Variant('(susssasa{sv}i)', ['OwnGit', 0, `${icons}/owngit-tile.svg`,
            String(notification.title), body, ['default', String(notification.action)],
            {'urgency': new GLib.Variant('y', 1)}, -1]);
        bus.call(notifyService, notifyPath, notifyService, 'Notify', parameters, new GLib.VariantType('(u)'),
            Gio.DBusCallFlags.NONE, 10000, null, (connection, result) => {
                try {
                    const [serviceID] = connection.call_finish(result).deepUnpack();
                    shownIDs.set(serviceID, id);
                    send({type: 'notified', id});
                } catch (e) {
                    send({type: 'notified', id, message: e.message || String(e)});
                }
            });
    });
}

function watchNotifications() {
    bus.signal_subscribe(notifyService, notifyService, 'ActionInvoked', notifyPath, null, Gio.DBusSignalFlags.NONE,
        (connection, sender, path, iface, signal, parameters) => {
            const [serviceID, action] = parameters.deepUnpack();
            if (action === 'default' && shownIDs.has(serviceID))
                send({type: 'notification_clicked', id: shownIDs.get(serviceID)});
        });
    bus.signal_subscribe(notifyService, notifyService, 'NotificationClosed', notifyPath, null, Gio.DBusSignalFlags.NONE,
        (connection, sender, path, iface, signal, parameters) => {
            shownIDs.delete(parameters.deepUnpack()[0]);
        });
}

// The panel.

function text(value, classes = []) {
    const label = new Gtk.Label({label: value, xalign: 0, wrap: true, max_width_chars: 42, css_classes: classes});
    label.set_use_markup(false);
    return label;
}

function iconImage(name, size) {
    return new Gtk.Image({gicon: Gio.FileIcon.new(Gio.File.new_for_path(`${icons}/${name}.svg`)),
        pixel_size: size, accessible_role: Gtk.AccessibleRole.PRESENTATION});
}

function button(label, name, classes, onClicked, accessibleName) {
    const widget = new Gtk.Button({label, name, css_classes: classes, halign: Gtk.Align.FILL});
    if (accessibleName) {
        widget.set_tooltip_text(accessibleName);
        widget.update_property([Gtk.AccessibleProperty.LABEL], [accessibleName]);
    }
    widget.connect('clicked', onClicked);
    return widget;
}

// field is text to copy: the text, selectable, and a Copy button.
function field(value, name, copyName) {
    const row = new Gtk.Box({spacing: 8, css_classes: ['owngit-field']});
    const label = new Gtk.Label({label: value, xalign: 0, selectable: true, wrap: true, hexpand: true,
        name, css_classes: ['monospace']});
    label.set_use_markup(false);
    label.set_wrap_mode(Pango.WrapMode.CHAR);
    row.append(label);
    const copy = button(panel.labels.copy, `copy-${name}`, [], () => {
        Gdk.Display.get_default().get_clipboard().set_content(Gdk.ContentProvider.new_for_value(value));
        copy.set_label(panel.labels.copied);
        GLib.timeout_add(GLib.PRIORITY_DEFAULT, 2000, () => {
            copy.set_label(panel.labels.copy);
            return GLib.SOURCE_REMOVE;
        });
    }, copyName);
    copy.set_valign(Gtk.Align.CENTER);
    row.append(copy);
    return row;
}

const conditionClass = {running: 'success', attention: 'warning', stopped: 'error', unavailable: 'dim-label'};

function panelContent() {
    const labels = panel.labels;
    const box = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL, spacing: 12,
        margin_top: 16, margin_bottom: 16, margin_start: 16, margin_end: 16, width_request: 340});

    const heading = new Gtk.Box({spacing: 10});
    heading.append(iconImage('owngit-tile', 32));
    const titles = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL, valign: Gtk.Align.CENTER});
    titles.append(text('OwnGit', ['heading']));
    if (panel.subtitle)
        titles.append(text(panel.subtitle, ['dim-label', 'caption']));
    heading.append(titles);
    box.append(heading);

    const state = new Gtk.Box({spacing: 6});
    state.append(iconImage(symbolName, 16));
    state.append(text(panel.state, ['heading', conditionClass[panel.condition] || 'dim-label']));
    box.append(state);

    const sentences = notice ? [notice, ...panel.notice] : panel.notice;
    if (sentences.length > 0 || panel.command) {
        const notes = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL, spacing: 6, css_classes: ['owngit-notice']});
        for (const sentence of sentences)
            notes.append(text(sentence));
        if (panel.command) {
            notes.append(text(panel.command_intro));
            notes.append(field(panel.command, 'command', labels.copy_command));
        }
        box.append(notes);
    }

    if (panel.clone_address) {
        const clone = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL, spacing: 4});
        clone.append(text(labels.clone_address, ['heading']));
        clone.append(field(panel.clone_address, 'clone', labels.copy_clone));
        clone.append(text(labels.clone_help, ['dim-label', 'caption']));
        box.append(clone);
    }

    const recent = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL, spacing: 4});
    recent.append(text(labels.recent, ['heading']));
    if (panel.pushes.length === 0)
        recent.append(text(panel.no_pushes, ['dim-label']));
    for (const push of panel.pushes) {
        const row = new Gtk.Box({spacing: 8});
        const name = text(push.repository, ['owngit-strong']);
        name.set_ellipsize(Pango.EllipsizeMode.END);
        name.set_wrap(false);
        row.append(name);
        const branch = text(push.branch, ['dim-label']);
        branch.set_ellipsize(Pango.EllipsizeMode.MIDDLE);
        branch.set_wrap(false);
        branch.set_hexpand(true);
        row.append(branch);
        row.append(text(push.when, ['dim-label', 'numeric']));
        recent.append(row);
    }
    box.append(recent);

    if (panel.can_open)
        box.append(button(labels.open, 'open', ['suggested-action'], () => send({type: 'open'})));

    if (notifications) {
        box.append(new Gtk.Separator());
        box.append(text(notifications.heading, ['heading']));
        if (notifications.error)
            box.append(text(notifications.error, ['error']));
        const list = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL, spacing: 2});
        for (const setting of notifications.settings) {
            const check = new Gtk.CheckButton({label: setting.label, active: setting.on,
                sensitive: setting.enabled, name: `notify-${setting.setting}`});
            if (setting.setting !== 'all')
                check.set_margin_start(setting.setting === 'only_others' ? 0 : 12);
            check.connect('toggled', () =>
                send({type: 'notification_setting', setting: setting.setting, on: check.get_active()}));
            list.append(check);
        }
        box.append(list);
        box.append(text(notifications.hint, ['dim-label', 'caption']));
    }

    box.append(new Gtk.Separator());
    box.append(text(labels.this_computer, ['heading']));
    const choices = new Gtk.Box({spacing: 8, homogeneous: true});
    choices.append(button(labels.hide, 'hide', [], () => send({type: 'hide'})));
    choices.append(button(labels.quit, 'quit', [], () => send({type: 'quit'})));
    box.append(choices);
    box.append(text(labels.keeps_running, ['dim-label', 'caption']));
    return box;
}

// renderPanel draws the panel again. The focus stays on the control that
// had it; a panel that just opened starts at its first button.
function renderPanel(opening = false) {
    if (!window || !window.get_visible() || !panel)
        return;
    const focused = opening ? null : window.get_focus();
    const focusedName = focused ? focused.get_name() : '';
    window.set_title(`OwnGit, ${panel.state}`);
    window.set_child(panelContent());
    focusByName(focusedName) || focusByName(panel.can_open ? 'open' : 'hide');
}

function focusByName(name) {
    if (!name)
        return false;
    const find = widget => {
        for (let child = widget.get_first_child(); child; child = child.get_next_sibling()) {
            if (child.get_name() === name)
                return child;
            const found = find(child);
            if (found)
                return found;
        }
        return null;
    };
    const widget = find(window);
    return widget ? widget.grab_focus() : false;
}

function createWindow() {
    window = new Gtk.Window({title: 'OwnGit', resizable: false, decorated: false});
    window.add_css_class('owngit-panel');
    window.set_hide_on_close(true);
    const keys = new Gtk.EventControllerKey();
    keys.connect('key-pressed', (controller, key) => {
        if (key !== Gdk.KEY_Escape)
            return false;
        hidePanel();
        return true;
    });
    window.add_controller(keys);
    // The panel closes when the owner turns to another window.
    window.connect('notify::is-active', () => {
        if (!window.is_active && window.get_visible())
            hidePanel();
    });
    window.connect('notify::visible', () => send({type: 'panel', open: window.get_visible()}));
}

function showPanel() {
    if (!panel)
        return;
    if (activationToken) {
        window.set_startup_id(activationToken);
        activationToken = '';
    }
    window.present();
    renderPanel(true);
}

function hidePanel() {
    notice = '';
    window.set_visible(false);
}

function togglePanel() {
    if (window.get_visible())
        hidePanel();
    else
        showPanel();
}

const css = `
.owngit-panel { border: 1px solid alpha(currentColor, 0.15); }
.owngit-field { background: alpha(currentColor, 0.06); border-radius: 8px; padding: 6px 6px 6px 10px; }
.owngit-notice { background: alpha(currentColor, 0.06); border-radius: 8px; padding: 10px; }
.owngit-strong { font-weight: bold; }
`;

// Messages from owngit.

function receive(message) {
    switch (message.type) {
    case 'init':
        icons = String(message.icons);
        start(String(message.name));
        break;
    case 'state': {
        // The panel is drawn again only when what it shows changed, so the
        // focus, a selection and a "Copied" stay while nothing changes.
        const changed = JSON.stringify([message.icon, message.symbol, message.panel, message.notifications]) !==
            JSON.stringify([iconName, symbolName, panel, notifications]);
        iconName = String(message.icon);
        symbolName = String(message.symbol);
        panel = message.panel;
        notifications = message.notifications || null;
        if (changed) {
            announce();
            renderPanel();
        }
        if (message.open === true)
            showPanel();
        break;
    }
    case 'opened':
        hidePanel();
        break;
    case 'notify':
        if (message.notification && typeof message.notification === 'object')
            notify(message.notification);
        break;
    case 'notice':
        notice = String(message.text);
        showPanel();
        break;
    }
}

function start(instanceName) {
    if (!Gtk.init_check()) {
        send({type: 'error', code: 'no_display', message: 'no desktop display is available'});
        loop.quit();
        return;
    }
    try {
        imports.gi.versions.Adw = '1';
        Adw = imports.gi.Adw;
        Adw.init();
    } catch (e) {
        Adw = null;
    }
    const provider = new Gtk.CssProvider();
    if (provider.load_from_string)
        provider.load_from_string(css);
    else
        provider.load_from_data(css, -1);
    Gtk.StyleContext.add_provider_for_display(Gdk.Display.get_default(), provider,
        Gtk.STYLE_PROVIDER_PRIORITY_APPLICATION);
    createWindow();
    // One icon per state directory in this session.
    Gio.bus_own_name_on_connection(bus, instanceName, Gio.BusNameOwnerFlags.DO_NOT_QUEUE,
        () => {
            const itemName = `org.kde.StatusNotifierItem-${new Gio.Credentials().get_unix_pid()}-1`;
            item.export(bus, '/StatusNotifierItem');
            menu.export(bus, '/MenuBar');
            Gio.bus_own_name_on_connection(bus, itemName, Gio.BusNameOwnerFlags.NONE, () => register(itemName), null);
            watchNotifications();
            send({type: 'ready'});
        },
        () => {
            send({type: 'error', code: 'already_running', message: 'the OwnGit icon already runs'});
            loop.quit();
        });
}

const input = new Gio.DataInputStream({base_stream: new GioUnix.InputStream({fd: 0, close_fd: false})});

function readNext() {
    input.read_line_async(GLib.PRIORITY_DEFAULT, null, (stream, result) => {
        let line;
        try {
            [line] = stream.read_line_finish_utf8(result);
        } catch (e) {
            line = null;
        }
        // owngit closed the pipe: the icon ends.
        if (line === null) {
            loop.quit();
            return;
        }
        let message = null;
        try {
            message = JSON.parse(line);
        } catch (e) {
            printerr('The OwnGit icon ignored a message that is not JSON.');
        }
        if (message && typeof message === 'object')
            receive(message);
        readNext();
    });
}

readNext();
loop.run();
