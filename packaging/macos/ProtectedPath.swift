import Darwin
import Foundation

/// What the icon needs to know about one name on the way to a program.
struct PathFacts {
    enum Kind { case file, folder, link, other }
    let kind: Kind
    let owner: uid_t
    let group: gid_t
    /// Permission bits, with the sticky bit.
    let mode: mode_t
    /// Where a link points.
    let target: String
    /// An access list entry allows changing it.
    let accessListWriter: Bool
}

/// protectedPathProblem names what another account could change on the way
/// to path, or returns nil when nothing: the rule of owngit's
/// state.RequireProtectedPath. Every folder, link and the file itself must
/// belong to this account or root; no one else may write it (group write
/// only for wheel, admin or this account's own group; a sticky folder that
/// everyone writes only above the file); no access list entry may allow
/// changes; links are followed after their owner is checked; and the file
/// is a regular file. facts reads one name without following it.
func protectedPathProblem(_ path: String, me: uid_t = getuid(), ownGroup: gid_t? = privateGroup(),
                          directory: Bool = false, facts: (String) -> PathFacts? = readPathFacts) -> String? {
    var pending = path.split(separator: "/").map(String.init)
    var current = ""
    var links = 0
    if let problem = changeable("/", facts("/"), last: pending.isEmpty, me: me, ownGroup: ownGroup, directory: directory) {
        return problem
    }
    while !pending.isEmpty {
        let name = pending.removeFirst()
        if name == "." || name.isEmpty {
            continue
        }
        if name == ".." {
            current = (current as NSString).deletingLastPathComponent
            continue
        }
        let candidate = current + "/" + name
        let entry = facts(candidate)
        if let entry, entry.kind == .link {
            links += 1
            guard links <= 40, entry.owner == 0 || entry.owner == me else {
                return candidate
            }
            pending = entry.target.split(separator: "/").map(String.init) + pending
            if entry.target.hasPrefix("/") {
                current = ""
            }
            continue
        }
        if let problem = changeable(candidate, entry, last: pending.isEmpty, me: me, ownGroup: ownGroup, directory: directory) {
            return problem
        }
        current = candidate
    }
    return nil
}

func protectedStateDirectoryProblem(_ path: String) -> String? {
    protectedPathProblem(path, me: geteuid(), directory: true) { name in
        guard let facts = readPathFacts(name) else { return nil }
        var mount = statfs()
        let mounted = facts.kind == .link ? (name as NSString).deletingLastPathComponent : name
        let folder = open(mounted, O_RDONLY | O_DIRECTORY | O_CLOEXEC)
        guard folder >= 0 else { return nil }
        defer { close(folder) }
        guard fstatfs(folder, &mount) == 0, mount.f_flags & UInt32(MNT_LOCAL) != 0 else { return nil }
        return facts
    }
}

private func changeable(_ path: String, _ entry: PathFacts?, last: Bool, me: uid_t, ownGroup: gid_t?, directory: Bool) -> String? {
    guard let entry, entry.owner == 0 || entry.owner == me, !entry.accessListWriter else {
        return path
    }
    // Others cannot rename what is in a sticky folder, so one above the
    // file may be writable by everyone.
    let stickyAbove = entry.mode & S_ISVTX != 0 && !last
    if !stickyAbove && (entry.mode & S_IWOTH != 0 ||
        entry.mode & S_IWGRP != 0 && !(entry.group == 0 || entry.group == 80 || entry.group == ownGroup)) {
        return path
    }
    if last ? entry.kind != (directory ? .folder : .file) : entry.kind != .folder {
        return path
    }
    return nil
}

/// privateGroup is this account's own group: its primary group when that
/// group has the account's name.
func privateGroup() -> gid_t? {
    guard let account = getpwuid(getuid()), let group = getgrgid(account.pointee.pw_gid),
          String(cString: account.pointee.pw_name) == String(cString: group.pointee.gr_name)
    else {
        return nil
    }
    return account.pointee.pw_gid
}

/// readPathFacts reads path without following a link.
func readPathFacts(_ path: String) -> PathFacts? {
    var info = stat()
    guard lstat(path, &info) == 0 else {
        return nil
    }
    let kind: PathFacts.Kind
    var target = ""
    switch info.st_mode & S_IFMT {
    case S_IFREG: kind = .file
    case S_IFDIR: kind = .folder
    case S_IFLNK:
        kind = .link
        target = (try? FileManager.default.destinationOfSymbolicLink(atPath: path)) ?? ""
    default: kind = .other
    }
    return PathFacts(kind: kind, owner: info.st_uid, group: info.st_gid, mode: info.st_mode & 0o7777,
                     target: target, accessListWriter: accessListAllowsChange(path))
}

/// accessListAllowsChange reports whether an access list entry of path
/// allows adding, removing or renaming entries, writing, or changing its
/// owner or access list. An unreadable access list counts as one.
private func accessListAllowsChange(_ path: String) -> Bool {
    guard let list = acl_get_link_np(path, ACL_TYPE_EXTENDED) else {
        return errno != ENOENT
    }
    defer { acl_free(UnsafeMutableRawPointer(list)) }
    let changes: [acl_perm_t] = [ACL_WRITE_DATA, ACL_APPEND_DATA, ACL_DELETE, ACL_DELETE_CHILD,
                                 ACL_WRITE_ATTRIBUTES, ACL_WRITE_EXTATTRIBUTES, ACL_WRITE_SECURITY, ACL_CHANGE_OWNER]
    var entry: acl_entry_t?
    var which = ACL_FIRST_ENTRY.rawValue
    while acl_get_entry(list, Int32(which), &entry) == 0, let current = entry {
        which = ACL_NEXT_ENTRY.rawValue
        var tag = ACL_UNDEFINED_TAG
        var permissions: acl_permset_t?
        guard acl_get_tag_type(current, &tag) == 0, tag == ACL_EXTENDED_ALLOW,
              acl_get_permset(current, &permissions) == 0, let permissions
        else {
            continue
        }
        if changes.contains(where: { acl_get_perm_np(permissions, $0) == 1 }) {
            return true
        }
    }
    return false
}
